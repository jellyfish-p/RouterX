package service

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrOIDCProviderDisabled          = errors.New("oidc provider is disabled")
	ErrOIDCInvalidCallback           = errors.New("oidc callback is invalid")
	ErrOIDCIdentityNotBound          = errors.New("oidc identity is not bound")
	ErrOIDCIdentityAlreadyBound      = errors.New("oidc identity is already bound")
	ErrOIDCRegistrationDisabled      = errors.New("oidc registration is disabled")
	ErrOIDCRegistrationTicketInvalid = errors.New("oidc registration ticket is invalid")
)

type oidcProviderConfig struct {
	Provider     string
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       string
}

type oidcDiscoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type oidcTokenClaims struct {
	Issuer            string
	Subject           string
	Email             string
	SuggestedUsername string
	DisplayName       string
}

type OIDCCallbackResult struct {
	User                 *model.User
	Token                string
	RegistrationRequired *OIDCRegistrationChallenge
}

type OIDCRegistrationChallenge struct {
	Provider          string
	Ticket            string
	SuggestedUsername string
	Email             string
}

type OIDCRegistrationResult struct {
	User      *model.User
	Token     string
	Identity  *model.UserIdentity
	Recovered bool
}

// OIDCLoginURL builds an Authorization Code Flow redirect from provider discovery.
// State is checked by the handler, while nonce is embedded into the ID Token.
func (s *AuthService) OIDCLoginURL(provider, state, nonce, redirectURI string) (string, error) {
	cfg, err := loadOIDCProviderConfig(provider)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(state) == "" || strings.TrimSpace(nonce) == "" {
		return "", ErrOIDCInvalidCallback
	}
	discovery, err := discoverOIDCProvider(cfg)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(discovery.AuthorizationEndpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrOIDCProviderDisabled
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("state", state)
	query.Set("nonce", nonce)
	query.Set("scope", oidcScopes(cfg.Scopes))
	if strings.TrimSpace(redirectURI) != "" {
		query.Set("redirect_uri", redirectURI)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// OIDCCallbackLogin validates the signed ID Token. It logs in an already-bound
// OIDC identity, or returns a signed registration challenge when first-time
// OIDC registration is explicitly enabled.
func (s *AuthService) OIDCCallbackLogin(provider, code, redirectURI, expectedNonce string) (*OIDCCallbackResult, error) {
	cfg, err := loadOIDCProviderConfig(provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(expectedNonce) == "" {
		return nil, ErrOIDCInvalidCallback
	}
	discovery, err := discoverOIDCProvider(cfg)
	if err != nil {
		return nil, err
	}
	idToken, err := exchangeOIDCCode(cfg, discovery, code, redirectURI)
	if err != nil {
		return nil, err
	}
	claims, err := validateOIDCIDToken(discovery, cfg, idToken, expectedNonce)
	if err != nil {
		return nil, err
	}

	var identity model.UserIdentity
	err = internal.DB.Preload("User").
		Where("method = ? AND provider = ? AND identifier = ?", model.UserIdentityMethodOIDC, cfg.Provider, claims.Subject).
		First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		challenge, challengeErr := s.oidcRegistrationChallenge(cfg, claims)
		if challengeErr != nil {
			return nil, challengeErr
		}
		return &OIDCCallbackResult{RegistrationRequired: challenge}, nil
	}
	if err != nil {
		return nil, err
	}
	if identity.User == nil {
		return nil, ErrOIDCIdentityNotBound
	}
	if identity.User.Status != common.UserStatusEnabled {
		if isRecoverableRegistrationIdentity(&identity) {
			challenge, challengeErr := s.oidcRegistrationChallenge(cfg, claims)
			if challengeErr != nil {
				return nil, challengeErr
			}
			return &OIDCCallbackResult{RegistrationRequired: challenge}, nil
		}
		return nil, ErrOIDCIdentityNotBound
	}
	now := time.Now()
	_ = internal.DB.Model(&identity).Update("last_used_at", &now).Error
	token, err := signUserLoginToken(identity.User.ID, identity.User.Role)
	if err != nil {
		return nil, err
	}
	return &OIDCCallbackResult{User: identity.User, Token: token}, nil
}

func (s *AuthService) oidcRegistrationChallenge(cfg oidcProviderConfig, claims oidcTokenClaims) (*OIDCRegistrationChallenge, error) {
	if err := oidcRegistrationPolicyError(cfg.Provider); err != nil {
		if errors.Is(err, ErrSelfRegistrationDisabled) ||
			errors.Is(err, ErrUsernameRegistrationDisabled) ||
			errors.Is(err, ErrRegistrationCaptchaRequired) ||
			errors.Is(err, ErrOIDCRegistrationDisabled) {
			return nil, ErrOIDCIdentityNotBound
		}
		return nil, err
	}
	email := normalizeEmail(claims.Email)
	suggestedUsername := suggestedOAuthUsername(cfg.Provider, map[string]interface{}{
		"preferred_username": claims.SuggestedUsername,
	}, email, claims.Subject)
	displayName := strings.TrimSpace(claims.DisplayName)
	if displayName == "" {
		displayName = suggestedUsername
	}
	ticketClaims := oauthRegistrationTicketClaims{
		Type:              "oidc_register",
		Provider:          cfg.Provider,
		Identifier:        claims.Subject,
		Email:             email,
		SuggestedUsername: suggestedUsername,
		DisplayName:       displayName,
		IssuedAt:          time.Now().Unix(),
		ExpiresAt:         time.Now().Add(10 * time.Minute).Unix(),
	}
	ticket, err := signOIDCRegistrationTicket(ticketClaims)
	if err != nil {
		return nil, err
	}
	return &OIDCRegistrationChallenge{
		Provider:          cfg.Provider,
		Ticket:            ticket,
		SuggestedUsername: suggestedUsername,
		Email:             email,
	}, nil
}

func (s *AuthService) OIDCRegister(provider, ticket, username, password, displayName, email string) (*OIDCRegistrationResult, error) {
	claims, err := parseOIDCRegistrationTicket(ticket)
	if err != nil {
		return nil, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || claims.Provider != provider {
		return nil, ErrOIDCRegistrationTicketInvalid
	}
	if err := oidcRegistrationPolicyError(provider); err != nil {
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

	var result *OIDCRegistrationResult
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		existing, err := findIdentityForRecoveryByProvider(tx, model.UserIdentityMethodOIDC, claims.Provider, claims.Identifier)
		if err != nil {
			return err
		}
		if existing != nil {
			if !isRecoverableRegistrationIdentity(existing) {
				return ErrOIDCIdentityAlreadyBound
			}
			recovered, err := recoverExternalRegisteredUser(tx, existing, password, displayName, email)
			if err != nil {
				return err
			}
			result = &OIDCRegistrationResult{
				User:      recovered,
				Identity:  existing,
				Recovered: true,
			}
			return nil
		}

		registered, err := registerPasswordUserTx(tx, username, password, displayName, email)
		if err != nil {
			return err
		}
		now := time.Now()
		identity := model.UserIdentity{
			UserID:     registered.User.ID,
			Method:     model.UserIdentityMethodOIDC,
			Provider:   claims.Provider,
			Identifier: claims.Identifier,
			VerifiedAt: &now,
			LastUsedAt: &now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return err
		}
		result = &OIDCRegistrationResult{
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
		return nil, ErrOIDCInvalidCallback
	}
	token, err := signUserLoginToken(result.User.ID, result.User.Role)
	if err != nil {
		return nil, err
	}
	result.Token = token
	return result, nil
}

// OIDCBindCallback binds a verified OIDC subject to an existing logged-in user.
// Email claims are intentionally ignored; the stable sub claim is the identity key.
func (s *AuthService) OIDCBindCallback(userID uint, provider, code, redirectURI, expectedNonce string) (*model.UserIdentity, error) {
	cfg, err := loadOIDCProviderConfig(provider)
	if err != nil {
		return nil, err
	}
	if userID == 0 || strings.TrimSpace(code) == "" || strings.TrimSpace(expectedNonce) == "" {
		return nil, ErrOIDCInvalidCallback
	}
	discovery, err := discoverOIDCProvider(cfg)
	if err != nil {
		return nil, err
	}
	idToken, err := exchangeOIDCCode(cfg, discovery, code, redirectURI)
	if err != nil {
		return nil, err
	}
	claims, err := validateOIDCIDToken(discovery, cfg, idToken, expectedNonce)
	if err != nil {
		return nil, err
	}

	var bound *model.UserIdentity
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrOIDCInvalidCallback
		}
		var existing model.UserIdentity
		err := tx.Where("method = ? AND provider = ? AND identifier = ?", model.UserIdentityMethodOIDC, cfg.Provider, claims.Subject).First(&existing).Error
		now := time.Now()
		switch {
		case err == nil:
			if existing.UserID != userID {
				return ErrOIDCIdentityAlreadyBound
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
				Method:     model.UserIdentityMethodOIDC,
				Provider:   cfg.Provider,
				Identifier: claims.Subject,
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
		return nil, ErrOIDCInvalidCallback
	}
	return bound, nil
}

func signOIDCRegistrationTicket(claims oauthRegistrationTicketClaims) (string, error) {
	return signOAuthRegistrationTicket(claims)
}

func parseOIDCRegistrationTicket(ticket string) (*oauthRegistrationTicketClaims, error) {
	claims, err := parseExternalRegistrationTicket(ticket)
	if err != nil {
		return nil, ErrOIDCRegistrationTicketInvalid
	}
	if claims.Type != "oidc_register" {
		return nil, ErrOIDCRegistrationTicketInvalid
	}
	return claims, nil
}

func oidcRegistrationPolicyError(provider string) error {
	if err := registrationPolicyError(); err != nil {
		return err
	}
	if !loginBoolSettingDefault("auth.register.oidc.enabled", false) {
		return ErrOIDCRegistrationDisabled
	}
	if !loginBoolSettingDefault("oidc."+provider+".register_enabled", false) {
		return ErrOIDCRegistrationDisabled
	}
	return nil
}

func loadOIDCProviderConfig(provider string) (oidcProviderConfig, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !validExternalProviderName(provider) {
		return oidcProviderConfig{}, ErrOIDCProviderDisabled
	}
	if !loginBoolSettingDefault("auth.login.oidc.enabled", false) || !loginBoolSettingDefault("oidc."+provider+".enabled", false) {
		return oidcProviderConfig{}, ErrOIDCProviderDisabled
	}
	cfg := oidcProviderConfig{
		Provider:     provider,
		Issuer:       strings.TrimRight(oidcProviderSetting(provider, "issuer"), "/"),
		ClientID:     oidcProviderSetting(provider, "client_id"),
		ClientSecret: oidcProviderSetting(provider, "client_secret"),
		Scopes:       oidcProviderSetting(provider, "scopes"),
	}
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return oidcProviderConfig{}, ErrOIDCProviderDisabled
	}
	return cfg, nil
}

func oidcProviderSetting(provider, suffix string) string {
	value, err := NewSettingService().Get("oidc." + provider + "." + suffix)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func discoverOIDCProvider(cfg oidcProviderConfig) (oidcDiscoveryDocument, error) {
	discoveryURL := cfg.Issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequest(http.MethodGet, discoveryURL, nil)
	if err != nil {
		return oidcDiscoveryDocument{}, err
	}
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return oidcDiscoveryDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oidcDiscoveryDocument{}, errors.New("oidc discovery failed")
	}
	var discovery oidcDiscoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return oidcDiscoveryDocument{}, err
	}
	discovery.Issuer = strings.TrimRight(strings.TrimSpace(discovery.Issuer), "/")
	if discovery.Issuer != cfg.Issuer || discovery.AuthorizationEndpoint == "" || discovery.TokenEndpoint == "" || discovery.JWKSURI == "" {
		return oidcDiscoveryDocument{}, ErrOIDCProviderDisabled
	}
	return discovery, nil
}

func exchangeOIDCCode(cfg oidcProviderConfig, discovery oidcDiscoveryDocument, code, redirectURI string) (string, error) {
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
	req, err := http.NewRequest(http.MethodPost, discovery.TokenEndpoint, strings.NewReader(values.Encode()))
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
		return "", errors.New("oidc token exchange failed")
	}
	var payload struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.IDToken) == "" {
		return "", ErrOIDCInvalidCallback
	}
	return strings.TrimSpace(payload.IDToken), nil
}

func validateOIDCIDToken(discovery oidcDiscoveryDocument, cfg oidcProviderConfig, rawToken, expectedNonce string) (oidcTokenClaims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	if header.Algorithm != "RS256" || strings.TrimSpace(header.KeyID) == "" {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	publicKey, err := fetchOIDCRSAKey(discovery.JWKSURI, header.KeyID)
	if err != nil {
		return oidcTokenClaims{}, err
	}
	signed := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	subject, _ := claims["sub"].(string)
	issuer, _ := claims["iss"].(string)
	nonce, _ := claims["nonce"].(string)
	if strings.TrimSpace(subject) == "" || issuer != discovery.Issuer || nonce != expectedNonce {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	if !oidcAudienceContains(claims["aud"], cfg.ClientID) {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	exp, ok := claims["exp"].(float64)
	if !ok || int64(exp) <= time.Now().Unix() {
		return oidcTokenClaims{}, ErrOIDCInvalidCallback
	}
	return oidcTokenClaims{
		Issuer:            issuer,
		Subject:           subject,
		Email:             oidcStringClaim(claims, "email"),
		SuggestedUsername: oidcStringClaim(claims, "preferred_username", "nickname", "username"),
		DisplayName:       oidcStringClaim(claims, "name", "display_name"),
	}, nil
}

func oidcStringClaim(claims map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := claims[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func fetchOIDCRSAKey(jwksURI, keyID string) (*rsa.PublicKey, error) {
	req, err := http.NewRequest(http.MethodGet, jwksURI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("oidc jwks fetch failed")
	}
	var payload struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			Use       string `json:"use"`
			KeyID     string `json:"kid"`
			Algorithm string `json:"alg"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	for _, key := range payload.Keys {
		if key.KeyID != keyID || key.KeyType != "RSA" {
			continue
		}
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		if key.Algorithm != "" && key.Algorithm != "RS256" {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(key.Modulus)
		if err != nil {
			return nil, ErrOIDCInvalidCallback
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(key.Exponent)
		if err != nil {
			return nil, ErrOIDCInvalidCallback
		}
		exponent := int(new(big.Int).SetBytes(exponentBytes).Int64())
		if len(modulus) == 0 || exponent <= 1 {
			return nil, ErrOIDCInvalidCallback
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: exponent}, nil
	}
	return nil, ErrOIDCInvalidCallback
}

func oidcAudienceContains(audience interface{}, clientID string) bool {
	switch value := audience.(type) {
	case string:
		return value == clientID
	case []interface{}:
		for _, item := range value {
			if text, ok := item.(string); ok && text == clientID {
				return true
			}
		}
	}
	return false
}

func oidcScopes(configured string) string {
	fields := strings.Fields(configured)
	for _, field := range fields {
		if field == "openid" {
			return strings.Join(fields, " ")
		}
	}
	if len(fields) == 0 {
		return "openid profile email"
	}
	return "openid " + strings.Join(fields, " ")
}
