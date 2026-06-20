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

type AlertDeliveryOutboxInfo struct {
	ID            uint       `json:"id"`
	AlertID       uint       `json:"alert_id"`
	Target        string     `json:"target"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"last_error,omitempty"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type AlertDeliveryReplayResult struct {
	Replayed int `json:"replayed"`
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

func AlertDeliveryOutboxInfoFromModel(item *model.AlertDeliveryOutbox) AlertDeliveryOutboxInfo {
	if item == nil {
		return AlertDeliveryOutboxInfo{}
	}
	return AlertDeliveryOutboxInfo{
		ID:            item.ID,
		AlertID:       item.AlertID,
		Target:        item.Target,
		Status:        item.Status,
		Attempts:      item.Attempts,
		LastError:     item.LastError,
		NextAttemptAt: item.NextAttemptAt,
		CompletedAt:   item.CompletedAt,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
	}
}

func AlertDeliveryOutboxInfosFromModels(items []model.AlertDeliveryOutbox) []AlertDeliveryOutboxInfo {
	result := make([]AlertDeliveryOutboxInfo, 0, len(items))
	for i := range items {
		result = append(result, AlertDeliveryOutboxInfoFromModel(&items[i]))
	}
	return result
}
