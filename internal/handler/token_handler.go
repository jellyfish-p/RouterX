package handler

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strconv"
	"strings"
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
	alertSvc *service.AlertService
}

func NewTokenHandler(svc *service.TokenService) *TokenHandler {
	return &TokenHandler{svc: svc, auditSvc: service.NewUserService(), alertSvc: service.NewAlertService()}
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
	token, err := h.svc.Create(user.ID, req.Name, req.RemainQuota, req.Unlimited, req.ExpiredAt, tokenMetadataFromRequest(req.Metadata))
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
	if req.Metadata != nil {
		metadataJSON, err := tokenMetadataJSONFromRequest(req.Metadata)
		if err != nil {
			common.FailWithStatus(c, 400, err.Error())
			return
		}
		updates["metadata_json"] = metadataJSON
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

func (h *TokenHandler) Rotate(c *gin.Context) {
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
	oldAfter, newToken, err := h.svc.RotateForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	plainKey := newToken.Key
	newToken.Key = ""
	after := map[string]interface{}{
		"rotated":     tokenAuditSummary(oldAfter),
		"replacement": tokenAuditSummary(newToken),
	}
	if err := h.recordAPIKeyAudit(c, user, "api_key.rotated", id, tokenAuditSummary(before), after); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.CreateTokenResponse{
		TokenResponse: dto.TokenFromModel(*newToken),
		Key:           plainKey,
	})
}

func (h *TokenHandler) Disable(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.ReportTokenLeakRequest
	_ = c.ShouldBindJSON(&req)
	before, err := h.svc.GetByIDForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	after, err := h.svc.DisableForUser(id, user.ID, req.Reason)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAPIKeyAudit(c, user, "api_key.disabled", id, tokenAuditSummary(before), tokenAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.TokenFromModel(*after))
}

func (h *TokenHandler) ReportLeak(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.ReportTokenLeakRequest
	_ = c.ShouldBindJSON(&req)
	before, err := h.svc.GetByIDForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	after, err := h.svc.ReportLeakForUser(id, user.ID, req.Reason)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	auditAfter := map[string]interface{}{
		"token":                   tokenAuditSummary(after),
		"replacement_recommended": true,
	}
	if err := h.recordAPIKeyAudit(c, user, "api_key.leak_reported", id, tokenAuditSummary(before), auditAfter); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	// 泄露上报进入告警收件箱，便于管理员无需翻审计日志也能主动处置。
	if _, err := h.alertSvc.CreateAPIKeyLeakAlert(after, user.ID); err != nil {
		common.FailWithStatus(c, 500, "创建 API Key 告警失败")
		return
	}
	common.Success(c, dto.ReportTokenLeakResponse{
		ID:                     after.ID,
		Status:                 after.Status,
		RevokedReason:          after.RevokedReason,
		ReplacementRecommended: true,
	})
}

func (h *TokenHandler) UpdateScope(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateTokenScopeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "API Key scope 参数无效")
		return
	}
	before, err := h.svc.GetByIDForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	after, err := h.svc.UpdateScopeForUser(id, user.ID, service.TokenScope{
		AllowModels:    req.AllowModels,
		APITypes:       req.APITypes,
		ChannelGroups:  req.ChannelGroups,
		EntryProtocols: req.EntryProtocols,
		IPCIDRs:        req.IPCIDRs,
		Methods:        req.Methods,
		DailyQuota:     req.DailyQuota,
		MonthlyQuota:   req.MonthlyQuota,
		MaxConcurrency: req.MaxConcurrency,
		RPM:            req.RPM,
		TPM:            req.TPM,
	})
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAPIKeyAudit(c, user, "api_key.scope_updated", id, tokenAuditSummary(before), tokenAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.TokenFromModel(*after))
}

func (h *TokenHandler) Usage(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	stats, err := h.svc.GetUsageForUser(id, user.ID)
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	common.Success(c, tokenUsageResponse(stats))
}

