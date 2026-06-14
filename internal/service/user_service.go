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

type UserService struct{}

func NewUserService() *UserService {
	return &UserService{}
}

// GetByID 根据 ID 获取用户。
func (s *UserService) GetByID(id uint) (*model.User, error) {
	var user model.User
	if err := internal.DB.First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// List 用户分页列表, 支持 keyword/role/status/group 筛选。
func (s *UserService) List(operatorRole int, page, pageSize int, keyword string, role, status *int, groupID *uint) ([]model.User, int64, error) {
	if operatorRole < common.RoleAdmin {
		return nil, 0, errors.New("admin role required")
	}
	if role != nil && (*role < common.RoleUser || *role > common.RoleSuper) {
		return nil, 0, errors.New("invalid role")
	}
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.User{})
	if keyword != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("username LIKE ? OR display_name LIKE ? OR email LIKE ?", like, like, like)
	}
	if role != nil {
		query = query.Where("role = ?", *role)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	if groupID != nil {
		query = query.Where("group_id = ?", *groupID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var users []model.User
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error
	return users, total, err
}

// Create 管理员创建用户。
func (s *UserService) Create(operatorRole int, username, password, displayName, email string, role int, quota int64, groupID *uint) (*model.User, error) {
	if operatorRole < common.RoleAdmin {
		return nil, errors.New("admin role required")
	}
	username = strings.TrimSpace(username)
	email = normalizeEmail(email)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	if len(password) < 6 {
		return nil, errors.New("password length must be at least 6")
	}
	if role != common.RoleUser {
		return nil, errors.New("admin user management can only create normal users")
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
			Quota:       quota,
			Status:      common.UserStatusEnabled,
			GroupID:     groupID,
		}
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		now := time.Now()
		identity := model.UserIdentity{
			UserID:       u.ID,
			Method:       model.UserIdentityMethodUsername,
			Provider:     model.UserIdentityProviderLocal,
			Identifier:   username,
			PasswordHash: hash,
			VerifiedAt:   &now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return err
		}
		user = u
		return nil
	})
	return user, err
}

// Update 管理员编辑用户信息。
func (s *UserService) UpdateByAdmin(operatorID uint, operatorRole int, targetID uint, updates map[string]interface{}) error {
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, targetID); err != nil {
		return err
	}
	allowed := filterUpdates(updates, "display_name", "email", "status", "group_id")
	if len(allowed) == 0 {
		return nil
	}
	return internal.DB.Model(&model.User{}).Where("id = ? AND role = ?", targetID, common.RoleUser).Updates(allowed).Error
}

// Delete 软删除用户。
func (s *UserService) DeleteByAdmin(operatorID uint, operatorRole int, targetID uint) error {
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, targetID); err != nil {
		return err
	}
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Token{}).Where("user_id = ?", targetID).Update("status", common.TokenStatusDisabled).Error; err != nil {
			return err
		}
		return tx.Where("role = ?", common.RoleUser).Delete(&model.User{}, targetID).Error
	})
}

// UpdateQuota 调整用户余额。
func (s *UserService) UpdateQuotaByAdmin(operatorID uint, operatorRole int, targetID uint, delta int64) error {
	if err := s.ensureNormalUserTarget(operatorID, operatorRole, targetID); err != nil {
		return err
	}
	query := internal.DB.Model(&model.User{}).Where("id = ? AND role = ?", targetID, common.RoleUser)
	if delta < 0 {
		query = query.Where("quota >= ?", -delta)
	}
	res := query.Update("quota", gorm.Expr("quota + ?", delta))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("insufficient user quota")
	}
	return nil
}

// UpdateSelf 用户自助修改个人信息。
func (s *UserService) UpdateSelf(id uint, displayName, email string) error {
	updates := map[string]interface{}{}
	if strings.TrimSpace(displayName) != "" {
		updates["display_name"] = strings.TrimSpace(displayName)
	}
	if strings.TrimSpace(email) != "" {
		normalized := normalizeEmail(email)
		updates["email"] = normalized
	}
	if len(updates) == 0 {
		return nil
	}
	return internal.DB.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

// RedeemCode 将未使用的充值码兑换到当前用户余额。当前阶段尚未落库 quota_transactions，
// 因此必须让状态更新和余额增加在同一事务内完成，避免重复兑换。
func (s *UserService) RedeemCode(userID uint, code string) (int64, int64, error) {
	code = strings.TrimSpace(code)
	if userID == 0 || code == "" {
		return 0, 0, errors.New("redem code is required")
	}
	var redeemedQuota int64
	var finalQuota int64
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var redem model.RedemCode
		if err := tx.Where("code = ? AND status = ?", code, common.RedemCodeStatusUnused).First(&redem).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("redem code is invalid or already used")
			}
			return err
		}
		now := time.Now()
		usedBy := userID
		res := tx.Model(&model.RedemCode{}).
			Where("id = ? AND status = ?", redem.ID, common.RedemCodeStatusUnused).
			Updates(map[string]interface{}{
				"status":  common.RedemCodeStatusUsed,
				"used_by": usedBy,
				"used_at": &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("redem code is invalid or already used")
		}
		if err := tx.Model(&model.User{}).Where("id = ?", userID).Update("quota", gorm.Expr("quota + ?", redem.Quota)).Error; err != nil {
			return err
		}
		var user model.User
		if err := tx.Select("quota").First(&user, userID).Error; err != nil {
			return err
		}
		redeemedQuota = redem.Quota
		finalQuota = user.Quota
		return nil
	})
	return redeemedQuota, finalQuota, err
}

func normalizePage(page, pageSize int) (int, int) {
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

func filterUpdates(updates map[string]interface{}, keys ...string) map[string]interface{} {
	allowed := make(map[string]interface{})
	for _, key := range keys {
		if v, ok := updates[key]; ok {
			allowed[key] = v
		}
	}
	return allowed
}

func (s *UserService) ensureNormalUserTarget(operatorID uint, operatorRole int, targetID uint) error {
	if operatorID == 0 {
		return errors.New("operator is required")
	}
	if operatorRole < common.RoleAdmin {
		return errors.New("admin role required")
	}
	if operatorID == targetID {
		return errors.New("admin user management cannot operate on self")
	}
	var target model.User
	if err := internal.DB.First(&target, targetID).Error; err != nil {
		return err
	}
	if target.Role >= common.RoleAdmin {
		return errors.New("admin accounts must be managed through super admin endpoints")
	}
	return nil
}
