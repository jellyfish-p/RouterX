package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/service"
)

// ApiKeyAuth Gin 中间件：API Key 鉴权 (用于 /v1/* 转发接口)。
// 从 Header: Authorization: Bearer sk-xxxx 提取 API Key，
// 查 tokens 表验证状态/有效期/余额，注入 Token + User 到 context。
func ApiKeyAuth() gin.HandlerFunc {
	return requireAPIKeyAuth
}

// GetApiKeyToken 从 gin.Context 中提取已鉴权的 Token。
func GetApiKeyToken(c *gin.Context) interface{} {
	v, _ := c.Get("current_token")
	if v == nil {
		v, _ = c.Get("token")
	}
	return v
}

// GetApiKeyUser 从 gin.Context 中提取已鉴权的 User。
func GetApiKeyUser(c *gin.Context) interface{} {
	v, _ := c.Get("current_user")
	if v == nil {
		v, _ = c.Get("user")
	}
	return v
}

// ApiKeyAuthRequired API Key 鉴权入口。
func ApiKeyAuthRequired() gin.HandlerFunc {
	return requireAPIKeyAuth
}

func requireAPIKeyAuth(c *gin.Context) {
	if !authenticateAPIKey(c) {
		c.Abort()
		return
	}
	release, ok := acquireAPIKeyConcurrency(c)
	if !ok {
		c.Abort()
		return
	}
	defer release()
	if !enforceAPIKeyRPMScope(c) {
		c.Abort()
		return
	}
	c.Next()
}

func authenticateAPIKey(c *gin.Context) bool {
	key, ok := common.BearerToken(c.GetHeader("Authorization"))
	if !ok {
		key = strings.TrimSpace(c.GetHeader("X-Api-Key"))
		ok = key != ""
	}
	if !ok {
		key = strings.TrimSpace(c.Query("key"))
		ok = key != ""
	}
	if !ok {
		writeProtocolAuthError(c, http.StatusUnauthorized, "invalid api key", "authentication_error", "invalid_api_key")
		return false
	}
	tokenSvc := service.NewTokenService()
	token, err := tokenSvc.ValidateAndGetToken(key)
	if err != nil {
		if errors.Is(err, service.ErrAPIUserDisabled) {
			writeProtocolAuthError(c, http.StatusForbidden, "user is disabled", "permission_error", "user_disabled")
			return false
		}
		writeProtocolAuthError(c, http.StatusUnauthorized, "invalid api key", "authentication_error", "invalid_api_key")
		return false
	}
	// 入口协议 scope 在 relay 解析前执行，确保拒绝响应仍保持当前协议的错误外形。
	if err := tokenSvc.CheckEntryProtocolScope(token, entryProtocol(c)); err != nil {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "entry protocol not allowed by api key scope", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "token_forbidden", "not_evaluated", policyDeniedScopeResult("entry_protocol"))
		writeProtocolAuthError(c, http.StatusForbidden, "entry protocol is not allowed by api key scope", "permission_error", "token_forbidden")
		return false
	}
	if err := tokenSvc.CheckIPScope(token, c.ClientIP()); err != nil {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "ip not allowed by api key scope", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "token_forbidden", "not_evaluated", policyDeniedScopeResult("ip"))
		writeProtocolAuthError(c, http.StatusForbidden, "ip is not allowed by api key scope", "permission_error", "token_forbidden")
		return false
	}
	if err := tokenSvc.CheckMethodScope(token, c.Request.Method, c.Request.URL.Path); err != nil {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "method not allowed by api key scope", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "token_forbidden", "not_evaluated", policyDeniedScopeResult("method"))
		writeProtocolAuthError(c, http.StatusForbidden, "method is not allowed by api key scope", "permission_error", "token_forbidden")
		return false
	}
	if err := tokenSvc.CheckDailyQuotaScope(token); err != nil {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "daily quota exceeded by api key scope", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "insufficient_quota", "scope_limit_exceeded", policyDeniedScopeResult("daily_quota"))
		writeProtocolAuthError(c, http.StatusTooManyRequests, "daily quota exceeded", "insufficient_quota", "insufficient_quota")
		return false
	}
	if err := tokenSvc.CheckMonthlyQuotaScope(token); err != nil {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "monthly quota exceeded by api key scope", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "insufficient_quota", "scope_limit_exceeded", policyDeniedScopeResult("monthly_quota"))
		writeProtocolAuthError(c, http.StatusTooManyRequests, "monthly quota exceeded", "insufficient_quota", "insufficient_quota")
		return false
	}
	if !tokenSvc.HasAvailableQuota(token) {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "insufficient quota", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "insufficient_quota", "unavailable", policyDeniedScopeResult("quota"))
		writeProtocolAuthError(c, http.StatusTooManyRequests, "insufficient quota", "insufficient_quota", "insufficient_quota")
		return false
	}
	c.Set("current_token", token)
	c.Set("token", token)
	if token.User != nil {
		c.Set("current_user", token.User)
		c.Set("user", token.User)
	}
	return true
}

