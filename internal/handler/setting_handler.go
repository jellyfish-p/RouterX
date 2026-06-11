package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/service"
)

type SettingHandler struct {
	svc *service.SettingService
}

func NewSettingHandler(svc *service.SettingService) *SettingHandler {
	return &SettingHandler{svc: svc}
}

// GET /v0/admin/setting — 获取所有系统设置
func (h *SettingHandler) GetAll(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// PUT /v0/admin/setting — 批量更新系统设置
func (h *SettingHandler) BatchSet(c *gin.Context) {
	// TODO: Phase 4 实现
	common.SuccessMsg(c, "设置已更新")
}
