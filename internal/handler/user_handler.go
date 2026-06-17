package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
	"routerx/internal/service"
)

type UserHandler struct {
	svc *service.UserService
}

func NewUserHandler(svc *service.UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// GET /v0/admin/user — 用户列表
func (h *UserHandler) List(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.UserListRequest
	_ = c.ShouldBindQuery(&req)
	users, total, err := h.svc.List(operator.Role, req.Page, req.PageSize, req.Keyword, req.Role, req.Status, req.GroupID)
	if err != nil {
		common.FailWithStatus(c, 500, "查询用户失败")
		return
	}
	data := make([]dto.UserBrief, 0, len(users))
	for i := range users {
		data = append(data, dto.UserBriefFromModel(&users[i]))
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: data})
}

// POST /v0/admin/user — 创建用户
func (h *UserHandler) Create(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	if operator.Role < common.RoleAdmin {
		common.FailWithStatus(c, 403, "需要管理员权限")
		return
	}
	var req dto.CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "创建用户参数无效")
		return
	}
	if req.Role != common.RoleUser {
		common.FailWithStatus(c, 403, "用户管理接口只能创建普通用户")
		return
	}
	user, err := h.svc.Create(operator.Role, req.Username, req.Password, req.DisplayName, req.Email, req.Role, req.Quota, req.GroupID)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "user.create", "user", user.ID, nil, userAuditSummary(user)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.UserBriefFromModel(user))
}

// PUT /v0/admin/user/:id — 编辑用户
func (h *UserHandler) Update(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "编辑用户参数无效")
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	updates := map[string]interface{}{}
	if req.DisplayName != "" {
		updates["display_name"] = req.DisplayName
	}
	if req.Email != "" {
		updates["email"] = req.Email
	}
	if req.Role != nil {
		_ = h.recordAdminAuditResult(c, operator, "user.denied", "user", id, userAuditSummary(before), map[string]interface{}{
			"requested_role": *req.Role,
			"reason":         "user_management_role_change_forbidden",
		}, "denied", "role_change_forbidden")
		common.FailWithStatus(c, 403, "用户管理接口不能变更角色")
		return
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.GroupID != nil {
		updates["group_id"] = *req.GroupID
	}
	if err := h.svc.UpdateByAdmin(operator.ID, operator.Role, id, updates); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	action := "user.update"
	if status, ok := updates["status"].(int); ok && status == common.UserStatusDisabled {
		action = "user.disable"
	}
	if err := h.recordAdminAudit(c, operator, action, "user", id, userAuditSummary(before), userAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "用户已更新")
}

// DELETE /v0/admin/user/:id — 删除用户
func (h *UserHandler) Delete(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.DeleteByAdmin(operator.ID, operator.Role, id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "user.delete", "user", id, userAuditSummary(before), nil); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "用户已删除")
}

// PATCH /v0/admin/user/:id/quota — 调整用户余额
func (h *UserHandler) UpdateQuota(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "额度参数无效")
		return
	}
	before, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.UpdateQuotaByAdmin(operator.ID, operator.Role, id, req.Quota, req.Reason, c.GetString("request_id")); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "user.quota_update", "user", id, userQuotaAuditSummary(before, req.Reason), userQuotaAuditSummary(after, req.Reason)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "额度已更新")
}

// GET /v0/admin/groups — 用户分组列表
func (h *UserHandler) ListGroups(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.UserGroupListRequest
	_ = c.ShouldBindQuery(&req)
	groups, total, err := h.svc.ListGroups(operator.Role, req.Page, req.PageSize, req.Keyword)
	if err != nil {
		common.FailWithStatus(c, 500, "查询用户分组失败")
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.UserGroupInfosFromModels(groups)})
}

// POST /v0/admin/groups — 创建用户分组
func (h *UserHandler) CreateGroup(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.CreateUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "用户分组参数无效")
		return
	}
	group, err := h.svc.CreateGroup(operator.Role, req.Name, req.Ratio)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "user_group.create", "user_group", group.ID, nil, userGroupAuditSummary(group)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.UserGroupInfoFromModel(group))
}

