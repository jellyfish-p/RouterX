package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
	"routerx/internal/service"
)

type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// POST /v0/user/register — 用户注册
func (h *AuthHandler) Register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "注册参数无效")
		return
	}
	result, err := h.svc.Register(service.RegisterInput{
		Username:       req.Username,
		Password:       req.Password,
		DisplayName:    req.DisplayName,
		Email:          req.Email,
		Phone:          req.Phone,
		RegisterMethod: req.RegisterMethod,
		CaptchaID:      req.CaptchaID,
		CaptchaCode:    req.CaptchaCode,
	})
	if err != nil {
		if errors.Is(err, service.ErrSelfRegistrationDisabled) ||
			errors.Is(err, service.ErrUsernameRegistrationDisabled) ||
			errors.Is(err, service.ErrEmailRegistrationDisabled) ||
			errors.Is(err, service.ErrPhoneRegistrationDisabled) ||
			errors.Is(err, service.ErrRegistrationCaptchaRequired) {
			common.FailWithStatus(c, 403, err.Error())
			return
		}
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if result == nil || result.User == nil {
		common.FailWithStatus(c, 500, "注册结果无效")
		return
	}
	if result.Recovered {
		if err := h.recordUserRecoverAudit(c, result.User); err != nil {
			common.FailWithStatus(c, 500, "写入审计日志失败")
			return
		}
	}
	common.Success(c, dto.UserBriefFromModel(result.User))
}

// POST /v0/user/login — 用户登录 (返回 JWT)
func (h *AuthHandler) UserLogin(c *gin.Context) {
	var req dto.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "登录参数无效")
		return
	}
	account := loginAccount(req)
	credentialType := strings.TrimSpace(req.CredentialType)
	if credentialType == "" {
		credentialType = "password"
	}
	var (
		user  *model.User
		token string
		err   error
	)
	switch credentialType {
	case "password":
		if req.Password == "" {
			common.FailWithStatus(c, 400, "登录参数无效")
			return
		}
		user, token, err = h.svc.UserLogin(account, req.Password)
	case "code":
		if account == "" || strings.TrimSpace(req.CaptchaID) == "" || strings.TrimSpace(req.CaptchaCode) == "" {
			common.FailWithStatus(c, 400, "登录参数无效")
			return
		}
		user, token, err = h.svc.UserCodeLogin(account, req.CaptchaID, req.CaptchaCode)
	default:
		common.FailWithStatus(c, 400, "登录参数无效")
		return
	}
	if err != nil {
		if errors.Is(err, service.ErrLoginCodeDisabled) ||
			errors.Is(err, service.ErrLoginCodeUnsupported) ||
			errors.Is(err, service.ErrLoginCodeVerifierUnavailable) {
			common.FailWithStatus(c, 403, err.Error())
			return
		}
		common.FailWithStatus(c, 401, "账号或凭据错误")
		return
	}
	if err := h.recordLoginAudit(c, user); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.LoginResponse{Token: token, User: dto.UserBriefFromModel(user)})
}

// GET /v0/user/oauth/:provider/login — 发起 OAuth 登录。
func (h *AuthHandler) OAuthLogin(c *gin.Context) {
	provider := c.Param("provider")
	state, err := common.GenerateRandomString(24)
	if err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OAuth state 失败")
		return
	}
	redirectURI := oauthCallbackURL(c, provider)
	location, err := h.svc.OAuthLoginURL(provider, state, redirectURI)
	if err != nil {
		common.FailWithStatus(c, http.StatusForbidden, err.Error())
		return
	}
	setOAuthStateCookie(c, provider, state, 10*60)
	c.Redirect(http.StatusFound, location)
}

