package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/service"
)

// AdminHandler 管理员账户管理接口。
// 仅超级管理员可调用，用于管理其他管理员账户的创建/编辑/删除。
type AdminHandler struct {
	svc *service.AdminService
}

func NewAdminHandler(svc *service.AdminService) *AdminHandler {
	return &AdminHandler{svc: svc}
}

// GET /v0/admin/admin — 管理员列表
func (h *AdminHandler) List(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// POST /v0/admin/admin — 创建管理员
func (h *AdminHandler) Create(c *gin.Context) {
	// TODO: Phase 4 实现
	// 1. 绑定 CreateAdminRequest
	// 2. 从 context 获取当前管理员 (判断 role 权限)
	// 3. 调用 svc.CreateAdmin
	common.Success(c, nil)
}

// PUT /v0/admin/admin/:id — 编辑管理员
func (h *AdminHandler) Update(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// DELETE /v0/admin/admin/:id — 删除管理员
func (h *AdminHandler) Delete(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}
