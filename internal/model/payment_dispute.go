package model

import "time"

// PaymentDispute 记录 provider 争议/拒付的当前事实。
// payment_events 保留每个 webhook 事件；本表按 provider_dispute_id 聚合生命周期状态。
type PaymentDispute struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	Provider          string    `gorm:"type:varchar(32);not null;uniqueIndex:idx_payment_disputes_provider_dispute,priority:1" json:"provider"`
	ProviderDisputeID string    `gorm:"type:varchar(128);not null;uniqueIndex:idx_payment_disputes_provider_dispute,priority:2" json:"provider_dispute_id"`
	OrderNo           string    `gorm:"type:varchar(64);not null;index" json:"order_no"`
	UserID            uint      `gorm:"not null;index" json:"user_id"`
	ProviderPaymentID string    `gorm:"type:varchar(128);not null;index" json:"provider_payment_id"`
	AmountMinor       int64     `gorm:"not null" json:"amount_minor"`
	Currency          string    `gorm:"type:varchar(16);not null" json:"currency"`
	Status            string    `gorm:"type:varchar(32);not null;index" json:"status"`
	Reason            string    `gorm:"type:varchar(128)" json:"reason,omitempty"`
	FundsStatus       string    `gorm:"type:varchar(32)" json:"funds_status,omitempty"`
	LastEventID       string    `gorm:"type:varchar(128);not null" json:"last_event_id"`
	LastEventType     string    `gorm:"type:varchar(128);not null" json:"last_event_type"`
	CreatedAt         time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
