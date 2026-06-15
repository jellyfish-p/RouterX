package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/relay"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

type LogService struct{}

func NewLogService() *LogService {
	return &LogService{}
}

// Record 写入请求日志 (异步/同步可选)。
func (s *LogService) Record(log *model.Log) error {
	if log == nil {
		return errors.New("log is required")
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}
	log.ErrorCode = normalizeLogErrorCode(log)
	log.ErrorSource = normalizeLogErrorSource(log)
	log.UpstreamStatus = normalizeLogUpstreamStatus(log)
	log.BillingSnapshot = normalizeLogBillingSnapshot(log)
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(log).Error; err != nil {
			return err
		}
		return updateTokenLastUsageSummary(tx, log)
	})
}

func updateTokenLastUsageSummary(tx *gorm.DB, log *model.Log) error {
	if log == nil || log.TokenID == nil || *log.TokenID == 0 {
		return nil
	}
	return tx.Model(&model.Token{}).Where("id = ?", *log.TokenID).Updates(map[string]interface{}{
		"last_used_at":         log.CreatedAt,
		"last_used_ip_hash":    usageSourceHash(log.IP),
		"last_user_agent_hash": usageSourceHash(log.UserAgent),
		"last_model":           strings.TrimSpace(log.Model),
		"last_error_code":      normalizeLogErrorCode(log),
	}).Error
}

func usageSourceHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeLogErrorCode(log *model.Log) string {
	if log == nil || log.Status != common.LogStatusFailed {
		return ""
	}
	if code := strings.TrimSpace(log.ErrorCode); code != "" {
		return code
	}
	msg := strings.ToLower(strings.TrimSpace(log.ErrorMsg))
	switch {
	case msg == "":
		return "unknown_error"
	case strings.Contains(msg, "insufficient quota"):
		return "insufficient_quota"
	case strings.Contains(msg, "rpm limit") || strings.Contains(msg, "tpm limit") || strings.Contains(msg, "concurrency limit"):
		return "rate_limit_exceeded"
	case strings.Contains(msg, "timeout"):
		return "upstream_timeout"
	case strings.Contains(msg, "upstream returned status"):
		fields := strings.Fields(msg)
		if len(fields) > 0 {
			return "upstream_" + fields[len(fields)-1]
		}
	case strings.Contains(msg, "model not allowed"):
		return "model_not_allowed"
	case strings.Contains(msg, "channel group not allowed"):
		return "route_forbidden"
	case strings.Contains(msg, "api key scope"):
		return "token_forbidden"
	case strings.Contains(msg, "no available channel"):
		return "no_available_channel"
	}
	return "relay_failed"
}

func normalizeLogErrorSource(log *model.Log) string {
	if log == nil || log.Status != common.LogStatusFailed {
		return ""
	}
	if source := strings.TrimSpace(log.ErrorSource); source != "" {
		return source
	}
	code := normalizeLogErrorCode(log)
	msg := strings.ToLower(strings.TrimSpace(log.ErrorMsg))
	switch {
	case strings.Contains(msg, "secret decrypt"):
		return common.LogErrorSourceChannel
	case strings.HasPrefix(code, "upstream_") || strings.Contains(msg, "upstream request") || strings.Contains(msg, "upstream response") || strings.Contains(msg, "upstream timeout"):
		return common.LogErrorSourceUpstream
	case code == "insufficient_quota" || code == "rate_limit_exceeded":
		return common.LogErrorSourceQuota
	case code == "no_available_channel" || code == "route_forbidden":
		return common.LogErrorSourceRoute
	case code == "token_forbidden" || code == "model_not_allowed":
		return common.LogErrorSourceAuth
	case strings.Contains(msg, "invalid request") || strings.Contains(msg, "bad request"):
		return common.LogErrorSourceRequest
	case strings.Contains(msg, "deduct") || strings.Contains(msg, "billing"):
		return common.LogErrorSourceBilling
	default:
		return common.LogErrorSourceSystem
	}
}

func normalizeLogUpstreamStatus(log *model.Log) int {
	if log == nil || log.Status != common.LogStatusFailed {
		return 0
	}
	if log.UpstreamStatus > 0 {
		return log.UpstreamStatus
	}
	if status := upstreamStatusFromCode(normalizeLogErrorCode(log)); status > 0 {
		return status
	}
	return upstreamStatusFromMessage(log.ErrorMsg)
}

func upstreamStatusFromCode(code string) int {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, "upstream_") {
		return 0
	}
	status, err := strconv.Atoi(strings.TrimPrefix(code, "upstream_"))
	if err != nil {
		return 0
	}
	return status
}

func upstreamStatusFromMessage(message string) int {
	msg := strings.ToLower(strings.TrimSpace(message))
	if !strings.Contains(msg, "upstream returned status") {
		return 0
	}
	fields := strings.Fields(msg)
	if len(fields) == 0 {
		return 0
	}
	status, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return 0
	}
	return status
}

