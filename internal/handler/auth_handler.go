package handler

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
	"routerx/internal/service"
)

type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// POST /v0/user/register — 用户注册
func (h *AuthHandler) Register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "注册参数无效")
		return
	}
	result, err := h.svc.Register(req.Username, req.Password, req.DisplayName, req.Email)
	if err != nil {
		if errors.Is(err, service.ErrSelfRegistrationDisabled) ||
			errors.Is(err, service.ErrUsernameRegistrationDisabled) ||
			errors.Is(err, service.ErrRegistrationCaptchaRequired) {
			common.FailWithStatus(c, 403, err.Error())
			return
		}
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if result == nil || result.User == nil {
		common.FailWithStatus(c, 500, "注册结果无效")
		return
	}
	if result.Recovered {
		if err := service.NewUserService().RecordAdminAuditLog(service.AdminAuditRecordInput{
			RequestID:    c.GetString("request_id"),
			ActorUserID:  result.User.ID,
			ActorRole:    result.User.Role,
			Action:       "user.recover",
			ResourceType: "user",
			ResourceID:   strconv.FormatUint(uint64(result.User.ID), 10),
			AfterSummary: auditSummary(dto.UserBriefFromModel(result.User)),
			Result:       "success",
			IP:           c.ClientIP(),
			UserAgent:    c.GetHeader("User-Agent"),
		}); err != nil {
			common.FailWithStatus(c, 500, "写入审计日志失败")
			return
		}
	}
	common.Success(c, dto.UserBriefFromModel(result.User))
}

// POST /v0/user/login — 用户登录 (返回 JWT)
func (h *AuthHandler) UserLogin(c *gin.Context) {
	var req dto.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "登录参数无效")
		return
	}
	account := loginAccount(req)
	user, token, err := h.svc.UserLogin(account, req.Password)
	if err != nil {
		common.FailWithStatus(c, 401, "账号或凭据错误")
		return
	}
	common.Success(c, dto.LoginResponse{Token: token, User: dto.UserBriefFromModel(user)})
}

// POST /v0/user/self/password — 修改密码
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req dto.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "修改密码参数无效")
		return
	}
	user, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	if err := h.svc.ChangePassword(user.ID, req.OldPassword, req.NewPassword); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "密码修改成功")
}

func loginAccount(req dto.LoginRequest) string {
	if req.Account != "" {
		return req.Account
	}
	return req.Username
}

func setJWTCookie(c *gin.Context, token string) {
	c.SetSameSite(2)
	c.SetCookie("jwt_token", token, 7*24*3600, "/", "", c.Request.TLS != nil, true)
}

func currentUser(c *gin.Context) (*model.User, bool) {
	v, ok := c.Get("current_user")
	if !ok {
		v, ok = c.Get("user")
	}
	if !ok {
		return nil, false
	}
	user, ok := v.(*model.User)
	return user, ok && user != nil
}
