package middleware

import (
	"github.com/gin-gonic/gin"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/service"
)

// UserJwtAuth Gin 中间件：用户 JWT 鉴权 (用于 /v0/user/* 管理接口)。
// 从 Header: Authorization: Bearer <jwt> 提取用户登录令牌，
// 验证 JWT 签名与有效期，注入当前 User 到 context。
// 与 ApiKeyAuth 的区别: 此中间件校验用户登录态 (JWT)，而非 API Key (sk-xxx)。
func UserJwtAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticateJWT(c) {
			c.Abort()
			return
		}
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
		if !authenticateJWT(c) {
			c.Abort()
			return
		}
		c.Next()
	}
}

func authenticateJWT(c *gin.Context) bool {
	raw, ok := common.BearerToken(c.GetHeader("Authorization"))
	if !ok {
		if cookie, err := c.Cookie("jwt_token"); err == nil {
			raw = cookie
			ok = raw != ""
		}
	}
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return false
	}
	secret, err := service.GetJWTSecret()
	if err != nil {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return false
	}
	claims, err := common.ParseUserJWT(raw, secret)
	if err != nil {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return false
	}
	var user model.User
	if err := internal.DB.First(&user, claims.UserID).Error; err != nil || user.Status != common.UserStatusEnabled {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return false
	}
	c.Set("current_user", &user)
	c.Set("user", &user)
	c.Set("jwt_claims", claims)
	return true
}
