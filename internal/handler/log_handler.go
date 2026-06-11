package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
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
	userID := queryUintPtr(c, "user_id")
	tokenID := queryUintPtr(c, "token_id")
	channelID := queryUintPtr(c, "channel_id")
	status := queryIntPtr(c, "status")
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	logs, total, err := h.svc.List(userID, tokenID, channelID, c.Query("model"), status, c.Query("start_time"), c.Query("end_time"), page, pageSize)
	if err != nil {
		common.FailWithStatus(c, 500, "查询日志失败")
		return
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: logs})
}

// DELETE /v0/admin/log — 清空日志
func (h *LogHandler) AdminClear(c *gin.Context) {
	before, ok := parseQueryTime(c.Query("before"))
	if !ok {
		common.FailWithStatus(c, 400, "必须提供 before 时间范围")
		return
	}
	if err := h.svc.ClearBefore(before); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "日志已清理")
}

// GET /v0/user/log — 我的请求日志 (用户视角)
func (h *LogHandler) UserList(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	userID := user.ID
	tokenID := queryUintPtr(c, "token_id")
	channelID := queryUintPtr(c, "channel_id")
	status := queryIntPtr(c, "status")
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	logs, total, err := h.svc.List(&userID, tokenID, channelID, c.Query("model"), status, c.Query("start_time"), c.Query("end_time"), page, pageSize)
	if err != nil {
		common.FailWithStatus(c, 500, "查询日志失败")
		return
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: logs})
}

// GET /v0/user/billing — 用量统计
func (h *LogHandler) UserBilling(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	callCount, totalQuota, totalTokens, err := h.svc.GetUserStats(user.ID, c.Query("start_time"), c.Query("end_time"))
	if err != nil {
		common.FailWithStatus(c, 500, "查询账单失败")
		return
	}
	common.Success(c, gin.H{
		"call_count":   callCount,
		"total_quota":  totalQuota,
		"total_tokens": totalTokens,
	})
}

// GET /v0/admin/dashboard — 仪表盘统计
func (h *LogHandler) Dashboard(c *gin.Context) {
	userCount, channelCount, tokenCount, todayCalls, todayQuota, activeChannels, err := h.svc.GetDashboardStats()
	if err != nil {
		common.FailWithStatus(c, 500, "查询仪表盘失败")
		return
	}
	common.Success(c, dto.DashboardStats{
		UserCount:          userCount,
		ChannelCount:       channelCount,
		TokenCount:         tokenCount,
		TodayCallCount:     todayCalls,
		TodayQuotaUsed:     todayQuota,
		ActiveChannelCount: activeChannels,
	})
}

func queryInt(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return fallback
	}
	return value
}

func queryIntPtr(c *gin.Context, key string) *int {
	raw := c.Query(key)
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &value
}

func queryUintPtr(c *gin.Context, key string) *uint {
	raw := c.Query(key)
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return nil
	}
	v := uint(value)
	return &v
}

func parseQueryTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
