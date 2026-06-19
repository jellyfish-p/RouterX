package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
	ErrOAuthProviderDisabled        = errors.New("oauth provider is disabled")
	ErrOAuthInvalidCallback         = errors.New("oauth callback is invalid")
	ErrOAuthIdentityNotBound        = errors.New("oauth identity is not bound")
	ErrOAuthIdentityAlreadyBound    = errors.New("oauth identity is already bound")
	ErrUserIdentityNotFound         = errors.New("user identity is not found")
	ErrUserIdentityPrimary          = errors.New("primary username identity cannot be unbound")
)

type AuthService struct{}

func NewAuthService() *AuthService {
	return &AuthService{}
}

type RegisterResult struct {
	User      *model.User
	Recovered bool
}

type oauthProviderConfig struct {
	Provider     string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserinfoURL  string
	Scopes       string
}

// Register 用户注册。
// 1. 校验启用的账号身份唯一性
// 2. bcrypt 哈希密码
// 3. 创建用户记录和本地 UserIdentity，或恢复已注销的普通用户；密码只保存到 username/local 主身份
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
				UserID:     u.ID,
				Method:     model.UserIdentityMethodEmail,
				Provider:   model.UserIdentityProviderLocal,
				Identifier: email,
				VerifiedAt: &now,
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
	var emailIdentity *model.UserIdentity
	if email != "" {
		var err error
		emailIdentity, err = findIdentityForRecovery(tx, model.UserIdentityMethodEmail, email)
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
		Where(
			"user_id = ? AND method = ? AND provider = ?",
			identity.UserID,
			model.UserIdentityMethodUsername,
			model.UserIdentityProviderLocal,
		).
		Updates(map[string]interface{}{
			"password_hash": hash,
			"verified_at":   &now,
		}).Error; err != nil {
		return nil, err
	}
	if email != "" {
		if emailIdentity == nil {
			if err := tx.Create(&model.UserIdentity{
				UserID:     identity.UserID,
				Method:     model.UserIdentityMethodEmail,
				Provider:   model.UserIdentityProviderLocal,
				Identifier: email,
				VerifiedAt: &now,
			}).Error; err != nil {
				return nil, err
			}
		} else if strings.TrimSpace(emailIdentity.PasswordHash) != "" {
			if err := tx.Model(&model.UserIdentity{}).Where("id = ?", emailIdentity.ID).Update("password_hash", "").Error; err != nil {
				return nil, err
			}
		}
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
	passwordHash, err := localAccountPasswordHash(identity.UserID)
	if err != nil || !common.CheckPassword(password, passwordHash) {
		return nil, "", errors.New("account or credential is invalid")
	}

	now := time.Now()
	_ = internal.DB.Model(identity).Update("last_used_at", &now).Error

	token, err := signUserLoginToken(identity.User.ID, identity.User.Role)
	if err != nil {
		return nil, "", err
	}
	return identity.User, token, nil
}

// OAuthLoginURL builds the provider authorization URL and keeps the generated
// state outside the database. The handler stores state in an HttpOnly cookie.
func (s *AuthService) OAuthLoginURL(provider, state, redirectURI string) (string, error) {
	cfg, err := loadOAuthProviderConfig(provider)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(state) == "" {
		return "", ErrOAuthInvalidCallback
	}
	parsed, err := url.Parse(cfg.AuthURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("oauth auth url is invalid")
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("state", state)
	if strings.TrimSpace(redirectURI) != "" {
		query.Set("redirect_uri", redirectURI)
	}
	if cfg.Scopes != "" {
		query.Set("scope", cfg.Scopes)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// OAuthCallbackLogin exchanges an OAuth code for userinfo and logs in an
// already-bound oauth identity. Matching email alone never creates a binding.
func (s *AuthService) OAuthCallbackLogin(provider, code, redirectURI string) (*model.User, string, error) {
	cfg, err := loadOAuthProviderConfig(provider)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(code) == "" {
		return nil, "", ErrOAuthInvalidCallback
	}
	accessToken, err := exchangeOAuthCode(cfg, code, redirectURI)
	if err != nil {
		return nil, "", err
	}
	userInfo, err := fetchOAuthUserinfo(cfg, accessToken)
	if err != nil {
		return nil, "", err
	}
	identifier := oauthStableIdentifier(userInfo)
	if identifier == "" {
		return nil, "", ErrOAuthInvalidCallback
	}
	var identity model.UserIdentity
	err = internal.DB.Preload("User").
		Where("method = ? AND provider = ? AND identifier = ?", model.UserIdentityMethodOAuth, cfg.Provider, identifier).
		First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", ErrOAuthIdentityNotBound
	}
	if err != nil {
		return nil, "", err
	}
	if identity.User == nil || identity.User.Status != common.UserStatusEnabled {
		return nil, "", ErrOAuthIdentityNotBound
	}
	now := time.Now()
	_ = internal.DB.Model(&identity).Update("last_used_at", &now).Error
	token, err := signUserLoginToken(identity.User.ID, identity.User.Role)
	if err != nil {
		return nil, "", err
	}
	return identity.User, token, nil
}

// OAuthBindCallback binds a provider subject to an existing logged-in user.
// It never matches by email; the provider stable id/sub is the only identity key.
func (s *AuthService) OAuthBindCallback(userID uint, provider, code, redirectURI string) (*model.UserIdentity, error) {
	cfg, err := loadOAuthProviderConfig(provider)
	if err != nil {
		return nil, err
	}
	if userID == 0 || strings.TrimSpace(code) == "" {
		return nil, ErrOAuthInvalidCallback
	}
	accessToken, err := exchangeOAuthCode(cfg, code, redirectURI)
	if err != nil {
		return nil, err
	}
	userInfo, err := fetchOAuthUserinfo(cfg, accessToken)
	if err != nil {
		return nil, err
	}
	identifier := oauthStableIdentifier(userInfo)
	if identifier == "" {
		return nil, ErrOAuthInvalidCallback
	}
	var bound *model.UserIdentity
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrOAuthInvalidCallback
		}
		var existing model.UserIdentity
		err := tx.Where("method = ? AND provider = ? AND identifier = ?", model.UserIdentityMethodOAuth, cfg.Provider, identifier).First(&existing).Error
		now := time.Now()
		switch {
		case err == nil:
			if existing.UserID != userID {
				return ErrOAuthIdentityAlreadyBound
			}
			if err := tx.Model(&existing).Updates(map[string]interface{}{
				"verified_at":  &now,
				"last_used_at": &now,
			}).Error; err != nil {
				return err
			}
			existing.VerifiedAt = &now
			existing.LastUsedAt = &now
			bound = &existing
			return nil
		case errors.Is(err, gorm.ErrRecordNotFound):
			identity := model.UserIdentity{
				UserID:     userID,
				Method:     model.UserIdentityMethodOAuth,
				Provider:   cfg.Provider,
				Identifier: identifier,
				VerifiedAt: &now,
				LastUsedAt: &now,
			}
			if err := tx.Create(&identity).Error; err != nil {
				return err
			}
			bound = &identity
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	if bound == nil {
		return nil, ErrOAuthInvalidCallback
	}
	return bound, nil
}

// ListUserIdentities returns active login identities owned by the current user.
// It intentionally uses GORM's default scope so soft-deleted identities stay hidden.
func (s *AuthService) ListUserIdentities(userID uint) ([]model.UserIdentity, error) {
	if userID == 0 {
		return nil, ErrUserIdentityNotFound
	}
	var identities []model.UserIdentity
	if err := internal.DB.Where("user_id = ?", userID).Order("id ASC").Find(&identities).Error; err != nil {
		return nil, err
	}
	return identities, nil
}

// UnbindUserIdentity soft-deletes a non-primary login identity owned by userID.
// The required username/local identity is protected so every account remains loginable.
func (s *AuthService) UnbindUserIdentity(userID, identityID uint) (*model.UserIdentity, error) {
	if userID == 0 || identityID == 0 {
		return nil, ErrUserIdentityNotFound
	}
	var removed *model.UserIdentity
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var identity model.UserIdentity
		err := tx.Where("id = ? AND user_id = ?", identityID, userID).First(&identity).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserIdentityNotFound
		}
		if err != nil {
			return err
		}
		if identity.Method == model.UserIdentityMethodUsername && identity.Provider == model.UserIdentityProviderLocal {
			return ErrUserIdentityPrimary
		}
		if err := tx.Delete(&identity).Error; err != nil {
			return err
		}
		removed = &identity
		return nil
	})
	if err != nil {
		return nil, err
	}
	if removed == nil {
		return nil, ErrUserIdentityNotFound
	}
	return removed, nil
}

