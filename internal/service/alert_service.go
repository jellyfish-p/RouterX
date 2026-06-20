package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"routerx/internal"
	"routerx/internal/model"
)

type AlertService struct {
	settingSvc *SettingService
	httpClient *http.Client
}

const (
	defaultAlertDeliveryListLimit      = 20
	defaultAlertDeliveryTimeoutSeconds = 5
	defaultAlertDeliveryMaxAttempts    = 3
	maxAlertDeliveryReplayLimit        = 100
)

func NewAlertService() *AlertService {
	return &AlertService{settingSvc: NewSettingService(), httpClient: http.DefaultClient}
}

type AlertFilter struct {
	Type         string
	Severity     string
	Status       string
	ResourceType string
	ResourceID   string
	UserID       *uint
	TokenID      *uint
	Page         int
	PageSize     int
}

type CreateAlertInput struct {
	Type         string
	Severity     string
	ResourceType string
	ResourceID   string
	UserID       *uint
	TokenID      *uint
	Title        string
	Message      string
	Details      map[string]interface{}
}

type AlertDeliveryFilter struct {
	AlertID  *uint
	Target   string
	Status   string
	Page     int
	PageSize int
}

type alertDeliveryConfig struct {
	Target      string
	Enabled     bool
	URL         string
	Timeout     time.Duration
	MaxAttempts int
	UserAgent   string
}

var alertDeliveryTargets = []string{
	model.AlertDeliveryTargetWebhook,
	model.AlertDeliveryTargetEmail,
	model.AlertDeliveryTargetIM,
}

var ErrInvalidAlertDeliveryTarget = errors.New("invalid alert delivery target")

func (s *AlertService) Create(input CreateAlertInput) (*model.AlertEvent, error) {
	input.Type = strings.TrimSpace(input.Type)
	input.Severity = strings.TrimSpace(input.Severity)
	input.ResourceType = strings.TrimSpace(input.ResourceType)
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	input.Title = strings.TrimSpace(input.Title)
	input.Message = strings.TrimSpace(input.Message)
	if input.Type == "" || input.ResourceType == "" || input.ResourceID == "" || input.Title == "" || input.Message == "" {
		return nil, errors.New("alert type, resource and message are required")
	}
	if input.Severity == "" {
		input.Severity = model.AlertSeverityWarning
	}

	alert := model.AlertEvent{
		Type:         input.Type,
		Severity:     input.Severity,
		Status:       model.AlertStatusOpen,
		ResourceType: input.ResourceType,
		ResourceID:   input.ResourceID,
		UserID:       input.UserID,
		TokenID:      input.TokenID,
		Title:        input.Title,
		Message:      input.Message,
		DetailsJSON:  model.NewJSONValue(input.Details),
	}
	deliveryTargets := s.configuredAlertDeliveryTargets()
	if err := internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&alert).Error; err != nil {
			return err
		}
		for _, target := range deliveryTargets {
			if err := enqueueAlertDelivery(tx, alert.ID, target); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &alert, nil
}

func (s *AlertService) CreateAPIKeyLeakAlert(token *model.Token, reporterUserID uint) (*model.AlertEvent, error) {
	if token == nil || token.ID == 0 {
		return nil, errors.New("token is required")
	}
	userID := token.UserID
	tokenID := token.ID
	return s.Create(CreateAlertInput{
		Type:         model.AlertTypeAPIKeyLeakReported,
		Severity:     model.AlertSeverityCritical,
		ResourceType: "api_key",
		ResourceID:   fmt.Sprint(token.ID),
		UserID:       &userID,
		TokenID:      &tokenID,
		Title:        "API Key leak reported",
		Message:      fmt.Sprintf("API Key %d was reported leaked and disabled. Review recent usage and rotate the credential.", token.ID),
		Details: map[string]interface{}{
			"reporter_user_id":        reporterUserID,
			"replacement_recommended": true,
		},
	})
}

func (s *AlertService) List(filter AlertFilter) ([]model.AlertEvent, int64, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	query := internal.DB.Model(&model.AlertEvent{})
	if typ := strings.TrimSpace(filter.Type); typ != "" {
		query = query.Where("type = ?", typ)
	}
	if severity := strings.TrimSpace(filter.Severity); severity != "" {
		query = query.Where("severity = ?", severity)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}
	if resourceType := strings.TrimSpace(filter.ResourceType); resourceType != "" {
		query = query.Where("resource_type = ?", resourceType)
	}
	if resourceID := strings.TrimSpace(filter.ResourceID); resourceID != "" {
		query = query.Where("resource_id = ?", resourceID)
	}
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if filter.TokenID != nil {
		query = query.Where("token_id = ?", *filter.TokenID)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var alerts []model.AlertEvent
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&alerts).Error
	return alerts, total, err
}

