package service

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// ListRedemCodes 查询充值码列表，供管理员按状态或 code 关键字检索。
func (s *UserService) ListRedemCodes(operatorRole int, page, pageSize int, status *int, keyword string) ([]model.RedemCode, int64, error) {
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
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where("code LIKE ?", "%"+keyword+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var codes []model.RedemCode
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&codes).Error
	return codes, total, err
}

// CreateRedemCodes 生成随机充值码，或导入管理员提供的指定充值码。
func (s *UserService) CreateRedemCodes(operatorRole int, quota int64, count int, codes []string) ([]model.RedemCode, error) {
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
				Code:   code,
				Quota:  quota,
				Status: common.RedemCodeStatusUnused,
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
	return NewChannelService().ListModels()
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

// CreatePaymentOrder 创建本地 pending 支付订单，保存商品金额、币种和入账额度快照。
func (s *UserService) CreatePaymentOrder(userID uint, provider, productID, payType, returnURL string) (*model.PaymentOrder, error) {
	if userID == 0 {
		return nil, errors.New("user is required")
	}
	provider, err := normalizePaymentProvider(provider)
	if err != nil {
		return nil, err
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
			return tx.Model(&event).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error
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
		return tx.Model(&event).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error
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
	ID            string            `json:"id"`
	Metadata      map[string]string `json:"metadata"`
	AmountTotal   int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
	PaymentStatus string            `json:"payment_status"`
	PaymentIntent string            `json:"payment_intent"`
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
		if event.Type != "checkout.session.completed" || !stripeCheckoutSucceeded(session) {
			now := time.Now()
			return tx.Model(&paymentEvent).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error
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
			return tx.Model(&paymentEvent).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error
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
		return tx.Model(&paymentEvent).Updates(map[string]interface{}{"processed": true, "processed_at": &now}).Error
	})
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
		usedBy := userID
		res := tx.Model(&model.RedemCode{}).
			Where("id = ? AND status = ?", redem.ID, common.RedemCodeStatusUnused).
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
