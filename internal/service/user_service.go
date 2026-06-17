package service

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

type UserService struct{}

func NewUserService() *UserService {
	return &UserService{}
}

type AdminAuditRecordInput struct {
	RequestID     string
	ActorUserID   uint
	ActorRole     int
	Action        string
	ResourceType  string
	ResourceID    string
	BeforeSummary string
	AfterSummary  string
	Result        string
	ErrorCode     string
	IP            string
	UserAgent     string
}

type QuotaTransactionFilter struct {
	UserID     *uint
	Type       string
	SourceType string
	SourceID   string
	StartTime  string
	EndTime    string
}

// RecordAdminAuditLog 写入管理审计摘要。审计失败不应泄露敏感请求体。
func (s *UserService) RecordAdminAuditLog(input AdminAuditRecordInput) error {
	log, err := buildAdminAuditLog(input)
	if err != nil {
		return err
	}
	return internal.DB.Create(log).Error
}

func recordAdminAuditLogWithDB(db *gorm.DB, input AdminAuditRecordInput) error {
	log, err := buildAdminAuditLog(input)
	if err != nil {
		return err
	}
	return db.Create(log).Error
}

func buildAdminAuditLog(input AdminAuditRecordInput) (*model.AdminAuditLog, error) {
	if input.ActorUserID == 0 || input.Action == "" || input.ResourceType == "" || input.ResourceID == "" {
		return nil, errors.New("invalid admin audit log")
	}
	result := strings.TrimSpace(input.Result)
	if result == "" {
		result = "success"
	}
	var requestID *string
	if trimmed := strings.TrimSpace(input.RequestID); trimmed != "" {
		requestID = &trimmed
	}
	log := model.AdminAuditLog{
		RequestID:     requestID,
		ActorUserID:   input.ActorUserID,
		ActorRole:     input.ActorRole,
		Action:        strings.TrimSpace(input.Action),
		ResourceType:  strings.TrimSpace(input.ResourceType),
		ResourceID:    strings.TrimSpace(input.ResourceID),
		BeforeSummary: strings.TrimSpace(input.BeforeSummary),
		AfterSummary:  strings.TrimSpace(input.AfterSummary),
		Result:        result,
		ErrorCode:     strings.TrimSpace(input.ErrorCode),
		IP:            strings.TrimSpace(input.IP),
		UserAgent:     strings.TrimSpace(input.UserAgent),
	}
	return &log, nil
}

const (
	paymentWebhookAuditActorUserID = 1

	adminAuditActionPaymentWebhookProcessed = "payment_webhook.processed"
	adminAuditActionPaymentOrderPaid        = "payment_order.paid"
	adminAuditActionPaymentRefundRequested  = "payment_refund.requested"
	adminAuditActionPaymentRefundProcessed  = "payment_refund.processed"
	adminAuditActionPaymentRefundDeducted   = "payment_refund.deducted"
	adminAuditActionPaymentRefundManual     = "payment_refund.manual"
	adminAuditActionPaymentDisputeCreated   = "payment_dispute.created"
	adminAuditActionPaymentDisputeUpdated   = "payment_dispute.updated"
	adminAuditActionPaymentDisputeClosed    = "payment_dispute.closed"
	adminAuditActionPaymentDisputeFunds     = "payment_dispute.funds_changed"
	adminAuditActionPaymentManualCredit     = "payment_manual_adjust.credit"
	adminAuditActionPaymentManualDebit      = "payment_manual_adjust.debit"
)

var allowedModelPriceModes = map[string]struct{}{
	"request": {},
	"token":   {},
	"second":  {},
	"tiered":  {},
}

var allowedChannelModelPriceOverrideModes = map[string]struct{}{
	"override":        {},
	"merge_variables": {},
}

func recordPaymentEventAudit(tx *gorm.DB, requestID, action string, event model.PaymentEvent, order *model.PaymentOrder, extra map[string]interface{}) error {
	summary := map[string]interface{}{
		"provider":        event.Provider,
		"event_id":        event.ProviderEventID,
		"event_type":      event.EventType,
		"order_no":        event.OrderNo,
		"processed":       event.Processed,
		"signature_valid": event.SignatureValid,
	}
	if order != nil {
		summary["order_no"] = order.OrderNo
		summary["user_id"] = order.UserID
		summary["product_id"] = order.ProductID
		summary["amount"] = order.Amount
		summary["currency"] = order.Currency
		summary["quota"] = order.Quota
		summary["order_status"] = order.Status
	}
	for key, value := range extra {
		summary[key] = value
	}
	afterSummary, err := json.Marshal(summary)
	if err != nil {
		return err
	}

	// 支付 provider 回调没有人类操作者；这里使用稳定的系统审计主体，便于统一查询。
	return recordAdminAuditLogWithDB(tx, AdminAuditRecordInput{
		RequestID:    requestID,
		ActorUserID:  paymentWebhookAuditActorUserID,
		ActorRole:    common.RoleSuper,
		Action:       action,
		ResourceType: common.QuotaSourceTypePaymentEvent,
		ResourceID:   event.ProviderEventID,
		AfterSummary: string(afterSummary),
		Result:       "success",
	})
}

type PaymentManualAdjustmentInput struct {
	UserID         uint
	OrderNo        string
	Amount         int64
	Reason         string
	IdempotencyKey string
	IP             string
	UserAgent      string
}

type PaymentManualAdjustmentResult struct {
	UserID         uint   `json:"user_id"`
	OrderNo        string `json:"order_no,omitempty"`
	Amount         int64  `json:"amount"`
	Type           string `json:"type"`
	BalanceBefore  int64  `json:"balance_before"`
	BalanceAfter   int64  `json:"balance_after"`
	IdempotencyKey string `json:"idempotency_key"`
}

type PaymentManualRefundInput struct {
	OrderNo        string
	RefundQuota    int64
	Reason         string
	IdempotencyKey string
	IP             string
	UserAgent      string
}

type PaymentManualRefundResult struct {
	UserID         uint   `json:"user_id"`
	OrderNo        string `json:"order_no"`
	RefundQuota    int64  `json:"refund_quota"`
	OrderStatus    string `json:"order_status"`
	BalanceBefore  int64  `json:"balance_before"`
	BalanceAfter   int64  `json:"balance_after"`
	IdempotencyKey string `json:"idempotency_key"`
}

type PaymentProviderRefundRequestInput struct {
	OrderNo        string
	RefundAmount   string
	Reason         string
	IdempotencyKey string
	IP             string
	UserAgent      string
}

type PaymentProviderRefundRequestResult struct {
	UserID            uint   `json:"user_id"`
	OrderNo           string `json:"order_no"`
	Provider          string `json:"provider"`
	ProviderRefundID  string `json:"provider_refund_id"`
	RefundAmount      string `json:"refund_amount"`
	RefundAmountMinor int64  `json:"refund_amount_minor"`
	RefundQuota       int64  `json:"refund_quota"`
	OrderStatus       string `json:"order_status"`
	IdempotencyKey    string `json:"idempotency_key"`
}

func (s *UserService) ListAdminAuditLogs(operatorRole int, page, pageSize int, action, resourceType, resourceID string, actorUserID uint, result, errorCode string, startTime, endTime int64) ([]model.AdminAuditLog, int64, error) {
	if operatorRole < common.RoleSuper {
		return nil, 0, errors.New("super admin role required")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.AdminAuditLog{})
	if strings.TrimSpace(action) != "" {
		query = query.Where("action = ?", strings.TrimSpace(action))
	}
	if strings.TrimSpace(resourceType) != "" {
		query = query.Where("resource_type = ?", strings.TrimSpace(resourceType))
	}
	if strings.TrimSpace(resourceID) != "" {
		query = query.Where("resource_id = ?", strings.TrimSpace(resourceID))
	}
	if actorUserID > 0 {
		query = query.Where("actor_user_id = ?", actorUserID)
	}
	if strings.TrimSpace(result) != "" {
		query = query.Where("result = ?", strings.TrimSpace(result))
	}
	if strings.TrimSpace(errorCode) != "" {
		query = query.Where("error_code = ?", strings.TrimSpace(errorCode))
	}
	if startTime > 0 {
		query = query.Where("created_at >= ?", time.Unix(startTime, 0))
	}
	if endTime > 0 {
		query = query.Where("created_at <= ?", time.Unix(endTime, 0))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var logs []model.AdminAuditLog
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error
	return logs, total, err
}

// GetByID 根据 ID 获取用户。
func (s *UserService) GetByID(id uint) (*model.User, error) {
	var user model.User
	if err := internal.DB.First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// List 用户分页列表, 支持 keyword/role/status/group 筛选。
func (s *UserService) List(operatorRole int, page, pageSize int, keyword string, role, status *int, groupID *uint) ([]model.User, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	if role != nil && (*role < common.RoleUser || *role > common.RoleSuper) {
		return nil, 0, errors.New("invalid role")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.User{})
	if keyword != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("username LIKE ? OR display_name LIKE ? OR email LIKE ?", like, like, like)
	}
	if role != nil {
		query = query.Where("role = ?", *role)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	if groupID != nil {
		query = query.Where("group_id = ?", *groupID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var users []model.User
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error
	return users, total, err
}

// ListGroups 返回管理端用户分组列表。分组倍率只作为分组元数据展示；
// 实际扣费倍率仍以 billing.* settings 为权威来源。
func (s *UserService) ListGroups(operatorRole int, page, pageSize int, keyword string) ([]model.Group, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.Group{})
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("name LIKE ?", like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var groups []model.Group
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&groups).Error
	return groups, total, err
}

func (s *UserService) GetGroupByID(id uint) (*model.Group, error) {
	var group model.Group
	if err := internal.DB.First(&group, id).Error; err != nil {
		return nil, err
	}
	return &group, nil
}

func (s *UserService) CreateGroup(operatorRole int, name string, ratio float64) (*model.Group, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	name, err := normalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	if ratio <= 0 {
		ratio = 1
	}
	var existing int64
	if err := internal.DB.Model(&model.Group{}).Where("name = ?", name).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, errors.New("group name already exists")
	}
	group := &model.Group{Name: name, Ratio: ratio}
	if err := internal.DB.Create(group).Error; err != nil {
		return nil, err
	}
	return group, nil
}

func (s *UserService) UpdateGroup(operatorRole int, id uint, name *string, ratio *float64) error {
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	var current model.Group
	if err := internal.DB.First(&current, id).Error; err != nil {
		return err
	}
	updates := map[string]interface{}{}
	if name != nil {
		normalizedName, err := normalizeGroupName(*name)
		if err != nil {
			return err
		}
		var duplicate int64
		if err := internal.DB.Model(&model.Group{}).Where("name = ? AND id <> ?", normalizedName, id).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errors.New("group name already exists")
		}
		updates["name"] = normalizedName
	}
	if ratio != nil {
		if *ratio <= 0 {
			return errors.New("group ratio must be positive")
		}
		updates["ratio"] = *ratio
	}
	if len(updates) == 0 {
		return nil
	}
	return internal.DB.Model(&model.Group{}).Where("id = ?", current.ID).Updates(updates).Error
}

func (s *UserService) DeleteGroup(operatorRole int, id uint) error {
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	var group model.Group
	if err := internal.DB.First(&group, id).Error; err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(group.Name), "default") {
		return errors.New("default group cannot be deleted")
	}
	var users int64
	if err := internal.DB.Model(&model.User{}).Where("group_id = ?", id).Count(&users).Error; err != nil {
		return err
	}
	if users > 0 {
		return errors.New("group is in use")
	}
	return internal.DB.Delete(&model.Group{}, id).Error
}

// ListQuotaTransactions 返回管理端额度流水列表。
// 额度流水只记录余额变更；模型调用消费仍以 logs.quota_used 为事实来源。
func (s *UserService) ListQuotaTransactions(operatorRole int, page, pageSize int, filter QuotaTransactionFilter) ([]model.QuotaTransaction, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	return s.listQuotaTransactions(page, pageSize, filter)
}

