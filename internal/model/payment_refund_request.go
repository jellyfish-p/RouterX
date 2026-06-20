package model

import "time"

// PaymentRefundRequest 记录 RouterX 主动向 provider 发起的退款请求。
// 它和 payment_events 分开：前者是出站请求，后者是 provider 回传的事实事件。
type PaymentRefundRequest struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	OrderNo          string    `gorm:"type:varchar(64);not null;index" json:"order_no"`
	UserID           uint      `gorm:"not null;index" json:"user_id"`
	Provider         string    `gorm:"type:varchar(32);not null;index" json:"provider"`
	ProviderRefundID string    `gorm:"type:varchar(128);not null;index" json:"provider_refund_id"`
	Amount           string    `gorm:"type:varchar(32);not null" json:"amount"`
	AmountMinor      int64     `gorm:"not null" json:"amount_minor"`
	Currency         string    `gorm:"type:varchar(16);not null" json:"currency"`
	RefundQuota      int64     `gorm:"not null" json:"refund_quota"`
	Status           string    `gorm:"type:varchar(32);not null;index" json:"status"`
	IdempotencyKey   string    `gorm:"type:varchar(191);not null;uniqueIndex" json:"idempotency_key"`
	Reason           string    `gorm:"type:text" json:"reason,omitempty"`
	ActorUserID      uint      `gorm:"not null;index" json:"actor_user_id"`
	RequestID        *string   `gorm:"type:varchar(128)" json:"request_id,omitempty"`
	CreatedAt        time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
