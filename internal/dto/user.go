package dto

import (
	"fmt"
	"time"

	"routerx/internal/model"
)

// UserListRequest 用户列表查询参数
type UserListRequest struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	Keyword  string `form:"keyword"` // 模糊搜索用户名/显示名
	Role     *int   `form:"role"`
	Status   *int   `form:"status"`
	GroupID  *uint  `form:"group_id"`
}

type RedemCodeListRequest struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	Status   *int   `form:"status"`
	Keyword  string `form:"keyword"`
	BatchNo  string `form:"batch_no"`
}

type UserGroupListRequest struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	Keyword  string `form:"keyword"`
}

// QuotaTransactionListRequest 额度流水查询参数。
// 用户侧会忽略 user_id，始终只返回当前登录用户的流水。
type QuotaTransactionListRequest struct {
	Page       int    `form:"page" binding:"omitempty,min=1"`
	PageSize   int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	UserID     *uint  `form:"user_id"`
	Type       string `form:"type"`
	SourceType string `form:"source_type"`
	SourceID   string `form:"source_id"`
	StartTime  string `form:"start_time"`
	EndTime    string `form:"end_time"`
}

type CreateUserGroupRequest struct {
	Name  string  `json:"name" binding:"required,min=1,max=64"`
	Ratio float64 `json:"ratio"`
}

type UpdateUserGroupRequest struct {
	Name  *string  `json:"name"`
	Ratio *float64 `json:"ratio"`
}

type UserGroupInfo struct {
	ID        uint      `json:"id"`
	Name      string    `json:"name"`
	Ratio     float64   `json:"ratio"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateUserRequest 创建用户请求 (Admin)
type CreateUserRequest struct {
	Username    string `json:"username" binding:"required,min=3,max=64"`
	Password    string `json:"password" binding:"required,min=6,max=128"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        int    `json:"role"`
	Quota       int64  `json:"quota"`
	GroupID     *uint  `json:"group_id"`
}

// UpdateUserRequest 编辑用户请求 (Admin)
type UpdateUserRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        *int   `json:"role"`
	Status      *int   `json:"status"`
	GroupID     *uint  `json:"group_id"`
}

// UpdateQuotaRequest 调整用户余额
type UpdateQuotaRequest struct {
	Quota  int64  `json:"quota" binding:"required"`
	Reason string `json:"reason"`
}

type QuotaTransactionInfo struct {
	ID            uint      `json:"id"`
	UserID        uint      `json:"user_id"`
	Type          string    `json:"type"`
	Amount        int64     `json:"amount"`
	BalanceBefore int64     `json:"balance_before"`
	BalanceAfter  int64     `json:"balance_after"`
	SourceType    string    `json:"source_type"`
	SourceID      string    `json:"source_id"`
	Reason        string    `json:"reason,omitempty"`
	ActorUserID   *uint     `json:"actor_user_id,omitempty"`
	RequestID     *string   `json:"request_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// UpdateSelfRequest 用户自助修改个人信息
type UpdateSelfRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

type CancelSelfRequest struct {
	Password string `json:"password" binding:"required"`
}

type RedeemCodeRequest struct {
	Code string `json:"code" binding:"required"`
}

type RedeemCodeResult struct {
	RedeemedQuota int64 `json:"redeemed_quota"`
	Quota         int64 `json:"quota"`
}

type UserModelInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PriceRule    string `json:"price_rule"`
	PricingReady bool   `json:"pricing_ready"`
}

type UserModelListResult struct {
	Models []UserModelInfo `json:"models"`
}

func UserModelInfosFromNames(names []string) []UserModelInfo {
	return UserModelInfosFromNamesAndPrices(names, nil)
}

func UserModelInfosFromNamesAndPrices(names []string, prices map[string]model.ModelPrice) []UserModelInfo {
	return UserModelInfosFromNamesAndPriceMaps(names, nil, prices)
}

func UserModelInfosFromNamesAndPriceMaps(names []string, channelPrices map[string]model.ChannelModelPrice, prices map[string]model.ModelPrice) []UserModelInfo {
	items := make([]UserModelInfo, 0, len(names))
	for _, name := range names {
		priceRule := "minimum_usage"
		pricingReady := false
		if price, ok := channelPrices[name]; ok && price.Enabled && price.UserEnabled {
			priceRule = fmt.Sprintf("channel_model_price:%s:v%d", price.PriceMode, price.RuleVersion)
			pricingReady = true
		} else if price, ok := prices[name]; ok && price.Enabled {
			priceRule = fmt.Sprintf("model_price:%s:v%d", price.PriceMode, price.RuleVersion)
			pricingReady = true
		}
		items = append(items, UserModelInfo{
			ID:           name,
			Name:         name,
			PriceRule:    priceRule,
			PricingReady: pricingReady,
		})
	}
	return items
}

type CreateRedemCodesRequest struct {
	Quota     int64    `json:"quota" binding:"required"`
	Count     int      `json:"count"`
	Codes     []string `json:"codes"`
	BatchNo   string   `json:"batch_no"`
	Note      string   `json:"note"`
	ExpiredAt *int64   `json:"expired_at"`
}

type RedemCodeInfo struct {
	ID        uint       `json:"id"`
	Code      string     `json:"code"`
	Quota     int64      `json:"quota"`
	Status    int        `json:"status"`
	BatchNo   string     `json:"batch_no,omitempty"`
	Note      string     `json:"note,omitempty"`
	ExpiredAt *time.Time `json:"expired_at,omitempty"`
	UsedBy    *uint      `json:"used_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
}

func RedemCodeInfoFromModel(code *model.RedemCode) RedemCodeInfo {
	if code == nil {
		return RedemCodeInfo{}
	}
	return RedemCodeInfo{
		ID:        code.ID,
		Code:      code.Code,
		Quota:     code.Quota,
		Status:    code.Status,
		BatchNo:   code.BatchNo,
		Note:      code.Note,
		ExpiredAt: code.ExpiredAt,
		UsedBy:    code.UsedBy,
		CreatedAt: code.CreatedAt,
		UsedAt:    code.UsedAt,
	}
}

func RedemCodeInfosFromModels(codes []model.RedemCode) []RedemCodeInfo {
	items := make([]RedemCodeInfo, 0, len(codes))
	for i := range codes {
		items = append(items, RedemCodeInfoFromModel(&codes[i]))
	}
	return items
}