// ListUserQuotaTransactions 返回当前用户自己的额度流水。
// 即使 query 中携带 user_id，也必须在服务层强制覆盖，避免越权查询。
func (s *UserService) ListUserQuotaTransactions(userID uint, page, pageSize int, filter QuotaTransactionFilter) ([]model.QuotaTransaction, int64, error) {
	filter.UserID = &userID
	return s.listQuotaTransactions(page, pageSize, filter)
}

func (s *UserService) listQuotaTransactions(page, pageSize int, filter QuotaTransactionFilter) ([]model.QuotaTransaction, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.QuotaTransaction{})
	query = applyQuotaTransactionFilters(query, filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var transactions []model.QuotaTransaction
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&transactions).Error
	return transactions, total, err
}

func applyQuotaTransactionFilters(query *gorm.DB, filter QuotaTransactionFilter) *gorm.DB {
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if txType := strings.TrimSpace(filter.Type); txType != "" {
		query = query.Where("type = ?", txType)
	}
	if sourceType := strings.TrimSpace(filter.SourceType); sourceType != "" {
		query = query.Where("source_type = ?", sourceType)
	}
	if sourceID := strings.TrimSpace(filter.SourceID); sourceID != "" {
		query = query.Where("source_id = ?", sourceID)
	}
	if t, ok := parseTime(filter.StartTime); ok {
		query = query.Where("created_at >= ?", t)
	}
	if t, ok := parseTime(filter.EndTime); ok {
		query = query.Where("created_at <= ?", t)
	}
	return query
}

// Create 管理员创建用户。
func (s *UserService) Create(operatorRole int, username, password, displayName, email string, role int, quota int64, groupID *uint) (*model.User, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	username = strings.TrimSpace(username)
	email = normalizeEmail(email)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	if len(password) < 6 {
		return nil, errors.New("password length must be at least 6")
	}
	if role != common.RoleUser {
		return nil, errors.New("admin user management can only create normal users")
	}
	if displayName == "" {
		displayName = username
	}
	var user *model.User
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		if exists, err := identityExists(tx, model.UserIdentityMethodUsername, username); err != nil {
			return err
		} else if exists {
			return errors.New("username already exists")
		}
		hash, err := common.HashPassword(password)
		if err != nil {
			return err
		}
		usernamePtr := username
		var emailPtr *string
		if email != "" {
			emailPtr = &email
		}
		u := &model.User{
			Username:    &usernamePtr,
			DisplayName: displayName,
			Email:       emailPtr,
			Role:        role,
			Quota:       quota,
			Status:      common.UserStatusEnabled,
			GroupID:     groupID,
		}
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		now := time.Now()
		identity := model.UserIdentity{
			UserID:       u.ID,
			Method:       model.UserIdentityMethodUsername,
			Provider:     model.UserIdentityProviderLocal,
			Identifier:   username,
			PasswordHash: hash,
			VerifiedAt:   &now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return err
		}
		user = u
		return nil
	})
	return user, err
}

// Update 管理员编辑用户信息。
func (s *UserService) UpdateByAdmin(operatorID uint, operatorRole int, targetID uint, updates map[string]interface{}) error {
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, targetID); err != nil {
		return err
	}
	allowed := filterUpdates(updates, "display_name", "email", "status", "group_id")
	if len(allowed) == 0 {
		return nil
	}
	return internal.DB.Model(&model.User{}).Where("id = ? AND role = ?", targetID, common.RoleUser).Updates(allowed).Error
}

// Delete 软删除用户。
func (s *UserService) DeleteByAdmin(operatorID uint, operatorRole int, targetID uint) error {
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, targetID); err != nil {
		return err
	}
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Token{}).Where("user_id = ?", targetID).Update("status", common.TokenStatusDisabled).Error; err != nil {
			return err
		}
		return tx.Where("role = ?", common.RoleUser).Delete(&model.User{}, targetID).Error
	})
}

// UpdateQuota 调整用户余额。
func (s *UserService) UpdateQuotaByAdmin(operatorID uint, operatorRole int, targetID uint, delta int64, reason, requestID string) error {
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, targetID); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "admin quota adjustment"
	}
	actorID := operatorID
	sourceID := fmt.Sprintf("admin:%d:user:%d:%d", operatorID, targetID, time.Now().UnixNano())
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyQuotaChange(tx, quotaChange{
			UserID:         targetID,
			Amount:         delta,
			Type:           common.QuotaTransactionTypeAdminAdjust,
			SourceType:     common.QuotaSourceTypeAdminAction,
			SourceID:       sourceID,
			IdempotencyKey: sourceID,
			Reason:         reason,
			ActorUserID:    &actorID,
			RequestID:      requestID,
		})
		return err
	})
}

// ApplyPaymentManualAdjustment 记录支付相关人工补账或扣回。
// 该路径必须留下额度流水和审计摘要，避免客服修正绕开账务事实。
func (s *UserService) ApplyPaymentManualAdjustment(operatorID uint, operatorRole int, input PaymentManualAdjustmentInput, requestID string) (*PaymentManualAdjustmentResult, error) {
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, input.UserID); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(input.Reason)
	requireReason, err := paymentBoolSettingDefault("payment.manual_adjust.require_reason", true)
	if err != nil {
		return nil, err
	}
	if requireReason && reason == "" {
		return nil, errors.New("reason is required")
	}
	if reason == "" {
		reason = "payment manual adjustment"
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, errors.New("idempotency_key is required")
	}
	if input.Amount == 0 {
		return nil, errors.New("manual adjustment amount must not be zero")
	}

	adjustmentType := common.QuotaTransactionTypeManualCredit
	auditAction := adminAuditActionPaymentManualCredit
	if input.Amount < 0 {
		adjustmentType = common.QuotaTransactionTypeManualDebit
		auditAction = adminAuditActionPaymentManualDebit
	}

	orderNo := strings.TrimSpace(input.OrderNo)
	sourceType := common.QuotaSourceTypeAdminAction
	sourceID := "payment_manual:" + idempotencyKey
	resourceType := "user"
	resourceID := strconv.FormatUint(uint64(input.UserID), 10)
	var linkedOrder *model.PaymentOrder
	if orderNo != "" {
		sourceType = common.QuotaSourceTypePaymentOrder
		sourceID = orderNo
		resourceType = common.QuotaSourceTypePaymentOrder
		resourceID = orderNo
	}

	var result *PaymentManualAdjustmentResult
	actorID := operatorID
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.QuotaTransaction
		err := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error
		switch {
		case err == nil:
			return errors.New("idempotency_key has already been used")
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		if orderNo != "" {
			var order model.PaymentOrder
			if err := tx.Where("order_no = ? AND user_id = ?", orderNo, input.UserID).First(&order).Error; err != nil {
				return err
			}
			linkedOrder = &order
		}

		balanceBefore, balanceAfter, err := applyQuotaChange(tx, quotaChange{
			UserID:         input.UserID,
			Amount:         input.Amount,
			Type:           adjustmentType,
			SourceType:     sourceType,
			SourceID:       sourceID,
			IdempotencyKey: idempotencyKey,
			Reason:         reason,
			ActorUserID:    &actorID,
			RequestID:      requestID,
		})
		if err != nil {
			return err
		}

		summary := map[string]interface{}{
			"user_id":         input.UserID,
			"order_no":        orderNo,
			"amount":          input.Amount,
			"type":            adjustmentType,
			"reason":          reason,
			"balance_before":  balanceBefore,
			"balance_after":   balanceAfter,
			"idempotency_key": idempotencyKey,
			"source_type":     sourceType,
			"source_id":       sourceID,
		}
		if linkedOrder != nil {
			summary["provider"] = linkedOrder.Provider
			summary["payment_status"] = linkedOrder.Status
			summary["payment_amount"] = linkedOrder.Amount
			summary["currency"] = linkedOrder.Currency
		}
		afterSummary, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if err := recordAdminAuditLogWithDB(tx, AdminAuditRecordInput{
			RequestID:    requestID,
			ActorUserID:  operatorID,
			ActorRole:    operatorRole,
			Action:       auditAction,
			ResourceType: resourceType,
			ResourceID:   resourceID,
			AfterSummary: string(afterSummary),
			Result:       "success",
			IP:           input.IP,
			UserAgent:    input.UserAgent,
		}); err != nil {
			return err
		}

		result = &PaymentManualAdjustmentResult{
			UserID:         input.UserID,
			OrderNo:        orderNo,
			Amount:         input.Amount,
			Type:           adjustmentType,
			BalanceBefore:  balanceBefore,
			BalanceAfter:   balanceAfter,
			IdempotencyKey: idempotencyKey,
		}
		return nil
	})
	return result, err
}

// ApplyPaymentManualRefund 记录管理员确认后的支付退款。
// 该接口面向已经在线下或 provider 侧完成的退款事实，负责扣回额度、更新订单状态和留下审计证据。
func (s *UserService) ApplyPaymentManualRefund(operatorID uint, operatorRole int, input PaymentManualRefundInput, requestID string) (*PaymentManualRefundResult, error) {
	orderNo := strings.TrimSpace(input.OrderNo)
	if orderNo == "" {
		return nil, errors.New("order_no is required")
	}
	if input.RefundQuota <= 0 {
		return nil, errors.New("refund_quota must be positive")
	}
	reason := strings.TrimSpace(input.Reason)
	requireReason, err := paymentBoolSettingDefault("payment.manual_adjust.require_reason", true)
	if err != nil {
		return nil, err
	}
	if requireReason && reason == "" {
		return nil, errors.New("reason is required")
	}
	if reason == "" {
		reason = "payment manual refund"
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, errors.New("idempotency_key is required")
	}

	var orderSnapshot model.PaymentOrder
	if err := internal.DB.Where("order_no = ?", orderNo).First(&orderSnapshot).Error; err != nil {
		return nil, err
	}
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, orderSnapshot.UserID); err != nil {
		return nil, err
	}
	allowNegative, err := paymentBoolSettingDefault("payment.refund.allow_negative_balance", false)
	if err != nil {
		return nil, err
	}

	actorID := operatorID
	var result *PaymentManualRefundResult
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.QuotaTransaction
		err := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error
		switch {
		case err == nil:
			return errors.New("idempotency_key has already been used")
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		var order model.PaymentOrder
		if err := tx.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
			return err
		}
		if order.Status != common.PaymentOrderStatusPaid {
			return errors.New("payment order is not paid")
		}
		if input.RefundQuota > order.Quota {
			return errors.New("refund_quota exceeds order quota")
		}

		refundStatus := common.PaymentOrderStatusRefunded
		if input.RefundQuota < order.Quota {
			refundStatus = common.PaymentOrderStatusPartiallyRefunded
		}
		balanceBefore, balanceAfter, err := applyQuotaChange(tx, quotaChange{
			UserID:         order.UserID,
			Amount:         -input.RefundQuota,
			Type:           common.QuotaTransactionTypeRefundDeduct,
			SourceType:     common.QuotaSourceTypeRefund,
			SourceID:       order.OrderNo,
			IdempotencyKey: idempotencyKey,
			Reason:         reason,
			ActorUserID:    &actorID,
			RequestID:      requestID,
			AllowNegative:  allowNegative,
		})
		if err != nil {
			return err
		}

		now := time.Now()
		if err := tx.Model(&order).Updates(map[string]interface{}{
			"status":     refundStatus,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		order.Status = refundStatus

		summary := map[string]interface{}{
			"user_id":         order.UserID,
			"order_no":        order.OrderNo,
			"refund_quota":    input.RefundQuota,
			"order_quota":     order.Quota,
			"order_status":    refundStatus,
			"reason":          reason,
			"balance_before":  balanceBefore,
			"balance_after":   balanceAfter,
			"idempotency_key": idempotencyKey,
			"allow_negative":  allowNegative,
			"provider":        order.Provider,
			"payment_amount":  order.Amount,
			"currency":        order.Currency,
		}
		afterSummary, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if err := recordAdminAuditLogWithDB(tx, AdminAuditRecordInput{
			RequestID:    requestID,
			ActorUserID:  operatorID,
			ActorRole:    operatorRole,
			Action:       adminAuditActionPaymentRefundManual,
			ResourceType: common.QuotaSourceTypePaymentOrder,
			ResourceID:   order.OrderNo,
			AfterSummary: string(afterSummary),
			Result:       "success",
			IP:           input.IP,
			UserAgent:    input.UserAgent,
		}); err != nil {
			return err
		}

		result = &PaymentManualRefundResult{
			UserID:         order.UserID,
			OrderNo:        order.OrderNo,
			RefundQuota:    input.RefundQuota,
			OrderStatus:    refundStatus,
			BalanceBefore:  balanceBefore,
			BalanceAfter:   balanceAfter,
			IdempotencyKey: idempotencyKey,
		}
		return nil
	})
	return result, err
}

