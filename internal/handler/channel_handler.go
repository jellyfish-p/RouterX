package handler

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
	"routerx/internal/service"
)

type ChannelHandler struct {
	svc      *service.ChannelService
	auditSvc *service.UserService
}

func NewChannelHandler(svc *service.ChannelService) *ChannelHandler {
	return &ChannelHandler{svc: svc, auditSvc: service.NewUserService()}
}

// GET /v0/admin/channel — 通道列表
func (h *ChannelHandler) List(c *gin.Context) {
	var req dto.ChannelListRequest
	_ = c.ShouldBindQuery(&req)
	channels, total, err := h.svc.List(req.Page, req.PageSize, req.Type, req.Status)
	if err != nil {
		common.FailWithStatus(c, 500, "查询通道失败")
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: h.channelInfos(channels)})
}

// POST /v0/admin/channel — 创建通道
func (h *ChannelHandler) Create(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.CreateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "创建通道参数无效")
		return
	}
	channel := &model.Channel{
		Idx:              req.Idx,
		Type:             req.Type,
		Name:             req.Name,
		Models:           req.Models,
		BaseURL:          req.BaseURL,
		BaseURLs:         model.NewJSONValue(req.BaseURLs),
		APIKey:           req.APIKey,
		APIKeys:          model.NewJSONValue(req.APIKeys),
		KeySelectionMode: req.KeySelectionMode,
		Upstreams:        model.NewJSONValue(req.Upstreams),
		ModelRewrites:    rawJSONValue(req.ModelRewrites),
		ChannelGroup:     req.Group,
		UpstreamOptions:  rawJSONValue(req.UpstreamOptions),
		Priority:         req.Priority,
		Weight:           req.Weight,
		Status:           req.Status,
	}
	if err := h.svc.Create(channel); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordChannelAudit(c, operator, "channel.create", channel.ID, nil, channelAuditSummary(channel)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, h.channelInfo(channel))
}

// PUT /v0/admin/channel/:id — 编辑通道
func (h *ChannelHandler) Update(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "编辑通道参数无效")
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	updates := map[string]interface{}{}
	if req.Idx != nil {
		updates["idx"] = *req.Idx
	}
	if req.Type != nil {
		updates["type"] = *req.Type
	}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Models != nil {
		updates["models"] = *req.Models
	}
	if req.BaseURL != nil {
		updates["base_url"] = *req.BaseURL
	}
	if req.BaseURLs != nil {
		updates["base_urls"] = model.NewJSONValue(*req.BaseURLs)
	}
	if req.APIKey != nil {
		updates["api_key"] = *req.APIKey
	}
	if req.APIKeys != nil {
		updates["api_keys"] = model.NewJSONValue(*req.APIKeys)
	}
	if req.KeySelectionMode != nil {
		updates["key_selection_mode"] = *req.KeySelectionMode
	}
	if req.Upstreams != nil {
		updates["upstreams"] = model.NewJSONValue(*req.Upstreams)
	}
	if req.ModelRewrites != nil {
		updates["model_rewrites"] = rawJSONValue(*req.ModelRewrites)
	}
	if req.Group != nil {
		updates["channel_group"] = *req.Group
	}
	if req.UpstreamOptions != nil {
		updates["upstream_options"] = rawJSONValue(*req.UpstreamOptions)
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if err := h.svc.Update(id, updates); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 500, "查询通道失败")
		return
	}
	if err := h.recordChannelAudit(c, operator, "channel.update", id, channelAuditSummary(before), channelAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "通道已更新")
}

// DELETE /v0/admin/channel/:id — 删除通道
func (h *ChannelHandler) Delete(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.Delete(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordChannelAudit(c, operator, "channel.delete", id, channelAuditSummary(before), nil); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "通道已删除")
}

// PATCH /v0/admin/channel/:id/disable — 禁用通道
func (h *ChannelHandler) Disable(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.Disable(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 500, "查询通道失败")
		return
	}
	if err := h.recordChannelAudit(c, operator, "channel.disable", id, channelAuditSummary(before), channelAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "通道已禁用")
}

// PATCH /v0/admin/channel/:id/enable — 启用通道
func (h *ChannelHandler) Enable(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.Enable(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 500, "查询通道失败")
		return
	}
	if err := h.recordChannelAudit(c, operator, "channel.enable", id, channelAuditSummary(before), channelAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "通道已启用")
}

