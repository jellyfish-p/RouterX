package service

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SetupService struct {
	userService    *UserService
	settingService *SettingService
}

var setupMu sync.Mutex

func NewSetupService(us *UserService, ss *SettingService) *SetupService {
	return &SetupService{userService: us, settingService: ss}
}

// GetInitStatus 返回系统初始化状态 (是否存在管理员用户)。
func (s *SetupService) GetInitStatus() (bool, error) {
	// 已初始化条件: users 表中 role>=1 的记录数 > 0
	var count int64
	if err := internal.DB.Model(&model.User{}).Where("role >= ?", 1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Init 首次初始化：创建超级管理员 + 写入默认系统设置。
// 仅在系统未初始化时可调用。
func (s *SetupService) Init(username, password, displayName, email string) (*model.User, error) {
	setupMu.Lock()
	defer setupMu.Unlock()

	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	if len(password) < 6 {
		return nil, errors.New("password length must be at least 6")
	}
	if displayName == "" {
		displayName = username
	}

	var created *model.User
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.User{}).Where("role >= ?", common.RoleAdmin).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return gorm.ErrDuplicatedKey
		}

		var identityCount int64
		if err := tx.Model(&model.UserIdentity{}).
			Where("method = ? AND provider = ? AND identifier = ?", model.UserIdentityMethodUsername, model.UserIdentityProviderLocal, username).
			Count(&identityCount).Error; err != nil {
			return err
		}
		if identityCount > 0 {
			return errors.New("username already exists")
		}

		passwordHash, err := common.HashPassword(password)
		if err != nil {
			return err
		}

		usernamePtr := username
		var emailPtr *string
		if email != "" {
			emailPtr = &email
		}
		user := &model.User{
			Username:    &usernamePtr,
			DisplayName: displayName,
			Email:       emailPtr,
			Role:        common.RoleSuper,
			Status:      common.UserStatusEnabled,
			Quota:       0,
		}
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		now := time.Now()
		identity := model.UserIdentity{
			UserID:       user.ID,
			Method:       model.UserIdentityMethodUsername,
			Provider:     model.UserIdentityProviderLocal,
			Identifier:   username,
			PasswordHash: passwordHash,
			VerifiedAt:   &now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return err
		}

		for category, values := range buildDefaultSettings() {
			for key, value := range values {
				setting := model.Setting{Key: key, Value: value, Category: category}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "key"}},
					DoNothing: true,
				}).Create(&setting).Error; err != nil {
					return err
				}
			}
		}
		bootstrapQuota, err := bootstrapAdminQuota(tx)
		if err != nil {
			return err
		}
		if bootstrapQuota > 0 {
			if err := tx.Model(user).Update("quota", bootstrapQuota).Error; err != nil {
				return err
			}
			user.Quota = bootstrapQuota
		}

		created = user
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, errors.New("system already initialized")
		}
		return nil, err
	}
	if s.settingService != nil {
		_ = s.settingService.LoadCache()
	}
	return created, nil
}

func buildDefaultSettings() map[string]map[string]string {
	jwtSecret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if jwtSecret == "" {
		jwtSecret, _ = common.GenerateRandomString(32)
	}
	return map[string]map[string]string{
		"server": {
			"server.port": "3000",
			"server.mode": "release",
		},
		"jwt": {
			"jwt.secret":             jwtSecret,
			"jwt.admin_expire_hours": "24",
			"jwt.user_expire_hours":  "168",
		},
		"rate_limit": {
			"rate_limit.enabled":           "true",
			"rate_limit.global_per_min":    "1000",
			"rate_limit.per_token_per_min": "60",
			"rate_limit.per_ip_per_min":    "30",
		},
		"relay": {
			"relay.timeout":                 "120",
			"relay.retry_count":             "0",
			"relay.retry_on_status":         "[429,500,502,503,504]",
			"relay.error_auto_ban":          "true",
			"relay.error_ban_threshold":     "10",
			"relay.max_request_body_bytes":  "10485760",
			"relay.max_response_body_bytes": "10485760",
			"relay.routerx_max_hops":        "3",
			"relay.log_body_max_bytes":      "0",
		},
		"routing": {
			"routing.channel_cache.enabled":     "true",
			"routing.channel_cache.preload":     "true",
			"routing.channel_cache.ttl_seconds": "60",
			"routing.channel_cache.version":     "1",
		},
		"billing": {
			"billing.default_ratio":                     "1.0",
			"billing.bootstrap_admin_quota":             "100000000",
			"billing.default_user_channel_group_access": `["default"]`,
			"billing.user_group_ratios":                 `{}`,
			"billing.channel_group_ratios":              `{}`,
			"billing.model_group_ratios":                `{}`,
			"billing.user_group_channel_ratios":         `{}`,
			"billing.user_group_channel_group_access":   `{}`,
			"billing.usage_missing_strategy":            "minimum",
		},
		"payment": {
			"payment.stripe.enabled":                       "false",
			"payment.epay.enabled":                         "false",
			"payment.epay.gateway":                         "",
			"payment.epay.pid":                             "",
			"payment.epay.notify_url":                      "",
			"payment.epay.return_url":                      "",
			"payment.epay.refund_url":                      "",
			"payment.currency":                             "usd",
			"payment.order_expire_minutes":                 "30",
			"payment.refund.auto_deduct":                   "false",
			"payment.refund.allow_negative_balance":        "false",
			"payment.dispute.auto_disable_tokens":          "false",
			"payment.manual_adjust.require_reason":         "true",
			"payment.manual_adjust.large_amount_threshold": "0",
		},
		"log": {
			"log.body_max_bytes":        "0",
			"log.request_body_enabled":  "false",
			"log.response_body_enabled": "false",
		},
		"observability": {
			"observability.metrics_enabled":   "false",
			"observability.audit_enabled":     "true",
			"observability.request_id_header": "X-Request-Id",
		},
		"ready": {
			"ready.production_strict": "true",
		},
	}
}

func bootstrapAdminQuota(tx *gorm.DB) (int64, error) {
	var setting model.Setting
	if err := tx.Where("key = ?", "billing.bootstrap_admin_quota").First(&setting).Error; err != nil {
		return 0, err
	}
	quota, err := strconv.ParseInt(strings.TrimSpace(setting.Value), 10, 64)
	if err != nil || quota < 0 {
		return 0, errors.New("billing.bootstrap_admin_quota must be a non-negative integer")
	}
	return quota, nil
}
