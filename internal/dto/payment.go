package dto

import (
	"time"

	"routerx/internal/model"
)

type CreatePaymentOrderRequest struct {
	Provider  string `json:"provider" binding:"required"`
	ProductID string `json:"product_id" binding:"required"`
	PayType   string `json:"pay_type"`
	ReturnURL string `json:"return_url"`
}

type PaymentProductInfo struct {
	ProductID  string `json:"product_id"`
	Name       string `json:"name"`
	Amount     string `json:"amount"`
	Currency   string `json:"currency"`
	Quota      int64  `json:"quota"`
	BaseQuota  int64  `json:"base_quota"`
	BonusQuota int64  `json:"bonus_quota"`
}

type PaymentOrderInfo struct {
	OrderNo     string     `json:"order_no"`
	Provider    string     `json:"provider"`
	Status      string     `json:"status"`
	ProductID   string     `json:"product_id"`
	Amount      string     `json:"amount"`
	Currency    string     `json:"currency"`
	Quota       int64      `json:"quota"`
	CheckoutURL *string    `json:"checkout_url,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	PaidAt      *time.Time `json:"paid_at,omitempty"`
}

func PaymentProductInfoFromModel(product *model.PaymentProduct) PaymentProductInfo {
	if product == nil {
		return PaymentProductInfo{}
	}
	return PaymentProductInfo{
		ProductID:  product.ProductID,
		Name:       product.Name,
		Amount:     product.Amount,
		Currency:   product.Currency,
		Quota:      product.Quota + product.BonusQuota,
		BaseQuota:  product.Quota,
		BonusQuota: product.BonusQuota,
	}
}

func PaymentProductInfosFromModels(products []model.PaymentProduct) []PaymentProductInfo {
	items := make([]PaymentProductInfo, 0, len(products))
	for i := range products {
		items = append(items, PaymentProductInfoFromModel(&products[i]))
	}
	return items
}

func PaymentOrderInfoFromModel(order *model.PaymentOrder) PaymentOrderInfo {
	if order == nil {
		return PaymentOrderInfo{}
	}
	return PaymentOrderInfo{
		OrderNo:     order.OrderNo,
		Provider:    order.Provider,
		Status:      order.Status,
		ProductID:   order.ProductID,
		Amount:      order.Amount,
		Currency:    order.Currency,
		Quota:       order.Quota,
		CheckoutURL: order.CheckoutURL,
		ExpiresAt:   order.ExpiredAt,
		CreatedAt:   order.CreatedAt,
		PaidAt:      order.PaidAt,
	}
}

func PaymentOrderInfosFromModels(orders []model.PaymentOrder) []PaymentOrderInfo {
	items := make([]PaymentOrderInfo, 0, len(orders))
	for i := range orders {
		items = append(items, PaymentOrderInfoFromModel(&orders[i]))
	}
	return items
}
