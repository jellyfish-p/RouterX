package middleware

import (
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
		common.FailWithStatus(c, 401, "未提供有效的 API Key")
		return false
	}
	token, err := service.NewTokenService().ValidateAndGetToken(key)
	if err != nil {
		common.FailWithStatus(c, 401, "未提供有效的 API Key")
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

func CurrentAPIToken(c *gin.Context) (*model.Token, bool) {
	v := GetApiKeyToken(c)
	token, ok := v.(*model.Token)
	return token, ok && token != nil
}
