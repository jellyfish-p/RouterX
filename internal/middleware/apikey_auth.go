package middleware

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
)

// ApiKeyAuth Gin 中间件：API Key 鉴权 (用于 /v1/* 转发接口)。
// 从 Header: Authorization: Bearer sk-xxxx 提取 API Key，
// 查 tokens 表验证状态/有效期/余额，注入 Token + User 到 context。
func ApiKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 2 实现
		// 1. 提取 Authorization header
		// 2. 格式校验: Bearer sk-xxx
		// 3. TokenService.ValidateAndGetToken
		// 4. 失败返回 401
		// 5. 成功: c.Set("token", token), c.Set("user", user)
		c.Next()
	}
}

// GetApiKeyToken 从 gin.Context 中提取已鉴权的 Token。
func GetApiKeyToken(c *gin.Context) interface{} {
	v, _ := c.Get("token")
	return v
}

// GetApiKeyUser 从 gin.Context 中提取已鉴权的 User。
func GetApiKeyUser(c *gin.Context) interface{} {
	v, _ := c.Get("user")
	return v
}

// ApiKeyAuthRequired API Key 鉴权入口。
func ApiKeyAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 2 — 包装 ApiKeyAuth() 逻辑
		common.FailWithStatus(c, 401, "未提供有效的 API Key")
		c.Abort()
	}
}
