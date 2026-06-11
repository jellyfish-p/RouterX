package service

import (
	"routerx/internal"
	"routerx/internal/model"
)

type UserService struct{}

func NewUserService() *UserService {
	return &UserService{}
}

// GetByID 根据 ID 获取用户。
func (s *UserService) GetByID(id uint) (*model.User, error) {
	// TODO: Phase 2 实现
	_ = internal.DB
	return nil, nil
}

// List 用户分页列表, 支持 keyword/role/status/group 筛选。
func (s *UserService) List(page, pageSize int, keyword string, role, status *int, groupID *uint) ([]model.User, int64, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, 0, nil
}

// Create 管理员创建用户。
func (s *UserService) Create(username, password, displayName, email string, role int, quota int64, groupID *uint) (*model.User, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, nil
}

// Update 管理员编辑用户信息。
func (s *UserService) Update(id uint, updates map[string]interface{}) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// Delete 软删除用户。
func (s *UserService) Delete(id uint) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// UpdateQuota 调整用户余额。
func (s *UserService) UpdateQuota(id uint, delta int64) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// UpdateSelf 用户自助修改个人信息。
func (s *UserService) UpdateSelf(id uint, displayName, email string) error {
	// TODO: Phase 5 实现
	_ = internal.DB
	return nil
}