// PUT /v0/admin/groups/:id — 更新用户分组
func (h *UserHandler) UpdateGroup(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "用户分组参数无效")
		return
	}
	before, err := h.svc.GetGroupByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.UpdateGroup(operator.Role, id, req.Name, req.Ratio); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetGroupByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "user_group.update", "user_group", id, userGroupAuditSummary(before), userGroupAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.UserGroupInfoFromModel(after))
}

// DELETE /v0/admin/groups/:id — 删除未使用的用户分组
func (h *UserHandler) DeleteGroup(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetGroupByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.DeleteGroup(operator.Role, id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "user_group.delete", "user_group", id, userGroupAuditSummary(before), nil); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "用户分组已删除")
}

// POST /v0/admin/payment/adjustments — 支付相关人工补账或扣回
func (h *UserHandler) CreatePaymentManualAdjustment(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.PaymentManualAdjustmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "人工修正参数无效")
		return
	}
	result, err := h.svc.ApplyPaymentManualAdjustment(operator.ID, operator.Role, service.PaymentManualAdjustmentInput{
		UserID:         req.UserID,
		OrderNo:        req.OrderNo,
		Amount:         req.Amount,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		IP:             c.ClientIP(),
		UserAgent:      c.GetHeader("User-Agent"),
	}, c.GetString("request_id"))
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.Success(c, result)
}

// POST /v0/admin/payment/refunds — 支付订单人工退款并扣回额度
func (h *UserHandler) CreatePaymentManualRefund(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.PaymentManualRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "人工退款参数无效")
		return
	}
	result, err := h.svc.ApplyPaymentManualRefund(operator.ID, operator.Role, service.PaymentManualRefundInput{
		OrderNo:        req.OrderNo,
		RefundQuota:    req.RefundQuota,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		IP:             c.ClientIP(),
		UserAgent:      c.GetHeader("User-Agent"),
	}, c.GetString("request_id"))
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.Success(c, result)
}

// POST /v0/admin/payment/refund-requests — 向支付 provider 发起退款请求
func (h *UserHandler) CreatePaymentProviderRefundRequest(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.PaymentProviderRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "退款请求参数无效")
		return
	}
	result, err := h.svc.CreatePaymentProviderRefundRequest(operator.ID, operator.Role, service.PaymentProviderRefundRequestInput{
		OrderNo:        req.OrderNo,
		RefundAmount:   req.RefundAmount,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		IP:             c.ClientIP(),
		UserAgent:      c.GetHeader("User-Agent"),
	}, c.GetString("request_id"))
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.Success(c, result)
}

// GET /v0/admin/redem — 充值码列表
func (h *UserHandler) ListRedemCodes(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.RedemCodeListRequest
	_ = c.ShouldBindQuery(&req)
	codes, total, err := h.svc.ListRedemCodes(operator.Role, req.Page, req.PageSize, req.Status, req.Keyword, req.BatchNo)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.RedemCodeInfosFromModels(codes)})
}

// POST /v0/admin/redem — 生成或导入充值码
func (h *UserHandler) CreateRedemCodes(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.CreateRedemCodesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "充值码参数无效")
		return
	}
	codes, err := h.svc.CreateRedemCodes(operator.Role, req.Quota, req.Count, req.Codes, req.BatchNo, req.Note, req.ExpiredAt)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	for i := range codes {
		if err := h.recordAdminAudit(c, operator, "redem_code.create", "redem_code", codes[i].ID, nil, redemCodeAuditSummary(&codes[i])); err != nil {
			common.FailWithStatus(c, 500, "写入审计日志失败")
			return
		}
	}
	common.Success(c, dto.RedemCodeInfosFromModels(codes))
}

// PATCH /v0/admin/redem/:id/disable — 作废未使用充值码
func (h *UserHandler) DisableRedemCode(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetRedemCodeAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.DisableRedemCode(operator.Role, id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetRedemCodeAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "redem_code.disable", "redem_code", id, redemCodeAuditSummary(before), redemCodeAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "充值码已作废")
}

