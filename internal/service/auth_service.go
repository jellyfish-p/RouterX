package service

import (
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var (
	ErrSelfRegistrationDisabled       = errors.New("self registration is disabled")
	ErrUsernameRegistrationDisabled   = errors.New("username registration is disabled")
	ErrEmailRegistrationDisabled      = errors.New("email registration is disabled")
	ErrPhoneRegistrationDisabled      = errors.New("phone registration is disabled")
	ErrRegistrationCaptchaRequired    = errors.New("registration captcha is required")
	ErrCaptchaStoreUnavailable        = errors.New("captcha store is not available")
	ErrLoginCodeDisabled              = errors.New("code login is disabled")
	ErrLoginCodeUnsupported           = errors.New("code login only supports email or phone accounts")
	ErrLoginCodeVerifierUnavailable   = errors.New("code login verifier is not available")
	ErrOAuthProviderDisabled          = errors.New("oauth provider is disabled")
	ErrOAuthInvalidCallback           = errors.New("oauth callback is invalid")
	ErrOAuthIdentityNotBound          = errors.New("oauth identity is not bound")
	ErrOAuthIdentityAlreadyBound      = errors.New("oauth identity is already bound")
	ErrOAuthRegistrationDisabled      = errors.New("oauth registration is disabled")
	ErrOAuthRegistrationTicketInvalid = errors.New("oauth registration ticket is invalid")
	ErrUserIdentityNotFound           = errors.New("user identity is not found")
	ErrUserIdentityPrimary            = errors.New("primary username identity cannot be unbound")
)

type AuthService struct{}

const (
	loginCodeRedisKeyPrefix       = "auth:login_code:"
	registerCaptchaRedisKeyPrefix = "auth:register_captcha:"
)

type loginCodeRecord struct {
	Method      string `json:"method"`
	Account     string `json:"account"`
	CodeHash    string `json:"code_hash"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
}

type registerCaptchaRecord struct {
	CodeHash    string `json:"code_hash"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
}

func NewAuthService() *AuthService {
	return &AuthService{}
}

type RegisterResult struct {
	User      *model.User
	Recovered bool
}

type RegisterCaptchaChallenge struct {
	CaptchaID       string
	CaptchaImageSVG string
	TTLSeconds      int
}

// RegisterInput carries the unified self-registration payload. Captcha fields
// are consumed from Redis when auth.register.captcha.required is enabled.
type RegisterInput struct {
	Username       string
	Password       string
	DisplayName    string
	Email          string
	Phone          string
	RegisterMethod string
	CaptchaID      string
	CaptchaCode    string
}

func (s *AuthService) CreateRegisterCaptcha() (*RegisterCaptchaChallenge, error) {
	if err := registrationMethodPolicyErrorForMethod("username"); err != nil {
		return nil, err
	}
	if internal.RDB == nil {
		return nil, ErrCaptchaStoreUnavailable
	}
	captchaID, err := common.GenerateRandomString(16)
	if err != nil {
		return nil, err
	}
	code, err := randomNumericCode(6)
	if err != nil {
		return nil, err
	}
	record := registerCaptchaRecord{
		CodeHash:    common.SHA256Hex(code),
		Attempts:    0,
		MaxAttempts: captchaMaxAttempts(),
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	ttl := captchaTTLSeconds()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := internal.RDB.Set(ctx, registerCaptchaRedisKey(captchaID), string(raw), time.Duration(ttl)*time.Second).Err(); err != nil {
		return nil, ErrCaptchaStoreUnavailable
	}
	return &RegisterCaptchaChallenge{
		CaptchaID:       captchaID,
		CaptchaImageSVG: renderRegisterCaptchaSVG(code),
		TTLSeconds:      ttl,
	}, nil
}

type OAuthCallbackResult struct {
	User                 *model.User
	Token                string
	RegistrationRequired *OAuthRegistrationChallenge
}

type OAuthRegistrationChallenge struct {
	Provider          string
	Ticket            string
	SuggestedUsername string
	Email             string
}

type OAuthRegistrationResult struct {
	User      *model.User
	Token     string
	Identity  *model.UserIdentity
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

type oauthRegistrationTicketClaims struct {
	Type              string `json:"typ"`
	Provider          string `json:"provider"`
	Identifier        string `json:"identifier"`
	Email             string `json:"email,omitempty"`
	SuggestedUsername string `json:"suggested_username,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	ExpiresAt         int64  `json:"exp"`
	IssuedAt          int64  `json:"iat"`
}

// Register 用户注册。
// 1. 校验启用的账号身份唯一性
// 2. bcrypt 哈希密码
// 3. 创建用户记录和本地 UserIdentity，或恢复已注销的普通用户；密码只保存到 username/local 主身份
// 4. 返回用户信息
func (s *AuthService) Register(input RegisterInput) (*RegisterResult, error) {
	method := normalizeRegisterMethod(input.RegisterMethod)
	if method == "" {
		return nil, errors.New("register_method is invalid")
	}
	if err := registrationMethodPolicyErrorForMethod(method); err != nil {
		return nil, err
	}
	username := strings.TrimSpace(input.Username)
	password := input.Password
	displayName := strings.TrimSpace(input.DisplayName)
	email := normalizeEmail(input.Email)
	phone := normalizePhone(input.Phone)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	if len(password) < 6 {
		return nil, errors.New("password length must be at least 6")
	}
	if displayName == "" {
		displayName = username
	}
	if method == "email" && email == "" {
		return nil, errors.New("email is required")
	}
	if method == "phone" && phone == "" {
		return nil, errors.New("phone is required")
	}
	if err := registrationCaptchaPolicyError(input.CaptchaID, input.CaptchaCode); err != nil {
		return nil, err
	}

	var result *RegisterResult
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		created, err := registerPasswordUserTx(tx, method, username, password, displayName, email, phone)
		if err != nil {
			return err
		}
		result = created
		return nil
	})
	return result, err
}

func registerPasswordUserTx(tx *gorm.DB, registerMethod, username, password, displayName, email, phone string) (*RegisterResult, error) {
	recoveryIdentity, err := findRegistrationRecoveryIdentity(tx, registerMethod, username, email, phone)
	if err != nil {
		return nil, err
	}
	if recoveryIdentity != nil {
		if !isRecoverableRegistrationIdentity(recoveryIdentity) {
			return nil, registrationIdentityConflictError(recoveryIdentity.Method)
		}
		recovered, err := recoverRegisteredUser(tx, recoveryIdentity, password, displayName, email, phone)
		if err != nil {
			return nil, err
		}
		return &RegisterResult{User: recovered, Recovered: true}, nil
	}
	if exists, err := identityExists(tx, model.UserIdentityMethodUsername, username); err != nil {
		return nil, err
	} else if exists {
		return nil, errors.New("username already exists")
	}
	if email != "" {
		if exists, err := identityExists(tx, model.UserIdentityMethodEmail, email); err != nil {
			return nil, err
		} else if exists {
			return nil, errors.New("email already exists")
		}
	}
	if phone != "" {
		if exists, err := identityExists(tx, model.UserIdentityMethodPhone, phone); err != nil {
			return nil, err
		} else if exists {
			return nil, errors.New("phone already exists")
		}
	}
	quota := registrationDefaultQuota()
	groupID, err := registrationDefaultGroupID(tx)
	if err != nil {
		return nil, err
	}

	hash, err := common.HashPassword(password)
	if err != nil {
		return nil, err
	}
	usernamePtr := username
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	var phonePtr *string
	if phone != "" {
		phonePtr = &phone
	}
	u := &model.User{
		Username:    &usernamePtr,
		DisplayName: displayName,
		Email:       emailPtr,
		Phone:       phonePtr,
		Role:        common.RoleUser,
		Quota:       quota,
		Status:      common.UserStatusEnabled,
		GroupID:     groupID,
	}
	if err := tx.Create(u).Error; err != nil {
		return nil, err
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
	if phone != "" {
		identities = append(identities, model.UserIdentity{
			UserID:     u.ID,
			Method:     model.UserIdentityMethodPhone,
			Provider:   model.UserIdentityProviderLocal,
			Identifier: phone,
			VerifiedAt: &now,
		})
	}
	if err := tx.Create(&identities).Error; err != nil {
		return nil, err
	}
	return &RegisterResult{User: u}, nil
}

func findRegistrationRecoveryIdentity(tx *gorm.DB, registerMethod, username, email, phone string) (*model.UserIdentity, error) {
	candidates := []struct {
		method     string
		identifier string
	}{}
	switch registerMethod {
	case "email":
		candidates = append(candidates, struct {
			method     string
			identifier string
		}{model.UserIdentityMethodEmail, email})
	case "phone":
		candidates = append(candidates, struct {
			method     string
			identifier string
		}{model.UserIdentityMethodPhone, phone})
	}
	candidates = append(candidates, struct {
		method     string
		identifier string
	}{model.UserIdentityMethodUsername, username})
	if registerMethod != "email" && email != "" {
		candidates = append(candidates, struct {
			method     string
			identifier string
		}{model.UserIdentityMethodEmail, email})
	}
	if registerMethod != "phone" && phone != "" {
		candidates = append(candidates, struct {
			method     string
			identifier string
		}{model.UserIdentityMethodPhone, phone})
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.identifier) == "" {
			continue
		}
		identity, err := findIdentityForRecovery(tx, candidate.method, candidate.identifier)
		if err != nil || identity != nil {
			return identity, err
		}
	}
	return nil, nil
}

func registrationIdentityConflictError(method string) error {
	switch method {
	case model.UserIdentityMethodEmail:
		return errors.New("email already exists")
	case model.UserIdentityMethodPhone:
		return errors.New("phone already exists")
	default:
		return errors.New("username already exists")
	}
}

func findIdentityForRecovery(tx *gorm.DB, method, identifier string) (*model.UserIdentity, error) {
	return findIdentityForRecoveryByProvider(tx, method, model.UserIdentityProviderLocal, identifier)
}

func findIdentityForRecoveryByProvider(tx *gorm.DB, method, provider, identifier string) (*model.UserIdentity, error) {
	var identity model.UserIdentity
	err := tx.Preload("User").
		Where("method = ? AND provider = ? AND identifier = ?", method, provider, identifier).
		First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

func isRecoverableRegistrationIdentity(identity *model.UserIdentity) bool {
	return identity != nil &&
		identity.User != nil &&
		identity.User.Role == common.RoleUser &&
		identity.User.Status == common.UserStatusDisabled
}

func recoverRegisteredUser(tx *gorm.DB, identity *model.UserIdentity, password, displayName, email, phone string) (*model.User, error) {
	if identity == nil || identity.User == nil {
		return nil, errors.New("account identity is invalid")
	}
	var emailIdentity *model.UserIdentity
	var phoneIdentity *model.UserIdentity
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
	if phone != "" {
		var err error
		phoneIdentity, err = findIdentityForRecovery(tx, model.UserIdentityMethodPhone, phone)
		if err != nil {
			return nil, err
		}
		if phoneIdentity != nil && phoneIdentity.UserID != identity.UserID {
			return nil, errors.New("phone already exists")
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
	if phone != "" {
		phonePtr := phone
		updates["phone"] = &phonePtr
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
	if phone != "" {
		if phoneIdentity == nil {
			if err := tx.Create(&model.UserIdentity{
				UserID:     identity.UserID,
				Method:     model.UserIdentityMethodPhone,
				Provider:   model.UserIdentityProviderLocal,
				Identifier: phone,
				VerifiedAt: &now,
			}).Error; err != nil {
				return nil, err
			}
		} else if strings.TrimSpace(phoneIdentity.PasswordHash) != "" {
			if err := tx.Model(&model.UserIdentity{}).Where("id = ?", phoneIdentity.ID).Update("password_hash", "").Error; err != nil {
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

// recoverExternalRegisteredUser reactivates a self-cancelled account that kept
// its OAuth/OIDC identity. Token/API-key state is intentionally left untouched.
func recoverExternalRegisteredUser(tx *gorm.DB, identity *model.UserIdentity, password, displayName, email string) (*model.User, error) {
	if !isRecoverableRegistrationIdentity(identity) {
		return nil, errors.New("external identity is not recoverable")
	}
	recovered, err := recoverRegisteredUser(tx, identity, password, displayName, email, "")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if err := tx.Model(&model.UserIdentity{}).Where("id = ?", identity.ID).Updates(map[string]interface{}{
		"verified_at":   &now,
		"last_used_at":  &now,
		"password_hash": "",
	}).Error; err != nil {
		return nil, err
	}
	identity.VerifiedAt = &now
	identity.LastUsedAt = &now
	identity.PasswordHash = ""
	return recovered, nil
}

func registrationPolicyError() error {
	return registrationPolicyErrorForMethod("username")
}

func registrationPolicyErrorForMethod(method string) error {
	if err := registrationMethodPolicyErrorForMethod(method); err != nil {
		return err
	}
	return registrationCaptchaPolicyError("", "")
}

func registrationMethodPolicyErrorForMethod(method string) error {
	settingSvc := NewSettingService()
	if enabled, err := settingSvc.GetBool("auth.register.enabled"); err != nil || !enabled {
		return ErrSelfRegistrationDisabled
	}
	switch method {
	case "username":
		if enabled, err := settingSvc.GetBool("auth.register.username.enabled"); err != nil || !enabled {
			return ErrUsernameRegistrationDisabled
		}
	case "email":
		if enabled, err := settingSvc.GetBool("auth.register.email.enabled"); err != nil || !enabled {
			return ErrEmailRegistrationDisabled
		}
	case "phone":
		if enabled, err := settingSvc.GetBool("auth.register.phone.enabled"); err != nil || !enabled {
			return ErrPhoneRegistrationDisabled
		}
	default:
		return errors.New("register_method is invalid")
	}
	return nil
}

func registrationCaptchaPolicyError(captchaID, captchaCode string) error {
	required, err := NewSettingService().GetBool("auth.register.captcha.required")
	if err != nil || !required {
		return nil
	}
	if strings.TrimSpace(captchaID) == "" || strings.TrimSpace(captchaCode) == "" {
		return ErrRegistrationCaptchaRequired
	}
	return verifyRegistrationCaptcha(captchaID, captchaCode)
}

func normalizeRegisterMethod(method string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		return "username"
	}
	switch method {
	case "username", "email", "phone":
		return method
	default:
		return ""
	}
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

// UserCodeLogin consumes a Redis-backed email/phone login code and issues a
// normal User JWT. Missing Redis still fails closed so code login never falls
// back to password authentication.
func (s *AuthService) UserCodeLogin(account, captchaID, captchaCode string) (*model.User, string, error) {
	account = strings.TrimSpace(account)
	if account == "" || strings.TrimSpace(captchaID) == "" || strings.TrimSpace(captchaCode) == "" {
		return nil, "", invalidLocalCredentialError()
	}
	method, ok := localCodeLoginMethod(account)
	if !ok {
		return nil, "", ErrLoginCodeUnsupported
	}
	if !localCodeLoginEnabled(method) {
		return nil, "", ErrLoginCodeDisabled
	}
	identity, err := findLocalIdentityByMethod(method, normalizeCodeLoginAccount(method, account))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", invalidLocalCredentialError()
		}
		return nil, "", err
	}
	if identity.User == nil || identity.User.Status != common.UserStatusEnabled {
		return nil, "", invalidLocalCredentialError()
	}
	if _, err := localAccountPasswordHash(identity.UserID); err != nil {
		return nil, "", invalidLocalCredentialError()
	}
	if err := verifyLoginCode(method, account, captchaID, captchaCode); err != nil {
		return nil, "", err
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

// OAuthCallbackLogin exchanges an OAuth code for userinfo. It logs in an
// already-bound oauth identity, or returns a signed registration challenge when
// first-time OAuth registration is explicitly enabled. Matching email alone
// never creates a binding.
func (s *AuthService) OAuthCallbackLogin(provider, code, redirectURI string) (*OAuthCallbackResult, error) {
	cfg, err := loadOAuthProviderConfig(provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(code) == "" {
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
	var identity model.UserIdentity
	err = internal.DB.Preload("User").
		Where("method = ? AND provider = ? AND identifier = ?", model.UserIdentityMethodOAuth, cfg.Provider, identifier).
		First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		challenge, challengeErr := s.oauthRegistrationChallenge(cfg, userInfo, identifier)
		if challengeErr != nil {
			return nil, challengeErr
		}
		return &OAuthCallbackResult{RegistrationRequired: challenge}, nil
	}
	if err != nil {
		return nil, err
	}
	if identity.User == nil {
		return nil, ErrOAuthIdentityNotBound
	}
	if identity.User.Status != common.UserStatusEnabled {
		if isRecoverableRegistrationIdentity(&identity) {
			challenge, challengeErr := s.oauthRegistrationChallenge(cfg, userInfo, identifier)
			if challengeErr != nil {
				return nil, challengeErr
			}
			return &OAuthCallbackResult{RegistrationRequired: challenge}, nil
		}
		return nil, ErrOAuthIdentityNotBound
	}
	now := time.Now()
	_ = internal.DB.Model(&identity).Update("last_used_at", &now).Error
	token, err := signUserLoginToken(identity.User.ID, identity.User.Role)
	if err != nil {
		return nil, err
	}
	return &OAuthCallbackResult{User: identity.User, Token: token}, nil
}

func (s *AuthService) oauthRegistrationChallenge(cfg oauthProviderConfig, userInfo map[string]interface{}, identifier string) (*OAuthRegistrationChallenge, error) {
	if err := oauthRegistrationProviderPolicyError(cfg.Provider); err != nil {
		if errors.Is(err, ErrSelfRegistrationDisabled) ||
			errors.Is(err, ErrUsernameRegistrationDisabled) ||
			errors.Is(err, ErrOAuthRegistrationDisabled) {
			return nil, ErrOAuthIdentityNotBound
		}
		return nil, err
	}
	email := normalizeEmail(oauthUserInfoString(userInfo, "email"))
	suggestedUsername := suggestedOAuthUsername(cfg.Provider, userInfo, email, identifier)
	displayName := oauthUserInfoString(userInfo, "name", "display_name")
	if displayName == "" {
		displayName = suggestedUsername
	}
	claims := oauthRegistrationTicketClaims{
		Type:              "oauth_register",
		Provider:          cfg.Provider,
		Identifier:        identifier,
		Email:             email,
		SuggestedUsername: suggestedUsername,
		DisplayName:       displayName,
		IssuedAt:          time.Now().Unix(),
		ExpiresAt:         time.Now().Add(10 * time.Minute).Unix(),
	}
	ticket, err := signOAuthRegistrationTicket(claims)
	if err != nil {
		return nil, err
	}
	return &OAuthRegistrationChallenge{
		Provider:          cfg.Provider,
		Ticket:            ticket,
		SuggestedUsername: suggestedUsername,
		Email:             email,
	}, nil
}

func (s *AuthService) OAuthRegister(provider, ticket, username, password, displayName, email, captchaID, captchaCode string) (*OAuthRegistrationResult, error) {
	claims, err := parseOAuthRegistrationTicket(ticket)
	if err != nil {
		return nil, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || claims.Provider != provider {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	if err := oauthRegistrationProviderPolicyError(provider); err != nil {
		return nil, err
	}
	username = strings.TrimSpace(username)
	email = normalizeEmail(email)
	if claims.Email != "" {
		email = claims.Email
	}
	if displayName = strings.TrimSpace(displayName); displayName == "" {
		displayName = strings.TrimSpace(claims.DisplayName)
	}
	if displayName == "" {
		displayName = username
	}
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	if len(password) < 6 {
		return nil, errors.New("password length must be at least 6")
	}
	if err := registrationCaptchaPolicyError(captchaID, captchaCode); err != nil {
		return nil, err
	}

	var result *OAuthRegistrationResult
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		existing, err := findIdentityForRecoveryByProvider(tx, model.UserIdentityMethodOAuth, claims.Provider, claims.Identifier)
		if err != nil {
			return err
		}
		if existing != nil {
			if !isRecoverableRegistrationIdentity(existing) {
				return ErrOAuthIdentityAlreadyBound
			}
			recovered, err := recoverExternalRegisteredUser(tx, existing, password, displayName, email)
			if err != nil {
				return err
			}
			result = &OAuthRegistrationResult{
				User:      recovered,
				Identity:  existing,
				Recovered: true,
			}
			return nil
		}

		registered, err := registerPasswordUserTx(tx, "username", username, password, displayName, email, "")
		if err != nil {
			return err
		}
		now := time.Now()
		identity := model.UserIdentity{
			UserID:     registered.User.ID,
			Method:     model.UserIdentityMethodOAuth,
			Provider:   claims.Provider,
			Identifier: claims.Identifier,
			VerifiedAt: &now,
			LastUsedAt: &now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return err
		}
		result = &OAuthRegistrationResult{
			User:      registered.User,
			Identity:  &identity,
			Recovered: registered.Recovered,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil || result.User == nil {
		return nil, ErrOAuthInvalidCallback
	}
	token, err := signUserLoginToken(result.User.ID, result.User.Role)
	if err != nil {
		return nil, err
	}
	result.Token = token
	return result, nil
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

func signOAuthRegistrationTicket(claims oauthRegistrationTicketClaims) (string, error) {
	secret, err := GetJWTSecret()
	if err != nil {
		return "", err
	}
	headerJSON, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := encodedHeader + "." + encodedClaims
	return signingInput + "." + signOAuthRegistrationInput(signingInput, secret), nil
}

func parseOAuthRegistrationTicket(ticket string) (*oauthRegistrationTicketClaims, error) {
	claims, err := parseExternalRegistrationTicket(ticket)
	if err != nil {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	if claims.Type != "oauth_register" {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	return claims, nil
}

func parseExternalRegistrationTicket(ticket string) (*oauthRegistrationTicketClaims, error) {
	secret, err := GetJWTSecret()
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSpace(ticket), ".")
	if len(parts) != 3 {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	signingInput := parts[0] + "." + parts[1]
	expected := signOAuthRegistrationInput(signingInput, secret)
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	var claims oauthRegistrationTicketClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	if claims.Type == "" ||
		claims.Provider == "" ||
		claims.Identifier == "" ||
		claims.ExpiresAt <= time.Now().Unix() {
		return nil, ErrOAuthRegistrationTicketInvalid
	}
	return &claims, nil
}

func signOAuthRegistrationInput(input, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// OAuth callbacks issue a short-lived registration ticket before the user can
// submit a captcha. The captcha gate is therefore applied by OAuthRegister.
func oauthRegistrationProviderPolicyError(provider string) error {
	if err := registrationMethodPolicyErrorForMethod("username"); err != nil {
		return err
	}
	if !loginBoolSettingDefault("auth.register.oauth.enabled", false) {
		return ErrOAuthRegistrationDisabled
	}
	if !loginBoolSettingDefault("oauth."+provider+".register_enabled", false) {
		return ErrOAuthRegistrationDisabled
	}
	return nil
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

func oauthUserInfoString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func suggestedOAuthUsername(provider string, payload map[string]interface{}, email, identifier string) string {
	for _, candidate := range []string{
		oauthUserInfoString(payload, "login", "preferred_username", "nickname", "username"),
		strings.Split(email, "@")[0],
		provider + "-" + common.SHA256Hex(identifier)[:8],
	} {
		if normalized := normalizeOAuthUsername(candidate); len(normalized) >= 3 {
			return normalized
		}
	}
	return "oauth-" + common.SHA256Hex(provider + ":" + identifier)[:8]
}

func normalizeOAuthUsername(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == '.' || r == ' ':
			b.WriteRune('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
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

func invalidLocalCredentialError() error {
	return errors.New("account or credential is invalid")
}

func verifyRegistrationCaptcha(captchaID, captchaCode string) error {
	if internal.RDB == nil {
		return ErrRegistrationCaptchaRequired
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	key := registerCaptchaRedisKey(captchaID)
	raw, err := internal.RDB.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return ErrRegistrationCaptchaRequired
	}
	if err != nil {
		return ErrRegistrationCaptchaRequired
	}
	var record registerCaptchaRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return ErrRegistrationCaptchaRequired
	}
	maxAttempts := record.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = captchaMaxAttempts()
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	codeHash := common.SHA256Hex(strings.TrimSpace(captchaCode))
	codeMatches := strings.TrimSpace(record.CodeHash) != "" && hmac.Equal([]byte(record.CodeHash), []byte(codeHash))
	if !codeMatches {
		return recordFailedRegistrationCaptchaAttempt(ctx, key, record, maxAttempts)
	}
	deleted, err := internal.RDB.Del(ctx, key).Result()
	if err != nil || deleted == 0 {
		return ErrRegistrationCaptchaRequired
	}
	return nil
}

func recordFailedRegistrationCaptchaAttempt(ctx context.Context, key string, record registerCaptchaRecord, maxAttempts int) error {
	record.Attempts++
	if record.Attempts >= maxAttempts {
		if err := internal.RDB.Del(ctx, key).Err(); err != nil {
			return ErrRegistrationCaptchaRequired
		}
		return ErrRegistrationCaptchaRequired
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return ErrRegistrationCaptchaRequired
	}
	if err := internal.RDB.Set(ctx, key, string(raw), redis.KeepTTL).Err(); err != nil {
		return ErrRegistrationCaptchaRequired
	}
	return ErrRegistrationCaptchaRequired
}

func registerCaptchaRedisKey(captchaID string) string {
	return registerCaptchaRedisKeyPrefix + strings.TrimSpace(captchaID)
}

func verifyLoginCode(method, account, captchaID, captchaCode string) error {
	if internal.RDB == nil {
		return ErrLoginCodeVerifierUnavailable
	}
	normalizedAccount := normalizeCodeLoginAccount(method, account)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	key := loginCodeRedisKey(captchaID)
	raw, err := internal.RDB.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return invalidLocalCredentialError()
	}
	if err != nil {
		return ErrLoginCodeVerifierUnavailable
	}
	var record loginCodeRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return ErrLoginCodeVerifierUnavailable
	}
	maxAttempts := record.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = captchaMaxAttempts()
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	recordAccount := normalizeCodeLoginAccount(record.Method, record.Account)
	codeHash := common.SHA256Hex(strings.TrimSpace(captchaCode))
	codeMatches := strings.TrimSpace(record.CodeHash) != "" && hmac.Equal([]byte(record.CodeHash), []byte(codeHash))
	if record.Method != method || recordAccount != normalizedAccount || !codeMatches {
		return recordFailedLoginCodeAttempt(ctx, key, record, maxAttempts)
	}
	deleted, err := internal.RDB.Del(ctx, key).Result()
	if err != nil {
		return ErrLoginCodeVerifierUnavailable
	}
	if deleted == 0 {
		return invalidLocalCredentialError()
	}
	return nil
}

func recordFailedLoginCodeAttempt(ctx context.Context, key string, record loginCodeRecord, maxAttempts int) error {
	record.Attempts++
	if record.Attempts >= maxAttempts {
		if err := internal.RDB.Del(ctx, key).Err(); err != nil {
			return ErrLoginCodeVerifierUnavailable
		}
		return invalidLocalCredentialError()
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return ErrLoginCodeVerifierUnavailable
	}
	if err := internal.RDB.Set(ctx, key, string(raw), redis.KeepTTL).Err(); err != nil {
		return ErrLoginCodeVerifierUnavailable
	}
	return invalidLocalCredentialError()
}

func loginCodeRedisKey(captchaID string) string {
	return loginCodeRedisKeyPrefix + strings.TrimSpace(captchaID)
}

func captchaMaxAttempts() int {
	value, err := NewSettingService().Get("auth.captcha.max_attempts")
	if err != nil {
		return 5
	}
	return common.ParsePositiveInt(value, 5)
}

func captchaTTLSeconds() int {
	value, err := NewSettingService().Get("auth.captcha.ttl_seconds")
	if err != nil {
		return 300
	}
	return common.ParsePositiveInt(value, 300)
}

func randomNumericCode(length int) (string, error) {
	if length <= 0 {
		return "", errors.New("captcha length is invalid")
	}
	const digits = "0123456789"
	var b strings.Builder
	b.Grow(length)
	for i := 0; i < length; i++ {
		n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}
		b.WriteByte(digits[n.Int64()])
	}
	return b.String(), nil
}

func renderRegisterCaptchaSVG(code string) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="160" height="48" viewBox="0 0 160 48" role="img" aria-label="registration captcha"><rect width="160" height="48" rx="6" fill="#f8fafc"/><path d="M8 34 C32 10, 58 44, 84 20 S132 12, 152 30" fill="none" stroke="#94a3b8" stroke-width="2"/><text x="80" y="31" text-anchor="middle" font-family="monospace" font-size="24" font-weight="700" letter-spacing="4" fill="#111827">%s</text></svg>`, code)
}

func identityExists(tx *gorm.DB, method, identifier string) (bool, error) {
	return identityExistsWithProvider(tx, method, model.UserIdentityProviderLocal, identifier)
}

func identityExistsWithProvider(tx *gorm.DB, method, provider, identifier string) (bool, error) {
	var count int64
	if err := tx.Model(&model.UserIdentity{}).
		Where("method = ? AND provider = ? AND identifier = ?", method, provider, identifier).
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

func findLocalIdentityByMethod(method, identifier string) (*model.UserIdentity, error) {
	var identity model.UserIdentity
	err := internal.DB.Preload("User").Where(
		"method = ? AND provider = ? AND identifier = ?",
		method,
		model.UserIdentityProviderLocal,
		identifier,
	).First(&identity).Error
	if err != nil {
		return nil, err
	}
	return &identity, nil
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

func localCodeLoginMethod(account string) (string, bool) {
	if strings.Contains(account, "@") {
		return model.UserIdentityMethodEmail, true
	}
	if strings.HasPrefix(account, "+") {
		return model.UserIdentityMethodPhone, true
	}
	return "", false
}

func localCodeLoginEnabled(method string) bool {
	switch method {
	case model.UserIdentityMethodEmail:
		return loginBoolSettingDefault("auth.login.email_code.enabled", false)
	case model.UserIdentityMethodPhone:
		return loginBoolSettingDefault("auth.login.phone_code.enabled", false)
	default:
		return false
	}
}

func normalizeCodeLoginAccount(method, account string) string {
	switch method {
	case model.UserIdentityMethodEmail:
		return normalizeEmail(account)
	case model.UserIdentityMethodPhone:
		return normalizePhone(account)
	default:
		return strings.TrimSpace(account)
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

func normalizePhone(phone string) string {
	return strings.TrimSpace(phone)
}