func (s *AlertService) Acknowledge(id, actorUserID uint) (*model.AlertEvent, error) {
	if id == 0 || actorUserID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var alert model.AlertEvent
	if err := internal.DB.First(&alert, id).Error; err != nil {
		return nil, err
	}
	if alert.Status != model.AlertStatusAcknowledged || alert.AckedAt == nil || alert.AckedByUserID == nil {
		now := time.Now()
		updates := map[string]interface{}{
			"status":           model.AlertStatusAcknowledged,
			"acked_at":         &now,
			"acked_by_user_id": actorUserID,
		}
		if err := internal.DB.Model(&model.AlertEvent{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, err
		}
		if err := internal.DB.First(&alert, id).Error; err != nil {
			return nil, err
		}
	}
	return &alert, nil
}

func (s *AlertService) ListDeliveries(filter AlertDeliveryFilter) ([]model.AlertDeliveryOutbox, int64, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	query := internal.DB.Model(&model.AlertDeliveryOutbox{})
	if filter.AlertID != nil {
		query = query.Where("alert_id = ?", *filter.AlertID)
	}
	if target := strings.TrimSpace(filter.Target); target != "" {
		query = query.Where("target = ?", target)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.AlertDeliveryOutbox
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	return items, total, err
}

func (s *AlertService) StartAlertDeliveryWorker(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		s.replayAlertDeliveryBatch(defaultAlertDeliveryListLimit)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.replayAlertDeliveryBatch(defaultAlertDeliveryListLimit)
			}
		}
	}()
}

func (s *AlertService) StartWebhookDeliveryWorker(ctx context.Context) {
	s.StartAlertDeliveryWorker(ctx)
}

