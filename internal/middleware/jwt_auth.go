package middleware

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
)

// UserJwtAuth Gin 中间件：用户 JWT 鉴权 (用于 /v0/user/* 管理接口)。
// 从 Header: Authorization: Bearer <jwt> 提取用户登录令牌，
// 验证 JWT 签名与有效期，注入当前 User 到 context。
// 与 ApiKeyAuth 的区别: 此中间件校验用户登录态 (JWT)，而非 API Key (sk-xxx)。
func UserJwtAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 5 实现
		// 1. 提取 Authorization header
		// 2. 解析 JWT，验证签名 (密钥来自 settings.jwt.secret)
		// 3. 校验过期时间
		// 4. 失败返回 401
		// 5. 成功: c.Set("user", user)
		c.Next()
	}
}

// GetJwtUser 从 gin.Context 中提取已鉴权的 User (JWT 方式)。
func GetJwtUser(c *gin.Context) interface{} {
	v, _ := c.Get("user")
	return v
}

// UserJwtAuthRequired 用户 JWT 鉴权入口。
func UserJwtAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 5 — 包装 UserJwtAuth() 逻辑
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		c.Abort()
	}
}