// GET /v0/admin/payment/products — 支付商品列表
func (h *UserHandler) ListPaymentProductsAdmin(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.PaymentProductListRequest
	_ = c.ShouldBindQuery(&req)
	products, total, err := h.svc.ListPaymentProductsAdmin(operator.Role, req.Page, req.PageSize, req.Keyword, req.Enabled)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.PaymentProductAdminInfosFromModels(products)})
}

// GET /v0/admin/model-prices — 系统模型价格列表
func (h *UserHandler) ListModelPricesAdmin(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.ModelPriceListRequest
	_ = c.ShouldBindQuery(&req)
	prices, total, err := h.svc.ListModelPricesAdmin(operator.Role, req.Page, req.PageSize, req.Keyword, req.Enabled)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.ModelPriceAdminInfosFromModels(prices)})
}

// GET /v0/admin/channel-model-prices — 通道模型价格覆盖列表
func (h *UserHandler) ListChannelModelPricesAdmin(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.ChannelModelPriceListRequest
	_ = c.ShouldBindQuery(&req)
	prices, total, err := h.svc.ListChannelModelPricesAdmin(operator.Role, req.Page, req.PageSize, req.Keyword, req.ChannelID, req.Enabled, req.UserEnabled)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.ChannelModelPriceAdminInfosFromModels(prices)})
}

// GET /v0/admin/audit — 管理审计日志列表
func (h *UserHandler) ListAdminAuditLogs(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.AdminAuditListRequest
	_ = c.ShouldBindQuery(&req)
	logs, total, err := h.svc.ListAdminAuditLogs(operator.Role, req.Page, req.PageSize, req.Action, req.ResourceType, req.ResourceID, req.ActorUserID, req.Result, req.ErrorCode, req.StartTime, req.EndTime)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.AdminAuditLogInfosFromModels(logs)})
}

// POST /v0/admin/payment/products — 创建支付商品
func (h *UserHandler) CreatePaymentProduct(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.UpsertPaymentProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "支付商品参数无效")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	product, err := h.svc.CreatePaymentProduct(operator.Role, paymentProductFromRequest(req, enabled))
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "payment_product.create", "payment_product", product.ID, nil, paymentProductAuditSummary(product)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.PaymentProductAdminInfoFromModel(product))
}

// POST /v0/admin/model-prices — 创建系统模型价格
func (h *UserHandler) CreateModelPrice(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.UpsertModelPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "模型价格参数无效")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	price, err := h.svc.CreateModelPrice(operator.Role, modelPriceFromRequest(req, enabled))
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAuditString(c, operator, "model_price.create", "model_price", price.Model, nil, modelPriceAuditSummary(price)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.ModelPriceAdminInfoFromModel(price))
}

// POST /v0/admin/channel-model-prices — 创建通道模型价格覆盖
func (h *UserHandler) CreateChannelModelPrice(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.UpsertChannelModelPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "通道模型价格参数无效")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	userEnabled := true
	if req.UserEnabled != nil {
		userEnabled = *req.UserEnabled
	}
	price, err := h.svc.CreateChannelModelPrice(operator.Role, channelModelPriceFromRequest(req, enabled, userEnabled))
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAuditString(c, operator, "channel_model_price.create", "channel_model_price", channelModelPriceResourceID(price), nil, channelModelPriceAuditSummary(price)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.ChannelModelPriceAdminInfoFromModel(price))
}

// PUT /v0/admin/payment/products/:id — 更新支付商品
func (h *UserHandler) UpdatePaymentProduct(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpsertPaymentProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "支付商品参数无效")
		return
	}
	before, err := h.svc.GetPaymentProductAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	product, err := h.svc.UpdatePaymentProduct(operator.Role, id, paymentProductFromRequest(req, false), req.Enabled)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, operator, "payment_product.update", "payment_product", product.ID, paymentProductAuditSummary(before), paymentProductAuditSummary(product)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.PaymentProductAdminInfoFromModel(product))
}

