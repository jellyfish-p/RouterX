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
	return func(c *gin.Context) {
		if !authenticateAPIKey(c) {
			c.Abort()
			return
		}
		c.Next()
	}
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
	return func(c *gin.Context) {
		if !authenticateAPIKey(c) {
			c.Abort()
			return
		}
		c.Next()
	}
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
		writeOpenAIAuthError(c, http.StatusUnauthorized, "invalid api key", "authentication_error", "invalid_api_key")
		return false
	}
	tokenSvc := service.NewTokenService()
	token, err := tokenSvc.ValidateAndGetToken(key)
	if err != nil {
		if errors.Is(err, service.ErrAPIUserDisabled) {
			writeOpenAIAuthError(c, http.StatusForbidden, "user is disabled", "permission_error", "user_disabled")
			return false
		}
		writeOpenAIAuthError(c, http.StatusUnauthorized, "invalid api key", "authentication_error", "invalid_api_key")
		return false
	}
	if !tokenSvc.HasAvailableQuota(token) {
		writeOpenAIAuthError(c, http.StatusTooManyRequests, "insufficient quota", "insufficient_quota", "insufficient_quota")
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

func writeOpenAIAuthError(c *gin.Context, status int, message, typ, code string) {
	c.JSON(status, common.OpenAIError(message, typ, code))
}

func CurrentAPIToken(c *gin.Context) (*model.Token, bool) {
	v := GetApiKeyToken(c)
	token, ok := v.(*model.Token)
	return token, ok && token != nil
}
