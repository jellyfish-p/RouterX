package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
	"routerx/internal/service"
)

type TokenHandler struct {
	svc      *service.TokenService
	auditSvc *service.UserService
}

func NewTokenHandler(svc *service.TokenService) *TokenHandler {
	return &TokenHandler{svc: svc, auditSvc: service.NewUserService()}
}

func (h *TokenHandler) List(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.TokenListRequest
	_ = c.ShouldBindQuery(&req)
	tokens, total, err := h.svc.List(user.ID, req.Page, req.PageSize)
	if err != nil {
		common.FailWithStatus(c, 500, "查询 API Key 失败")
		return
	}
	data := make([]dto.TokenResponse, 0, len(tokens))
	for _, token := range tokens {
		data = append(data, dto.TokenFromModel(token))
	}
	page, pageSize := pageValues(req.Page, req.PageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: data})
}

func (h *TokenHandler) Create(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.CreateTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "创建 API Key 参数无效")
		return
	}
	token, err := h.svc.Create(user.ID, req.Name, req.RemainQuota, req.Unlimited, req.ExpiredAt)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	plainKey := token.Key
	token.Key = ""
	if err := h.recordAPIKeyAudit(c, user, "api_key.created", token.ID, nil, tokenAuditSummary(token)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.CreateTokenResponse{
		TokenResponse: dto.TokenFromModel(*token),
		Key:           plainKey,
	})
}

func (h *TokenHandler) Update(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "编辑 API Key 参数无效")
		return
	}
	before, err := h.svc.GetByIDForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.RemainQuota != nil || req.Unlimited != nil {
		_ = h.recordAPIKeyAuditResult(c, user, "api_key.quota_limit_denied", id, tokenAuditSummary(before), tokenQuotaDeniedAuditSummary(before, req), "denied", "api_key_quota_edit_forbidden")
		common.FailWithStatus(c, 403, "API Key 额度不能通过编辑接口修改")
		return
	}
	if req.ExpiredAt != nil {
		if *req.ExpiredAt <= 0 {
			updates["expired_at"] = nil
		} else {
			t := time.Unix(*req.ExpiredAt, 0)
			updates["expired_at"] = &t
		}
	}
	if err := h.svc.Update(id, updates); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetByIDForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 500, "查询 API Key 失败")
		return
	}
	action := "api_key.updated"
	if status, ok := updates["status"].(int); ok && status == common.TokenStatusDisabled {
		action = "api_key.disabled"
	}
	if err := h.recordAPIKeyAudit(c, user, action, id, tokenAuditSummary(before), tokenAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "API Key 已更新")
}

func (h *TokenHandler) Delete(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetByIDForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	if err := h.svc.Delete(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAPIKeyAudit(c, user, "api_key.deleted", id, tokenAuditSummary(before), nil); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "API Key 已删除")
}

func (h *TokenHandler) recordAPIKeyAudit(c *gin.Context, operator *model.User, action string, id uint, before, after interface{}) error {
	return h.recordAPIKeyAuditResult(c, operator, action, id, before, after, "success", "")
}

func (h *TokenHandler) recordAPIKeyAuditResult(c *gin.Context, operator *model.User, action string, id uint, before, after interface{}, result, errorCode string) error {
	return h.auditSvc.RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:     c.GetString("request_id"),
		ActorUserID:   operator.ID,
		ActorRole:     operator.Role,
		Action:        action,
		ResourceType:  "api_key",
		ResourceID:    strconv.FormatUint(uint64(id), 10),
		BeforeSummary: auditSummary(before),
		AfterSummary:  auditSummary(after),
		Result:        result,
		ErrorCode:     errorCode,
		IP:            c.ClientIP(),
		UserAgent:     c.GetHeader("User-Agent"),
	})
}

// tokenAuditSummary 使用公开 DTO 字段白名单，避免把哈希或一次性明文 Key 写入审计。
func tokenAuditSummary(token *model.Token) map[string]interface{} {
	if token == nil {
		return nil
	}
	info := dto.TokenFromModel(*token)
	return map[string]interface{}{
		"id":           info.ID,
		"user_id":      info.UserID,
		"name":         info.Name,
		"status":       info.Status,
		"expired_at":   info.ExpiredAt,
		"remain_quota": info.RemainQuota,
		"unlimited":    info.Unlimited,
		"created_at":   info.CreatedAt,
		"updated_at":   info.UpdatedAt,
	}
}

func tokenQuotaDeniedAuditSummary(token *model.Token, req dto.UpdateTokenRequest) map[string]interface{} {
	summary := map[string]interface{}{
		"token":  tokenAuditSummary(token),
		"reason": "api_key_quota_edit_forbidden",
	}
	if req.RemainQuota != nil {
		summary["requested_remain_quota"] = *req.RemainQuota
	}
	if req.Unlimited != nil {
		summary["requested_unlimited"] = *req.Unlimited
	}
	return summary
}
