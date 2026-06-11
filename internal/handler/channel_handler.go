package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/service"
)

type ChannelHandler struct {
	svc *service.ChannelService
}

func NewChannelHandler(svc *service.ChannelService) *ChannelHandler {
	return &ChannelHandler{svc: svc}
}

// GET /v0/admin/channel — 通道列表
func (h *ChannelHandler) List(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// POST /v0/admin/channel — 创建通道
func (h *ChannelHandler) Create(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// PUT /v0/admin/channel/:id — 编辑通道
func (h *ChannelHandler) Update(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// DELETE /v0/admin/channel/:id — 删除通道
func (h *ChannelHandler) Delete(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// POST /v0/admin/channel/:id/test — 测试通道连通性
func (h *ChannelHandler) Test(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}