func (s *AlertService) ReplayAlertDeliveryOutbox(limit int, target string) (int, error) {
	if limit <= 0 {
		limit = defaultAlertDeliveryListLimit
	}
	if limit > maxAlertDeliveryReplayLimit {
		limit = maxAlertDeliveryReplayLimit
	}

	configs, err := s.alertDeliveryConfigs(target)
	if err != nil {
		return 0, err
	}
	if len(configs) == 0 {
		return 0, nil
	}

	replayed := 0
	var firstErr error
	for _, cfg := range configs {
		if replayed >= limit {
			break
		}
		count, err := s.replayAlertDeliveryTarget(cfg, limit-replayed)
		replayed += count
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return replayed, firstErr
}

func (s *AlertService) ReplayWebhookDeliveryOutbox(limit int) (int, error) {
	return s.ReplayAlertDeliveryOutbox(limit, model.AlertDeliveryTargetWebhook)
}

func (s *AlertService) replayAlertDeliveryTarget(cfg alertDeliveryConfig, limit int) (int, error) {
	if !cfg.Enabled || limit <= 0 {
		return 0, nil
	}

	var items []model.AlertDeliveryOutbox
	if err := internal.DB.
		Where("target = ? AND status = ? AND next_attempt_at <= ?", cfg.Target, model.AlertDeliveryStatusPending, time.Now()).
		Order("id ASC").
		Limit(limit).
		Find(&items).Error; err != nil {
		return 0, err
	}

	replayed := 0
	var firstErr error
	for _, item := range items {
		var alert model.AlertEvent
		if err := internal.DB.First(&alert, item.AlertID).Error; err != nil {
			if markErr := markAlertDeliveryTerminalFailed(item.ID, err); markErr != nil && firstErr == nil {
				firstErr = markErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := s.sendAlertDelivery(cfg, &alert); err != nil {
			if markErr := markAlertDeliveryFailed(item, cfg.MaxAttempts, err); markErr != nil && firstErr == nil {
				firstErr = markErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := markAlertDeliveryCompleted(item.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		replayed++
	}
	return replayed, firstErr
}

func enqueueAlertDelivery(tx *gorm.DB, alertID uint, target string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.AlertDeliveryOutbox{
		AlertID:       alertID,
		Target:        target,
		Status:        model.AlertDeliveryStatusPending,
		NextAttemptAt: time.Now(),
	}).Error
}

func (s *AlertService) configuredAlertDeliveryTargets() []string {
	configs, err := s.alertDeliveryConfigs("")
	if err != nil {
		stdlog.Printf("[AlertService] WARN: alert delivery setting read failed: %v", err)
		return nil
	}
	targets := make([]string, 0, len(configs))
	for _, cfg := range configs {
		targets = append(targets, cfg.Target)
	}
	return targets
}

func (s *AlertService) alertDeliveryConfigs(target string) ([]alertDeliveryConfig, error) {
	target = strings.TrimSpace(target)
	targets := alertDeliveryTargets
	if target != "" {
		normalized, ok := normalizeAlertDeliveryTarget(target)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrInvalidAlertDeliveryTarget, target)
		}
		targets = []string{normalized}
	}

	configs := make([]alertDeliveryConfig, 0, len(targets))
	for _, item := range targets {
		cfg, err := s.alertDeliveryConfig(item)
		if err != nil {
			return nil, err
		}
		if cfg.Enabled {
			configs = append(configs, cfg)
		}
	}
	return configs, nil
}

func (s *AlertService) alertDeliveryConfig(target string) (alertDeliveryConfig, error) {
	cfg := alertDeliveryConfig{
		Target:      target,
		Timeout:     time.Duration(defaultAlertDeliveryTimeoutSeconds) * time.Second,
		MaxAttempts: defaultAlertDeliveryMaxAttempts,
		UserAgent:   alertDeliveryUserAgent(target),
	}
	settingSvc := s.settingSvc
	if settingSvc == nil {
		settingSvc = NewSettingService()
	}
	prefix := "alert." + target

	enabled, err := settingSvc.GetBool(prefix + ".enabled")
	if err != nil {
		if isMissingSetting(err) {
			return cfg, nil
		}
		return cfg, err
	}
	cfg.Enabled = enabled
	urlValue, err := settingSvc.Get(prefix + ".url")
	if err != nil {
		if !isMissingSetting(err) {
			return cfg, err
		}
	} else {
		cfg.URL = strings.TrimSpace(urlValue)
	}
	cfg.Enabled = cfg.Enabled && cfg.URL != ""

	timeoutSeconds, err := settingSvc.GetInt(prefix + ".timeout_seconds")
	if err != nil {
		if !isMissingSetting(err) {
			return cfg, err
		}
	} else if timeoutSeconds > 0 {
		cfg.Timeout = time.Duration(timeoutSeconds) * time.Second
	}

	maxAttempts, err := settingSvc.GetInt(prefix + ".max_attempts")
	if err != nil {
		if !isMissingSetting(err) {
			return cfg, err
		}
	} else if maxAttempts > 0 {
		cfg.MaxAttempts = maxAttempts
	}
	return cfg, nil
}

func normalizeAlertDeliveryTarget(target string) (string, bool) {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, item := range alertDeliveryTargets {
		if target == item {
			return item, true
		}
	}
	return "", false
}

func alertDeliveryUserAgent(target string) string {
	switch target {
	case model.AlertDeliveryTargetEmail:
		return "RouterX-Alert-Email"
	case model.AlertDeliveryTargetIM:
		return "RouterX-Alert-IM"
	default:
		return "RouterX-Alert-Webhook"
	}
}

func (s *AlertService) replayAlertDeliveryBatch(batchSize int) {
	if replayed, err := s.ReplayAlertDeliveryOutbox(batchSize, ""); err != nil {
		stdlog.Printf("[AlertService] WARN: alert delivery replay failed replayed=%d: %v", replayed, err)
	}
}

func (s *AlertService) sendAlertDelivery(cfg alertDeliveryConfig, alert *model.AlertEvent) error {
	if alert == nil || !cfg.Enabled {
		return nil
	}
	// Rebuild the payload from the sanitized alert fact so the outbox never stores secrets.
	payload := map[string]interface{}{
		"event":         "routerx.alert",
		"target":        cfg.Target,
		"id":            alert.ID,
		"type":          alert.Type,
		"severity":      alert.Severity,
		"status":        alert.Status,
		"resource_type": alert.ResourceType,
		"resource_id":   alert.ResourceID,
		"user_id":       alert.UserID,
		"token_id":      alert.TokenID,
		"title":         alert.Title,
		"message":       alert.Message,
		"details":       alert.DetailsJSON,
		"created_at":    alert.CreatedAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", cfg.UserAgent)
	req.Header.Set("X-RouterX-Alert-Type", alert.Type)
	req.Header.Set("X-RouterX-Alert-Target", cfg.Target)

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("alert %s delivery returned status %d", cfg.Target, resp.StatusCode)
	}
	return nil
}

func markAlertDeliveryCompleted(id uint) error {
	now := time.Now()
	return internal.DB.Model(&model.AlertDeliveryOutbox{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":       model.AlertDeliveryStatusCompleted,
			"last_error":   "",
			"completed_at": &now,
		}).Error
}

func markAlertDeliveryFailed(item model.AlertDeliveryOutbox, maxAttempts int, cause error) error {
	attempts := item.Attempts + 1
	status := model.AlertDeliveryStatusPending
	nextAttemptAt := time.Now().Add(alertDeliveryBackoff(attempts))
	if maxAttempts > 0 && attempts >= maxAttempts {
		status = model.AlertDeliveryStatusFailed
		nextAttemptAt = time.Now()
	}
	return internal.DB.Model(&model.AlertDeliveryOutbox{}).
		Where("id = ?", item.ID).
		Updates(map[string]interface{}{
			"status":          status,
			"attempts":        gorm.Expr("attempts + ?", 1),
			"last_error":      truncateAlertDeliveryError(cause),
			"next_attempt_at": nextAttemptAt,
		}).Error
}

func markAlertDeliveryTerminalFailed(id uint, cause error) error {
	return internal.DB.Model(&model.AlertDeliveryOutbox{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":     model.AlertDeliveryStatusFailed,
			"last_error": truncateAlertDeliveryError(cause),
		}).Error
}

func alertDeliveryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(attempt*attempt) * time.Minute
}

func truncateAlertDeliveryError(cause error) string {
	if cause == nil {
		return ""
	}
	msg := strings.TrimSpace(cause.Error())
	if len(msg) > 2048 {
		return msg[:2048]
	}
	return msg
}

func isMissingSetting(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
