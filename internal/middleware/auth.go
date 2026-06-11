package middleware

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/model"
)

// AdminAuth Gin 中间件：管理员 Cookie/Session 鉴权。
// 从 Cookie (session_id 或 jwt_token) 中提取会话标识，
// 验证有效性后注入当前管理员 User 到 context。
func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticateAdmin(c) {
			c.Abort()
			return
		}
		c.Next()
	}
}

// GetAdminUser 从 gin.Context 中提取已鉴权的管理员用户。
func GetAdminUser(c *gin.Context) interface{} {
	v, _ := c.Get("admin_user")
	return v
}

// RequireSuperAdmin 仅允许超级管理员 (role=2) 通过。
func RequireSuperAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := currentUser(c)
		if !ok || user.Role < common.RoleSuper {
			common.FailWithStatus(c, 403, "需要超级管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}

// AdminAuthRequired 是 AdminAuth 的别名，用于路由组。
// 用法: adminGroup.Use(middleware.AdminAuthRequired())
func AdminAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticateAdmin(c) {
			c.Abort()
			return
		}
		c.Next()
	}
}

func authenticateAdmin(c *gin.Context) bool {
	if !authenticateJWT(c) {
		return false
	}
	user, ok := currentUser(c)
	if !ok || user.Role < common.RoleAdmin {
		common.FailWithStatus(c, 403, "需要管理员权限")
		return false
	}
	c.Set("admin_user", user)
	return true
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