// GET /v0/user/oauth/:provider/callback — 处理 OAuth 回调并登录已绑定身份，或返回首次注册票据。
func (h *AuthHandler) OAuthCallback(c *gin.Context) {
	provider := c.Param("provider")
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	cookieState, err := c.Cookie(oauthStateCookieName(provider))
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		common.FailWithStatus(c, http.StatusBadRequest, "OAuth state 无效或已过期")
		return
	}
	setOAuthStateCookie(c, provider, "", -1)
	result, err := h.svc.OAuthCallbackLogin(provider, code, oauthCallbackURL(c, provider))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, service.ErrOAuthIdentityNotBound) || errors.Is(err, service.ErrOAuthProviderDisabled) {
			status = http.StatusForbidden
		} else if errors.Is(err, service.ErrOAuthInvalidCallback) {
			status = http.StatusBadRequest
		}
		common.FailWithStatus(c, status, err.Error())
		return
	}
	if result != nil && result.RegistrationRequired != nil {
		common.Success(c, dto.OAuthRegistrationRequiredResponse{
			RegistrationRequired: true,
			RegistrationTicket:   result.RegistrationRequired.Ticket,
			Provider:             result.RegistrationRequired.Provider,
			SuggestedUsername:    result.RegistrationRequired.SuggestedUsername,
			Email:                result.RegistrationRequired.Email,
		})
		return
	}
	if result == nil || result.User == nil {
		common.FailWithStatus(c, http.StatusBadRequest, "OAuth 回调结果无效")
		return
	}
	if err := h.recordLoginAudit(c, result.User); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
		return
	}
	common.Success(c, dto.LoginResponse{Token: result.Token, User: dto.UserBriefFromModel(result.User)})
}

// POST /v0/user/oauth/:provider/register — 使用 OAuth 回调票据补齐本地用户名密码账号。
func (h *AuthHandler) OAuthRegister(c *gin.Context) {
	provider := c.Param("provider")
	var req dto.OAuthRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, http.StatusBadRequest, "OAuth 注册参数无效")
		return
	}
	result, err := h.svc.OAuthRegister(provider, req.RegistrationTicket, req.Username, req.Password, req.DisplayName, req.Email, req.CaptchaID, req.CaptchaCode)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, service.ErrSelfRegistrationDisabled) ||
			errors.Is(err, service.ErrUsernameRegistrationDisabled) ||
			errors.Is(err, service.ErrRegistrationCaptchaRequired) ||
			errors.Is(err, service.ErrOAuthRegistrationDisabled) ||
			errors.Is(err, service.ErrOAuthProviderDisabled) {
			status = http.StatusForbidden
		} else if errors.Is(err, service.ErrOAuthIdentityAlreadyBound) {
			status = http.StatusConflict
		}
		common.FailWithStatus(c, status, err.Error())
		return
	}
	if result == nil || result.User == nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "OAuth 注册结果无效")
		return
	}
	if result.Recovered {
		if err := h.recordUserRecoverAudit(c, result.User); err != nil {
			common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
	} else {
		if err := h.recordIdentityBoundAudit(c, result.User.ID, result.Identity); err != nil {
			common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
	}
	if err := h.recordLoginAudit(c, result.User); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
		return
	}
	common.Success(c, dto.LoginResponse{Token: result.Token, User: dto.UserBriefFromModel(result.User)})
}

// GET /v0/user/oidc/:provider/login — 发起 OIDC 登录。
func (h *AuthHandler) OIDCLogin(c *gin.Context) {
	provider := c.Param("provider")
	state, err := common.GenerateRandomString(24)
	if err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OIDC state 失败")
		return
	}
	nonce, err := common.GenerateRandomString(24)
	if err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OIDC nonce 失败")
		return
	}
	location, err := h.svc.OIDCLoginURL(provider, state, nonce, oidcCallbackURL(c, provider))
	if err != nil {
		common.FailWithStatus(c, http.StatusForbidden, err.Error())
		return
	}
	setOIDCStateCookie(c, provider, state, 10*60)
	setOIDCNonceCookie(c, provider, nonce, 10*60)
	c.Redirect(http.StatusFound, location)
}

