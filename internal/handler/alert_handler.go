package handler

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/service"
)

type AlertHandler struct {
	svc *service.AlertService
}

func NewAlertHandler(svc *service.AlertService) *AlertHandler {
	return &AlertHandler{svc: svc}
}

func (h *AlertHandler) List(c *gin.Context) {
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	alerts, total, err := h.svc.List(service.AlertFilter{
		Type:         c.Query("type"),
		Severity:     c.Query("severity"),
		Status:       c.Query("status"),
		ResourceType: c.Query("resource_type"),
		ResourceID:   c.Query("resource_id"),
		UserID:       queryUintPtr(c, "user_id"),
		TokenID:      queryUintPtr(c, "token_id"),
		Page:         page,
		PageSize:     pageSize,
	})
	if err != nil {
		common.FailWithStatus(c, 500, "查询告警失败")
		return
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.AlertEventInfosFromModels(alerts)})
}

func (h *AlertHandler) ListDeliveries(c *gin.Context) {
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	items, total, err := h.svc.ListDeliveries(service.AlertDeliveryFilter{
		AlertID:  queryUintPtr(c, "alert_id"),
		Target:   c.Query("target"),
		Status:   c.Query("status"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		common.FailWithStatus(c, 500, "查询告警投递失败")
		return
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.AlertDeliveryOutboxInfosFromModels(items)})
}

func (h *AlertHandler) ReplayDeliveries(c *gin.Context) {
	limit := queryInt(c, "limit", 20)
	replayed, err := h.svc.ReplayWebhookDeliveryOutbox(limit)
	if err != nil {
		common.FailWithStatus(c, 500, "重放告警投递失败")
		return
	}
	common.Success(c, dto.AlertDeliveryReplayResult{Replayed: replayed})
}

func (h *AlertHandler) Ack(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	alert, err := h.svc.Acknowledge(id, operator.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.FailWithStatus(c, 404, "告警不存在")
			return
		}
		common.FailWithStatus(c, 500, "确认告警失败")
		return
	}
	common.Success(c, dto.AlertEventInfoFromModel(alert))
}
