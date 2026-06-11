package handler

import (
	"strings"

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
	settings, err := h.svc.GetAll(c.Query("category"))
	if err != nil {
		common.FailWithStatus(c, 500, "查询设置失败")
		return
	}
	for i := range settings {
		if isSensitiveSetting(settings[i].Key) {
			settings[i].Value = common.RedactSecret(settings[i].Value)
		}
	}
	common.Success(c, settings)
}

// PUT /v0/admin/setting — 批量更新系统设置
func (h *SettingHandler) BatchSet(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "设置参数无效")
		return
	}
	if err := h.svc.BatchSet(req); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "设置已更新")
}

func isSensitiveSetting(key string) bool {
	key = strings.ToLower(key)
	sensitiveParts := []string{"secret", "password", "token", "api_key", "apikey", "private_key", "webhook_secret", "client_secret", "payment"}
	for _, part := range sensitiveParts {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}