// POST /v0/admin/channel/:id/test — 测试通道连通性
func (h *ChannelHandler) Test(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	success, responseMs, modelCount, err := h.svc.Test(id)
	result := dto.TestChannelResult{Success: success, ResponseMs: responseMs, ModelCount: modelCount}
	if err != nil {
		result.Error = err.Error()
		if auditErr := h.recordChannelAuditResult(c, operator, "channel.test", id, channelAuditSummary(before), channelTestAuditSummary(before, result), "failed", "channel_test_failed"); auditErr != nil {
			common.FailWithStatus(c, 500, "写入审计日志失败")
			return
		}
		common.Success(c, result)
		return
	}
	after, getErr := h.svc.GetByID(id)
	if getErr != nil {
		common.FailWithStatus(c, 500, "查询通道失败")
		return
	}
	if auditErr := h.recordChannelAudit(c, operator, "channel.test", id, channelAuditSummary(before), channelTestAuditSummary(after, result)); auditErr != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, result)
}

// GET /v0/admin/channel/:id/models — 从单上游 URL 获取模型列表
func (h *ChannelHandler) FetchModels(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	channel, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	models, err := h.svc.FetchUpstreamModels(id)
	if err != nil {
		if auditErr := h.recordChannelAuditResult(c, operator, "channel.fetch_models", id, channelAuditSummary(channel), map[string]interface{}{
			"channel": channelAuditSummary(channel),
			"error":   err.Error(),
		}, "failed", "channel_fetch_models_failed"); auditErr != nil {
			common.FailWithStatus(c, 500, "写入审计日志失败")
			return
		}
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if auditErr := h.recordChannelAudit(c, operator, "channel.fetch_models", id, channelAuditSummary(channel), map[string]interface{}{
		"channel":     channelAuditSummary(channel),
		"model_count": len(models),
		"models":      models,
	}); auditErr != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.FetchChannelModelsResult{Models: models})
}

func (h *ChannelHandler) recordChannelAudit(c *gin.Context, operator *model.User, action string, id uint, before, after interface{}) error {
	return h.recordChannelAuditResult(c, operator, action, id, before, after, "success", "")
}

func (h *ChannelHandler) channelInfo(channel *model.Channel) dto.ChannelInfo {
	info := dto.ChannelInfoFromModel(channel)
	if channel == nil || h == nil || h.svc == nil {
		return info
	}
	health := h.svc.ChannelHealthSummary(*channel)
	info.HealthStatus = health.Status
	info.HealthReason = health.Reason
	info.CooldownRemainingSeconds = health.CooldownRemainingSeconds
	return info
}

func (h *ChannelHandler) channelInfos(channels []model.Channel) []dto.ChannelInfo {
	items := make([]dto.ChannelInfo, 0, len(channels))
	for i := range channels {
		items = append(items, h.channelInfo(&channels[i]))
	}
	return items
}

func (h *ChannelHandler) recordChannelAuditResult(c *gin.Context, operator *model.User, action string, id uint, before, after interface{}, result, errorCode string) error {
	return h.auditSvc.RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:     c.GetString("request_id"),
		ActorUserID:   operator.ID,
		ActorRole:     operator.Role,
		Action:        action,
		ResourceType:  "channel",
		ResourceID:    strconv.FormatUint(uint64(id), 10),
		BeforeSummary: auditSummary(before),
		AfterSummary:  auditSummary(after),
		Result:        result,
		ErrorCode:     errorCode,
		IP:            c.ClientIP(),
		UserAgent:     c.GetHeader("User-Agent"),
	})
}

// channelAuditSummary uses the public DTO shape as a whitelist so encrypted
// upstream secrets are never copied into admin_audit_logs.
func channelAuditSummary(channel *model.Channel) map[string]interface{} {
	if channel == nil {
		return nil
	}
	info := dto.ChannelInfoFromModel(channel)
	return map[string]interface{}{
		"id":                 info.ID,
		"idx":                info.Idx,
		"type":               info.Type,
		"name":               info.Name,
		"models":             info.Models,
		"base_url":           info.BaseURL,
		"base_urls":          info.BaseURLs,
		"key_selection_mode": info.KeySelectionMode,
		"api_key_count":      info.APIKeyCount,
		"upstreams":          info.Upstreams,
		"model_rewrites":     info.ModelRewrites,
		"group":              info.Group,
		"priority":           info.Priority,
		"weight":             info.Weight,
		"status":             info.Status,
		"response_ms":        info.ResponseMs,
		"balance":            info.Balance,
		"error_count":        info.ErrorCount,
	}
}

func channelTestAuditSummary(channel *model.Channel, result dto.TestChannelResult) map[string]interface{} {
	return map[string]interface{}{
		"channel":     channelAuditSummary(channel),
		"success":     result.Success,
		"response_ms": result.ResponseMs,
		"model_count": result.ModelCount,
		"error":       result.Error,
	}
}

func rawJSONValue(raw json.RawMessage) model.JSONValue {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if !json.Valid(raw) {
		return nil
	}
	return model.JSONValue(raw)
}
