package service

import (
	"errors"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AdminService 管理员账户管理服务。
// 管理员可查看管理员账户；创建/编辑/删除仅允许超级管理员按等级规则操作。
type AdminService struct{}

func NewAdminService() *AdminService {
	return &AdminService{}
}

// ListAdmins 列出所有管理员账户 (role >= 1)。
func (s *AdminService) ListAdmins(operatorRole int, page, pageSize int) ([]model.User, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.User{}).Where("role >= ?", common.RoleAdmin)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var admins []model.User
	err := query.Order("role DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&admins).Error
	return admins, total, err
}

// CreateAdmin 创建管理员账户。
// 仅超级管理员可创建低于自身等级的管理员账户。
func (s *AdminService) CreateAdmin(operatorRole int, username, password, displayName, email string, role int) (*model.User, error) {
	if operatorRole < common.RoleSuper {
		return nil, errors.New("super admin role required")
	}
	if role < common.RoleAdmin || role > common.RoleSuper {
		role = common.RoleAdmin
	}
	if role >= operatorRole {
		return nil, errors.New("cannot create same or higher role admin account")
	}
	username = strings.TrimSpace(username)
	email = normalizeEmail(email)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
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
			Status:      common.UserStatusEnabled,
		}
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Create(&model.UserIdentity{
			UserID:       u.ID,
			Method:       model.UserIdentityMethodUsername,
			Provider:     model.UserIdentityProviderLocal,
			Identifier:   username,
			PasswordHash: hash,
			VerifiedAt:   &now,
		}).Error; err != nil {
			return err
		}
		user = u
		return nil
	})
	return user, err
}

// UpdateAdmin 编辑管理员账户 (角色/状态/信息)。
func (s *AdminService) UpdateAdmin(operatorID uint, operatorRole int, targetID uint, updates map[string]interface{}) error {
	if operatorRole < common.RoleSuper {
		return errors.New("super admin role required")
	}
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		var target model.User
		if err := tx.First(&target, targetID).Error; err != nil {
			return err
		}
		if target.Role < common.RoleAdmin {
			return errors.New("target is not an admin account")
		}
		allowed := filterUpdates(updates, "display_name", "email", "role", "status")
		if operatorID == targetID {
			if _, ok := allowed["role"]; ok {
				return errors.New("cannot change own admin role")
			}
			if _, ok := allowed["status"]; ok {
				return errors.New("cannot change own admin status")
			}
		}
		if operatorID != targetID && target.Role >= operatorRole {
			return errors.New("cannot manage same or higher role admin account")
		}
		if role, ok := allowed["role"].(int); ok {
			if role < common.RoleAdmin || role > common.RoleSuper {
				return errors.New("invalid admin role")
			}
			if role >= operatorRole {
				return errors.New("cannot promote admin to same or higher role")
			}
			if target.Role == common.RoleSuper && role != common.RoleSuper {
				if err := ensureAnotherEnabledSuper(tx, targetID); err != nil {
					return err
				}
			}
		}
		if status, ok := allowed["status"].(int); ok {
			if status != common.UserStatusDisabled && status != common.UserStatusEnabled {
				return errors.New("invalid admin status")
			}
			if target.Role == common.RoleSuper && status != common.UserStatusEnabled {
				if err := ensureAnotherEnabledSuper(tx, targetID); err != nil {
					return err
				}
			}
		}
		if len(allowed) == 0 {
			return nil
		}
		return tx.Model(&target).Updates(allowed).Error
	})
}

// DeleteAdmin 删除管理员账户。
// 禁止删除超级管理员，不可删除自己。
func (s *AdminService) DeleteAdmin(operatorID uint, operatorRole int, targetID uint) error {
	if operatorRole < common.RoleSuper {
		return errors.New("super admin role required")
	}
	if operatorID == targetID {
		return errors.New("cannot delete self")
	}
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		var target model.User
		if err := tx.First(&target, targetID).Error; err != nil {
			return err
		}
		if target.Role < common.RoleAdmin {
			return errors.New("target is not an admin account")
		}
		if target.Role >= operatorRole {
			return errors.New("cannot delete same or higher role admin account")
		}
		if target.Role >= common.RoleSuper {
			if err := ensureAnotherEnabledSuper(tx, targetID); err != nil {
				return err
			}
		}
		return tx.Delete(&target).Error
	})
}

func ensureAnotherEnabledSuper(tx *gorm.DB, targetID uint) error {
	var count int64
	if err := tx.Model(&model.User{}).
		Where("role = ? AND status = ? AND id <> ?", common.RoleSuper, common.UserStatusEnabled, targetID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("at least one enabled super admin is required")
	}
	return nil
}