func acquireAPIKeyConcurrency(c *gin.Context) (func(), bool) {
	token, ok := CurrentAPIToken(c)
	if !ok {
		return func() {}, true
	}
	tokenSvc := service.NewTokenService()
	release, err := tokenSvc.AcquireConcurrencyScope(token)
	if err != nil {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "concurrency limit exceeded by api key scope", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "rate_limit_exceeded", "rate_limit_exceeded", policyDeniedScopeResult("max_concurrency"))
		writeProtocolAuthError(c, http.StatusTooManyRequests, "concurrency limit exceeded", "rate_limit_error", "rate_limit_exceeded")
		return nil, false
	}
	return release, true
}

func enforceAPIKeyRPMScope(c *gin.Context) bool {
	token, ok := CurrentAPIToken(c)
	if !ok {
		return true
	}
	tokenSvc := service.NewTokenService()
	if err := tokenSvc.CheckRPMScope(token); err != nil {
		tokenSvc.RecordScopeDeniedPolicyLog(token, "rpm limit exceeded by api key scope", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "rate_limit_exceeded", "rate_limit_exceeded", policyDeniedScopeResult("rpm"))
		writeProtocolAuthError(c, http.StatusTooManyRequests, "rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
		return false
	}
	return true
}

func policyDeniedScopeResult(deniedKey string) map[string]interface{} {
	result := map[string]interface{}{
		"api_type":      "not_evaluated",
		"model":         "not_evaluated",
		"channel_group": "not_evaluated",
	}
	result[deniedKey] = "deny"
	return result
}

func writeProtocolAuthError(c *gin.Context, status int, message, typ, code string) {
	switch entryProtocol(c) {
	case "anthropic":
		c.JSON(status, common.AnthropicError(message, typ))
	case "gemini":
		c.JSON(status, common.GeminiError(status, message, geminiStatusText(status)))
	default:
		c.JSON(status, common.OpenAIError(message, typ, code))
	}
}

// entryProtocol resolves the client-facing protocol before the relay handler runs.
func entryProtocol(c *gin.Context) string {
	path := c.Request.URL.Path
	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	switch {
	case strings.HasPrefix(path, "/v1/messages"):
		return "anthropic"
	case strings.TrimSpace(c.GetHeader("anthropic-version")) != "" || format == "anthropic":
		return "anthropic"
	case strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent") || strings.Contains(path, ":countTokens"):
		return "gemini"
	case format == "gemini" || format == "google":
		return "gemini"
	default:
		return "openai"
	}
}

func geminiStatusText(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	default:
		return "INTERNAL"
	}
}

func CurrentAPIToken(c *gin.Context) (*model.Token, bool) {
	v := GetApiKeyToken(c)
	token, ok := v.(*model.Token)
	return token, ok && token != nil
}
