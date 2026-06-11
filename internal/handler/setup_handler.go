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
	var req dto.SetupInitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "初始化参数无效")
		return
	}
	user, err := h.svc.Init(req.Username, req.Password, req.DisplayName, req.Email)
	if err != nil {
		common.FailWithStatus(c, 409, err.Error())
		return
	}
	common.Success(c, dto.UserBriefFromModel(user))
}
