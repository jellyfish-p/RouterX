package handler

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
	"routerx/internal/service"
)

type LogHandler struct {
	svc      *service.LogService
	auditSvc *service.UserService
}

func NewLogHandler(svc *service.LogService) *LogHandler {
	return &LogHandler{svc: svc, auditSvc: service.NewUserService()}
}

// GET /v0/admin/log — 请求日志列表 (Admin 全局视角)
func (h *LogHandler) AdminList(c *gin.Context) {
	userID := queryUintPtr(c, "user_id")
	tokenID := queryUintPtr(c, "token_id")
	channelID := queryUintPtr(c, "channel_id")
	status := queryIntPtr(c, "status")
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	logs, total, err := h.svc.List(userID, tokenID, channelID, c.Query("model"), status, c.Query("error_code"), c.Query("error_source"), queryIntPtr(c, "upstream_status"), c.Query("start_time"), c.Query("end_time"), page, pageSize)
	if err != nil {
		common.FailWithStatus(c, 500, "查询日志失败")
		return
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: logs})
}

// DELETE /v0/admin/log — 清空日志
func (h *LogHandler) AdminClear(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	before, ok := parseQueryTime(c.Query("before"))
	if !ok {
		common.FailWithStatus(c, 400, "必须提供 before 时间范围")
		return
	}
	if err := h.svc.ClearBefore(before); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordLogAudit(c, operator, "log.clear", logClearAuditSummary(before)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "日志已清理")
}

// GET /v0/admin/log/export — 导出脱敏后的调用日志 CSV。
func (h *LogHandler) AdminExport(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	filter := adminLogFilterFromQuery(c)
	logs, limit, err := h.svc.Export(filter, queryInt(c, "limit", 0))
	if err != nil {
		common.FailWithStatus(c, 500, "导出日志失败")
		return
	}
	csvBytes, err := buildLogExportCSV(logs)
	if err != nil {
		common.FailWithStatus(c, 500, "生成日志导出文件失败")
		return
	}
	if err := h.recordLogAudit(c, operator, "log.export", logExportAuditSummary(filter, limit, len(logs))); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="routerx-logs.csv"`)
	c.Data(200, "text/csv; charset=utf-8", csvBytes)
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
	logs, total, err := h.svc.List(&userID, tokenID, channelID, c.Query("model"), status, c.Query("error_code"), c.Query("error_source"), queryIntPtr(c, "upstream_status"), c.Query("start_time"), c.Query("end_time"), page, pageSize)
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
	callCount, totalQuota, totalTokens, err := h.svc.GetUserStats(user.ID, queryUintPtr(c, "token_id"), c.Query("start_time"), c.Query("end_time"))
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

func adminLogFilterFromQuery(c *gin.Context) service.LogFilters {
	return service.LogFilters{
		UserID:         queryUintPtr(c, "user_id"),
		TokenID:        queryUintPtr(c, "token_id"),
		ChannelID:      queryUintPtr(c, "channel_id"),
		ModelName:      c.Query("model"),
		Status:         queryIntPtr(c, "status"),
		ErrorCode:      c.Query("error_code"),
		ErrorSource:    c.Query("error_source"),
		UpstreamStatus: queryIntPtr(c, "upstream_status"),
		StartTime:      c.Query("start_time"),
		EndTime:        c.Query("end_time"),
	}
}

func buildLogExportCSV(logs []model.Log) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write([]string{
		"id",
		"user_id",
		"token_id",
		"channel_id",
		"model",
		"prompt_tokens",
		"completion_tokens",
		"total_tokens",
		"usage_source",
		"quota_used",
		"status",
		"error_code",
		"error_source",
		"upstream_status",
		"request_id",
		"created_at",
	}); err != nil {
		return nil, err
	}
	for _, entry := range logs {
		if err := writer.Write(logExportCSVRecord(entry)); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return buf.Bytes(), writer.Error()
}

func logExportCSVRecord(entry model.Log) []string {
	return []string{
		strconv.FormatUint(uint64(entry.ID), 10),
		strconv.FormatUint(uint64(entry.UserID), 10),
		nullableUintCSV(entry.TokenID),
		nullableUintCSV(entry.ChannelID),
		entry.Model,
		strconv.Itoa(entry.PromptTokens),
		strconv.Itoa(entry.CompletionTokens),
		strconv.Itoa(entry.TotalTokens),
		entry.UsageSource,
		strconv.FormatInt(entry.QuotaUsed, 10),
		strconv.Itoa(entry.Status),
		entry.ErrorCode,
		entry.ErrorSource,
		strconv.Itoa(entry.UpstreamStatus),
		entry.RequestID,
		entry.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func nullableUintCSV(value *uint) string {
	if value == nil || *value == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(*value), 10)
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

func (h *LogHandler) recordLogAudit(c *gin.Context, operator *model.User, action string, after interface{}) error {
	return h.auditSvc.RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:    c.GetString("request_id"),
		ActorUserID:  operator.ID,
		ActorRole:    operator.Role,
		Action:       action,
		ResourceType: "log",
		ResourceID:   "admin-log",
		AfterSummary: auditSummary(after),
		Result:       "success",
		IP:           c.ClientIP(),
		UserAgent:    c.GetHeader("User-Agent"),
	})
}

func logClearAuditSummary(before time.Time) map[string]interface{} {
	return map[string]interface{}{
		"before": before.UTC().Format(time.RFC3339),
	}
}

func logExportAuditSummary(filter service.LogFilters, limit, exportedCount int) map[string]interface{} {
	summary := map[string]interface{}{
		"limit":          limit,
		"exported_count": exportedCount,
	}
	filters := map[string]interface{}{}
	if filter.UserID != nil {
		filters["user_id"] = *filter.UserID
	}
	if filter.TokenID != nil {
		filters["token_id"] = *filter.TokenID
	}
	if filter.ChannelID != nil {
		filters["channel_id"] = *filter.ChannelID
	}
	if filter.ModelName != "" {
		filters["model"] = filter.ModelName
	}
	if filter.Status != nil {
		filters["status"] = *filter.Status
	}
	if filter.ErrorCode != "" {
		filters["error_code"] = filter.ErrorCode
	}
	if filter.ErrorSource != "" {
		filters["error_source"] = filter.ErrorSource
	}
	if filter.UpstreamStatus != nil {
		filters["upstream_status"] = *filter.UpstreamStatus
	}
	if filter.StartTime != "" {
		filters["start_time"] = filter.StartTime
	}
	if filter.EndTime != "" {
		filters["end_time"] = filter.EndTime
	}
	if len(filters) > 0 {
		summary["filters"] = filters
	}
	return summary
}
