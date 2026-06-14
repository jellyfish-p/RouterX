package model

import "time"

// PaymentProduct 充值商品表。
// 商品金额、币种和入账额度都来自服务端配置，创建订单时会保存快照。
type PaymentProduct struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	ProductID          string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"product_id"`
	Name               string    `gorm:"type:varchar(128);not null" json:"name"`
	Amount             string    `gorm:"type:varchar(32);not null" json:"amount"`
	Currency           string    `gorm:"type:varchar(16);not null" json:"currency"`
	Quota              int64     `gorm:"not null" json:"quota"`
	BonusQuota         int64     `gorm:"not null;default:0" json:"bonus_quota"`
	Enabled            bool      `gorm:"not null;default:true;index" json:"enabled"`
	ProviderConfigJSON JSONValue `gorm:"type:json" json:"provider_config_json,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// PaymentOrder 支付订单表。
// pending 订单只代表购买意图；只有可信 provider 回调才能进一步入账。
type PaymentOrder struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	OrderNo           string     `gorm:"type:varchar(64);not null;uniqueIndex" json:"order_no"`
	UserID            uint       `gorm:"not null;index:idx_payment_orders_user_created_at,priority:1" json:"user_id"`
	User              *User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
	ProductID         string     `gorm:"type:varchar(64);not null" json:"product_id"`
	Provider          string     `gorm:"type:varchar(32);not null;index" json:"provider"`
	Amount            string     `gorm:"type:varchar(32);not null" json:"amount"`
	Currency          string     `gorm:"type:varchar(16);not null" json:"currency"`
	Quota             int64      `gorm:"not null" json:"quota"`
	Status            string     `gorm:"type:varchar(32);not null;index" json:"status"`
	ProviderOrderID   *string    `gorm:"type:varchar(128);index" json:"provider_order_id,omitempty"`
	ProviderPaymentID *string    `gorm:"type:varchar(128)" json:"provider_payment_id,omitempty"`
	CheckoutURL       *string    `gorm:"type:text" json:"checkout_url,omitempty"`
	PaidAt            *time.Time `json:"paid_at,omitempty"`
	ExpiredAt         *time.Time `json:"expired_at,omitempty"`
	CreatedAt         time.Time  `gorm:"index:idx_payment_orders_user_created_at,priority:2" json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// PaymentEvent 支付事件表。
// Webhook 切片会使用该表记录 provider 事件、签名结果和幂等处理状态。
type PaymentEvent struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	Provider        string     `gorm:"type:varchar(32);not null;uniqueIndex:idx_payment_events_provider_event,priority:1" json:"provider"`
	ProviderEventID string     `gorm:"type:varchar(128);not null;uniqueIndex:idx_payment_events_provider_event,priority:2" json:"provider_event_id"`
	OrderNo         string     `gorm:"type:varchar(64);not null;index" json:"order_no"`
	EventType       string     `gorm:"type:varchar(128);not null" json:"event_type"`
	Payload         string     `gorm:"type:text" json:"payload"`
	SignatureValid  bool       `gorm:"not null;default:false" json:"signature_valid"`
	Processed       bool       `gorm:"not null;default:false" json:"processed"`
	ProcessedAt     *time.Time `json:"processed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}