// PUT /v0/admin/model-prices/:id — 更新系统模型价格
func (h *UserHandler) UpdateModelPrice(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpsertModelPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "模型价格参数无效")
		return
	}
	before, err := h.svc.GetModelPriceAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	price, err := h.svc.UpdateModelPrice(operator.Role, id, modelPriceFromRequest(req, false), req.Enabled)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAuditString(c, operator, "model_price.update", "model_price", price.Model, modelPriceAuditSummary(before), modelPriceAuditSummary(price)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.ModelPriceAdminInfoFromModel(price))
}

// PUT /v0/admin/channel-model-prices/:id — 更新通道模型价格覆盖
func (h *UserHandler) UpdateChannelModelPrice(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpsertChannelModelPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "通道模型价格参数无效")
		return
	}
	before, err := h.svc.GetChannelModelPriceAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	price, err := h.svc.UpdateChannelModelPrice(operator.Role, id, channelModelPriceFromRequest(req, false, false), req.Enabled, req.UserEnabled)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAuditString(c, operator, "channel_model_price.update", "channel_model_price", channelModelPriceResourceID(price), channelModelPriceAuditSummary(before), channelModelPriceAuditSummary(price)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.ChannelModelPriceAdminInfoFromModel(price))
}

// PATCH /v0/admin/payment/products/:id/disable — 禁用支付商品
func (h *UserHandler) DisablePaymentProduct(c *gin.Context) {
	h.setPaymentProductEnabled(c, false)
}

// PATCH /v0/admin/payment/products/:id/enable — 启用支付商品
func (h *UserHandler) EnablePaymentProduct(c *gin.Context) {
	h.setPaymentProductEnabled(c, true)
}

func (h *UserHandler) setPaymentProductEnabled(c *gin.Context, enabled bool) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetPaymentProductAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.SetPaymentProductEnabled(operator.Role, id, enabled); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetPaymentProductAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	action := "payment_product.disable"
	if enabled {
		action = "payment_product.enable"
	}
	if err := h.recordAdminAudit(c, operator, action, "payment_product", id, paymentProductAuditSummary(before), paymentProductAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	if enabled {
		common.SuccessMsg(c, "支付商品已启用")
		return
	}
	common.SuccessMsg(c, "支付商品已禁用")
}

// PATCH /v0/admin/model-prices/:id/disable — 禁用系统模型价格
func (h *UserHandler) DisableModelPrice(c *gin.Context) {
	h.setModelPriceEnabled(c, false)
}

// PATCH /v0/admin/model-prices/:id/enable — 启用系统模型价格
func (h *UserHandler) EnableModelPrice(c *gin.Context) {
	h.setModelPriceEnabled(c, true)
}

func (h *UserHandler) setModelPriceEnabled(c *gin.Context, enabled bool) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetModelPriceAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.SetModelPriceEnabled(operator.Role, id, enabled); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetModelPriceAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	action := "model_price.disable"
	if enabled {
		action = "model_price.enable"
	}
	if err := h.recordAdminAuditString(c, operator, action, "model_price", after.Model, modelPriceAuditSummary(before), modelPriceAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	if enabled {
		common.SuccessMsg(c, "模型价格已启用")
		return
	}
	common.SuccessMsg(c, "模型价格已禁用")
}

// PATCH /v0/admin/channel-model-prices/:id/disable — 禁用通道模型价格覆盖
func (h *UserHandler) DisableChannelModelPrice(c *gin.Context) {
	h.setChannelModelPriceEnabled(c, false)
}

// PATCH /v0/admin/channel-model-prices/:id/enable — 启用通道模型价格覆盖
func (h *UserHandler) EnableChannelModelPrice(c *gin.Context) {
	h.setChannelModelPriceEnabled(c, true)
}