// GET /v0/user/oidc/:provider/callback — 处理 OIDC 回调并登录已绑定身份，或返回首次注册票据。
func (h *AuthHandler) OIDCCallback(c *gin.Context) {
	provider := c.Param("provider")
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	cookieState, err := c.Cookie(oidcStateCookieName(provider))
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		common.FailWithStatus(c, http.StatusBadRequest, "OIDC state 无效或已过期")
		return
	}
	nonce, err := c.Cookie(oidcNonceCookieName(provider))
	if err != nil || strings.TrimSpace(nonce) == "" {
		common.FailWithStatus(c, http.StatusBadRequest, "OIDC nonce 无效或已过期")
		return
	}
	setOIDCStateCookie(c, provider, "", -1)
	setOIDCNonceCookie(c, provider, "", -1)

	result, err := h.svc.OIDCCallbackLogin(provider, code, oidcCallbackURL(c, provider), nonce)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, service.ErrOIDCIdentityNotBound) || errors.Is(err, service.ErrOIDCProviderDisabled) {
			status = http.StatusForbidden
		} else if errors.Is(err, service.ErrOIDCInvalidCallback) {
			status = http.StatusBadRequest
		}
		common.FailWithStatus(c, status, err.Error())
		return
	}
	if result != nil && result.RegistrationRequired != nil {
		common.Success(c, dto.OIDCRegistrationRequiredResponse{
			RegistrationRequired: true,
			RegistrationTicket:   result.RegistrationRequired.Ticket,
			Provider:             result.RegistrationRequired.Provider,
			SuggestedUsername:    result.RegistrationRequired.SuggestedUsername,
			Email:                result.RegistrationRequired.Email,
		})
		return
	}
	if result == nil || result.User == nil {
		common.FailWithStatus(c, http.StatusBadRequest, "OIDC 回调结果无效")
		return
	}
	if err := h.recordLoginAudit(c, result.User); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
		return
	}
	common.Success(c, dto.LoginResponse{Token: result.Token, User: dto.UserBriefFromModel(result.User)})
}

// POST /v0/user/oidc/:provider/register — 使用 OIDC 回调票据补齐本地用户名密码账号。
func (h *AuthHandler) OIDCRegister(c *gin.Context) {
	provider := c.Param("provider")
	var req dto.OIDCRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, http.StatusBadRequest, "OIDC 注册参数无效")
		return
	}
	result, err := h.svc.OIDCRegister(provider, req.RegistrationTicket, req.Username, req.Password, req.DisplayName, req.Email, req.CaptchaID, req.CaptchaCode)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, service.ErrSelfRegistrationDisabled) ||
			errors.Is(err, service.ErrUsernameRegistrationDisabled) ||
			errors.Is(err, service.ErrRegistrationCaptchaRequired) ||
			errors.Is(err, service.ErrOIDCRegistrationDisabled) ||
			errors.Is(err, service.ErrOIDCProviderDisabled) {
			status = http.StatusForbidden
		} else if errors.Is(err, service.ErrOIDCIdentityAlreadyBound) {
			status = http.StatusConflict
		}
		common.FailWithStatus(c, status, err.Error())
		return
	}
	if result == nil || result.User == nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "OIDC 注册结果无效")
		return
	}
	if result.Recovered {
		if err := h.recordUserRecoverAudit(c, result.User); err != nil {
			common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
	} else {
		if err := h.recordIdentityBoundAudit(c, result.User.ID, result.Identity); err != nil {
			common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
	}
	if err := h.recordLoginAudit(c, result.User); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
		return
	}
	common.Success(c, dto.LoginResponse{Token: result.Token, User: dto.UserBriefFromModel(result.User)})
}