// CreatePaymentProviderRefundRequest 向支付 provider 发起退款请求。
// 请求成功只代表 provider 已受理；订单最终退款状态仍以后续可信 webhook 为准。
func (s *UserService) CreatePaymentProviderRefundRequest(operatorID uint, operatorRole int, input PaymentProviderRefundRequestInput, requestID string) (*PaymentProviderRefundRequestResult, error) {
	orderNo := strings.TrimSpace(input.OrderNo)
	if orderNo == "" {
		return nil, errors.New("order_no is required")
	}
	reason := strings.TrimSpace(input.Reason)
	requireReason, err := paymentBoolSettingDefault("payment.manual_adjust.require_reason", true)
	if err != nil {
		return nil, err
	}
	if requireReason && reason == "" {
		return nil, errors.New("reason is required")
	}
	if reason == "" {
		reason = "payment provider refund request"
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, errors.New("idempotency_key is required")
	}

	var existing model.PaymentRefundRequest
	err = internal.DB.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error
	switch {
	case err == nil:
		return nil, errors.New("idempotency_key has already been used")
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}

	var order model.PaymentOrder
	if err := internal.DB.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		return nil, err
	}
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, order.UserID); err != nil {
		return nil, err
	}
	if order.Status != common.PaymentOrderStatusPaid {
		return nil, errors.New("payment order is not paid")
	}
	orderAmountMinor, err := decimalAmountToMinorUnits(order.Amount)
	if err != nil || orderAmountMinor <= 0 {
		return nil, errors.New("invalid payment order amount")
	}
	refundAmount := strings.TrimSpace(input.RefundAmount)
	if refundAmount == "" {
		refundAmount = formatMinorUnits(orderAmountMinor)
	}
	refundAmountMinor, err := decimalAmountToMinorUnits(refundAmount)
	if err != nil || refundAmountMinor <= 0 {
		return nil, errors.New("refund_amount must be positive")
	}
	if refundAmountMinor > orderAmountMinor {
		return nil, errors.New("refund_amount exceeds order amount")
	}
	refundAmount = formatMinorUnits(refundAmountMinor)
	refundQuota := proportionalRefundQuota(order.Quota, refundAmountMinor, orderAmountMinor)
	if refundQuota <= 0 {
		return nil, errors.New("refund_quota must be positive")
	}

	providerRefundID, providerStatus, _, err := createProviderRefund(order, refundAmountMinor, idempotencyKey, reason)
	if err != nil {
		return nil, err
	}
	if providerStatus == "" {
		providerStatus = "pending"
	}

	var result *PaymentProviderRefundRequestResult
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		var duplicate model.PaymentRefundRequest
		err := tx.Where("idempotency_key = ?", idempotencyKey).First(&duplicate).Error
		switch {
		case err == nil:
			return errors.New("idempotency_key has already been used")
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		var requestIDPtr *string
		if trimmed := strings.TrimSpace(requestID); trimmed != "" {
			requestIDPtr = &trimmed
		}
		refundRequest := model.PaymentRefundRequest{
			OrderNo:          order.OrderNo,
			UserID:           order.UserID,
			Provider:         order.Provider,
			ProviderRefundID: providerRefundID,
			Amount:           refundAmount,
			AmountMinor:      refundAmountMinor,
			Currency:         strings.ToLower(strings.TrimSpace(order.Currency)),
			RefundQuota:      refundQuota,
			Status:           providerStatus,
			IdempotencyKey:   idempotencyKey,
			Reason:           reason,
			ActorUserID:      operatorID,
			RequestID:        requestIDPtr,
		}
		if err := tx.Create(&refundRequest).Error; err != nil {
			return err
		}

		now := time.Now()
		if err := tx.Model(&model.PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
			"status":     common.PaymentOrderStatusRefundPending,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}

		summary := map[string]interface{}{
			"user_id":             order.UserID,
			"order_no":            order.OrderNo,
			"provider":            order.Provider,
			"provider_refund_id":  providerRefundID,
			"provider_status":     providerStatus,
			"refund_amount":       refundAmount,
			"refund_amount_minor": refundAmountMinor,
			"currency":            strings.ToLower(strings.TrimSpace(order.Currency)),
			"refund_quota":        refundQuota,
			"order_status":        common.PaymentOrderStatusRefundPending,
			"reason":              reason,
			"idempotency_key":     idempotencyKey,
		}
		afterSummary, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if err := recordAdminAuditLogWithDB(tx, AdminAuditRecordInput{
			RequestID:    requestID,
			ActorUserID:  operatorID,
			ActorRole:    operatorRole,
			Action:       adminAuditActionPaymentRefundRequested,
			ResourceType: common.QuotaSourceTypePaymentOrder,
			ResourceID:   order.OrderNo,
			AfterSummary: string(afterSummary),
			Result:       "success",
			IP:           input.IP,
			UserAgent:    input.UserAgent,
		}); err != nil {
			return err
		}

		result = &PaymentProviderRefundRequestResult{
			UserID:            order.UserID,
			OrderNo:           order.OrderNo,
			Provider:          order.Provider,
			ProviderRefundID:  providerRefundID,
			RefundAmount:      refundAmount,
			RefundAmountMinor: refundAmountMinor,
			RefundQuota:       refundQuota,
			OrderStatus:       common.PaymentOrderStatusRefundPending,
			IdempotencyKey:    idempotencyKey,
		}
		return nil
	})
	return result, err
}

func createProviderRefund(order model.PaymentOrder, amountMinor int64, idempotencyKey, reason string) (string, string, string, error) {
	switch order.Provider {
	case common.PaymentProviderStripe:
		return createStripeRefund(order, amountMinor, idempotencyKey, reason)
	case common.PaymentProviderEpay:
		return createEpayRefund(order, amountMinor, idempotencyKey, reason)
	default:
		return "", "", "", errors.New("payment provider refund request does not support provider")
	}
}

// ListRedemCodes 查询充值码列表，供管理员按状态、批次或关键字检索。
func (s *UserService) ListRedemCodes(operatorRole int, page, pageSize int, status *int, keyword, batchNo string) ([]model.RedemCode, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	if status != nil && *status != common.RedemCodeStatusUnused && *status != common.RedemCodeStatusUsed && *status != common.RedemCodeStatusDisabled {
		return nil, 0, errors.New("invalid redem code status")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.RedemCode{})
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	if batchNo = strings.TrimSpace(batchNo); batchNo != "" {
		query = query.Where("batch_no = ?", batchNo)
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("code LIKE ? OR batch_no LIKE ? OR note LIKE ?", like, like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var codes []model.RedemCode
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&codes).Error
	return codes, total, err
}

func (s *UserService) GetRedemCodeAdmin(operatorRole int, id uint) (*model.RedemCode, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if id == 0 {
		return nil, errors.New("redem code is required")
	}
	var code model.RedemCode
	if err := internal.DB.First(&code, id).Error; err != nil {
		return nil, err
	}
	return &code, nil
}

// GetRedemCodeByCode 按兑换码查询当前状态，用于兑换成功后的脱敏审计摘要。
func (s *UserService) GetRedemCodeByCode(code string) (*model.RedemCode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("redem code is required")
	}
	var redem model.RedemCode
	if err := internal.DB.Where("code = ?", code).First(&redem).Error; err != nil {
		return nil, err
	}
	return &redem, nil
}

// CreateRedemCodes 生成随机充值码，或导入管理员提供的指定充值码。
func (s *UserService) CreateRedemCodes(operatorRole int, quota int64, count int, codes []string, batchNo, note string, expiredAtUnix *int64) ([]model.RedemCode, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if quota <= 0 {
		return nil, errors.New("quota must be positive")
	}
	normalized, err := normalizeRedemCodeInputs(codes)
	if err != nil {
		return nil, err
	}
	if len(normalized) > 0 {
		count = len(normalized)
	} else if count <= 0 {
		count = 1
	}
	if count > 100 {
		return nil, errors.New("redem code count must be at most 100")
	}
	batchNo = strings.TrimSpace(batchNo)
	if len(batchNo) > 64 {
		return nil, errors.New("redem code batch_no length must be at most 64")
	}
	note = strings.TrimSpace(note)
	if len(note) > 256 {
		return nil, errors.New("redem code note length must be at most 256")
	}
	var expiredAt *time.Time
	if expiredAtUnix != nil && *expiredAtUnix > 0 {
		t := time.Unix(*expiredAtUnix, 0)
		if !t.After(time.Now()) {
			return nil, errors.New("redem code expired_at must be in the future")
		}
		expiredAt = &t
	}

	created := make([]model.RedemCode, 0, count)
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		if len(normalized) == 0 {
			var err error
			normalized, err = generateUniqueRedemCodes(tx, count)
			if err != nil {
				return err
			}
		} else {
			var existing int64
			if err := tx.Model(&model.RedemCode{}).Where("code IN ?", normalized).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				return errors.New("redem code already exists")
			}
		}
		for _, code := range normalized {
			created = append(created, model.RedemCode{
				Code:      code,
				Quota:     quota,
				Status:    common.RedemCodeStatusUnused,
				BatchNo:   batchNo,
				Note:      note,
				ExpiredAt: expiredAt,
			})
		}
		return tx.Create(&created).Error
	})
	return created, err
}

// DisableRedemCode 作废未使用充值码；已使用或不存在的码不会被修改。
func (s *UserService) DisableRedemCode(operatorRole int, id uint) error {
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	res := internal.DB.Model(&model.RedemCode{}).
		Where("id = ? AND status = ?", id, common.RedemCodeStatusUnused).
		Update("status", common.RedemCodeStatusDisabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("redem code is not unused or not found")
	}
	return nil
}

// ListAvailableModels 返回普通用户当前可见的启用通道模型集合。
func (s *UserService) ListAvailableModels() ([]string, error) {
	models, _, _, err := s.ListAvailableModelsWithPrices()
	return models, err
}

// ListAvailableModelsWithPrices 返回普通用户模型列表所需的可见模型和价格状态。
// 通道级规则同时携带 user_enabled，可在不删除通道模型的情况下隐藏普通用户入口。
func (s *UserService) ListAvailableModelsWithPrices() ([]string, map[string]model.ChannelModelPrice, map[string]model.ModelPrice, error) {
	var channels []model.Channel
	if err := internal.DB.
		Where("status = ?", common.ChannelStatusEnabled).
		Order("priority DESC, idx ASC, id ASC").
		Find(&channels).Error; err != nil {
		return nil, nil, nil, err
	}
	if len(channels) == 0 {
		return []string{}, map[string]model.ChannelModelPrice{}, map[string]model.ModelPrice{}, nil
	}

	channelIDs := make([]uint, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
	}
	var channelPrices []model.ChannelModelPrice
	if err := internal.DB.Where("channel_id IN ?", channelIDs).Find(&channelPrices).Error; err != nil {
		return nil, nil, nil, err
	}
	channelPriceByChannelModel := make(map[string]model.ChannelModelPrice, len(channelPrices))
	for _, price := range channelPrices {
		channelPriceByChannelModel[channelModelPriceKey(price.ChannelID, price.Model)] = price
	}

	visible := map[string]struct{}{}
	channelPricesByModel := map[string]model.ChannelModelPrice{}
	for _, channel := range channels {
		for _, modelName := range splitModels(channel.Models) {
			if modelName == "" || modelName == "*" {
				continue
			}
			price, hasChannelPrice := channelPriceByChannelModel[channelModelPriceKey(channel.ID, modelName)]
			if hasChannelPrice && !price.UserEnabled {
				continue
			}
			if _, seen := visible[modelName]; seen {
				continue
			}
			visible[modelName] = struct{}{}
			if hasChannelPrice && price.Enabled {
				channelPricesByModel[modelName] = price
			}
		}
	}

	modelNames := make([]string, 0, len(visible))
	for modelName := range visible {
		modelNames = append(modelNames, modelName)
	}
	sort.Strings(modelNames)
	modelPrices, err := s.ListEnabledModelPrices(modelNames)
	if err != nil {
		return nil, nil, nil, err
	}
	return modelNames, channelPricesByModel, modelPrices, nil
}

