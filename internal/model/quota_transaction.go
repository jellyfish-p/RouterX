package model

import "time"

// QuotaTransaction 额度变更流水。
// 支付、充值码、人工调整等入账/扣回操作都应写入这里，模型调用消费仍以 logs 为事实来源。
type QuotaTransaction struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	UserID         uint      `gorm:"not null;index:idx_quota_transactions_user_created_at,priority:1" json:"user_id"`
	User           *User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Type           string    `gorm:"type:varchar(32);not null" json:"type"`
	Amount         int64     `gorm:"not null" json:"amount"`
	BalanceBefore  int64     `gorm:"not null" json:"balance_before"`
	BalanceAfter   int64     `gorm:"not null" json:"balance_after"`
	SourceType     string    `gorm:"type:varchar(64);not null;index:idx_quota_transactions_source,priority:1" json:"source_type"`
	SourceID       string    `gorm:"type:varchar(128);not null;index:idx_quota_transactions_source,priority:2" json:"source_id"`
	IdempotencyKey string    `gorm:"type:varchar(191);not null;uniqueIndex" json:"idempotency_key"`
	Reason         string    `gorm:"type:text" json:"reason,omitempty"`
	ActorUserID    *uint     `gorm:"index" json:"actor_user_id,omitempty"`
	ActorUser      *User     `gorm:"foreignKey:ActorUserID" json:"actor_user,omitempty"`
	RequestID      *string   `gorm:"type:varchar(128)" json:"request_id,omitempty"`
	CreatedAt      time.Time `gorm:"not null;index:idx_quota_transactions_user_created_at,priority:2" json:"created_at"`
}