// GET /v0/user/oidc/:provider/bind — 登录用户发起 OIDC 身份绑定。
func (h *AuthHandler) OIDCBind(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, http.StatusUnauthorized, "未登录或登录已过期")
		return
	}
	provider := c.Param("provider")
	state, err := common.GenerateRandomString(24)
	if err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OIDC state 失败")
		return
	}
	nonce, err := common.GenerateRandomString(24)
	if err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OIDC nonce 失败")
		return
	}
	location, err := h.svc.OIDCLoginURL(provider, state, nonce, oidcBindCallbackURL(c, provider))
	if err != nil {
		common.FailWithStatus(c, http.StatusForbidden, err.Error())
		return
	}
	setOIDCStateCookie(c, provider, state, 10*60)
	setOIDCNonceCookie(c, provider, nonce, 10*60)
	if err := setOIDCBindCookie(c, provider, state, user.ID, 10*60); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OIDC 绑定凭据失败")
		return
	}
	c.Redirect(http.StatusFound, location)
}

// GET /v0/user/oidc/:provider/bind/callback — 处理 OIDC 绑定回调。
func (h *AuthHandler) OIDCBindCallback(c *gin.Context) {
	provider := c.Param("provider")
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	cookieState, err := c.Cookie(oidcStateCookieName(provider))
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		common.FailWithStatus(c, http.StatusBadRequest, "OIDC state 无效或已过期")
		return
	}
	nonce, err := c.Cookie(oidcNonceCookieName(provider))
	if err != nil || strings.TrimSpace(nonce) == "" {
		common.FailWithStatus(c, http.StatusBadRequest, "OIDC nonce 无效或已过期")
		return
	}
	userID, err := oidcBindCookieUserID(c, provider, state)
	if err != nil {
		common.FailWithStatus(c, http.StatusBadRequest, "OIDC 绑定凭据无效或已过期")
		return
	}
	setOIDCStateCookie(c, provider, "", -1)
	setOIDCNonceCookie(c, provider, "", -1)
	setOIDCBindCookie(c, provider, "", 0, -1)

	identity, err := h.svc.OIDCBindCallback(userID, provider, code, oidcBindCallbackURL(c, provider), nonce)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, service.ErrOIDCIdentityAlreadyBound) {
			status = http.StatusConflict
		} else if errors.Is(err, service.ErrOIDCProviderDisabled) {
			status = http.StatusForbidden
		} else if errors.Is(err, service.ErrOIDCInvalidCallback) {
			status = http.StatusBadRequest
		}
		common.FailWithStatus(c, status, err.Error())
		return
	}
	if err := h.recordIdentityBoundAudit(c, userID, identity); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
		return
	}
	common.Success(c, gin.H{
		"method":     identity.Method,
		"provider":   identity.Provider,
		"identifier": identity.Identifier,
	})
}

// GET /v0/user/oauth/:provider/bind — 登录用户发起 OAuth 身份绑定。
func (h *AuthHandler) OAuthBind(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, http.StatusUnauthorized, "未登录或登录已过期")
		return
	}
	provider := c.Param("provider")
	state, err := common.GenerateRandomString(24)
	if err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OAuth state 失败")
		return
	}
	redirectURI := oauthBindCallbackURL(c, provider)
	location, err := h.svc.OAuthLoginURL(provider, state, redirectURI)
	if err != nil {
		common.FailWithStatus(c, http.StatusForbidden, err.Error())
		return
	}
	setOAuthStateCookie(c, provider, state, 10*60)
	if err := setOAuthBindCookie(c, provider, state, user.ID, 10*60); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "生成 OAuth 绑定凭据失败")
		return
	}
	c.Redirect(http.StatusFound, location)
}

