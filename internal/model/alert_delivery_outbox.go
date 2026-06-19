package model

import "time"

const (
	AlertDeliveryTargetWebhook = "webhook"
	AlertDeliveryTargetEmail   = "email"
	AlertDeliveryTargetIM      = "im"

	AlertDeliveryStatusPending   = "pending"
	AlertDeliveryStatusCompleted = "completed"
	AlertDeliveryStatusFailed    = "failed"
)

// AlertDeliveryOutbox records alert notifications that still need to be sent
// to external channels. Payloads are rebuilt from AlertEvent so this table never
// stores API key secrets or channel credentials.
type AlertDeliveryOutbox struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	AlertID       uint       `gorm:"not null;uniqueIndex:idx_alert_delivery_target_alert,priority:2;index" json:"alert_id"`
	Target        string     `gorm:"type:varchar(32);not null;default:'webhook';uniqueIndex:idx_alert_delivery_target_alert,priority:1;index" json:"target"`
	Status        string     `gorm:"type:varchar(32);not null;default:'pending';index" json:"status"`
	Attempts      int        `gorm:"not null;default:0" json:"attempts"`
	LastError     string     `gorm:"type:text" json:"last_error,omitempty"`
	NextAttemptAt time.Time  `gorm:"not null;index" json:"next_attempt_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