func (h *TokenHandler) LeakWindow(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	stats, err := h.svc.GetLeakWindowForUser(id, user.ID, queryInt(c, "window_hours", 24))
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	common.Success(c, tokenLeakWindowResponse(stats))
}

func (h *TokenHandler) AdminList(c *gin.Context) {
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	tokens, total, err := h.svc.ListByFilter(adminTokenFilterFromQuery(c, page, pageSize))
	if err != nil {
		common.FailWithStatus(c, 500, "查询 API Key 失败")
		return
	}
	data := make([]dto.TokenResponse, 0, len(tokens))
	for _, token := range tokens {
		data = append(data, dto.TokenFromModel(token))
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: data})
}

func (h *TokenHandler) AdminExport(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	limit := normalizeTokenExportLimit(queryInt(c, "limit", 0))
	filter := adminTokenFilterFromQuery(c, 1, limit)
	tokens, _, err := h.svc.ListByFilter(filter)
	if err != nil {
		common.FailWithStatus(c, 500, "导出 API Key 失败")
		return
	}
	csvBytes, err := buildTokenExportCSV(tokens)
	if err != nil {
		common.FailWithStatus(c, 500, "生成 API Key 导出文件失败")
		return
	}
	if err := h.recordAPIKeyAuditResource(c, operator, "api_key.export", "export", nil, tokenExportAuditSummary(filter, limit, len(tokens)), "success", ""); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="routerx-api-keys.csv"`)
	c.Data(200, "text/csv; charset=utf-8", csvBytes)
}

func (h *TokenHandler) AdminRisk(c *gin.Context) {
	userID := queryUintPtr(c, "user_id")
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	items, total, err := h.svc.ListRisk(service.TokenRiskFilter{
		UserID:        userID,
		WindowHours:   queryInt(c, "window_hours", 24),
		MinErrorCount: queryInt64(c, "min_error_count", 3),
		LowQuotaBelow: queryInt64(c, "low_quota_below", 100),
		Page:          page,
		PageSize:      pageSize,
	})
	if err != nil {
		common.FailWithStatus(c, 500, "查询 API Key 风险视图失败")
		return
	}
	data := make([]dto.TokenRiskResponse, 0, len(items))
	for _, item := range items {
		data = append(data, tokenRiskResponse(item))
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: data})
}

func (h *TokenHandler) AdminLeakWindow(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	stats, err := h.svc.GetLeakWindow(id, queryInt(c, "window_hours", 24))
	if err != nil {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	common.Success(c, tokenLeakWindowResponse(stats))
}

func (h *TokenHandler) BatchDisable(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.BatchDisableTokensRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "批量禁用 API Key 参数无效")
		return
	}
	result, matched, err := h.svc.BatchDisable(service.BatchDisableTokensInput{
		TokenIDs: req.TokenIDs,
		UserID:   req.UserID,
		Reason:   req.Reason,
	})
	if err != nil {
		if errors.Is(err, service.ErrBatchDisableNoFilter) {
			after := tokenBatchDeniedAuditSummary(req.TokenIDs, req.UserID, req.Reason, err)
			if auditErr := h.recordAPIKeyAuditResource(c, operator, "api_key.batch_disable_denied", "batch", nil, after, "denied", "api_key_batch_filter_required"); auditErr != nil {
				common.FailWithStatus(c, 500, "写入审计日志失败")
				return
			}
		}
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	resp := dto.BatchDisableTokensResponse{
		MatchedCount:  result.MatchedCount,
		DisabledCount: result.DisabledCount,
		Reason:        result.Reason,
		TokenIDs:      result.TokenIDs,
	}
	after := tokenBatchDisableAuditSummary(req, resp, matched)
	if err := h.recordAPIKeyAuditResource(c, operator, "api_key.batch_disabled", "batch", nil, after, "success", ""); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, resp)
}

func (h *TokenHandler) BatchExpire(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.BatchExpireTokensRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "批量过期 API Key 参数无效")
		return
	}
	result, matched, err := h.svc.BatchExpire(service.BatchExpireTokensInput{
		TokenIDs: req.TokenIDs,
		UserID:   req.UserID,
		Reason:   req.Reason,
	})
	if err != nil {
		if errors.Is(err, service.ErrBatchExpireNoFilter) {
			after := tokenBatchDeniedAuditSummary(req.TokenIDs, req.UserID, req.Reason, err)
			if auditErr := h.recordAPIKeyAuditResource(c, operator, "api_key.batch_expire_denied", "batch", nil, after, "denied", "api_key_batch_filter_required"); auditErr != nil {
				common.FailWithStatus(c, 500, "写入审计日志失败")
				return
			}
		}
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	resp := dto.BatchExpireTokensResponse{
		MatchedCount: result.MatchedCount,
		ExpiredCount: result.ExpiredCount,
		Reason:       result.Reason,
		ExpiredAt:    result.ExpiredAt,
		TokenIDs:     result.TokenIDs,
	}
	after := tokenBatchExpireAuditSummary(req, resp, matched)
	if err := h.recordAPIKeyAuditResource(c, operator, "api_key.batch_expired", "batch", nil, after, "success", ""); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, resp)
}

func (h *TokenHandler) recordAPIKeyAudit(c *gin.Context, operator *model.User, action string, id uint, before, after interface{}) error {
	return h.recordAPIKeyAuditResult(c, operator, action, id, before, after, "success", "")
}

func (h *TokenHandler) recordAPIKeyAuditResult(c *gin.Context, operator *model.User, action string, id uint, before, after interface{}, result, errorCode string) error {
	return h.recordAPIKeyAuditResource(c, operator, action, strconv.FormatUint(uint64(id), 10), before, after, result, errorCode)
}

func (h *TokenHandler) recordAPIKeyAuditResource(c *gin.Context, operator *model.User, action string, resourceID string, before, after interface{}, result, errorCode string) error {
	return h.auditSvc.RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:     c.GetString("request_id"),
		ActorUserID:   operator.ID,
		ActorRole:     operator.Role,
		Action:        action,
		ResourceType:  "api_key",
		ResourceID:    resourceID,
		BeforeSummary: auditSummary(before),
		AfterSummary:  auditSummary(after),
		Result:        result,
		ErrorCode:     errorCode,
		IP:            c.ClientIP(),
		UserAgent:     c.GetHeader("User-Agent"),
	})
}

func tokenUsageResponse(stats service.TokenUsageStats) dto.TokenUsageResponse {
	return dto.TokenUsageResponse{
		TokenID:      stats.TokenID,
		CallCount:    stats.CallCount,
		SuccessCount: stats.SuccessCount,
		ErrorCount:   stats.ErrorCount,
		TotalQuota:   stats.TotalQuota,
		TotalTokens:  stats.TotalTokens,
		LastUsedAt:   stats.LastUsedAt,
		LastModel:    stats.LastModel,
		LastStatus:   stats.LastStatus,
		LastErrorMsg: stats.LastErrorMsg,
	}
}

func tokenRiskResponse(item service.TokenRiskItem) dto.TokenRiskResponse {
	return dto.TokenRiskResponse{
		Token:               dto.TokenFromModel(item.Token),
		CallCount:           item.CallCount,
		SuccessCount:        item.SuccessCount,
		ErrorCount:          item.ErrorCount,
		TotalQuota:          item.TotalQuota,
		TotalTokens:         item.TotalTokens,
		LastUsedAt:          item.LastUsedAt,
		LastModel:           item.LastModel,
		LastStatus:          item.LastStatus,
		LastErrorCode:       item.LastErrorCode,
		RiskLevel:           item.RiskLevel,
		RiskReasons:         item.RiskReasons,
		RecommendedAction:   item.RecommendedAction,
		RotationRecommended: item.RotationRecommended,
		RotationReason:      item.RotationReason,
		WindowStart:         item.WindowStart,
	}
}

func tokenLeakWindowResponse(stats service.TokenLeakWindowStats) dto.TokenLeakWindowResponse {
	return dto.TokenLeakWindowResponse{
		Token:             dto.TokenFromModel(stats.Token),
		TokenID:           stats.Token.ID,
		WindowHours:       stats.WindowHours,
		WindowStart:       stats.WindowStart,
		WindowEnd:         stats.WindowEnd,
		CallCount:         stats.CallCount,
		SuccessCount:      stats.SuccessCount,
		ErrorCount:        stats.ErrorCount,
		TotalQuota:        stats.TotalQuota,
		TotalTokens:       stats.TotalTokens,
		FirstUsedAt:       stats.FirstUsedAt,
		LastUsedAt:        stats.LastUsedAt,
		Models:            tokenLeakWindowCounters(stats.Models),
		ErrorCodes:        tokenLeakWindowCounters(stats.ErrorCodes),
		SourceIPHashes:    tokenLeakWindowCounters(stats.SourceIPHashes),
		LastUsedIPHash:    stats.LastUsedIPHash,
		LastUserAgentHash: stats.LastUserAgentHash,
	}
}

func tokenLeakWindowCounters(items []service.TokenLeakWindowCounter) []dto.TokenLeakWindowCounterResponse {
	out := make([]dto.TokenLeakWindowCounterResponse, 0, len(items))
	for _, item := range items {
		out = append(out, dto.TokenLeakWindowCounterResponse{
			Value:      item.Value,
			Count:      item.Count,
			LastSeenAt: item.LastSeenAt,
		})
	}
	return out
}

func tokenMetadataFromRequest(req *dto.TokenMetadataRequest) service.TokenMetadata {
	if req == nil {
		return service.TokenMetadata{}
	}
	return service.TokenMetadata{
		Environment:   req.Environment,
		Team:          req.Team,
		App:           req.App,
		Tags:          req.Tags,
		ExternalID:    req.ExternalID,
		Note:          req.Note,
		PrincipalType: req.PrincipalType,
		PrincipalID:   req.PrincipalID,
		PrincipalName: req.PrincipalName,
	}
}

func tokenMetadataJSONFromRequest(req *dto.TokenMetadataRequest) (model.JSONValue, error) {
	metadata, err := service.NormalizeTokenMetadata(tokenMetadataFromRequest(req))
	if err != nil {
		return nil, err
	}
	if metadata.Environment == "" && metadata.Team == "" && metadata.App == "" && metadata.ExternalID == "" &&
		metadata.Note == "" && metadata.PrincipalType == "" && metadata.PrincipalID == "" &&
		metadata.PrincipalName == "" && len(metadata.Tags) == 0 {
		return nil, nil
	}
	return model.NewJSONValue(metadata), nil
}

func adminTokenFilterFromQuery(c *gin.Context, page, pageSize int) service.TokenListFilter {
	return service.TokenListFilter{
		UserID:        queryUintPtr(c, "user_id"),
		Status:        queryIntPtr(c, "status"),
		Environment:   c.Query("environment"),
		Team:          c.Query("team"),
		App:           c.Query("app"),
		Tag:           c.Query("tag"),
		PrincipalType: c.Query("principal_type"),
		PrincipalID:   c.Query("principal_id"),
		Page:          page,
		PageSize:      pageSize,
	}
}

func normalizeTokenExportLimit(limit int) int {
	if limit <= 0 {
		return 1000
	}
	if limit > 10000 {
		return 10000
	}
	return limit
}

func buildTokenExportCSV(tokens []model.Token) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	header := []string{"id", "user_id", "name", "status", "environment", "team", "app", "principal_type", "principal_id", "principal_name", "tags", "external_id", "note", "expired_at", "last_used_at", "last_model", "last_error_code", "created_at"}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, token := range tokens {
		metadata := service.ParseTokenMetadata(token.MetadataJSON)
		if err := writer.Write([]string{
			strconv.FormatUint(uint64(token.ID), 10),
			strconv.FormatUint(uint64(token.UserID), 10),
			token.Name,
			strconv.Itoa(token.Status),
			metadata.Environment,
			metadata.Team,
			metadata.App,
			metadata.PrincipalType,
			metadata.PrincipalID,
			metadata.PrincipalName,
			strings.Join(metadata.Tags, "|"),
			metadata.ExternalID,
			metadata.Note,
			timePtrString(token.ExpiredAt),
			timePtrString(token.LastUsedAt),
			token.LastModel,
			token.LastErrorCode,
			token.CreatedAt.Format(time.RFC3339),
		}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return buf.Bytes(), writer.Error()
}

func tokenExportAuditSummary(filter service.TokenListFilter, limit, exportedCount int) map[string]interface{} {
	return map[string]interface{}{
		"filters": map[string]interface{}{
			"user_id":        filter.UserID,
			"status":         filter.Status,
			"environment":    strings.TrimSpace(filter.Environment),
			"team":           strings.TrimSpace(filter.Team),
			"app":            strings.TrimSpace(filter.App),
			"tag":            strings.TrimSpace(filter.Tag),
			"principal_type": strings.TrimSpace(filter.PrincipalType),
			"principal_id":   strings.TrimSpace(filter.PrincipalID),
		},
		"limit":          limit,
		"exported_count": exportedCount,
	}
}

func timePtrString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format(time.RFC3339)
}

// tokenAuditSummary 使用公开 DTO 字段白名单，避免把哈希或一次性明文 Key 写入审计。
func tokenAuditSummary(token *model.Token) map[string]interface{} {
	if token == nil {
		return nil
	}
	info := dto.TokenFromModel(*token)
	return map[string]interface{}{
		"id":                   info.ID,
		"user_id":              info.UserID,
		"name":                 info.Name,
		"status":               info.Status,
		"expired_at":           info.ExpiredAt,
		"remain_quota":         info.RemainQuota,
		"unlimited":            info.Unlimited,
		"rotated_from_id":      info.RotatedFromID,
		"revoked_reason":       info.RevokedReason,
		"scope":                info.Scope,
		"metadata":             info.Metadata,
		"last_used_at":         info.LastUsedAt,
		"last_used_ip_hash":    info.LastUsedIPHash,
		"last_user_agent_hash": info.LastUserAgentHash,
		"last_model":           info.LastModel,
		"last_error_code":      info.LastErrorCode,
		"created_at":           info.CreatedAt,
		"updated_at":           info.UpdatedAt,
	}
}

func queryInt64(c *gin.Context, key string, fallback int64) int64 {
	value, err := strconv.ParseInt(c.Query(key), 10, 64)
	if err != nil {
		return fallback
	}
	return value
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

func tokenBatchDisableAuditSummary(req dto.BatchDisableTokensRequest, resp dto.BatchDisableTokensResponse, matched []model.Token) map[string]interface{} {
	tokens := make([]map[string]interface{}, 0, len(matched))
	for i := range matched {
		tokens = append(tokens, tokenAuditSummary(&matched[i]))
	}
	return map[string]interface{}{
		"filters": map[string]interface{}{
			"token_ids": req.TokenIDs,
			"user_id":   req.UserID,
		},
		"result": resp,
		"tokens": tokens,
	}
}

func tokenBatchExpireAuditSummary(req dto.BatchExpireTokensRequest, resp dto.BatchExpireTokensResponse, matched []model.Token) map[string]interface{} {
	tokens := make([]map[string]interface{}, 0, len(matched))
	for i := range matched {
		tokens = append(tokens, tokenAuditSummary(&matched[i]))
	}
	return map[string]interface{}{
		"filters": map[string]interface{}{
			"token_ids": req.TokenIDs,
			"user_id":   req.UserID,
		},
		"result": resp,
		"tokens": tokens,
	}
}

func tokenBatchDeniedAuditSummary(tokenIDs []uint, userID *uint, reason string, cause error) map[string]interface{} {
	return map[string]interface{}{
		"filters": map[string]interface{}{
			"token_ids": tokenIDs,
			"user_id":   userID,
		},
		"reason":  reason,
		"message": cause.Error(),
	}
}
