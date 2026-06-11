package service

import (
	"errors"
	"os"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"strings"
	"time"

	"gorm.io/gorm"
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
	username = strings.TrimSpace(username)
	email = normalizeEmail(email)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	if len(password) < 6 {
		return nil, errors.New("password length must be at least 6")
	}
	if displayName == "" {
		displayName = username
	}

	var user *model.User
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		if exists, err := identityExists(tx, model.UserIdentityMethodUsername, username); err != nil {
			return err
		} else if exists {
			return errors.New("username already exists")
		}
		if email != "" {
			if exists, err := identityExists(tx, model.UserIdentityMethodEmail, email); err != nil {
				return err
			} else if exists {
				return errors.New("email already exists")
			}
		}

		hash, err := common.HashPassword(password)
		if err != nil {
			return err
		}
		usernamePtr := username
		var emailPtr *string
		if email != "" {
			emailPtr = &email
		}
		u := &model.User{
			Username:    &usernamePtr,
			DisplayName: displayName,
			Email:       emailPtr,
			Role:        common.RoleUser,
			Status:      common.UserStatusEnabled,
		}
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		now := time.Now()
		identities := []model.UserIdentity{{
			UserID:       u.ID,
			Method:       model.UserIdentityMethodUsername,
			Provider:     model.UserIdentityProviderLocal,
			Identifier:   username,
			PasswordHash: hash,
			VerifiedAt:   &now,
		}}
		if email != "" {
			identities = append(identities, model.UserIdentity{
				UserID:       u.ID,
				Method:       model.UserIdentityMethodEmail,
				Provider:     model.UserIdentityProviderLocal,
				Identifier:   email,
				PasswordHash: hash,
				VerifiedAt:   &now,
			})
		}
		if err := tx.Create(&identities).Error; err != nil {
			return err
		}
		user = u
		return nil
	})
	return user, err
}

// AdminLogin 管理员登录。
// 1. 查 user_identities 并关联 users: role>=1, status=1
// 2. 验证密码
// 3. 生成 JWT / Session Cookie
// 4. 返回用户信息 (不返密码)
func (s *AuthService) AdminLogin(username, password string) (*model.User, string, error) {
	user, token, err := s.UserLogin(username, password)
	if err != nil {
		return nil, "", err
	}
	if user.Role < common.RoleAdmin {
		return nil, "", errors.New("admin permission required")
	}
	return user, token, nil
}

// UserLogin 用户登录 (返回 JWT)。
// 支持通过已启用的 username/email/phone 本地身份匹配密码。
func (s *AuthService) UserLogin(username, password string) (*model.User, string, error) {
	account := strings.TrimSpace(username)
	if account == "" || password == "" {
		return nil, "", errors.New("account or credential is invalid")
	}
	identity, err := findLocalIdentity(account)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errors.New("account or credential is invalid")
		}
		return nil, "", err
	}
	if identity.User == nil || identity.User.Status != common.UserStatusEnabled {
		return nil, "", errors.New("account or credential is invalid")
	}
	if !common.CheckPassword(password, identity.PasswordHash) {
		return nil, "", errors.New("account or credential is invalid")
	}

	now := time.Now()
	_ = internal.DB.Model(identity).Update("last_used_at", &now).Error

	secret, err := GetJWTSecret()
	if err != nil {
		return nil, "", err
	}
	expireHours := GetUserJWTExpireHours()
	sessionID, err := common.GenerateRandomString(16)
	if err != nil {
		return nil, "", err
	}
	token, err := common.SignUserJWT(identity.User.ID, identity.User.Role, sessionID, time.Duration(expireHours)*time.Hour, secret)
	if err != nil {
		return nil, "", err
	}
	return identity.User, token, nil
}

// ChangePassword 修改密码。
func (s *AuthService) ChangePassword(userID uint, oldPassword, newPassword string) error {
	if len(newPassword) < 6 {
		return errors.New("new password length must be at least 6")
	}
	var identity model.UserIdentity
	if err := internal.DB.Where(
		"user_id = ? AND method = ? AND provider = ?",
		userID,
		model.UserIdentityMethodUsername,
		model.UserIdentityProviderLocal,
	).First(&identity).Error; err != nil {
		return err
	}
	if !common.CheckPassword(oldPassword, identity.PasswordHash) {
		return errors.New("old password is invalid")
	}
	hash, err := common.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return internal.DB.Model(&identity).Update("password_hash", hash).Error
}

// Logout 管理员登出 (清除 Session)。
func (s *AuthService) Logout(sessionID string) error {
	return nil
}

// ValidateAdminSession 验证 Admin 会话有效性。
func (s *AuthService) ValidateAdminSession(sessionID string) (*model.User, error) {
	return nil, errors.New("session validation is not used; use User JWT")
}

func GetJWTSecret() (string, error) {
	if secret := strings.TrimSpace(os.Getenv("JWT_SECRET")); secret != "" {
		return secret, nil
	}
	if internal.DB == nil {
		return "", errors.New("database is not initialized")
	}
	var setting model.Setting
	if err := internal.DB.Where("key = ?", "jwt.secret").First(&setting).Error; err != nil {
		return "", errors.New("jwt secret is not configured")
	}
	if strings.TrimSpace(setting.Value) == "" {
		return "", errors.New("jwt secret is empty")
	}
	return setting.Value, nil
}

func GetUserJWTExpireHours() int {
	if internal.DB == nil {
		return 168
	}
	var setting model.Setting
	if err := internal.DB.Where("key = ?", "jwt.user_expire_hours").First(&setting).Error; err != nil {
		return 168
	}
	return common.ParsePositiveInt(setting.Value, 168)
}

func identityExists(tx *gorm.DB, method, identifier string) (bool, error) {
	var count int64
	if err := tx.Model(&model.UserIdentity{}).
		Where("method = ? AND provider = ? AND identifier = ?", method, model.UserIdentityProviderLocal, identifier).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func findLocalIdentity(account string) (*model.UserIdentity, error) {
	candidates := []struct {
		method     string
		identifier string
	}{
		{model.UserIdentityMethodUsername, account},
	}
	if strings.Contains(account, "@") {
		candidates = append([]struct {
			method     string
			identifier string
		}{{model.UserIdentityMethodEmail, normalizeEmail(account)}}, candidates...)
	}
	if strings.HasPrefix(account, "+") {
		candidates = append([]struct {
			method     string
			identifier string
		}{{model.UserIdentityMethodPhone, account}}, candidates...)
	}

	var lastErr error
	for _, candidate := range candidates {
		var identity model.UserIdentity
		err := internal.DB.Preload("User").Where(
			"method = ? AND provider = ? AND identifier = ?",
			candidate.method,
			model.UserIdentityProviderLocal,
			candidate.identifier,
		).First(&identity).Error
		if err == nil {
			return &identity, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = gorm.ErrRecordNotFound
	}
	return nil, lastErr
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
