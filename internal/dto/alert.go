package dto

import (
	"time"

	"routerx/internal/model"
)

type AlertEventInfo struct {
	ID            uint            `json:"id"`
	Type          string          `json:"type"`
	Severity      string          `json:"severity"`
	Status        string          `json:"status"`
	ResourceType  string          `json:"resource_type"`
	ResourceID    string          `json:"resource_id"`
	UserID        *uint           `json:"user_id,omitempty"`
	TokenID       *uint           `json:"token_id,omitempty"`
	Title         string          `json:"title"`
	Message       string          `json:"message"`
	Details       model.JSONValue `json:"details,omitempty"`
	AckedAt       *time.Time      `json:"acked_at,omitempty"`
	AckedByUserID *uint           `json:"acked_by_user_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func AlertEventInfoFromModel(alert *model.AlertEvent) AlertEventInfo {
	if alert == nil {
		return AlertEventInfo{}
	}
	return AlertEventInfo{
		ID:            alert.ID,
		Type:          alert.Type,
		Severity:      alert.Severity,
		Status:        alert.Status,
		ResourceType:  alert.ResourceType,
		ResourceID:    alert.ResourceID,
		UserID:        alert.UserID,
		TokenID:       alert.TokenID,
		Title:         alert.Title,
		Message:       alert.Message,
		Details:       alert.DetailsJSON,
		AckedAt:       alert.AckedAt,
		AckedByUserID: alert.AckedByUserID,
		CreatedAt:     alert.CreatedAt,
		UpdatedAt:     alert.UpdatedAt,
	}
}

func AlertEventInfosFromModels(alerts []model.AlertEvent) []AlertEventInfo {
	items := make([]AlertEventInfo, 0, len(alerts))
	for i := range alerts {
		items = append(items, AlertEventInfoFromModel(&alerts[i]))
	}
	return items
}