func (h *UserHandler) setChannelModelPriceEnabled(c *gin.Context, enabled bool) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetChannelModelPriceAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.SetChannelModelPriceEnabled(operator.Role, id, enabled); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetChannelModelPriceAdmin(operator.Role, id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	action := "channel_model_price.disable"
	if enabled {
		action = "channel_model_price.enable"
	}
	if err := h.recordAdminAuditString(c, operator, action, "channel_model_price", channelModelPriceResourceID(after), channelModelPriceAuditSummary(before), channelModelPriceAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	if enabled {
		common.SuccessMsg(c, "通道模型价格已启用")
		return
	}
	common.SuccessMsg(c, "通道模型价格已禁用")
}

// GET /v0/user/models — 当前用户可用模型列表
func (h *UserHandler) Models(c *gin.Context) {
	if _, ok := currentUser(c); !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	models, channelPrices, prices, err := h.svc.ListAvailableModelsWithPrices()
	if err != nil {
		common.FailWithStatus(c, 500, "查询模型失败")
		return
	}
	common.Success(c, dto.UserModelListResult{Models: dto.UserModelInfosFromNamesAndPriceMaps(models, channelPrices, prices)})
}

func paymentProductFromRequest(req dto.UpsertPaymentProductRequest, enabled bool) model.PaymentProduct {
	return model.PaymentProduct{
		ProductID:          req.ProductID,
		Name:               req.Name,
		Amount:             req.Amount,
		Currency:           req.Currency,
		Quota:              req.Quota,
		BonusQuota:         req.BonusQuota,
		Enabled:            enabled,
		ProviderConfigJSON: req.ProviderConfigJSON,
	}
}

func modelPriceFromRequest(req dto.UpsertModelPriceRequest, enabled bool) model.ModelPrice {
	return model.ModelPrice{
		Model:           req.Model,
		PriceMode:       req.PriceMode,
		PriceExpression: req.PriceExpression,
		VariablesJSON:   req.VariablesJSON,
		UnitTokens:      req.UnitTokens,
		Enabled:         enabled,
	}
}

func channelModelPriceFromRequest(req dto.UpsertChannelModelPriceRequest, enabled, userEnabled bool) model.ChannelModelPrice {
	return model.ChannelModelPrice{
		ChannelID:       req.ChannelID,
		Model:           req.Model,
		Enabled:         enabled,
		UserEnabled:     userEnabled,
		PriceMode:       req.PriceMode,
		OverrideMode:    req.OverrideMode,
		PriceExpression: req.PriceExpression,
		VariablesJSON:   req.VariablesJSON,
		UnitTokens:      req.UnitTokens,
	}
}

func (h *UserHandler) recordAdminAudit(c *gin.Context, operator *model.User, action, resourceType string, resourceID uint, before, after interface{}) error {
	return h.recordAdminAuditResult(c, operator, action, resourceType, resourceID, before, after, "success", "")
}

func (h *UserHandler) recordAdminAuditResult(c *gin.Context, operator *model.User, action, resourceType string, resourceID uint, before, after interface{}, result, errorCode string) error {
	return h.recordAdminAuditStringResult(c, operator, action, resourceType, strconv.FormatUint(uint64(resourceID), 10), before, after, result, errorCode)
}

func (h *UserHandler) recordAdminAuditString(c *gin.Context, operator *model.User, action, resourceType, resourceID string, before, after interface{}) error {
	return h.recordAdminAuditStringResult(c, operator, action, resourceType, resourceID, before, after, "success", "")
}

func (h *UserHandler) recordAdminAuditStringResult(c *gin.Context, operator *model.User, action, resourceType, resourceID string, before, after interface{}, result, errorCode string) error {
	return h.svc.RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:     c.GetString("request_id"),
		ActorUserID:   operator.ID,
		ActorRole:     operator.Role,
		Action:        action,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		BeforeSummary: auditSummary(before),
		AfterSummary:  auditSummary(after),
		Result:        result,
		ErrorCode:     errorCode,
		IP:            c.ClientIP(),
		UserAgent:     c.GetHeader("User-Agent"),
	})
}

func auditSummary(value interface{}) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func paymentProductAuditSummary(product *model.PaymentProduct) map[string]interface{} {
	if product == nil {
		return nil
	}
	return map[string]interface{}{
		"id":          product.ID,
		"product_id":  product.ProductID,
		"name":        product.Name,
		"amount":      product.Amount,
		"currency":    product.Currency,
		"quota":       product.Quota,
		"bonus_quota": product.BonusQuota,
		"enabled":     product.Enabled,
	}
}

