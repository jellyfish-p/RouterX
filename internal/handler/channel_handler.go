package handler

import (
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
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: channels})
}

// POST /v0/admin/channel — 创建通道
func (h *ChannelHandler) Create(c *gin.Context) {
	var req dto.CreateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "创建通道参数无效")
		return
	}
	channel := &model.Channel{
		Type:     req.Type,
		Name:     req.Name,
		Models:   req.Models,
		BaseURL:  req.BaseURL,
		APIKey:   req.APIKey,
		Priority: req.Priority,
		Weight:   req.Weight,
		Status:   common.ChannelStatusEnabled,
	}
	if err := h.svc.Create(channel); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.Success(c, channel)
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
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Models != nil {
		updates["models"] = *req.Models
	}
	if req.BaseURL != nil {
		updates["base_url"] = *req.BaseURL
	}
	if req.APIKey != nil {
		updates["api_key"] = *req.APIKey
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
