package service

import (
	"routerx/internal"
	"routerx/internal/model"
)

// AdminService 管理员账户管理服务。
// 仅超级管理员可操作，用于创建/编辑/删除其他管理员账户。
type AdminService struct{}

func NewAdminService() *AdminService {
	return &AdminService{}
}

// ListAdmins 列出所有管理员账户 (role >= 1)。
func (s *AdminService) ListAdmins(page, pageSize int) ([]model.User, int64, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, 0, nil
}

// CreateAdmin 创建管理员账户。
// 仅超级管理员可设置 role=2，普通管理员创建的 role 默认为 1。
func (s *AdminService) CreateAdmin(operatorRole int, username, password, displayName, email string, role int) (*model.User, error) {
	// TODO: Phase 4 实现
	// 1. 校验 operatorRole 权限 (role=2 可创建任何角色, role=1 只能创建 role=1)
	// 2. 校验本地账号身份唯一性
	// 3. bcrypt 哈希密码
	// 4. 创建用户记录和本地 UserIdentity
	_ = internal.DB
	return nil, nil
}

// UpdateAdmin 编辑管理员账户 (角色/状态/信息)。
func (s *AdminService) UpdateAdmin(operatorRole int, targetID uint, updates map[string]interface{}) error {
	// TODO: Phase 4 实现
	// 1. 校验操作权限 (不能编辑比自己权限高的管理员)
	// 2. 更新字段 (role, status, display_name, email)
	_ = internal.DB
	return nil
}

// DeleteAdmin 删除管理员账户。
// 禁止删除超级管理员，不可删除自己。
func (s *AdminService) DeleteAdmin(operatorID, targetID uint) error {
	// TODO: Phase 4 实现
	// 1. 查询目标用户，校验 role 权限链
	// 2. 不可删除自己
	// 3. 软删除
	_ = internal.DB
	return nil
}