func modelPriceAuditSummary(price *model.ModelPrice) map[string]interface{} {
	if price == nil {
		return nil
	}
	return map[string]interface{}{
		"id":               price.ID,
		"model":            price.Model,
		"price_mode":       price.PriceMode,
		"price_expression": price.PriceExpression,
		"unit_tokens":      price.UnitTokens,
		"rule_version":     price.RuleVersion,
		"enabled":          price.Enabled,
	}
}

func channelModelPriceResourceID(price *model.ChannelModelPrice) string {
	if price == nil {
		return ""
	}
	return strconv.FormatUint(uint64(price.ChannelID), 10) + ":" + price.Model
}

func channelModelPriceAuditSummary(price *model.ChannelModelPrice) map[string]interface{} {
	if price == nil {
		return nil
	}
	return map[string]interface{}{
		"id":               price.ID,
		"channel_id":       price.ChannelID,
		"model":            price.Model,
		"enabled":          price.Enabled,
		"user_enabled":     price.UserEnabled,
		"price_mode":       price.PriceMode,
		"override_mode":    price.OverrideMode,
		"price_expression": price.PriceExpression,
		"unit_tokens":      price.UnitTokens,
		"rule_version":     price.RuleVersion,
	}
}

func paymentOrderAuditSummary(order *model.PaymentOrder) map[string]interface{} {
	if order == nil {
		return nil
	}
	return map[string]interface{}{
		"id":         order.ID,
		"order_no":   order.OrderNo,
		"user_id":    order.UserID,
		"product_id": order.ProductID,
		"provider":   order.Provider,
		"amount":     order.Amount,
		"currency":   order.Currency,
		"quota":      order.Quota,
		"status":     order.Status,
		"expires_at": order.ExpiredAt,
		"created_at": order.CreatedAt,
	}
}

func userAuditSummary(user *model.User) map[string]interface{} {
	if user == nil {
		return nil
	}
	brief := dto.UserBriefFromModel(user)
	return map[string]interface{}{
		"id":           brief.ID,
		"username":     brief.Username,
		"display_name": brief.DisplayName,
		"email":        brief.Email,
		"role":         brief.Role,
		"quota":        brief.Quota,
		"status":       brief.Status,
		"group_id":     brief.GroupID,
	}
}

func userGroupAuditSummary(group *model.Group) map[string]interface{} {
	if group == nil {
		return nil
	}
	return map[string]interface{}{
		"id":    group.ID,
		"name":  group.Name,
		"ratio": group.Ratio,
	}
}

func userQuotaAuditSummary(user *model.User, reason string) map[string]interface{} {
	if user == nil {
		return nil
	}
	return map[string]interface{}{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"quota":        user.Quota,
		"reason":       reason,
	}
}

func redemCodeAuditSummary(code *model.RedemCode) map[string]interface{} {
	if code == nil {
		return nil
	}
	return map[string]interface{}{
		"id":         code.ID,
		"code":       common.RedactSecret(code.Code),
		"quota":      code.Quota,
		"status":     code.Status,
		"batch_no":   code.BatchNo,
		"note":       code.Note,
		"expired_at": code.ExpiredAt,
		"used_by":    code.UsedBy,
	}
}

// GET /v0/user/payment/products — 充值商品列表
func (h *UserHandler) PaymentProducts(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	products, err := h.svc.ListPaymentProducts(user.ID)
	if err != nil {
		common.FailWithStatus(c, 500, "查询充值商品失败")
		return
	}
	common.Success(c, dto.PaymentProductInfosFromModels(products))
}

// POST /v0/user/payment/orders — 创建支付订单
func (h *UserHandler) CreatePaymentOrder(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.CreatePaymentOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "支付订单参数无效")
		return
	}
	order, err := h.svc.CreatePaymentOrder(user.ID, req.Provider, req.ProductID, req.PayType, req.ReturnURL)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAudit(c, user, "payment_order.create", "payment_order", order.ID, nil, paymentOrderAuditSummary(order)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.PaymentOrderInfoFromModel(order))
}

