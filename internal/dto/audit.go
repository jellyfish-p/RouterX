package dto

import (
	"time"

	"routerx/internal/model"
)

type AdminAuditListRequest struct {
	Page         int    `form:"page"`
	PageSize     int    `form:"page_size"`
	Action       string `form:"action"`
	ResourceType string `form:"resource_type"`
	ResourceID   string `form:"resource_id"`
	ActorUserID  uint   `form:"actor_user_id"`
}

type AdminAuditLogInfo struct {
	ID            uint      `json:"id"`
	RequestID     *string   `json:"request_id,omitempty"`
	ActorUserID   uint      `json:"actor_user_id"`
	ActorRole     int       `json:"actor_role"`
	Action        string    `json:"action"`
	ResourceType  string    `json:"resource_type"`
	ResourceID    string    `json:"resource_id"`
	BeforeSummary string    `json:"before_summary,omitempty"`
	AfterSummary  string    `json:"after_summary,omitempty"`
	Result        string    `json:"result"`
	ErrorCode     string    `json:"error_code,omitempty"`
	IP            string    `json:"ip,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func AdminAuditLogInfoFromModel(log *model.AdminAuditLog) AdminAuditLogInfo {
	if log == nil {
		return AdminAuditLogInfo{}
	}
	return AdminAuditLogInfo{
		ID:            log.ID,
		RequestID:     log.RequestID,
		ActorUserID:   log.ActorUserID,
		ActorRole:     log.ActorRole,
		Action:        log.Action,
		ResourceType:  log.ResourceType,
		ResourceID:    log.ResourceID,
		BeforeSummary: log.BeforeSummary,
		AfterSummary:  log.AfterSummary,
		Result:        log.Result,
		ErrorCode:     log.ErrorCode,
		IP:            log.IP,
		UserAgent:     log.UserAgent,
		CreatedAt:     log.CreatedAt,
	}
}

func AdminAuditLogInfosFromModels(logs []model.AdminAuditLog) []AdminAuditLogInfo {
	items := make([]AdminAuditLogInfo, 0, len(logs))
	for i := range logs {
		items = append(items, AdminAuditLogInfoFromModel(&logs[i]))
	}
	return items
}
