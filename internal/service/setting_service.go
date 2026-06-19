package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
)

type SettingService struct{}

func NewSettingService() *SettingService {
	return &SettingService{}
}

// Get 读取一个配置项。
// 优先从 Redis 缓存取，未命中则查 DB 并回填缓存。
func (s *SettingService) Get(key string) (string, error) {
	if internal.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		v, err := internal.RDB.HGet(ctx, "settings", key).Result()
		if err == nil {
			return v, nil
		}
		if err != redis.Nil {
			// Redis 是可降级依赖，继续回落 DB。
		}
	}

	var setting model.Setting
	if err := internal.DB.Where("key = ?", key).First(&setting).Error; err != nil {
		return "", err
	}
	if internal.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = internal.RDB.HSet(ctx, "settings", key, setting.Value).Err()
	}
	return setting.Value, nil
}

// GetInt 读取整数配置。
func (s *SettingService) GetInt(key string) (int, error) {
	v, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n, nil
	}
	var result int
	err = json.Unmarshal([]byte(v), &result)
	return result, err
}

// GetBool 读取布尔配置。
func (s *SettingService) GetBool(key string) (bool, error) {
	v, err := s.Get(key)
	if err != nil {
		return false, err
	}
	var result bool
	err = json.Unmarshal([]byte(v), &result)
	return result, err
}

// GetAll 获取全量配置 (可选按 category 过滤)。
func (s *SettingService) GetAll(category string) ([]model.Setting, error) {
	var settings []model.Setting
	query := internal.DB.Model(&model.Setting{}).Order("category ASC, key ASC")
	if category != "" {
		query = query.Where("category = ?", category)
	}
	if err := query.Find(&settings).Error; err != nil {
		return nil, err
	}
	return settings, nil
}

// EnsureDefaults 补齐当前阶段默认 settings。
// 它只插入缺失 key，不覆盖管理员已经修改过的配置值。
func (s *SettingService) EnsureDefaults() error {
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		for category, values := range buildDefaultSettings() {
			for key, value := range values {
				if err := validateSettingValue(key, value); err != nil {
					return err
				}
				setting := model.Setting{Key: key, Value: value, Category: category}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "key"}},
					DoNothing: true,
				}).Create(&setting).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.LoadCache()
}

// Set 写入单个配置项, 写 DB 后刷新 Redis 缓存。
func (s *SettingService) Set(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("setting key is required")
	}
	if err := validateSettingValue(key, value); err != nil {
		return err
	}
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var setting model.Setting
		err := tx.Where("key = ?", key).First(&setting).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(&model.Setting{Key: key, Value: value, Category: categoryFromKey(key)}).Error
		case err != nil:
			return err
		default:
			return tx.Model(&setting).Updates(map[string]interface{}{
				"value":    value,
				"category": categoryFromKey(key),
			}).Error
		}
	})
	if err != nil {
		return err
	}
	if internal.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = internal.RDB.HSet(ctx, "settings", key, value).Err()
	}
	applyRuntimeSetting(key, value)
	return nil
}