// ListEnabledModelPrices 返回启用中的系统模型价格，用于用户侧模型列表展示价格状态。
func (s *UserService) ListEnabledModelPrices(modelNames []string) (map[string]model.ModelPrice, error) {
	pricesByModel := make(map[string]model.ModelPrice)
	if len(modelNames) == 0 {
		return pricesByModel, nil
	}
	seen := map[string]struct{}{}
	names := make([]string, 0, len(modelNames))
	for _, raw := range modelNames {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return pricesByModel, nil
	}
	var prices []model.ModelPrice
	if err := internal.DB.Where("model IN ? AND enabled = ?", names, true).Find(&prices).Error; err != nil {
		return nil, err
	}
	for _, price := range prices {
		pricesByModel[price.Model] = price
	}
	return pricesByModel, nil
}

func channelModelPriceKey(channelID uint, modelName string) string {
	return strconv.FormatUint(uint64(channelID), 10) + ":" + strings.TrimSpace(modelName)
}

// ListChannelModelPricesAdmin 返回管理端通道模型价格覆盖列表。
func (s *UserService) ListChannelModelPricesAdmin(operatorRole int, page, pageSize int, keyword string, channelID *uint, enabled, userEnabled *bool) ([]model.ChannelModelPrice, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.ChannelModelPrice{})
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("model LIKE ? OR price_mode LIKE ? OR override_mode LIKE ?", like, like, like)
	}
	if channelID != nil {
		query = query.Where("channel_id = ?", *channelID)
	}
	if enabled != nil {
		query = query.Where("enabled = ?", *enabled)
	}
	if userEnabled != nil {
		query = query.Where("user_enabled = ?", *userEnabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var prices []model.ChannelModelPrice
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&prices).Error
	return prices, total, err
}

func (s *UserService) GetChannelModelPriceAdmin(operatorRole int, id uint) (*model.ChannelModelPrice, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if id == 0 {
		return nil, errors.New("channel model price is required")
	}
	var price model.ChannelModelPrice
	if err := internal.DB.First(&price, id).Error; err != nil {
		return nil, err
	}
	return &price, nil
}

func (s *UserService) CreateChannelModelPrice(operatorRole int, price model.ChannelModelPrice) (*model.ChannelModelPrice, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	normalized, err := normalizeChannelModelPrice(price)
	if err != nil {
		return nil, err
	}
	normalized.RuleVersion = 1
	var existing int64
	if err := internal.DB.Model(&model.ChannelModelPrice{}).Where("channel_id = ? AND model = ?", normalized.ChannelID, normalized.Model).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, errors.New("channel model price already exists")
	}
	if err := internal.DB.Create(&normalized).Error; err != nil {
		return nil, err
	}
	return &normalized, nil
}

func (s *UserService) UpdateChannelModelPrice(operatorRole int, id uint, price model.ChannelModelPrice, enabled, userEnabled *bool) (*model.ChannelModelPrice, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if id == 0 {
		return nil, errors.New("channel model price is required")
	}
	normalized, err := normalizeChannelModelPrice(price)
	if err != nil {
		return nil, err
	}
	var current model.ChannelModelPrice
	if err := internal.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	var duplicate int64
	if err := internal.DB.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ? AND model = ? AND id <> ?", normalized.ChannelID, normalized.Model, id).
		Count(&duplicate).Error; err != nil {
		return nil, err
	}
	if duplicate > 0 {
		return nil, errors.New("channel model price already exists")
	}
	updates := map[string]interface{}{
		"channel_id":       normalized.ChannelID,
		"model":            normalized.Model,
		"price_mode":       normalized.PriceMode,
		"override_mode":    normalized.OverrideMode,
		"price_expression": normalized.PriceExpression,
		"variables_json":   normalized.VariablesJSON,
		"unit_tokens":      normalized.UnitTokens,
		"rule_version":     current.RuleVersion + 1,
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if userEnabled != nil {
		updates["user_enabled"] = *userEnabled
	}
	if err := internal.DB.Model(&current).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := internal.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	return &current, nil
}

func (s *UserService) SetChannelModelPriceEnabled(operatorRole int, id uint, enabled bool) error {
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	if id == 0 {
		return errors.New("channel model price is required")
	}
	res := internal.DB.Model(&model.ChannelModelPrice{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"enabled":      enabled,
			"rule_version": gorm.Expr("rule_version + ?", 1),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("channel model price not found")
	}
	return nil
}

func normalizeChannelModelPrice(price model.ChannelModelPrice) (model.ChannelModelPrice, error) {
	price.Model = strings.TrimSpace(price.Model)
	price.PriceMode = strings.ToLower(strings.TrimSpace(price.PriceMode))
	price.OverrideMode = strings.ToLower(strings.TrimSpace(price.OverrideMode))
	price.PriceExpression = strings.TrimSpace(price.PriceExpression)
	if price.ChannelID == 0 {
		return model.ChannelModelPrice{}, errors.New("channel_id is required")
	}
	if price.Model == "" {
		return model.ChannelModelPrice{}, errors.New("model is required")
	}
	if _, ok := allowedModelPriceModes[price.PriceMode]; !ok {
		return model.ChannelModelPrice{}, errors.New("channel model price mode is invalid")
	}
	if price.OverrideMode == "" {
		price.OverrideMode = "override"
	}
	if _, ok := allowedChannelModelPriceOverrideModes[price.OverrideMode]; !ok {
		return model.ChannelModelPrice{}, errors.New("channel model price override_mode is invalid")
	}
	if price.PriceExpression == "" {
		return model.ChannelModelPrice{}, errors.New("channel model price expression is required")
	}
	if price.UnitTokens == 0 {
		price.UnitTokens = 1000
	}
	if price.UnitTokens < 0 {
		return model.ChannelModelPrice{}, errors.New("channel model price unit_tokens must be positive")
	}
	if len(price.VariablesJSON) > 0 && !json.Valid(price.VariablesJSON) {
		return model.ChannelModelPrice{}, errors.New("channel model price variables_json must be valid json")
	}
	var channel model.Channel
	if err := internal.DB.First(&channel, price.ChannelID).Error; err != nil {
		return model.ChannelModelPrice{}, err
	}
	if !channelSupportsModel(channel.Models, price.Model) {
		return model.ChannelModelPrice{}, errors.New("channel does not expose model")
	}
	return price, nil
}

// ListModelPricesAdmin 返回管理端系统模型价格列表，可按模型名/模式过滤。
func (s *UserService) ListModelPricesAdmin(operatorRole int, page, pageSize int, keyword string, enabled *bool) ([]model.ModelPrice, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.ModelPrice{})
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("model LIKE ? OR price_mode LIKE ?", like, like)
	}
	if enabled != nil {
		query = query.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var prices []model.ModelPrice
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&prices).Error
	return prices, total, err
}

func (s *UserService) GetModelPriceAdmin(operatorRole int, id uint) (*model.ModelPrice, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if id == 0 {
		return nil, errors.New("model price is required")
	}
	var price model.ModelPrice
	if err := internal.DB.First(&price, id).Error; err != nil {
		return nil, err
	}
	return &price, nil
}

func (s *UserService) CreateModelPrice(operatorRole int, price model.ModelPrice) (*model.ModelPrice, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	normalized, err := normalizeModelPrice(price)
	if err != nil {
		return nil, err
	}
	normalized.RuleVersion = 1
	var existing int64
	if err := internal.DB.Model(&model.ModelPrice{}).Where("model = ?", normalized.Model).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, errors.New("model price already exists")
	}
	if err := internal.DB.Create(&normalized).Error; err != nil {
		return nil, err
	}
	return &normalized, nil
}

func (s *UserService) UpdateModelPrice(operatorRole int, id uint, price model.ModelPrice, enabled *bool) (*model.ModelPrice, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if id == 0 {
		return nil, errors.New("model price is required")
	}
	normalized, err := normalizeModelPrice(price)
	if err != nil {
		return nil, err
	}
	var current model.ModelPrice
	if err := internal.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	var duplicate int64
	if err := internal.DB.Model(&model.ModelPrice{}).Where("model = ? AND id <> ?", normalized.Model, id).Count(&duplicate).Error; err != nil {
		return nil, err
	}
	if duplicate > 0 {
		return nil, errors.New("model price already exists")
	}
	updates := map[string]interface{}{
		"model":            normalized.Model,
		"price_mode":       normalized.PriceMode,
		"price_expression": normalized.PriceExpression,
		"variables_json":   normalized.VariablesJSON,
		"unit_tokens":      normalized.UnitTokens,
		"rule_version":     current.RuleVersion + 1,
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if err := internal.DB.Model(&current).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := internal.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	return &current, nil
}

func (s *UserService) SetModelPriceEnabled(operatorRole int, id uint, enabled bool) error {
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	if id == 0 {
		return errors.New("model price is required")
	}
	res := internal.DB.Model(&model.ModelPrice{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"enabled":      enabled,
			"rule_version": gorm.Expr("rule_version + ?", 1),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("model price not found")
	}
	return nil
}

func normalizeModelPrice(price model.ModelPrice) (model.ModelPrice, error) {
	price.Model = strings.TrimSpace(price.Model)
	price.PriceMode = strings.ToLower(strings.TrimSpace(price.PriceMode))
	price.PriceExpression = strings.TrimSpace(price.PriceExpression)
	if price.Model == "" {
		return model.ModelPrice{}, errors.New("model is required")
	}
	if _, ok := allowedModelPriceModes[price.PriceMode]; !ok {
		return model.ModelPrice{}, errors.New("model price mode is invalid")
	}
	if price.PriceExpression == "" {
		return model.ModelPrice{}, errors.New("model price expression is required")
	}
	if price.UnitTokens == 0 {
		price.UnitTokens = 1000
	}
	if price.UnitTokens < 0 {
		return model.ModelPrice{}, errors.New("model price unit_tokens must be positive")
	}
	if len(price.VariablesJSON) > 0 && !json.Valid(price.VariablesJSON) {
		return model.ModelPrice{}, errors.New("model price variables_json must be valid json")
	}
	return price, nil
}

// ListPaymentProducts 返回当前可购买的启用充值商品。
func (s *UserService) ListPaymentProducts(userID uint) ([]model.PaymentProduct, error) {
	if userID == 0 {
		return nil, errors.New("user is required")
	}
	var products []model.PaymentProduct
	err := internal.DB.Where("enabled = ?", true).Order("id ASC").Find(&products).Error
	return products, err
}

// ListPaymentProductsAdmin 返回管理端充值商品列表，包含禁用商品。
func (s *UserService) ListPaymentProductsAdmin(operatorRole int, page, pageSize int, keyword string, enabled *bool) ([]model.PaymentProduct, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.PaymentProduct{})
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("product_id LIKE ? OR name LIKE ?", like, like)
	}
	if enabled != nil {
		query = query.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var products []model.PaymentProduct
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&products).Error
	return products, total, err
}