// GET /v0/user/oauth/:provider/bind/callback — 处理 OAuth 绑定回调。
func (h *AuthHandler) OAuthBindCallback(c *gin.Context) {
	provider := c.Param("provider")
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	cookieState, err := c.Cookie(oauthStateCookieName(provider))
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		common.FailWithStatus(c, http.StatusBadRequest, "OAuth state 无效或已过期")
		return
	}
	userID, err := oauthBindCookieUserID(c, provider, state)
	if err != nil {
		common.FailWithStatus(c, http.StatusBadRequest, "OAuth 绑定凭据无效或已过期")
		return
	}
	setOAuthStateCookie(c, provider, "", -1)
	setOAuthBindCookie(c, provider, "", 0, -1)

	identity, err := h.svc.OAuthBindCallback(userID, provider, code, oauthBindCallbackURL(c, provider))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, service.ErrOAuthIdentityAlreadyBound) {
			status = http.StatusConflict
		} else if errors.Is(err, service.ErrOAuthProviderDisabled) {
			status = http.StatusForbidden
		} else if errors.Is(err, service.ErrOAuthInvalidCallback) {
			status = http.StatusBadRequest
		}
		common.FailWithStatus(c, status, err.Error())
		return
	}
	if err := h.recordIdentityBoundAudit(c, userID, identity); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
		return
	}
	common.Success(c, gin.H{
		"method":     identity.Method,
		"provider":   identity.Provider,
		"identifier": identity.Identifier,
	})
}

// GET /v0/user/identities — 当前用户身份列表。
func (h *AuthHandler) ListIdentities(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, http.StatusUnauthorized, "未登录或登录已过期")
		return
	}
	identities, err := h.svc.ListUserIdentities(user.ID)
	if err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "读取身份列表失败")
		return
	}
	common.Success(c, dto.UserIdentityBriefsFromModels(identities))
}

// DELETE /v0/user/identities/:id — 解绑当前用户的非主登录身份。
func (h *AuthHandler) UnbindIdentity(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, http.StatusUnauthorized, "未登录或登录已过期")
		return
	}
	parsed, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || parsed == 0 {
		common.FailWithStatus(c, http.StatusBadRequest, "身份 ID 无效")
		return
	}
	identity, err := h.svc.UnbindUserIdentity(user.ID, uint(parsed))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrUserIdentityPrimary):
			common.FailWithStatus(c, http.StatusBadRequest, err.Error())
		case errors.Is(err, service.ErrUserIdentityNotFound):
			common.FailWithStatus(c, http.StatusNotFound, err.Error())
		default:
			common.FailWithStatus(c, http.StatusInternalServerError, "解绑身份失败")
		}
		return
	}
	if err := h.recordIdentityUnboundAudit(c, user, identity); err != nil {
		common.FailWithStatus(c, http.StatusInternalServerError, "写入审计日志失败")
		return
	}
	common.Success(c, dto.UserIdentityBriefFromModel(identity))
}

// POST /v0/user/self/password — 修改密码
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req dto.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "修改密码参数无效")
		return
	}
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	if err := h.svc.ChangePassword(user.ID, req.OldPassword, req.NewPassword); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordPasswordChangedAudit(c, user); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "密码修改成功")
}

func loginAccount(req dto.LoginRequest) string {
	if req.Account != "" {
		return req.Account
	}
	return req.Username
}

// recordLoginAudit records successful logins without storing credentials or the issued JWT.
func (h *AuthHandler) recordLoginAudit(c *gin.Context, user *model.User) error {
	if user == nil {
		return nil
	}
	return service.NewUserService().RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:    c.GetString("request_id"),
		ActorUserID:  user.ID,
		ActorRole:    user.Role,
		Action:       "user.login",
		ResourceType: "user",
		ResourceID:   strconv.FormatUint(uint64(user.ID), 10),
		AfterSummary: auditSummary(dto.UserBriefFromModel(user)),
		Result:       "success",
		IP:           c.ClientIP(),
		UserAgent:    c.GetHeader("User-Agent"),
	})
}

func (h *AuthHandler) recordUserRecoverAudit(c *gin.Context, user *model.User) error {
	if user == nil {
		return nil
	}
	return service.NewUserService().RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:    c.GetString("request_id"),
		ActorUserID:  user.ID,
		ActorRole:    user.Role,
		Action:       "user.recover",
		ResourceType: "user",
		ResourceID:   strconv.FormatUint(uint64(user.ID), 10),
		AfterSummary: auditSummary(dto.UserBriefFromModel(user)),
		Result:       "success",
		IP:           c.ClientIP(),
		UserAgent:    c.GetHeader("User-Agent"),
	})
}

