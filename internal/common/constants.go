package common

// 用户角色
const (
	RoleUser  = 0
	RoleAdmin = 1
	RoleSuper = 2
)

// 用户状态
const (
	UserStatusDisabled = 0
	UserStatusEnabled  = 1
)

// 令牌状态
const (
	TokenStatusDisabled = 0
	TokenStatusEnabled  = 1
)

// 通道类型 (厂商)
const (
	ChannelTypeOpenAI       = 1
	ChannelTypeAzure        = 2
	ChannelTypeClaude       = 3
	ChannelTypeGemini       = 4
	ChannelTypeQwen         = 5
	ChannelTypeDeepSeek     = 6
	ChannelTypeXAI          = 7
	ChannelTypeRouterX      = 8
	ChannelTypeOpenAICompat = 100 // OpenAI-compatible 通用类型
)

// 通道状态
const (
	ChannelStatusDisabled  = 0
	ChannelStatusEnabled   = 1
	ChannelStatusManualOff = 2 // 手动维护
)

// 日志状态
const (
	LogStatusUnknown = 0
	LogStatusSuccess = 1
	LogStatusFailed  = 2
)

// 充值码状态
const (
	RedemCodeStatusUnused   = 0
	RedemCodeStatusUsed     = 1
	RedemCodeStatusDisabled = 2
)

// 额度流水类型
const (
	QuotaTransactionTypePaymentGrant = "payment_grant"
	QuotaTransactionTypeRedemRedeem  = "redem_redeem"
	QuotaTransactionTypeAdminAdjust  = "admin_adjust"
	QuotaTransactionTypeRefundDeduct = "refund_deduct"
	QuotaTransactionTypeManualCredit = "manual_credit"
	QuotaTransactionTypeManualDebit  = "manual_debit"
)

// 额度流水来源类型
const (
	QuotaSourceTypePaymentOrder = "payment_order"
	QuotaSourceTypePaymentEvent = "payment_event"
	QuotaSourceTypeRedemCode    = "redem_code"
	QuotaSourceTypeAdminAction  = "admin_action"
	QuotaSourceTypeRefund       = "refund"
)

// 支付 provider 和订单状态
const (
	PaymentProviderStripe = "stripe"
	PaymentProviderEpay   = "epay"

	PaymentOrderStatusPending  = "pending"
	PaymentOrderStatusPaid     = "paid"
	PaymentOrderStatusFailed   = "failed"
	PaymentOrderStatusClosed   = "closed"
	PaymentOrderStatusRefunded = "refunded"
)

// 未限制额度标记
const QuotaUnlimited = -1

// 额度单位: 100000000 基础单位 = 1 货币额度
const QuotaPerUnit = 100000000