func signUserLoginToken(userID uint, role int) (string, error) {
	secret, err := GetJWTSecret()
	if err != nil {
		return "", err
	}
	expireHours := GetUserJWTExpireHours()
	sessionID, err := common.GenerateRandomString(16)
	if err != nil {
		return "", err
	}
	return common.SignUserJWT(userID, role, sessionID, time.Duration(expireHours)*time.Hour, secret)
}

func loadOAuthProviderConfig(provider string) (oauthProviderConfig, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !validExternalProviderName(provider) {
		return oauthProviderConfig{}, ErrOAuthProviderDisabled
	}
	if !loginBoolSettingDefault("auth.login.oauth.enabled", false) || !loginBoolSettingDefault("oauth."+provider+".enabled", false) {
		return oauthProviderConfig{}, ErrOAuthProviderDisabled
	}
	cfg := oauthProviderConfig{
		Provider:     provider,
		ClientID:     oauthProviderSetting(provider, "client_id"),
		ClientSecret: oauthProviderSetting(provider, "client_secret"),
		AuthURL:      oauthProviderSetting(provider, "auth_url"),
		TokenURL:     oauthProviderSetting(provider, "token_url"),
		UserinfoURL:  oauthProviderSetting(provider, "userinfo_url"),
		Scopes:       oauthProviderSetting(provider, "scopes"),
	}
	if cfg.ClientID == "" || cfg.AuthURL == "" || cfg.TokenURL == "" || cfg.UserinfoURL == "" {
		return oauthProviderConfig{}, ErrOAuthProviderDisabled
	}
	return cfg, nil
}

