package middleware

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
)

// AdminAuth Gin 中间件：管理员 Cookie/Session 鉴权。
// 从 Cookie (session_id 或 jwt_token) 中提取会话标识，
// 验证有效性后注入当前管理员 User 到 context。
func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 2 实现
		// 1. 读取 Cookie: session_id 或 jwt_token
		// 2. 查 Redis / JWT 验证有效性
		// 3. 查 DB 校验用户 role >= RoleAdmin
		// 4. 失败返回 401
		// 5. 成功: c.Set("admin_user", user)
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
		// TODO: Phase 4 实现
		// 1. 获取 admin_user
		// 2. 检查 role == RoleSuper
		// 3. 不满足返回 403
		c.Next()
	}
}

// AdminAuthRequired 是 AdminAuth 的别名，用于路由组。
// 用法: adminGroup.Use(middleware.AdminAuthRequired())
func AdminAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 2 — 包装 AdminAuth() 逻辑
		// 临时透传，后续替换为完整鉴权
		// 在初始化完成前放行 setup 路由
		common.FailWithStatus(c, 401, "未登录")
		c.Abort()
	}
}