func (s *UserService) GetPaymentProductAdmin(operatorRole int, id uint) (*model.PaymentProduct, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if id == 0 {
		return nil, errors.New("payment product is required")
	}
	var product model.PaymentProduct
	if err := internal.DB.First(&product, id).Error; err != nil {
		return nil, err
	}
	return &product, nil
}

func (s *UserService) CreatePaymentProduct(operatorRole int, product model.PaymentProduct) (*model.PaymentProduct, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	normalized, err := normalizePaymentProduct(product)
	if err != nil {
		return nil, err
	}
	var existing int64
	if err := internal.DB.Model(&model.PaymentProduct{}).Where("product_id = ?", normalized.ProductID).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, errors.New("payment product already exists")
	}
	if err := internal.DB.Create(&normalized).Error; err != nil {
		return nil, err
	}
	return &normalized, nil
}

func (s *UserService) UpdatePaymentProduct(operatorRole int, id uint, product model.PaymentProduct, enabled *bool) (*model.PaymentProduct, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	if id == 0 {
		return nil, errors.New("payment product is required")
	}
	normalized, err := normalizePaymentProduct(product)
	if err != nil {
		return nil, err
	}
	var current model.PaymentProduct
	if err := internal.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	var duplicate int64
	if err := internal.DB.Model(&model.PaymentProduct{}).
		Where("product_id = ? AND id <> ?", normalized.ProductID, id).
		Count(&duplicate).Error; err != nil {
		return nil, err
	}
	if duplicate > 0 {
		return nil, errors.New("payment product already exists")
	}
	updates := map[string]interface{}{
		"product_id":           normalized.ProductID,
		"name":                 normalized.Name,
		"amount":               normalized.Amount,
		"currency":             normalized.Currency,
		"quota":                normalized.Quota,
		"bonus_quota":          normalized.BonusQuota,
		"provider_config_json": normalized.ProviderConfigJSON,
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if err := internal.DB.Model(&current).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := internal.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	return &current, nil
}

func (s *UserService) SetPaymentProductEnabled(operatorRole int, id uint, enabled bool) error {
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	if id == 0 {
		return errors.New("payment product is required")
	}
	res := internal.DB.Model(&model.PaymentProduct{}).Where("id = ?", id).Update("enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("payment product not found")
	}
	return nil
}

func normalizePaymentProduct(product model.PaymentProduct) (model.PaymentProduct, error) {
	product.ProductID = strings.TrimSpace(product.ProductID)
	product.Name = strings.TrimSpace(product.Name)
	product.Amount = strings.TrimSpace(product.Amount)
	product.Currency = strings.ToLower(strings.TrimSpace(product.Currency))
	if product.ProductID == "" || product.Name == "" {
		return model.PaymentProduct{}, errors.New("payment product id and name are required")
	}
	amount, err := decimalAmountToMinorUnits(product.Amount)
	if err != nil || amount <= 0 {
		return model.PaymentProduct{}, errors.New("payment product amount must be positive")
	}
	if len(product.Currency) != 3 {
		return model.PaymentProduct{}, errors.New("payment product currency must be a 3-letter code")
	}
	if product.Quota <= 0 {
		return model.PaymentProduct{}, errors.New("payment product quota must be positive")
	}
	if product.BonusQuota < 0 {
		return model.PaymentProduct{}, errors.New("payment product bonus quota must be non-negative")
	}
	if len(product.ProviderConfigJSON) > 0 && !json.Valid(product.ProviderConfigJSON) {
		return model.PaymentProduct{}, errors.New("payment product provider config must be valid json")
	}
	return product, nil
}

// CreatePaymentOrder 创建本地 pending 支付订单，保存商品金额、币种和入账额度快照。
func (s *UserService) CreatePaymentOrder(userID uint, provider, productID, payType, returnURL string) (*model.PaymentOrder, error) {
	if userID == 0 {
		return nil, errors.New("user is required")
	}
	provider, err := normalizePaymentProvider(provider)
	if err != nil {
		return nil, err
	}
	providerEnabled, err := paymentProviderEnabled(provider)
	if err != nil {
		return nil, err
	}
	if !providerEnabled {
		return nil, errors.New("payment provider is disabled")
	}
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return nil, errors.New("product_id is required")
	}
	var product model.PaymentProduct
	if err := internal.DB.Where("product_id = ? AND enabled = ?", productID, true).First(&product).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("payment product is unavailable")
		}
		return nil, err
	}

	orderNo, err := generatePaymentOrderNo()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expireDuration, err := paymentOrderExpireDuration()
	if err != nil {
		return nil, err
	}
	expiredAt := now.Add(expireDuration)
	providerOrderID := "local_" + orderNo
	checkoutURL := "/v0/user/payment/orders/" + orderNo
	if provider == common.PaymentProviderStripe {
		sessionID, sessionURL, err := createStripeCheckoutSession(orderNo, userID, product, returnURL)
		if err != nil {
			return nil, err
		}
		if sessionID != "" && sessionURL != "" {
			providerOrderID = sessionID
			checkoutURL = sessionURL
		}
	}
	if provider == common.PaymentProviderEpay {
		if signedURL, err := epayCheckoutURL(orderNo, product, payType); err != nil {
			return nil, err
		} else if signedURL != "" {
			checkoutURL = signedURL
		}
	}
	order := &model.PaymentOrder{
		OrderNo:         orderNo,
		UserID:          userID,
		ProductID:       product.ProductID,
		Provider:        provider,
		Amount:          product.Amount,
		Currency:        strings.ToLower(strings.TrimSpace(product.Currency)),
		Quota:           product.Quota + product.BonusQuota,
		Status:          common.PaymentOrderStatusPending,
		ProviderOrderID: &providerOrderID,
		CheckoutURL:     &checkoutURL,
		ExpiredAt:       &expiredAt,
	}
	if err := internal.DB.Create(order).Error; err != nil {
		return nil, err
	}
	return order, nil
}

func createStripeCheckoutSession(orderNo string, userID uint, product model.PaymentProduct, returnURL string) (string, string, error) {
	secret := strings.TrimSpace(os.Getenv("PAYMENT_STRIPE_SECRET_KEY"))
	if secret == "" {
		return "", "", nil
	}
	returnURL = strings.TrimSpace(returnURL)
	if returnURL == "" {
		return "", "", nil
	}
	parsedReturnURL, err := url.Parse(returnURL)
	if err != nil || parsedReturnURL.Scheme == "" || parsedReturnURL.Host == "" {
		return "", "", errors.New("stripe return_url must be an absolute URL")
	}
	amountMinor, err := decimalAmountToMinorUnits(product.Amount)
	if err != nil {
		return "", "", err
	}
	currency := strings.ToLower(strings.TrimSpace(product.Currency))
	if currency == "" {
		return "", "", errors.New("stripe currency is required")
	}

	userIDText := strconv.FormatUint(uint64(userID), 10)
	values := url.Values{}
	values.Set("mode", "payment")
	values.Set("success_url", returnURL)
	values.Set("cancel_url", returnURL)
	values.Set("client_reference_id", orderNo)
	values.Set("line_items[0][price_data][currency]", currency)
	values.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(amountMinor, 10))
	values.Set("line_items[0][price_data][product_data][name]", product.Name)
	values.Set("line_items[0][quantity]", "1")
	values.Set("metadata[order_no]", orderNo)
	values.Set("metadata[product_id]", product.ProductID)
	values.Set("metadata[user_id]", userIDText)
	values.Set("payment_intent_data[metadata][order_no]", orderNo)
	values.Set("payment_intent_data[metadata][product_id]", product.ProductID)
	values.Set("payment_intent_data[metadata][user_id]", userIDText)
	values.Set("payment_intent_data[metadata][routerx_order_source]", "payment_order")

	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv("PAYMENT_STRIPE_API_BASE")), "/")
	if apiBase == "" {
		apiBase = "https://api.stripe.com"
	}
	req, err := http.NewRequest(http.MethodPost, apiBase+"/v1/checkout/sessions", strings.NewReader(values.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("stripe checkout session failed: status %d", resp.StatusCode)
	}
	var result struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", err
	}
	result.ID = strings.TrimSpace(result.ID)
	result.URL = strings.TrimSpace(result.URL)
	if result.ID == "" || result.URL == "" {
		return "", "", errors.New("stripe checkout session response missing id or url")
	}
	return result.ID, result.URL, nil
}

func createStripeRefund(order model.PaymentOrder, amountMinor int64, idempotencyKey, reason string) (string, string, string, error) {
	secret := strings.TrimSpace(os.Getenv("PAYMENT_STRIPE_SECRET_KEY"))
	if secret == "" {
		return "", "", "", errors.New("stripe secret key is not configured")
	}
	if order.ProviderPaymentID == nil || strings.TrimSpace(*order.ProviderPaymentID) == "" {
		return "", "", "", errors.New("stripe payment_intent is required")
	}
	if amountMinor <= 0 {
		return "", "", "", errors.New("refund_amount must be positive")
	}

	values := url.Values{}
	values.Set("payment_intent", strings.TrimSpace(*order.ProviderPaymentID))
	values.Set("amount", strconv.FormatInt(amountMinor, 10))
	values.Set("metadata[order_no]", order.OrderNo)
	values.Set("metadata[idempotency_key]", idempotencyKey)
	values.Set("metadata[reason]", reason)
	values.Set("metadata[routerx_refund_source]", "admin_refund_request")

	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv("PAYMENT_STRIPE_API_BASE")), "/")
	if apiBase == "" {
		apiBase = "https://api.stripe.com"
	}
	req, err := http.NewRequest(http.MethodPost, apiBase+"/v1/refunds", strings.NewReader(values.Encode()))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", "", fmt.Errorf("stripe refund request failed: status %d", resp.StatusCode)
	}
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", "", err
	}
	result.ID = strings.TrimSpace(result.ID)
	result.Status = strings.TrimSpace(result.Status)
	if result.ID == "" {
		return "", "", "", errors.New("stripe refund response missing id")
	}
	return result.ID, result.Status, string(body), nil
}