func (h *AuthHandler) recordIdentityBoundAudit(c *gin.Context, userID uint, identity *model.UserIdentity) error {
	if identity == nil {
		return nil
	}
	return service.NewUserService().RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:    c.GetString("request_id"),
		ActorUserID:  userID,
		ActorRole:    common.RoleUser,
		Action:       "user.identity_bound",
		ResourceType: "user_identity",
		ResourceID:   identity.Provider + ":" + identity.Identifier,
		AfterSummary: auditSummary(map[string]interface{}{
			"method":     identity.Method,
			"provider":   identity.Provider,
			"identifier": identity.Identifier,
		}),
		Result:    "success",
		IP:        c.ClientIP(),
		UserAgent: c.GetHeader("User-Agent"),
	})
}

func (h *AuthHandler) recordIdentityUnboundAudit(c *gin.Context, user *model.User, identity *model.UserIdentity) error {
	if user == nil || identity == nil {
		return nil
	}
	summary := map[string]interface{}{
		"method":     identity.Method,
		"provider":   identity.Provider,
		"identifier": identity.Identifier,
	}
	return service.NewUserService().RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:     c.GetString("request_id"),
		ActorUserID:   user.ID,
		ActorRole:     user.Role,
		Action:        "user.identity_unbound",
		ResourceType:  "user_identity",
		ResourceID:    identity.Provider + ":" + identity.Identifier,
		BeforeSummary: auditSummary(summary),
		Result:        "success",
		IP:            c.ClientIP(),
		UserAgent:     c.GetHeader("User-Agent"),
	})
}

func (h *AuthHandler) recordPasswordChangedAudit(c *gin.Context, user *model.User) error {
	if user == nil {
		return nil
	}
	return service.NewUserService().RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:    c.GetString("request_id"),
		ActorUserID:  user.ID,
		ActorRole:    user.Role,
		Action:       "user.password_changed",
		ResourceType: "user",
		ResourceID:   strconv.FormatUint(uint64(user.ID), 10),
		AfterSummary: auditSummary(map[string]interface{}{
			"user":             dto.UserBriefFromModel(user),
			"password_changed": true,
		}),
		Result:    "success",
		IP:        c.ClientIP(),
		UserAgent: c.GetHeader("User-Agent"),
	})
}

func setJWTCookie(c *gin.Context, token string) {
	c.SetSameSite(2)
	c.SetCookie("jwt_token", token, 7*24*3600, "/", "", c.Request.TLS != nil, true)
}

func setOAuthStateCookie(c *gin.Context, provider, state string, maxAge int) {
	c.SetSameSite(2)
	c.SetCookie(oauthStateCookieName(provider), state, maxAge, "/v0/user/oauth/"+provider, "", c.Request.TLS != nil, true)
}

func setOIDCStateCookie(c *gin.Context, provider, state string, maxAge int) {
	c.SetSameSite(2)
	c.SetCookie(oidcStateCookieName(provider), state, maxAge, "/v0/user/oidc/"+provider, "", c.Request.TLS != nil, true)
}

func setOIDCNonceCookie(c *gin.Context, provider, nonce string, maxAge int) {
	c.SetSameSite(2)
	c.SetCookie(oidcNonceCookieName(provider), nonce, maxAge, "/v0/user/oidc/"+provider, "", c.Request.TLS != nil, true)
}

func setOIDCBindCookie(c *gin.Context, provider, state string, userID uint, maxAge int) error {
	value := ""
	if maxAge >= 0 {
		signature, err := oidcBindSignature(provider, state, userID)
		if err != nil {
			return err
		}
		value = fmt.Sprintf("%d:%s", userID, signature)
	}
	c.SetSameSite(2)
	c.SetCookie(oidcBindCookieName(provider), value, maxAge, "/v0/user/oidc/"+provider, "", c.Request.TLS != nil, true)
	return nil
}

