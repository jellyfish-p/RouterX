package service

import (
	"os"

	"routerx/internal"
	"routerx/internal/model"
)

type SetupService struct {
	userService    *UserService
	settingService *SettingService
}

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
	// TODO: Phase 1 实现
	// 1. 检查是否已初始化, 若已初始化则返回错误
	// 2. 开启事务:
	//    a. 创建超级管理员用户和本地 UserIdentity (role=2)
	//    b. 批量写入 settings 表默认值 (jwt.secret, server.port, ...)
	// 3. 提交事务, 加载 settings 到 Redis 缓存
	_ = internal.DB
	return nil, nil
}

var defaultSettings = map[string]map[string]string{
	"server": {
		"server.port": "3000",
		"server.mode": "release",
	},
	"jwt": {
		"jwt.secret":             os.Getenv("JWT_SECRET"), // 生产和多实例应通过环境变量指定
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
		"relay.timeout":             "120",
		"relay.retry_count":         "2",
		"relay.error_auto_ban":      "true",
		"relay.error_ban_threshold": "10",
	},
	"cors": {
		"cors.allowed_origins":   `["http://localhost:5173","http://localhost:5174"]`,
		"cors.allow_credentials": "true",
	},
	"billing": {
		"billing.default_ratio": "1.0",
	},
}
