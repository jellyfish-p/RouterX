package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/dto"
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
	updates := map[string]interface{}{}
	if req.DisplayName != "" {
		updates["display_name"] = req.DisplayName
	}
	if req.Email != "" {
		updates["email"] = req.Email
	}
	if req.Role != nil {
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
	if err := h.svc.DeleteByAdmin(operator.ID, operator.Role, id); err != nil {
		common.FailWithStatus(c, 400, err.Error())
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
	if err := h.svc.UpdateQuotaByAdmin(operator.ID, operator.Role, id, req.Quota); err != nil {
		common.FailWithStatus(c, 400, err.Error())
		return
	}
	common.SuccessMsg(c, "额度已更新")
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
