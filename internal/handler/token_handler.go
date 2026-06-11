package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/service"
)

type TokenHandler struct {
	svc *service.TokenService
}

func NewTokenHandler(svc *service.TokenService) *TokenHandler {
	return &TokenHandler{svc: svc}
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
	if !h.ownsToken(user.ID, id) {
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
	if req.RemainQuota != nil {
		updates["remain_quota"] = *req.RemainQuota
	}
	if req.Unlimited != nil {
		updates["unlimited"] = *req.Unlimited
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
	if !h.ownsToken(user.ID, id) {
		common.FailWithStatus(c, 404, "API Key 不存在")
		return
	}
	if err := h.svc.Delete(id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "API Key 已删除")
}

func (h *TokenHandler) ownsToken(userID, tokenID uint) bool {
	_, err := h.svc.GetByIDForUser(tokenID, userID)
	return err == nil
}