func normalizeLogBillingSnapshot(log *model.Log) string {
	if log == nil {
		return ""
	}
	if log.Status != common.LogStatusSuccess || log.QuotaUsed <= 0 {
		return strings.TrimSpace(log.BillingSnapshot)
	}
	if snapshot := strings.TrimSpace(log.BillingSnapshot); snapshot != "" {
		return snapshot
	}
	usageSource := strings.TrimSpace(log.UsageSource)
	if usageSource == "" {
		usageSource = common.LogUsageSourceMinimum
		if log.TotalTokens > 0 {
			usageSource = common.LogUsageSourceUpstream
		}
	}
	payer := "user"
	if log.TokenID != nil && *log.TokenID > 0 {
		payer = "token_and_user"
	}
	expressionSource := "p0_usage"
	if usageSource == common.LogUsageSourceMinimum {
		expressionSource = "minimum"
	}
	var usage *relay.Usage
	if log.TotalTokens > 0 {
		usage = &relay.Usage{
			PromptTokens:     log.PromptTokens,
			CompletionTokens: log.CompletionTokens,
			TotalTokens:      log.TotalTokens,
		}
	}
	snapshot := map[string]interface{}{
		"schema":                      "routerx.snapshot.v1",
		"kind":                        "billing",
		"stage":                       "p1",
		"source":                      "billing",
		"redacted":                    true,
		"billing_status":              "settled",
		"price_source":                expressionSource,
		"billing_expression_source":   expressionSource,
		"billing_expression_snapshot": buildP0BillingExpressionSnapshot(usage, usageSource, log.QuotaUsed),
		"multiplier_snapshot":         defaultMultiplierSnapshot(),
		"usage_source":                usageSource,
		"payer":                       payer,
		"prompt_tokens":               log.PromptTokens,
		"completion_tokens":           log.CompletionTokens,
		"total_tokens":                log.TotalTokens,
		"final_quota_used":            log.QuotaUsed,
		"deduction_result":            "applied",
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

// List 日志分页查询, 支持多维筛选。
func (s *LogService) List(userID, tokenID, channelID *uint, modelName string, status *int, startTime, endTime string, page, pageSize int) ([]model.Log, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.Log{})
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}
	if tokenID != nil {
		query = query.Where("token_id = ?", *tokenID)
	}
	if channelID != nil {
		query = query.Where("channel_id = ?", *channelID)
	}
	if modelName != "" {
		query = query.Where("model = ?", modelName)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	if t, ok := parseTime(startTime); ok {
		query = query.Where("created_at >= ?", t)
	}
	if t, ok := parseTime(endTime); ok {
		query = query.Where("created_at <= ?", t)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var logs []model.Log
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error
	return logs, total, err
}

// Clear 清空日志 (软删除或 TRUNCATE)。
func (s *LogService) Clear() error {
	before := time.Now().AddDate(0, 0, -90)
	return s.ClearBefore(before)
}

func (s *LogService) ClearBefore(before time.Time) error {
	if before.IsZero() || before.After(time.Now()) {
		return errors.New("valid before time is required")
	}
	return internal.DB.Where("created_at < ?", before).Delete(&model.Log{}).Error
}

// GetUserStats 用户用量统计 (指定时间段内的调用次数 + 总消耗)。
func (s *LogService) GetUserStats(userID uint, startTime, endTime string) (callCount int64, totalQuota int64, totalTokens int64, err error) {
	query := internal.DB.Model(&model.Log{}).Where("user_id = ? AND status = ?", userID, common.LogStatusSuccess)
	if t, ok := parseTime(startTime); ok {
		query = query.Where("created_at >= ?", t)
	}
	if t, ok := parseTime(endTime); ok {
		query = query.Where("created_at <= ?", t)
	}
	type aggregate struct {
		CallCount   int64
		TotalQuota  int64
		TotalTokens int64
	}
	var result aggregate
	err = query.Select("COUNT(*) AS call_count, COALESCE(SUM(quota_used), 0) AS total_quota, COALESCE(SUM(total_tokens), 0) AS total_tokens").Scan(&result).Error
	return result.CallCount, result.TotalQuota, result.TotalTokens, err
}

// GetDashboardStats 仪表盘全局统计。
func (s *LogService) GetDashboardStats() (userCount, channelCount, tokenCount, todayCalls, todayQuota, activeChannels int64, err error) {
	if err = internal.DB.Model(&model.User{}).Count(&userCount).Error; err != nil {
		return
	}
	if err = internal.DB.Model(&model.Channel{}).Count(&channelCount).Error; err != nil {
		return
	}
	if err = internal.DB.Model(&model.Token{}).Count(&tokenCount).Error; err != nil {
		return
	}
	if err = internal.DB.Model(&model.Channel{}).Where("status = ?", common.ChannelStatusEnabled).Count(&activeChannels).Error; err != nil {
		return
	}
	start := time.Now().Truncate(24 * time.Hour)
	type aggregate struct {
		TodayCalls int64
		TodayQuota int64
	}
	var result aggregate
	err = internal.DB.Model(&model.Log{}).
		Where("created_at >= ?", start).
		Select("COUNT(*) AS today_calls, COALESCE(SUM(quota_used), 0) AS today_quota").
		Scan(&result).Error
	return userCount, channelCount, tokenCount, result.TodayCalls, result.TodayQuota, activeChannels, err
}

func parseTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
