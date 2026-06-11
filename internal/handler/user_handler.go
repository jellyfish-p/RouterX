package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/service"
)

type UserHandler struct {
	svc *service.UserService
}

func NewUserHandler(svc *service.UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// GET /v0/admin/user — 用户列表
func (h *UserHandler) List(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// POST /v0/admin/user — 创建用户
func (h *UserHandler) Create(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// PUT /v0/admin/user/:id — 编辑用户
func (h *UserHandler) Update(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// DELETE /v0/admin/user/:id — 删除用户
func (h *UserHandler) Delete(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// PATCH /v0/admin/user/:id/quota — 调整用户余额
func (h *UserHandler) UpdateQuota(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// GET /v0/user/self — 获取个人信息
func (h *UserHandler) Self(c *gin.Context) {
	// TODO: Phase 5 实现
	common.Success(c, nil)
}

// PUT /v0/user/self — 修改个人信息
func (h *UserHandler) UpdateSelf(c *gin.Context) {
	// TODO: Phase 5 实现
	common.Success(c, nil)
}
