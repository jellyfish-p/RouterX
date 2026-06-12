package handler

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
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
	var req dto.ChannelListRequest
	_ = c.ShouldBindQuery(&req)
	channels, total, err := h.svc.List(req.Page, req.PageSize, req.Type, req.Status)
	if err != nil {
		common.FailWithStatus(c, 500, "查询通道失败")
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.ChannelInfosFromModels(channels)})
}

// POST /v0/admin/channel — 创建通道
func (h *ChannelHandler) Create(c *gin.Context) {
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
	common.Success(c, dto.ChannelInfoFromModel(channel))
}

// PUT /v0/admin/channel/:id — 编辑通道
func (h *ChannelHandler) Update(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "编辑通道参数无效")
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
	common.SuccessMsg(c, "通道已更新")
}

// DELETE /v0/admin/channel/:id — 删除通道
func (h *ChannelHandler) Delete(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "通道已删除")
}

// PATCH /v0/admin/channel/:id/disable — 禁用通道
func (h *ChannelHandler) Disable(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Disable(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "通道已禁用")
}

// PATCH /v0/admin/channel/:id/enable — 启用通道
func (h *ChannelHandler) Enable(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Enable(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "通道已启用")
}

// POST /v0/admin/channel/:id/test — 测试通道连通性
func (h *ChannelHandler) Test(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	success, responseMs, modelCount, err := h.svc.Test(id)
	result := dto.TestChannelResult{Success: success, ResponseMs: responseMs, ModelCount: modelCount}
	if err != nil {
		result.Error = err.Error()
		common.Success(c, result)
		return
	}
	common.Success(c, result)
}

// GET /v0/admin/channel/:id/models — 从单上游 URL 获取模型列表
func (h *ChannelHandler) FetchModels(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	models, err := h.svc.FetchUpstreamModels(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.Success(c, dto.FetchChannelModelsResult{Models: models})
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
