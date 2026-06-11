package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/service"
)

type LogHandler struct {
	svc *service.LogService
}

func NewLogHandler(svc *service.LogService) *LogHandler {
	return &LogHandler{svc: svc}
}

// GET /v0/admin/log — 请求日志列表 (Admin 全局视角)
func (h *LogHandler) AdminList(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}

// DELETE /v0/admin/log — 清空日志
func (h *LogHandler) AdminClear(c *gin.Context) {
	// TODO: Phase 4 实现
	common.SuccessMsg(c, "日志已清空")
}

// GET /v0/user/log — 我的请求日志 (用户视角)
func (h *LogHandler) UserList(c *gin.Context) {
	// TODO: Phase 5 实现
	common.Success(c, nil)
}

// GET /v0/user/billing — 用量统计
func (h *LogHandler) UserBilling(c *gin.Context) {
	// TODO: Phase 5 实现
	common.Success(c, nil)
}

// GET /v0/admin/dashboard — 仪表盘统计
func (h *LogHandler) Dashboard(c *gin.Context) {
	// TODO: Phase 4 实现
	common.Success(c, nil)
}
