package handler

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/service"
)

type SettingHandler struct {
	svc      *service.SettingService
	auditSvc *service.UserService
}

func NewSettingHandler(svc *service.SettingService) *SettingHandler {
	return &SettingHandler{svc: svc, auditSvc: service.NewUserService()}
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
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "设置参数无效")
		return
	}
	normalized := normalizeSettingInputs(req)
	beforeValues := h.currentSettingValues(normalized)
	if err := h.svc.BatchSet(normalized); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	for _, key := range sortedSettingKeys(normalized) {
		after := normalized[key]
		action := "setting.create"
		if beforeValues[key] != nil {
			action = "setting.update"
		}
		if err := h.auditSvc.RecordAdminAuditLog(service.AdminAuditRecordInput{
			RequestID:     c.GetString("request_id"),
			ActorUserID:   operator.ID,
			ActorRole:     operator.Role,
			Action:        action,
			ResourceType:  "setting",
			ResourceID:    key,
			BeforeSummary: auditSummary(settingAuditSummary(key, beforeValues[key])),
			AfterSummary:  auditSummary(settingAuditSummary(key, &after)),
			Result:        "success",
			IP:            c.ClientIP(),
			UserAgent:     c.GetHeader("User-Agent"),
		}); err != nil {
			common.FailWithStatus(c, 500, "写入审计日志失败")
			return
		}
	}
	common.SuccessMsg(c, "设置已更新")
}

func normalizeSettingInputs(settings map[string]string) map[string]string {
	normalized := make(map[string]string, len(settings))
	for key, value := range settings {
		key = strings.TrimSpace(key)
		if key != "" {
			normalized[key] = value
		}
	}
	return normalized
}

func sortedSettingKeys(settings map[string]string) []string {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (h *SettingHandler) currentSettingValues(settings map[string]string) map[string]*string {
	values := make(map[string]*string, len(settings))
	for _, key := range sortedSettingKeys(settings) {
		value, err := h.svc.Get(key)
		if err != nil {
			continue
		}
		v := value
		values[key] = &v
	}
	return values
}

func settingAuditSummary(key string, value *string) map[string]string {
	if value == nil {
		return nil
	}
	displayValue := *value
	if isSensitiveSetting(key) {
		displayValue = common.RedactSecret(displayValue)
	}
	return map[string]string{
		"key":   key,
		"value": displayValue,
	}
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