func setOAuthBindCookie(c *gin.Context, provider, state string, userID uint, maxAge int) error {
	value := ""
	if maxAge >= 0 {
		signature, err := oauthBindSignature(provider, state, userID)
		if err != nil {
			return err
		}
		value = fmt.Sprintf("%d:%s", userID, signature)
	}
	c.SetSameSite(2)
	c.SetCookie(oauthBindCookieName(provider), value, maxAge, "/v0/user/oauth/"+provider, "", c.Request.TLS != nil, true)
	return nil
}

func oauthBindCookieUserID(c *gin.Context, provider, state string) (uint, error) {
	raw, err := c.Cookie(oauthBindCookieName(provider))
	if err != nil {
		return 0, err
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return 0, errors.New("invalid oauth bind cookie")
	}
	parsed, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("invalid oauth bind user")
	}
	expected, err := oauthBindSignature(provider, state, uint(parsed))
	if err != nil {
		return 0, err
	}
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) != 1 {
		return 0, errors.New("invalid oauth bind signature")
	}
	return uint(parsed), nil
}

func oidcBindCookieUserID(c *gin.Context, provider, state string) (uint, error) {
	raw, err := c.Cookie(oidcBindCookieName(provider))
	if err != nil {
		return 0, err
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return 0, errors.New("invalid oidc bind cookie")
	}
	parsed, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("invalid oidc bind user")
	}
	expected, err := oidcBindSignature(provider, state, uint(parsed))
	if err != nil {
		return 0, err
	}
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) != 1 {
		return 0, errors.New("invalid oidc bind signature")
	}
	return uint(parsed), nil
}

func oauthStateCookieName(provider string) string {
	return "routerx_oauth_state_" + strings.ToLower(strings.TrimSpace(provider))
}

func oauthBindCookieName(provider string) string {
	return "routerx_oauth_bind_" + strings.ToLower(strings.TrimSpace(provider))
}

func oidcStateCookieName(provider string) string {
	return "routerx_oidc_state_" + strings.ToLower(strings.TrimSpace(provider))
}

func oidcNonceCookieName(provider string) string {
	return "routerx_oidc_nonce_" + strings.ToLower(strings.TrimSpace(provider))
}

func oidcBindCookieName(provider string) string {
	return "routerx_oidc_bind_" + strings.ToLower(strings.TrimSpace(provider))
}

func oauthBindSignature(provider, state string, userID uint) (string, error) {
	secret, err := service.GetJWTSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(provider))))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.TrimSpace(state)))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatUint(uint64(userID), 10)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func oidcBindSignature(provider, state string, userID uint) (string, error) {
	secret, err := service.GetJWTSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("oidc"))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(provider))))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.TrimSpace(state)))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatUint(uint64(userID), 10)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func oauthCallbackURL(c *gin.Context, provider string) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request != nil && c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.Request.Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host + "/v0/user/oauth/" + provider + "/callback"
}

func oauthBindCallbackURL(c *gin.Context, provider string) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request != nil && c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.Request.Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host + "/v0/user/oauth/" + provider + "/bind/callback"
}

func oidcCallbackURL(c *gin.Context, provider string) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request != nil && c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.Request.Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host + "/v0/user/oidc/" + provider + "/callback"
}

func oidcBindCallbackURL(c *gin.Context, provider string) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request != nil && c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.Request.Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host + "/v0/user/oidc/" + provider + "/bind/callback"
}

func currentUser(c *gin.Context) (*model.User, bool) {
	v, ok := c.Get("current_user")
	if !ok {
		v, ok = c.Get("user")
	}
	if !ok {
		return nil, false
	}
	user, ok := v.(*model.User)
	return user, ok && user != nil
}
