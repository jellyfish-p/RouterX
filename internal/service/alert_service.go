package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"routerx/internal"
	"routerx/internal/model"
)

type AlertService struct{}

func NewAlertService() *AlertService {
	return &AlertService{}
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
	if err := internal.DB.Create(&alert).Error; err != nil {
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