func createEpayRefund(order model.PaymentOrder, amountMinor int64, idempotencyKey, reason string) (string, string, string, error) {
	key := strings.TrimSpace(os.Getenv("PAYMENT_EPAY_KEY"))
	if key == "" {
		return "", "", "", errors.New("epay key is not configured")
	}
	pid, pidOK, err := paymentSetting("payment.epay.pid")
	if err != nil {
		return "", "", "", err
	}
	refundURL, refundURLOK, err := paymentSetting("payment.epay.refund_url")
	if err != nil {
		return "", "", "", err
	}
	if !pidOK || !refundURLOK {
		return "", "", "", errors.New("epay refund settings are not configured")
	}
	if amountMinor <= 0 {
		return "", "", "", errors.New("refund_amount must be positive")
	}

	values := map[string]string{
		"act":             "refund",
		"pid":             pid,
		"out_trade_no":    order.OrderNo,
		"money":           formatMinorUnits(amountMinor),
		"reason":          reason,
		"idempotency_key": idempotencyKey,
		"sign_type":       "MD5",
	}
	if order.ProviderPaymentID != nil && strings.TrimSpace(*order.ProviderPaymentID) != "" {
		values["trade_no"] = strings.TrimSpace(*order.ProviderPaymentID)
	}
	values["sign"] = epaySign(values, key)

	form := url.Values{}
	for name, value := range values {
		form.Set(name, value)
	}
	req, err := http.NewRequest(http.MethodPost, refundURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", "", fmt.Errorf("epay refund request failed: status %d", resp.StatusCode)
	}
	var result struct {
		Code     interface{} `json:"code"`
		Msg      string      `json:"msg"`
		RefundNo string      `json:"refund_no"`
		RefundID string      `json:"refund_id"`
		TradeNo  string      `json:"trade_no"`
		Status   string      `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", "", err
	}
	if !epayRefundAccepted(result.Code) {
		msg := strings.TrimSpace(result.Msg)
		if msg == "" {
			msg = "epay refund request rejected"
		}
		return "", "", "", errors.New(msg)
	}
	providerRefundID := firstNonEmpty(result.RefundNo, result.RefundID, result.TradeNo)
	if providerRefundID == "" {
		providerRefundID = fallbackEpayRefundID(order.OrderNo, idempotencyKey)
	}
	return providerRefundID, strings.TrimSpace(result.Status), string(body), nil
}

func epayRefundAccepted(code interface{}) bool {
	switch value := code.(type) {
	case float64:
		return value == 1
	case int:
		return value == 1
	case string:
		normalized := strings.ToLower(strings.TrimSpace(value))
		return normalized == "1" || normalized == "success" || normalized == "succeeded"
	case bool:
		return value
	default:
		return false
	}
}

func fallbackEpayRefundID(orderNo, idempotencyKey string) string {
	sum := md5.Sum([]byte(orderNo + ":" + idempotencyKey))
	return "epay_refund_" + hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func epayCheckoutURL(orderNo string, product model.PaymentProduct, payType string) (string, error) {
	key := strings.TrimSpace(os.Getenv("PAYMENT_EPAY_KEY"))
	gateway, gatewayOK, err := paymentSetting("payment.epay.gateway")
	if err != nil {
		return "", err
	}
	pid, pidOK, err := paymentSetting("payment.epay.pid")
	if err != nil {
		return "", err
	}
	notifyURL, notifyOK, err := paymentSetting("payment.epay.notify_url")
	if err != nil {
		return "", err
	}
	returnURL, returnOK, err := paymentSetting("payment.epay.return_url")
	if err != nil {
		return "", err
	}
	if key == "" || !gatewayOK || !pidOK || !notifyOK || !returnOK {
		return "", nil
	}
	payType = strings.TrimSpace(payType)
	if payType == "" {
		payType = "alipay"
	}
	values := map[string]string{
		"pid":          pid,
		"type":         payType,
		"out_trade_no": orderNo,
		"notify_url":   notifyURL,
		"return_url":   returnURL,
		"name":         product.Name,
		"money":        product.Amount,
		"sign_type":    "MD5",
	}
	values["sign"] = epaySign(values, key)
	parsed, err := url.Parse(gateway)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	for name, value := range values {
		query.Set(name, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func paymentSetting(key string) (string, bool, error) {
	value, err := NewSettingService().Get(key)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	value = strings.TrimSpace(value)
	return value, value != "", nil
}

func paymentProviderEnabled(provider string) (bool, error) {
	enabled, err := NewSettingService().GetBool("payment." + provider + ".enabled")
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return enabled, err
}

func paymentOrderExpireDuration() (time.Duration, error) {
	minutes, err := NewSettingService().GetInt("payment.order_expire_minutes")
	if errors.Is(err, gorm.ErrRecordNotFound) {
		minutes = 30
	} else if err != nil {
		return 0, err
	}
	if minutes <= 0 {
		return 0, errors.New("payment.order_expire_minutes must be a positive integer")
	}
	return time.Duration(minutes) * time.Minute, nil
}

// ListPaymentOrders 查询当前用户自己的支付订单列表。
func (s *UserService) ListPaymentOrders(userID uint, page, pageSize int) ([]model.PaymentOrder, int64, error) {
	if userID == 0 {
		return nil, 0, errors.New("user is required")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.PaymentOrder{}).Where("user_id = ?", userID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var orders []model.PaymentOrder
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&orders).Error
	return orders, total, err
}

// GetPaymentOrder 查询当前用户自己的支付订单详情。
func (s *UserService) GetPaymentOrder(userID uint, orderNo string) (*model.PaymentOrder, error) {
	orderNo = strings.TrimSpace(orderNo)
	if userID == 0 || orderNo == "" {
		return nil, errors.New("payment order is required")
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("user_id = ? AND order_no = ?", userID, orderNo).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// CancelPaymentOrder 将当前用户自己的 pending 支付订单关闭。
// 已关闭订单按幂等成功处理；已支付、退款中或已退款订单不能被用户取消。
func (s *UserService) CancelPaymentOrder(userID uint, orderNo string) (*model.PaymentOrder, bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	if userID == 0 || orderNo == "" {
		return nil, false, errors.New("payment order is required")
	}
	var result *model.PaymentOrder
	changed := false
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var order model.PaymentOrder
		if err := tx.Where("user_id = ? AND order_no = ?", userID, orderNo).First(&order).Error; err != nil {
			return err
		}
		switch order.Status {
		case common.PaymentOrderStatusClosed:
			result = &order
			return nil
		case common.PaymentOrderStatusPending:
		default:
			return errors.New("payment order is not pending")
		}

		now := time.Now()
		updates := map[string]interface{}{
			"status":     common.PaymentOrderStatusClosed,
			"updated_at": now,
		}
		res := tx.Model(&model.PaymentOrder{}).
			Where("id = ? AND status = ?", order.ID, common.PaymentOrderStatusPending).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if err := tx.First(&order, order.ID).Error; err != nil {
				return err
			}
			if order.Status == common.PaymentOrderStatusClosed {
				result = &order
				return nil
			}
			return errors.New("payment order is not pending")
		}
		order.Status = common.PaymentOrderStatusClosed
		order.UpdatedAt = now
		result = &order
		changed = true
		return nil
	})
	return result, changed, err
}

// GetEpayReturnOrder 返回易支付同步返回页可展示的本地订单状态。
// 同步返回不可信，只允许读本地订单快照，入账必须等待异步通知。
func (s *UserService) GetEpayReturnOrder(orderNo string) (*model.PaymentOrder, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, errors.New("payment order is required")
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("order_no = ? AND provider = ?", orderNo, common.PaymentProviderEpay).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// ProcessEpayNotify 验证易支付异步通知，并在可信成功事件中幂等入账。
func (s *UserService) ProcessEpayNotify(values map[string]string, requestID string) error {
	key := strings.TrimSpace(os.Getenv("PAYMENT_EPAY_KEY"))
	if key == "" {
		return errors.New("epay key is not configured")
	}
	if !verifyEpaySign(values, key) {
		return errors.New("invalid epay signature")
	}
	orderNo := strings.TrimSpace(values["out_trade_no"])
	tradeNo := strings.TrimSpace(values["trade_no"])
	money := strings.TrimSpace(values["money"])
	status := strings.TrimSpace(values["trade_status"])
	if orderNo == "" || money == "" || !epayTradeSucceeded(status) {
		return errors.New("invalid epay notify")
	}
	eventID := tradeNo
	if eventID == "" {
		eventID = common.PaymentProviderEpay + ":" + orderNo + ":" + status
	}
	payload, _ := json.Marshal(redactedEpayPayload(values))

	return internal.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.PaymentEvent
		err := tx.Where("provider = ? AND provider_event_id = ?", common.PaymentProviderEpay, eventID).First(&existing).Error
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		event := model.PaymentEvent{
			Provider:        common.PaymentProviderEpay,
			ProviderEventID: eventID,
			OrderNo:         orderNo,
			EventType:       "notify",
			Payload:         string(payload),
			SignatureValid:  true,
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}

		var order model.PaymentOrder
		if err := tx.Where("order_no = ? AND provider = ?", orderNo, common.PaymentProviderEpay).First(&order).Error; err != nil {
			return err
		}
		if strings.TrimSpace(order.Amount) != money {
			return errors.New("epay notify amount mismatch")
		}
		if order.Status == common.PaymentOrderStatusPaid {
			now := time.Now()
			if err := tx.Model(&event).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error; err != nil {
				return err
			}
			event.Processed = true
			event.ProcessedAt = &now
			return recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentWebhookProcessed, event, &order, map[string]interface{}{
				"idempotent": true,
			})
		}
		if order.Status != common.PaymentOrderStatusPending {
			return errors.New("payment order is not pending")
		}

		now := time.Now()
		updates := map[string]interface{}{
			"status":     common.PaymentOrderStatusPaid,
			"paid_at":    &now,
			"updated_at": now,
		}
		if tradeNo != "" {
			updates["provider_payment_id"] = tradeNo
		}
		if err := tx.Model(&order).Updates(updates).Error; err != nil {
			return err
		}
		if _, _, err := applyQuotaChange(tx, quotaChange{
			UserID:         order.UserID,
			Amount:         order.Quota,
			Type:           common.QuotaTransactionTypePaymentGrant,
			SourceType:     common.QuotaSourceTypePaymentOrder,
			SourceID:       order.OrderNo,
			IdempotencyKey: "payment_order:" + order.OrderNo,
			Reason:         "epay payment grant",
			RequestID:      requestID,
		}); err != nil {
			return err
		}
		if err := tx.Model(&event).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error; err != nil {
			return err
		}
		event.Processed = true
		event.ProcessedAt = &now
		order.Status = common.PaymentOrderStatusPaid
		if tradeNo != "" {
			order.ProviderPaymentID = &tradeNo
		}
		if err := recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentWebhookProcessed, event, &order, nil); err != nil {
			return err
		}
		return recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentOrderPaid, event, &order, nil)
	})
}

type stripeWebhookEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object stripeCheckoutSession `json:"object"`
	} `json:"data"`
}

type stripeCheckoutSession struct {
	ID             string            `json:"id"`
	Metadata       map[string]string `json:"metadata"`
	Amount         int64             `json:"amount"`
	AmountTotal    int64             `json:"amount_total"`
	AmountRefunded int64             `json:"amount_refunded"`
	Currency       string            `json:"currency"`
	PaymentStatus  string            `json:"payment_status"`
	PaymentIntent  string            `json:"payment_intent"`
	Status         string            `json:"status"`
	Reason         string            `json:"reason"`
}

// ProcessStripeWebhook 验证 Stripe 原始签名，并在可信 Checkout 成功事件中幂等入账。
func (s *UserService) ProcessStripeWebhook(raw []byte, signatureHeader, requestID string) error {
	secret := strings.TrimSpace(os.Getenv("PAYMENT_STRIPE_WEBHOOK_SECRET"))
	if secret == "" {
		return errors.New("stripe webhook secret is not configured")
	}
	if !verifyStripeSignature(raw, signatureHeader, secret) {
		return errors.New("invalid stripe signature")
	}
	var event stripeWebhookEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	event.ID = strings.TrimSpace(event.ID)
	event.Type = strings.TrimSpace(event.Type)
	if event.ID == "" || event.Type == "" {
		return errors.New("invalid stripe event")
	}
	session := event.Data.Object
	orderNo := strings.TrimSpace(session.Metadata["order_no"])

	return internal.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.PaymentEvent
		err := tx.Where("provider = ? AND provider_event_id = ?", common.PaymentProviderStripe, event.ID).First(&existing).Error
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		paymentEvent := model.PaymentEvent{
			Provider:        common.PaymentProviderStripe,
			ProviderEventID: event.ID,
			OrderNo:         orderNo,
			EventType:       event.Type,
			Payload:         string(raw),
			SignatureValid:  true,
		}
		if err := tx.Create(&paymentEvent).Error; err != nil {
			return err
		}
		if event.Type == "charge.refunded" {
			return processStripeRefund(tx, &paymentEvent, session, requestID)
		}
		if isStripeDisputeEvent(event.Type) {
			return processStripeDispute(tx, &paymentEvent, session, requestID)
		}
		if event.Type != "checkout.session.completed" || !stripeCheckoutSucceeded(session) {
			now := time.Now()
			if err := tx.Model(&paymentEvent).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error; err != nil {
				return err
			}
			paymentEvent.Processed = true
			paymentEvent.ProcessedAt = &now
			return recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentWebhookProcessed, paymentEvent, nil, map[string]interface{}{
				"accepted_without_order": true,
			})
		}
		if orderNo == "" {
			return errors.New("stripe event missing order_no")
		}

		var order model.PaymentOrder
		if err := tx.Where("order_no = ? AND provider = ?", orderNo, common.PaymentProviderStripe).First(&order).Error; err != nil {
			return err
		}
		if err := verifyStripeOrderSnapshot(order, session); err != nil {
			return err
		}
		if order.Status == common.PaymentOrderStatusPaid {
			now := time.Now()
			if err := tx.Model(&paymentEvent).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error; err != nil {
				return err
			}
			paymentEvent.Processed = true
			paymentEvent.ProcessedAt = &now
			return recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentWebhookProcessed, paymentEvent, &order, map[string]interface{}{
				"idempotent": true,
			})
		}
		if order.Status != common.PaymentOrderStatusPending {
			return errors.New("payment order is not pending")
		}

		now := time.Now()
		providerPaymentID := strings.TrimSpace(session.PaymentIntent)
		if providerPaymentID == "" {
			providerPaymentID = strings.TrimSpace(session.ID)
		}
		updates := map[string]interface{}{
			"status":     common.PaymentOrderStatusPaid,
			"paid_at":    &now,
			"updated_at": now,
		}
		if providerPaymentID != "" {
			updates["provider_payment_id"] = providerPaymentID
		}
		if err := tx.Model(&order).Updates(updates).Error; err != nil {
			return err
		}
		if _, _, err := applyQuotaChange(tx, quotaChange{
			UserID:         order.UserID,
			Amount:         order.Quota,
			Type:           common.QuotaTransactionTypePaymentGrant,
			SourceType:     common.QuotaSourceTypePaymentOrder,
			SourceID:       order.OrderNo,
			IdempotencyKey: "payment_order:" + order.OrderNo,
			Reason:         "stripe payment grant",
			RequestID:      requestID,
		}); err != nil {
			return err
		}
		if err := tx.Model(&paymentEvent).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error; err != nil {
			return err
		}
		paymentEvent.Processed = true
		paymentEvent.ProcessedAt = &now
		order.Status = common.PaymentOrderStatusPaid
		if providerPaymentID != "" {
			order.ProviderPaymentID = &providerPaymentID
		}
		if err := recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentWebhookProcessed, paymentEvent, &order, nil); err != nil {
			return err
		}
		return recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentOrderPaid, paymentEvent, &order, nil)
	})
}

func processStripeRefund(tx *gorm.DB, event *model.PaymentEvent, session stripeCheckoutSession, requestID string) error {
	orderNo := strings.TrimSpace(session.Metadata["order_no"])
	paymentIntent := strings.TrimSpace(session.PaymentIntent)
	if orderNo == "" && paymentIntent == "" {
		return errors.New("stripe refund missing order reference")
	}
	query := tx.Where("provider = ?", common.PaymentProviderStripe)
	if orderNo != "" {
		query = query.Where("order_no = ?", orderNo)
	} else {
		query = query.Where("provider_payment_id = ?", paymentIntent)
	}
	var order model.PaymentOrder
	if err := query.First(&order).Error; err != nil {
		return err
	}
	orderAmountMinor, refundedAmountMinor, err := verifyStripeRefundSnapshot(order, session)
	if err != nil {
		return err
	}

	now := time.Now()
	eventUpdates := map[string]interface{}{"processed": true, "processed_at": &now}
	if event.OrderNo == "" {
		eventUpdates["order_no"] = order.OrderNo
		event.OrderNo = order.OrderNo
	}
	if order.Status == common.PaymentOrderStatusRefunded {
		if err := tx.Model(event).Updates(eventUpdates).Error; err != nil {
			return err
		}
		event.Processed = true
		event.ProcessedAt = &now
		return recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentRefundProcessed, *event, &order, map[string]interface{}{
			"idempotent": true,
		})
	}
	if order.Status != common.PaymentOrderStatusPaid && order.Status != common.PaymentOrderStatusRefundPending {
		return errors.New("payment order is not paid")
	}
	refundStatus := common.PaymentOrderStatusRefunded
	refundType := "full"
	refundQuota := order.Quota
	if refundedAmountMinor < orderAmountMinor {
		refundStatus = common.PaymentOrderStatusPartiallyRefunded
		refundType = "partial"
		refundQuota = proportionalRefundQuota(order.Quota, refundedAmountMinor, orderAmountMinor)
	}
	if err := tx.Model(&order).Updates(map[string]interface{}{
		"status":     refundStatus,
		"updated_at": now,
	}).Error; err != nil {
		return err
	}
	order.Status = refundStatus
	if err := tx.Model(&model.PaymentRefundRequest{}).
		Where("order_no = ? AND status IN ?", order.OrderNo, []string{"pending", "succeeded"}).
		Update("status", refundStatus).Error; err != nil {
		return err
	}

	autoDeduct, err := paymentBoolSettingDefault("payment.refund.auto_deduct", false)
	if err != nil {
		return err
	}
	allowNegative, err := paymentBoolSettingDefault("payment.refund.allow_negative_balance", false)
	if err != nil {
		return err
	}
	deducted := false
	if autoDeduct {
		var user model.User
		if err := tx.Select("id", "quota").First(&user, order.UserID).Error; err != nil {
			return err
		}
		if refundQuota > 0 && (allowNegative || user.Quota >= refundQuota) {
			reason := "stripe refund deduct"
			if refundType == "partial" {
				reason = "stripe partial refund deduct"
			}
			if _, _, err := applyQuotaChange(tx, quotaChange{
				UserID:         order.UserID,
				Amount:         -refundQuota,
				Type:           common.QuotaTransactionTypeRefundDeduct,
				SourceType:     common.QuotaSourceTypeRefund,
				SourceID:       event.ProviderEventID,
				IdempotencyKey: "refund:" + event.ProviderEventID,
				Reason:         reason,
				RequestID:      requestID,
				AllowNegative:  allowNegative,
			}); err != nil {
				return err
			}
			deducted = true
		}
	}
	if err := tx.Model(event).Updates(eventUpdates).Error; err != nil {
		return err
	}
	event.Processed = true
	event.ProcessedAt = &now
	refundSummary := map[string]interface{}{
		"amount_refunded": refundedAmountMinor,
		"order_amount":    orderAmountMinor,
		"auto_deduct":     autoDeduct,
		"allow_negative":  allowNegative,
		"deducted":        deducted,
		"refund_quota":    refundQuota,
		"refund_type":     refundType,
	}
	if err := recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentRefundProcessed, *event, &order, refundSummary); err != nil {
		return err
	}
	if deducted {
		return recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentRefundDeducted, *event, &order, refundSummary)
	}
	return nil
}

// processStripeDispute 记录争议生命周期事实，并在 created 阶段执行可选风控动作。
func processStripeDispute(tx *gorm.DB, event *model.PaymentEvent, session stripeCheckoutSession, requestID string) error {
	orderNo := strings.TrimSpace(session.Metadata["order_no"])
	paymentIntent := strings.TrimSpace(session.PaymentIntent)
	if orderNo == "" && paymentIntent == "" {
		return errors.New("stripe dispute missing order reference")
	}
	query := tx.Where("provider = ?", common.PaymentProviderStripe)
	if orderNo != "" {
		query = query.Where("order_no = ?", orderNo)
	} else {
		query = query.Where("provider_payment_id = ?", paymentIntent)
	}
	var order model.PaymentOrder
	if err := query.First(&order).Error; err != nil {
		return err
	}
	orderAmountMinor, disputeAmountMinor, err := verifyStripeDisputeSnapshot(order, session)
	if err != nil {
		return err
	}

	disputeID := strings.TrimSpace(session.ID)
	if disputeID == "" {
		return errors.New("stripe dispute id is required")
	}
	disputeStatus := strings.TrimSpace(session.Status)
	if disputeStatus == "" {
		disputeStatus = stripeDisputeStatusFromEvent(event.EventType)
	}
	fundsStatus := stripeDisputeFundsStatus(event.EventType)
	autoDisableTokens, err := paymentBoolSettingDefault("payment.dispute.auto_disable_tokens", false)
	if err != nil {
		return err
	}
	now := time.Now()
	eventUpdates := map[string]interface{}{"processed": true, "processed_at": &now}
	if event.OrderNo == "" {
		eventUpdates["order_no"] = order.OrderNo
		event.OrderNo = order.OrderNo
	}

	tokensDisabled := int64(0)
	if autoDisableTokens && event.EventType == "charge.dispute.created" {
		result := tx.Model(&model.Token{}).
			Where("user_id = ? AND status = ?", order.UserID, common.TokenStatusEnabled).
			Updates(map[string]interface{}{
				"status":         common.TokenStatusDisabled,
				"revoked_reason": "payment_dispute",
				"updated_at":     now,
			})
		if result.Error != nil {
			return result.Error
		}
		tokensDisabled = result.RowsAffected
	}
	if err := upsertStripeDispute(tx, model.PaymentDispute{
		Provider:          common.PaymentProviderStripe,
		ProviderDisputeID: disputeID,
		OrderNo:           order.OrderNo,
		UserID:            order.UserID,
		ProviderPaymentID: paymentIntent,
		AmountMinor:       disputeAmountMinor,
		Currency:          strings.ToLower(strings.TrimSpace(order.Currency)),
		Status:            disputeStatus,
		Reason:            strings.TrimSpace(session.Reason),
		FundsStatus:       fundsStatus,
		LastEventID:       event.ProviderEventID,
		LastEventType:     event.EventType,
	}); err != nil {
		return err
	}
	if err := tx.Model(event).Updates(eventUpdates).Error; err != nil {
		return err
	}
	event.Processed = true
	event.ProcessedAt = &now
	disputeSummary := map[string]interface{}{
		"dispute_id":          disputeID,
		"amount_disputed":     disputeAmountMinor,
		"order_amount":        orderAmountMinor,
		"payment_intent":      paymentIntent,
		"dispute_status":      disputeStatus,
		"funds_status":        fundsStatus,
		"reason":              strings.TrimSpace(session.Reason),
		"auto_disable_tokens": autoDisableTokens,
		"tokens_disabled":     tokensDisabled,
	}
	if event.EventType == "charge.dispute.created" {
		if err := recordPaymentEventAudit(tx, requestID, adminAuditActionPaymentDisputeCreated, *event, &order, disputeSummary); err != nil {
			return err
		}
	}
	return recordPaymentDisputeAudit(tx, requestID, stripeDisputeAuditAction(event.EventType), event.ProviderEventID, disputeID, order, disputeSummary)
}

func upsertStripeDispute(tx *gorm.DB, dispute model.PaymentDispute) error {
	now := time.Now()
	var existing model.PaymentDispute
	err := tx.Where("provider = ? AND provider_dispute_id = ?", dispute.Provider, dispute.ProviderDisputeID).First(&existing).Error
	switch {
	case err == nil:
		return tx.Model(&existing).Updates(map[string]interface{}{
			"order_no":            dispute.OrderNo,
			"user_id":             dispute.UserID,
			"provider_payment_id": dispute.ProviderPaymentID,
			"amount_minor":        dispute.AmountMinor,
			"currency":            dispute.Currency,
			"status":              dispute.Status,
			"reason":              dispute.Reason,
			"funds_status":        dispute.FundsStatus,
			"last_event_id":       dispute.LastEventID,
			"last_event_type":     dispute.LastEventType,
			"updated_at":          now,
		}).Error
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return err
	}
	dispute.CreatedAt = now
	dispute.UpdatedAt = now
	return tx.Create(&dispute).Error
}

func recordPaymentDisputeAudit(tx *gorm.DB, requestID, action, eventID, disputeID string, order model.PaymentOrder, extra map[string]interface{}) error {
	summary := map[string]interface{}{
		"provider":   common.PaymentProviderStripe,
		"event_id":   eventID,
		"dispute_id": disputeID,
		"order_no":   order.OrderNo,
		"user_id":    order.UserID,
		"product_id": order.ProductID,
		"amount":     order.Amount,
		"currency":   order.Currency,
		"quota":      order.Quota,
	}
	for key, value := range extra {
		summary[key] = value
	}
	afterSummary, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return recordAdminAuditLogWithDB(tx, AdminAuditRecordInput{
		RequestID:    requestID,
		ActorUserID:  paymentWebhookAuditActorUserID,
		ActorRole:    common.RoleSuper,
		Action:       action,
		ResourceType: "payment_dispute",
		ResourceID:   disputeID,
		AfterSummary: string(afterSummary),
		Result:       "success",
	})
}

func isStripeDisputeEvent(eventType string) bool {
	switch eventType {
	case "charge.dispute.created", "charge.dispute.updated", "charge.dispute.closed", "charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated":
		return true
	default:
		return false
	}
}

func stripeDisputeAuditAction(eventType string) string {
	switch eventType {
	case "charge.dispute.created":
		return adminAuditActionPaymentDisputeCreated
	case "charge.dispute.updated":
		return adminAuditActionPaymentDisputeUpdated
	case "charge.dispute.closed":
		return adminAuditActionPaymentDisputeClosed
	case "charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated":
		return adminAuditActionPaymentDisputeFunds
	default:
		return adminAuditActionPaymentDisputeUpdated
	}
}

func stripeDisputeStatusFromEvent(eventType string) string {
	switch eventType {
	case "charge.dispute.created":
		return "needs_response"
	case "charge.dispute.closed":
		return "closed"
	case "charge.dispute.funds_withdrawn":
		return "funds_withdrawn"
	case "charge.dispute.funds_reinstated":
		return "funds_reinstated"
	default:
		return "updated"
	}
}

func stripeDisputeFundsStatus(eventType string) string {
	switch eventType {
	case "charge.dispute.funds_withdrawn":
		return "withdrawn"
	case "charge.dispute.funds_reinstated":
		return "reinstated"
	default:
		return ""
	}
}

func verifyStripeSignature(raw []byte, signatureHeader, secret string) bool {
	var timestamp string
	signatures := make([]string, 0, 1)
	for _, part := range strings.Split(signatureHeader, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signatures = append(signatures, value)
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return false
	}
	signedAt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	eventTime := time.Unix(signedAt, 0)
	if time.Since(eventTime) > 5*time.Minute || time.Until(eventTime) > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "." + string(raw)))
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		decoded, err := hex.DecodeString(signature)
		if err == nil && hmac.Equal(decoded, expected) {
			return true
		}
	}
	return false
}

func stripeCheckoutSucceeded(session stripeCheckoutSession) bool {
	return strings.EqualFold(strings.TrimSpace(session.PaymentStatus), "paid")
}

func verifyStripeOrderSnapshot(order model.PaymentOrder, session stripeCheckoutSession) error {
	expectedAmount, err := decimalAmountToMinorUnits(order.Amount)
	if err != nil {
		return err
	}
	if expectedAmount != session.AmountTotal {
		return errors.New("stripe amount mismatch")
	}
	if strings.ToLower(strings.TrimSpace(order.Currency)) != strings.ToLower(strings.TrimSpace(session.Currency)) {
		return errors.New("stripe currency mismatch")
	}
	if productID := strings.TrimSpace(session.Metadata["product_id"]); productID != "" && productID != order.ProductID {
		return errors.New("stripe product mismatch")
	}
	if userID := strings.TrimSpace(session.Metadata["user_id"]); userID != "" {
		parsed, err := strconv.ParseUint(userID, 10, 64)
		if err != nil || uint(parsed) != order.UserID {
			return errors.New("stripe user mismatch")
		}
	}
	if order.ProviderOrderID != nil {
		expectedSessionID := strings.TrimSpace(*order.ProviderOrderID)
		if expectedSessionID != "" && session.ID != "" && expectedSessionID != strings.TrimSpace(session.ID) {
			return errors.New("stripe session mismatch")
		}
	}
	return nil
}

func verifyStripeRefundSnapshot(order model.PaymentOrder, session stripeCheckoutSession) (int64, int64, error) {
	expectedAmount, err := decimalAmountToMinorUnits(order.Amount)
	if err != nil {
		return 0, 0, err
	}
	refundedAmount := session.AmountRefunded
	if refundedAmount == 0 {
		refundedAmount = session.Amount
	}
	if refundedAmount <= 0 || refundedAmount > expectedAmount {
		return 0, 0, errors.New("stripe refund amount mismatch")
	}
	if strings.ToLower(strings.TrimSpace(order.Currency)) != strings.ToLower(strings.TrimSpace(session.Currency)) {
		return 0, 0, errors.New("stripe refund currency mismatch")
	}
	return expectedAmount, refundedAmount, nil
}

func verifyStripeDisputeSnapshot(order model.PaymentOrder, session stripeCheckoutSession) (int64, int64, error) {
	expectedAmount, err := decimalAmountToMinorUnits(order.Amount)
	if err != nil {
		return 0, 0, err
	}
	disputeAmount := session.Amount
	if disputeAmount <= 0 || disputeAmount > expectedAmount {
		return 0, 0, errors.New("stripe dispute amount mismatch")
	}
	if strings.ToLower(strings.TrimSpace(order.Currency)) != strings.ToLower(strings.TrimSpace(session.Currency)) {
		return 0, 0, errors.New("stripe dispute currency mismatch")
	}
	return expectedAmount, disputeAmount, nil
}

func proportionalRefundQuota(orderQuota, refundedAmount, orderAmount int64) int64 {
	if orderQuota <= 0 || refundedAmount <= 0 || orderAmount <= 0 {
		return 0
	}
	if refundedAmount >= orderAmount {
		return orderQuota
	}
	return orderQuota * refundedAmount / orderAmount
}

func paymentBoolSettingDefault(key string, fallback bool) (bool, error) {
	value, err := NewSettingService().GetBool(key)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fallback, nil
	}
	return value, err
}

func decimalAmountToMinorUnits(amount string) (int64, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" || strings.HasPrefix(amount, "-") {
		return 0, errors.New("invalid amount")
	}
	parts := strings.Split(amount, ".")
	if len(parts) > 2 {
		return 0, errors.New("invalid amount")
	}
	whole := parts[0]
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) {
		return 0, errors.New("invalid amount")
	}
	fraction := "00"
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) > 2 || !allDigits(fraction) {
			return 0, errors.New("invalid amount")
		}
		fraction += strings.Repeat("0", 2-len(fraction))
	}
	major, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	minor, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, err
	}
	return major*100 + minor, nil
}

func formatMinorUnits(amountMinor int64) string {
	return fmt.Sprintf("%d.%02d", amountMinor/100, amountMinor%100)
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// UpdateSelf 用户自助修改个人信息。
func (s *UserService) UpdateSelf(id uint, displayName, email string) error {
	updates := map[string]interface{}{}
	if strings.TrimSpace(displayName) != "" {
		updates["display_name"] = strings.TrimSpace(displayName)
	}
	if strings.TrimSpace(email) != "" {
		normalized := normalizeEmail(email)
		updates["email"] = normalized
	}
	if len(updates) == 0 {
		return nil
	}
	return internal.DB.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

// RedeemCode 将未使用的充值码兑换到当前用户余额。
// 充值码状态、用户余额和额度流水必须在同一事务内完成，避免重复兑换或账实不一致。
func (s *UserService) RedeemCode(userID uint, code string, requestID string) (int64, int64, error) {
	code = strings.TrimSpace(code)
	if userID == 0 || code == "" {
		return 0, 0, errors.New("redem code is required")
	}
	var redeemedQuota int64
	var finalQuota int64
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var redem model.RedemCode
		if err := tx.Where("code = ? AND status = ?", code, common.RedemCodeStatusUnused).First(&redem).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("redem code is invalid or already used")
			}
			return err
		}
		now := time.Now()
		if redem.ExpiredAt != nil && !redem.ExpiredAt.After(now) {
			return errors.New("redem code is expired")
		}
		usedBy := userID
		res := tx.Model(&model.RedemCode{}).
			Where("id = ? AND status = ?", redem.ID, common.RedemCodeStatusUnused).
			Where("expired_at IS NULL OR expired_at > ?", now).
			Updates(map[string]interface{}{
				"status":  common.RedemCodeStatusUsed,
				"used_by": usedBy,
				"used_at": &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("redem code is invalid or already used")
		}
		_, balanceAfter, err := applyQuotaChange(tx, quotaChange{
			UserID:         userID,
			Amount:         redem.Quota,
			Type:           common.QuotaTransactionTypeRedemRedeem,
			SourceType:     common.QuotaSourceTypeRedemCode,
			SourceID:       fmt.Sprint(redem.ID),
			IdempotencyKey: fmt.Sprintf("redem_code:%d", redem.ID),
			Reason:         "redem code redeem",
			RequestID:      requestID,
		})
		if err != nil {
			return err
		}
		redeemedQuota = redem.Quota
		finalQuota = balanceAfter
		return nil
	})
	return redeemedQuota, finalQuota, err
}

func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func normalizeRedemCodeInputs(codes []string) ([]string, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	if len(codes) > 100 {
		return nil, errors.New("redem code count must be at most 100")
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(codes))
	for _, raw := range codes {
		code := strings.TrimSpace(raw)
		if len(code) < 4 || len(code) > 64 {
			return nil, errors.New("redem code length must be between 4 and 64")
		}
		if _, ok := seen[code]; ok {
			return nil, errors.New("redem code duplicated in request")
		}
		seen[code] = struct{}{}
		normalized = append(normalized, code)
	}
	return normalized, nil
}

func generateUniqueRedemCodes(tx *gorm.DB, count int) ([]string, error) {
	seen := map[string]struct{}{}
	codes := make([]string, 0, count)
	for attempts := 0; len(codes) < count && attempts < count*20; attempts++ {
		code := common.GenerateRedemCode()
		if _, ok := seen[code]; ok {
			continue
		}
		var existing int64
		if err := tx.Model(&model.RedemCode{}).Where("code = ?", code).Count(&existing).Error; err != nil {
			return nil, err
		}
		if existing > 0 {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	if len(codes) != count {
		return nil, errors.New("failed to generate unique redem codes")
	}
	return codes, nil
}

func normalizePaymentProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case common.PaymentProviderStripe, common.PaymentProviderEpay:
		return provider, nil
	default:
		return "", errors.New("unsupported payment provider")
	}
}

func generatePaymentOrderNo() (string, error) {
	suffix, err := common.GenerateRandomString(4)
	if err != nil {
		return "", err
	}
	return "pay_" + time.Now().UTC().Format("20060102150405") + suffix, nil
}

func verifyEpaySign(values map[string]string, key string) bool {
	sign := strings.ToLower(strings.TrimSpace(values["sign"]))
	if sign == "" {
		return false
	}
	return sign == epaySign(values, key)
}

func epaySign(values map[string]string, key string) string {
	keys := make([]string, 0, len(values))
	for k, v := range values {
		if k == "sign" || k == "sign_type" || strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values[k])
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	return fmt.Sprintf("%x", sum)
}

func redactedEpayPayload(values map[string]string) map[string]string {
	payload := make(map[string]string, len(values))
	for k, v := range values {
		if k == "sign" {
			continue
		}
		payload[k] = v
	}
	return payload
}

func epayTradeSucceeded(status string) bool {
	return status == "TRADE_SUCCESS" || status == "TRADE_FINISHED"
}

func filterUpdates(updates map[string]interface{}, keys ...string) map[string]interface{} {
	allowed := make(map[string]interface{})
	for _, key := range keys {
		if v, ok := updates[key]; ok {
			allowed[key] = v
		}
	}
	return allowed
}

func normalizeGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("group name is required")
	}
	if len(name) > 64 {
		return "", errors.New("group name is too long")
	}
	return name, nil
}

func (s *UserService) ensureNormalUserTarget(operatorID uint, operatorRole int, targetID uint) error {
	if operatorID == 0 {
		return errors.New("operator is required")
	}
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	if operatorID == targetID {
		return errors.New("admin user management cannot operate on self")
	}
	var target model.User
	if err := internal.DB.First(&target, targetID).Error; err != nil {
		return err
	}
	if target.Role >= common.RoleAdmin {
		return errors.New("admin accounts must be managed through super admin endpoints")
	}
	return nil
}
