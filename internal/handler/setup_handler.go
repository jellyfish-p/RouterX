package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/service"
)

type SetupHandler struct {
	svc *service.SetupService
}

func NewSetupHandler(svc *service.SetupService) *SetupHandler {
	return &SetupHandler{svc: svc}
}

// GET /v0/setup/status — 查询系统初始化状态
func (h *SetupHandler) Status(c *gin.Context) {
	initialized, err := h.svc.GetInitStatus()
	if err != nil {
		common.Fail(c, "查询初始化状态失败")
		return
	}
	common.Success(c, dto.InitStatusResponse{Initialized: initialized})
}

// POST /v0/setup/init — 首次初始化
func (h *SetupHandler) Init(c *gin.Context) {
	// TODO: Phase 1 实现
	// 1. 绑定 SetupInitRequest
	// 2. 调用 svc.Init
	// 3. 返回成功
	common.Fail(c, "not implemented")
}
