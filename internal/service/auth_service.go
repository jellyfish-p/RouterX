package service

import (
	"routerx/internal"
	"routerx/internal/model"
)

type AuthService struct{}

func NewAuthService() *AuthService {
	return &AuthService{}
}

// Register 用户注册。
// 1. 校验启用的账号身份唯一性
// 2. bcrypt 哈希密码
// 3. 创建用户记录和本地 UserIdentity (role=0, status=1)
// 4. 返回用户信息
func (s *AuthService) Register(username, password, displayName, email string) (*model.User, error) {
	// TODO: Phase 2 实现
	_ = internal.DB
	return nil, nil
}

// AdminLogin 管理员登录。
// 1. 查 user_identities 并关联 users: role>=1, status=1
// 2. 验证密码
// 3. 生成 JWT / Session Cookie
// 4. 返回用户信息 (不返密码)
func (s *AuthService) AdminLogin(username, password string) (*model.User, string, error) {
	// TODO: Phase 2 实现
	_ = internal.DB
	return nil, "", nil
}

// UserLogin 用户登录 (返回 JWT)。
// 支持通过已启用的 username/email/phone 本地身份匹配密码。
func (s *AuthService) UserLogin(username, password string) (*model.User, string, error) {
	// TODO: Phase 5 实现
	_ = internal.DB
	return nil, "", nil
}

// ChangePassword 修改密码。
func (s *AuthService) ChangePassword(userID uint, oldPassword, newPassword string) error {
	// TODO: Phase 2 实现
	_ = internal.DB
	return nil
}

// Logout 管理员登出 (清除 Session)。
func (s *AuthService) Logout(sessionID string) error {
	// TODO: Phase 2 实现
	return nil
}

// ValidateAdminSession 验证 Admin 会话有效性。
func (s *AuthService) ValidateAdminSession(sessionID string) (*model.User, error) {
	// TODO: Phase 2 实现
	return nil, nil
}
