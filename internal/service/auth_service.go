package service

import (
	"errors"
	"os"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrSelfRegistrationDisabled     = errors.New("self registration is disabled")
	ErrUsernameRegistrationDisabled = errors.New("username registration is disabled")
	ErrRegistrationCaptchaRequired  = errors.New("registration captcha is required")
)

type AuthService struct{}

func NewAuthService() *AuthService {
	return &AuthService{}
}

type RegisterResult struct {
	User      *model.User
	Recovered bool
}

// Register 用户注册。
// 1. 校验启用的账号身份唯一性
// 2. bcrypt 哈希密码
// 3. 创建用户记录和本地 UserIdentity，或恢复已注销的普通用户
// 4. 返回用户信息
func (s *AuthService) Register(username, password, displayName, email string) (*RegisterResult, error) {
	if err := registrationPolicyError(); err != nil {
		return nil, err
	}
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

	var result *RegisterResult
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		usernameIdentity, err := findIdentityForRecovery(tx, model.UserIdentityMethodUsername, username)
		if err != nil {
			return err
		}
		if usernameIdentity != nil {
			if usernameIdentity.User == nil || usernameIdentity.User.Role != common.RoleUser || usernameIdentity.User.Status != common.UserStatusDisabled {
				return errors.New("username already exists")
			}
			recovered, err := recoverRegisteredUser(tx, usernameIdentity, password, displayName, email)
			if err != nil {
				return err
			}
			result = &RegisterResult{User: recovered, Recovered: true}
			return nil
		}
		if email != "" {
			if exists, err := identityExists(tx, model.UserIdentityMethodEmail, email); err != nil {
				return err
			} else if exists {
				return errors.New("email already exists")
			}
		}
		quota := registrationDefaultQuota()
		groupID, err := registrationDefaultGroupID(tx)
		if err != nil {
			return err
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
			Quota:       quota,
			Status:      common.UserStatusEnabled,
			GroupID:     groupID,
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
		result = &RegisterResult{User: u}
		return nil
	})
	return result, err
}

func findIdentityForRecovery(tx *gorm.DB, method, identifier string) (*model.UserIdentity, error) {
	var identity model.UserIdentity
	err := tx.Preload("User").
		Where("method = ? AND provider = ? AND identifier = ?", method, model.UserIdentityProviderLocal, identifier).
		First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

func recoverRegisteredUser(tx *gorm.DB, identity *model.UserIdentity, password, displayName, email string) (*model.User, error) {
	if identity == nil || identity.User == nil {
		return nil, errors.New("account identity is invalid")
	}
	if email != "" {
		emailIdentity, err := findIdentityForRecovery(tx, model.UserIdentityMethodEmail, email)
		if err != nil {
			return nil, err
		}
		if emailIdentity != nil && emailIdentity.UserID != identity.UserID {
			return nil, errors.New("email already exists")
		}
	}
	hash, err := common.HashPassword(password)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{
		"status": common.UserStatusEnabled,
	}
	if strings.TrimSpace(displayName) != "" {
		updates["display_name"] = strings.TrimSpace(displayName)
	}
	if email != "" {
		emailPtr := email
		updates["email"] = &emailPtr
	}
	if err := tx.Model(&model.User{}).Where("id = ? AND role = ?", identity.UserID, common.RoleUser).Updates(updates).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	if err := tx.Model(&model.UserIdentity{}).
		Where("user_id = ? AND provider = ?", identity.UserID, model.UserIdentityProviderLocal).
		Updates(map[string]interface{}{
			"password_hash": hash,
			"verified_at":   &now,
		}).Error; err != nil {
		return nil, err
	}
	var recovered model.User
	if err := tx.First(&recovered, identity.UserID).Error; err != nil {
		return nil, err
	}
	return &recovered, nil
}

func registrationPolicyError() error {
	settingSvc := NewSettingService()
	if enabled, err := settingSvc.GetBool("auth.register.enabled"); err != nil || !enabled {
		return ErrSelfRegistrationDisabled
	}
	if enabled, err := settingSvc.GetBool("auth.register.username.enabled"); err != nil || !enabled {
		return ErrUsernameRegistrationDisabled
	}
	// Captcha-backed self-registration is intentionally fail-closed until the captcha service lands.
	if required, err := settingSvc.GetBool("auth.register.captcha.required"); err == nil && required {
		return ErrRegistrationCaptchaRequired
	}
	return nil
}

func registrationDefaultQuota() int64 {
	raw, err := NewSettingService().Get("auth.register.default_quota")
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func registrationDefaultGroupID(tx *gorm.DB) (*uint, error) {
	raw, err := NewSettingService().Get("auth.register.default_group_id")
	if err != nil {
		return nil, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var group model.Group
	if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
		if err := tx.First(&group, uint(id)).Error; err != nil {
			return nil, err
		}
		groupID := group.ID
		return &groupID, nil
	}
	err = tx.Where("name = ?", raw).First(&group).Error
	if err == nil {
		groupID := group.ID
		return &groupID, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) && raw == "default" {
		return nil, nil
	}
	return nil, err
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
		if !localPasswordLoginEnabled(candidate.method) {
			continue
		}
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

func localPasswordLoginEnabled(method string) bool {
	switch method {
	case model.UserIdentityMethodUsername:
		// Username/password is the baseline local login method and cannot be disabled by settings.
		return true
	case model.UserIdentityMethodEmail:
		return loginBoolSettingDefault("auth.login.email_password.enabled", false)
	case model.UserIdentityMethodPhone:
		return loginBoolSettingDefault("auth.login.phone_password.enabled", false)
	default:
		return false
	}
}

func loginBoolSettingDefault(key string, fallback bool) bool {
	enabled, err := NewSettingService().GetBool(key)
	if err != nil {
		return fallback
	}
	return enabled
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