// BatchSet 批量更新配置项。
func (s *SettingService) BatchSet(settings map[string]string) error {
	normalized := make(map[string]string, len(settings))
	for key, value := range settings {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if err := validateSettingValue(key, value); err != nil {
			return err
		}
		normalized[key] = value
	}
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		for key, value := range normalized {
			var setting model.Setting
			err := tx.Where("key = ?", key).First(&setting).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(&model.Setting{Key: key, Value: value, Category: categoryFromKey(key)}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if err := tx.Model(&setting).Updates(map[string]interface{}{
				"value":    value,
				"category": categoryFromKey(key),
			}).Error; err != nil {
				return err
			}
		}
		if internal.RDB != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for key, value := range normalized {
				_ = internal.RDB.HSet(ctx, "settings", key, value).Err()
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key, value := range normalized {
		applyRuntimeSetting(key, value)
	}
	return nil
}

// LoadCache 启动时将全量 settings 加载到 Redis Hash。
func (s *SettingService) LoadCache() error {
	var settings []model.Setting
	if err := internal.DB.Find(&settings).Error; err != nil {
		return err
	}
	values := make(map[string]interface{}, len(settings))
	for _, setting := range settings {
		values[setting.Key] = setting.Value
		applyRuntimeSetting(setting.Key, setting.Value)
	}
	if internal.RDB == nil || len(values) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return internal.RDB.HSet(ctx, "settings", values).Err()
}

// ValidateSettingValue exposes the same registry validation used by writes so
// readiness checks can detect config drift caused by direct database edits.
func ValidateSettingValue(key, value string) error {
	return validateSettingValue(key, value)
}

func validateSettingValue(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "jwt.secret":
		if len(value) < 32 {
			return errors.New("jwt.secret must be at least 32 characters")
		}
	case "server.port":
		return validatePortSetting(key, value)
	case "server.mode":
		return validateServerModeSetting(key, value)
	case "jwt.admin_expire_hours", "jwt.user_expire_hours",
		"relay.timeout", "relay.error_ban_threshold", "relay.error_probe_batch_size", "relay.routerx_max_hops", "routing.channel_cache.version", "payment.order_expire_minutes",
		"alert.webhook.timeout_seconds", "alert.webhook.max_attempts":
		return validatePositiveIntSetting(key, value)
	case "rate_limit.global_per_min", "rate_limit.per_token_per_min", "rate_limit.per_ip_per_min", "rate_limit.per_user_per_min", "rate_limit.per_model_per_min", "rate_limit.per_channel_per_min":
		return validateNonNegativeIntSetting(key, value)
	case "relay.retry_count", "relay.max_request_body_bytes", "relay.max_multipart_file_bytes", "relay.max_response_body_bytes", "relay.log_body_max_bytes", "log.body_max_bytes", "billing.bootstrap_admin_quota",
		"relay.error_ban_cooldown_seconds", "relay.error_probe_interval_seconds",
		"auth.register.default_quota",
		"routing.channel_cache.ttl_seconds",
		"payment.manual_adjust.large_amount_threshold":
		return validateNonNegativeIntSetting(key, value)
	case "relay.retry_on_status":
		return validateHTTPErrorStatusArraySetting(key, value)
	case "rate_limit.enabled", "relay.error_auto_ban", "relay.error_probe_enabled", "log.request_body_enabled", "log.response_body_enabled",
		"auth.login.username_password.enabled", "auth.login.email_password.enabled", "auth.login.phone_password.enabled", "auth.login.email_code.enabled", "auth.login.phone_code.enabled", "auth.login.oauth.enabled", "auth.login.oidc.enabled",
		"auth.register.enabled", "auth.register.username.enabled", "auth.register.email.enabled", "auth.register.phone.enabled", "auth.register.captcha.required",
		"routing.channel_cache.enabled", "routing.channel_cache.preload",
		"ready.production_strict", "payment.epay.enabled", "payment.stripe.enabled",
		"payment.refund.auto_deduct", "payment.refund.allow_negative_balance", "payment.dispute.auto_disable_tokens", "payment.manual_adjust.require_reason",
		"observability.metrics_enabled", "observability.audit_enabled", "observability.structured_logs_enabled",
		"alert.webhook.enabled":
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New(key + " must be a boolean")
		}
		if key == "auth.login.username_password.enabled" && !enabled {
			return errors.New("auth.login.username_password.enabled cannot be disabled")
		}
	case "auth.register.default_group_id":
		return validateNonEmptySetting(key, value)
	case "observability.request_id_header":
		if !common.ValidHTTPHeaderName(value) {
			return errors.New(key + " must be a valid HTTP header name")
		}
	case "billing.default_ratio":
		ratio, err := strconv.ParseFloat(value, 64)
		if err != nil || ratio <= 0 {
			return errors.New("billing.default_ratio must be a positive number")
		}
	case "billing.default_user_channel_group_access":
		return validateStringArrayJSONSetting(key, value)
	case "billing.usage_missing_strategy":
		return validateUsageMissingStrategySetting(key, value)
	case "billing.user_group_ratios", "billing.channel_group_ratios", "billing.model_group_ratios":
		return validatePositiveRatioMapSetting(key, value)
	case "billing.user_group_channel_ratios":
		return validateNestedPositiveRatioMapSetting(key, value)
	case "billing.user_group_channel_group_access":
		return validateChannelGroupAccessSetting(key, value)
	case "payment.currency":
		if len(value) != 3 {
			return errors.New("payment.currency must be a 3-letter currency code")
		}
	case "payment.epay.gateway", "payment.epay.notify_url", "payment.epay.return_url", "payment.epay.refund_url", "alert.webhook.url":
		return validateOptionalURLSetting(key, value)
	}
	return nil
}

