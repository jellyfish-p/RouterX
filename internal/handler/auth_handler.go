package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/service"
)

type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// POST /v0/admin/login — 管理员登录
func (h *AuthHandler) AdminLogin(c *gin.Context) {
	// TODO: Phase 2 实现
	// 1. 绑定 LoginRequest
	// 2. 调用 svc.AdminLogin
	// 3. 设置 HttpOnly Cookie (JWT / SessionID)
	// 4. 返回用户信息
	common.Success(c, nil)
}

// POST /v0/admin/logout — 管理员登出
func (h *AuthHandler) AdminLogout(c *gin.Context) {
	// TODO: Phase 2 实现
	// 1. 获取 Cookie session_id
	// 2. 调用 svc.Logout 清除会话
	// 3. 清除客户端 Cookie
	common.SuccessMsg(c, "已登出")
}

// POST /v0/user/register — 用户注册
func (h *AuthHandler) Register(c *gin.Context) {
	// TODO: Phase 5 实现
	common.Success(c, nil)
}

// POST /v0/user/login — 用户登录 (返回 JWT)
func (h *AuthHandler) UserLogin(c *gin.Context) {
	// TODO: Phase 5 实现
	common.Success(c, nil)
}

// POST /v0/user/self/password — 修改密码
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	// TODO: Phase 2/5 实现
	common.SuccessMsg(c, "密码修改成功")
}
