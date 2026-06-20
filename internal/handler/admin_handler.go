package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
	"routerx/internal/model"
	"routerx/internal/service"
)

// AdminHandler 管理员账户管理接口。
// 仅超级管理员可调用，用于管理其他管理员账户的创建/编辑/删除。
type AdminHandler struct {
	svc      *service.AdminService
	auditSvc *service.UserService
}

func NewAdminHandler(svc *service.AdminService) *AdminHandler {
	return &AdminHandler{svc: svc, auditSvc: service.NewUserService()}
}

// GET /v0/admin/admin — 管理员列表
func (h *AdminHandler) List(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	if operator.Role < common.RoleAdmin {
		common.FailWithStatus(c, 403, "需要管理员权限")
		return
	}
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	admins, total, err := h.svc.ListAdmins(operator.Role, page, pageSize)
	if err != nil {
		common.FailWithStatus(c, 500, "查询管理员失败")
		return
	}
	data := make([]dto.UserBrief, 0, len(admins))
	for i := range admins {
		data = append(data, dto.UserBriefFromModel(&admins[i]))
	}
	page, pageSize = pageValues(page, pageSize)
	common.Success(c, dto.PaginatedResult{Total: total, Page: page, PageSize: pageSize, Data: data})
}

// POST /v0/admin/admin — 创建管理员
func (h *AdminHandler) Create(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	var req dto.CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.FailWithStatus(c, 400, "创建管理员参数无效")
		return
	}
	user, err := h.svc.CreateAdmin(operator.Role, req.Username, req.Password, req.DisplayName, req.Email, req.Role)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAccountAudit(c, operator, "admin.create", user.ID, nil, adminAccountAuditSummary(user)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.Success(c, dto.UserBriefFromModel(user))
}

// PUT /v0/admin/admin/:id — 编辑管理员
func (h *AdminHandler) Update(c *gin.Context) {
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
		common.FailWithStatus(c, 400, "编辑管理员参数无效")
		return
	}
	before, err := h.svc.GetAdminByID(id)
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
		updates["role"] = *req.Role
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if err := h.svc.UpdateAdmin(operator.ID, operator.Role, id, updates); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	after, err := h.svc.GetAdminByID(id)
	if err != nil {
		common.FailWithStatus(c, 500, "查询管理员失败")
		return
	}
	action := "admin.update"
	if status, ok := updates["status"].(int); ok && status == common.UserStatusDisabled {
		action = "admin.disable"
	}
	if err := h.recordAdminAccountAudit(c, operator, action, id, adminAccountAuditSummary(before), adminAccountAuditSummary(after)); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "管理员已更新")
}

// DELETE /v0/admin/admin/:id — 删除管理员
func (h *AdminHandler) Delete(c *gin.Context) {
	operator, ok := currentUser(c)
	if !ok {
		common.FailWithStatus(c, 401, "未登录或登录已过期")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	before, err := h.svc.GetAdminByID(id)
	if err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.svc.DeleteAdmin(operator.ID, operator.Role, id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	if err := h.recordAdminAccountAudit(c, operator, "admin.delete", id, adminAccountAuditSummary(before), nil); err != nil {
		common.FailWithStatus(c, 500, "写入审计日志失败")
		return
	}
	common.SuccessMsg(c, "管理员已删除")
}

func (h *AdminHandler) recordAdminAccountAudit(c *gin.Context, operator *model.User, action string, id uint, before, after interface{}) error {
	return h.auditSvc.RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID:     c.GetString("request_id"),
		ActorUserID:   operator.ID,
		ActorRole:     operator.Role,
		Action:        action,
		ResourceType:  "admin",
		ResourceID:    strconv.FormatUint(uint64(id), 10),
		BeforeSummary: auditSummary(before),
		AfterSummary:  auditSummary(after),
		Result:        "success",
		IP:            c.ClientIP(),
		UserAgent:     c.GetHeader("User-Agent"),
	})
}

func adminAccountAuditSummary(user *model.User) map[string]interface{} {
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
		"status":       brief.Status,
	}
}