func validatePortSetting(key, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 65535 {
		return errors.New(key + " must be between 1 and 65535")
	}
	return nil
}

func validateUsageMissingStrategySetting(key, value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "minimum", "reject":
		return nil
	default:
		return errors.New(key + " must be one of: minimum, reject")
	}
}

func validateServerModeSetting(key, value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug", "test", "release":
		return nil
	default:
		return errors.New(key + " must be debug, test or release")
	}
}

func validateOptionalURLSetting(key, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New(key + " must be an absolute URL")
	}
	return nil
}

func validatePositiveIntSetting(key, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return errors.New(key + " must be a positive integer")
	}
	return nil
}

func validateNonNegativeIntSetting(key, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return errors.New(key + " must be a non-negative integer")
	}
	return nil
}

func validateNonEmptySetting(key, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New(key + " cannot be empty")
	}
	return nil
}

func validateStringArrayJSONSetting(key, value string) error {
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return errors.New(key + " must be a JSON string array")
	}
	for _, item := range values {
		if strings.TrimSpace(item) == "" {
			return errors.New(key + " cannot contain empty values")
		}
	}
	return nil
}

func validateHTTPErrorStatusArraySetting(key, value string) error {
	_, err := parseHTTPErrorStatusArraySetting(key, value)
	return err
}

func parseHTTPErrorStatusArraySetting(key, value string) ([]int, error) {
	var values []int
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return nil, errors.New(key + " must be a JSON integer array")
	}
	if len(values) == 0 {
		return nil, errors.New(key + " cannot be empty")
	}
	seen := make(map[int]struct{}, len(values))
	for _, status := range values {
		if status < 400 || status > 599 {
			return nil, errors.New(key + " values must be HTTP error status codes")
		}
		if _, ok := seen[status]; ok {
			return nil, errors.New(key + " cannot contain duplicate status codes")
		}
		seen[status] = struct{}{}
	}
	return values, nil
}

func validatePositiveRatioMapSetting(key, value string) error {
	var values map[string]float64
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return errors.New(key + " must be a JSON object")
	}
	for name, ratio := range values {
		if strings.TrimSpace(name) == "" {
			return errors.New(key + " cannot contain empty keys")
		}
		if !validPositiveRatio(ratio) {
			return errors.New(key + " values must be positive numbers")
		}
	}
	return nil
}

func validateNestedPositiveRatioMapSetting(key, value string) error {
	var values map[string]map[string]float64
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return errors.New(key + " must be a nested JSON object")
	}
	for outerKey, innerValues := range values {
		if strings.TrimSpace(outerKey) == "" {
			return errors.New(key + " cannot contain empty outer keys")
		}
		for innerKey, ratio := range innerValues {
			if strings.TrimSpace(innerKey) == "" {
				return errors.New(key + " cannot contain empty inner keys")
			}
			if !validPositiveRatio(ratio) {
				return errors.New(key + " values must be positive numbers")
			}
		}
	}
	return nil
}

func validateChannelGroupAccessSetting(key, value string) error {
	var rules map[string]struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if err := json.Unmarshal([]byte(value), &rules); err != nil {
		return errors.New(key + " must be a JSON object")
	}
	for group, rule := range rules {
		if strings.TrimSpace(group) == "" {
			return errors.New(key + " cannot contain empty user group keys")
		}
		for _, item := range append(rule.Allow, rule.Deny...) {
			if strings.TrimSpace(item) == "" {
				return errors.New(key + " cannot contain empty channel groups")
			}
		}
	}
	return nil
}

func categoryFromKey(key string) string {
	for i, ch := range key {
		if ch == '.' && i > 0 {
			return key[:i]
		}
	}
	if key == "" {
		return "general"
	}
	return "general"
}

func applyRuntimeSetting(key, value string) {
	switch key {
	case "observability.request_id_header":
		common.SetRequestIDHeaderName(value)
	case "observability.structured_logs_enabled":
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		common.SetStructuredLogsEnabled(err == nil && enabled)
	}
}