// GET /v0/user/payment/orders — 当前用户支付订单列表
func (h *UserHandler) PaymentOrders(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.UserListRequest
	_ = c.ShouldBindQuery(&req)
	orders, total, err := h.svc.ListPaymentOrders(user.ID, req.Page, req.PageSize)
	if err != nil {
		common.FailWithStatus(c, 500, "查询支付订单失败")
		return
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: dto.PaymentOrderInfosFromModels(orders)})
}

// GET /v0/user/payment/orders/:order_no — 当前用户支付订单详情
func (h *UserHandler) PaymentOrder(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	order, err := h.svc.GetPaymentOrder(user.ID, c.Param("order_no"))
	if err != nil {
		common.FailWithStatus(c, 404, "支付订单不存在")
		return
	}
	common.Success(c, dto.PaymentOrderInfoFromModel(order))
}

// POST /v0/payment/stripe/webhook — Stripe 异步通知
func (h *UserHandler) StripeWebhook(c *gin.Context) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	if err := h.svc.ProcessStripeWebhook(raw, c.GetHeader("Stripe-Signature"), c.GetString("request_id")); err != nil {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	c.String(http.StatusOK, "success")
}

// POST /v0/payment/epay/notify — 易支付异步通知
func (h *UserHandler) EpayNotify(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	values := make(map[string]string, len(c.Request.PostForm))
	for key, raw := range c.Request.PostForm {
		if len(raw) > 0 {
			values[key] = raw[0]
		}
	}
	if err := h.svc.ProcessEpayNotify(values, c.GetString("request_id")); err != nil {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	c.String(http.StatusOK, "success")
}

// GET /v0/payment/epay/return — 易支付同步返回只读状态
func (h *UserHandler) EpayReturn(c *gin.Context) {
	orderNo := c.Query("out_trade_no")
	if orderNo == "" {
		orderNo = c.Query("order_no")
	}
	order, err := h.svc.GetEpayReturnOrder(orderNo)
	if err != nil {
		common.FailWithStatus(c, http.StatusNotFound, "支付订单不存在")
		return
	}
	common.Success(c, dto.PaymentOrderInfoFromModel(order))
}

// GET /v0/user/self — 获取个人信息
func (h *UserHandler) Self(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	fresh, err := h.svc.GetByID(user.ID)
	if err != nil {
		common.FailWithStatus(c, 404, "用户不存在")
		return
	}
	common.Success(c, dto.UserBriefFromModel(fresh))
}

// PUT /v0/user/self — 修改个人信息
func (h *UserHandler) UpdateSelf(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.UpdateSelfRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "个人信息参数无效")
		return
	}
	if err := h.svc.UpdateSelf(user.ID, req.DisplayName, req.Email); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "个人信息已更新")
}

// POST /v0/user/redem — 使用充值码给当前账户增加额度
func (h *UserHandler) RedeemCode(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.RedeemCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "充值码参数无效")
		return
	}
	redeemedQuota, quota, err := h.svc.RedeemCode(user.ID, req.Code, c.GetString("request_id"))
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	code, err := h.svc.GetRedemCodeByCode(req.Code)
	if err != nil {
		common.FailWithStatus(c, 500, "查询充值码失败")
		return
	}
	if err := h.recordAdminAudit(c, user, "redem_code.redeem", "redem_code", code.ID, nil, redemCodeRedeemAuditSummary(code, redeemedQuota, quota)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.RedeemCodeResult{RedeemedQuota: redeemedQuota, Quota: quota})
}

func redemCodeRedeemAuditSummary(code *model.RedemCode, redeemedQuota, balanceAfter int64) map[string]interface{} {
	summary := redemCodeAuditSummary(code)
	if summary == nil {
		return nil
	}
	summary["redeemed_quota"] = redeemedQuota
	summary["balance_after"] = balanceAfter
	return summary
}

func parseUintParam(c *gin.Context, name string) (uint, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		common.FailWithStatus(c, 400, "ID 参数无效")
		return 0, false
	}
	return uint(id), true
}

func pageValues(page, pageSize int) (int, int) {
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