func oauthProviderSetting(provider, suffix string) string {
	value, err := NewSettingService().Get("oauth." + provider + "." + suffix)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func validExternalProviderName(provider string) bool {
	if provider == "" || len(provider) > 64 {
		return false
	}
	for _, r := range provider {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func exchangeOAuthCode(cfg oauthProviderConfig, code, redirectURI string) (string, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("code", strings.TrimSpace(code))
	values.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		values.Set("client_secret", cfg.ClientSecret)
	}
	if strings.TrimSpace(redirectURI) != "" {
		values.Set("redirect_uri", redirectURI)
	}
	req, err := http.NewRequest(http.MethodPost, cfg.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New("oauth token exchange failed")
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", errors.New("oauth token response missing access_token")
	}
	return strings.TrimSpace(payload.AccessToken), nil
}

func fetchOAuthUserinfo(cfg oauthProviderConfig, accessToken string) (map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.UserinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("oauth userinfo fetch failed")
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func oauthStableIdentifier(payload map[string]interface{}) string {
	for _, key := range []string{"id", "sub"} {
		switch value := payload[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case float64:
			if value > 0 {
				return strconv.FormatInt(int64(value), 10)
			}
		}
	}
	return ""
}

func oauthHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func localAccountPasswordHash(userID uint) (string, error) {
	var identity model.UserIdentity
	err := internal.DB.Where(
		"user_id = ? AND method = ? AND provider = ?",
		userID,
		model.UserIdentityMethodUsername,
		model.UserIdentityProviderLocal,
	).First(&identity).Error
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(identity.PasswordHash) == "" {
		return "", errors.New("account password is not configured")
	}
	return identity.PasswordHash, nil
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
