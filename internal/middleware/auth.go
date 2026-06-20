package middleware

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/service"
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
			if ok {
				recordSuperAdminDeniedAudit(c, user)
			}
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

func recordSuperAdminDeniedAudit(c *gin.Context, user *model.User) {
	action, resourceType, resourceID := superAdminDeniedAuditTarget(c)
	// Denied audits are best-effort; authorization must stay denied even if logging fails.
	_ = service.NewUserService().RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:    c.GetString("request_id"),
		ActorUserID:  user.ID,
		ActorRole:    user.Role,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		AfterSummary: middlewareAuditSummary(superAdminDeniedAuditSummary(c, user)),
		Result:       "denied",
		ErrorCode:    "super_admin_required",
		IP:           c.ClientIP(),
		UserAgent:    c.GetHeader("User-Agent"),
	})
}

func superAdminDeniedAuditTarget(c *gin.Context) (string, string, string) {
	path := c.FullPath()
	if path == "" && c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	resourceID := path
	if c.Request != nil {
		resourceID = c.Request.Method + " " + path
	}
	switch {
	case strings.HasPrefix(path, "/v0/admin/admin"):
		return "admin.denied", "admin", resourceID
	case strings.HasPrefix(path, "/v0/admin/setting"):
		return "setting.denied", "setting", resourceID
	case strings.HasPrefix(path, "/v0/admin/audit"):
		return "audit.denied", "audit", resourceID
	default:
		return "admin.denied", "admin_route", resourceID
	}
}

func superAdminDeniedAuditSummary(c *gin.Context, user *model.User) map[string]interface{} {
	path := c.FullPath()
	if path == "" && c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	method := ""
	if c.Request != nil {
		method = c.Request.Method
	}
	return map[string]interface{}{
		"actor_user_id": user.ID,
		"actor_role":    user.Role,
		"required_role": common.RoleSuper,
		"method":        method,
		"path":          path,
	}
}

func middlewareAuditSummary(value interface{}) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}
