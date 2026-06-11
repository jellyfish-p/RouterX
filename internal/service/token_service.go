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

type TokenService struct{}

func NewTokenService() *TokenService {
	return &TokenService{}
}

// ValidateAndGetToken 验证 API Key 有效性：
// 1. 查 tokens 表匹配 key
// 2. 校验 status=1 且未过期
// 3. 校验 remain_quota > 0 或 unlimited=true
// 4. 返回关联 User 信息
func (s *TokenService) ValidateAndGetToken(key string) (*model.Token, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "sk-") {
		return nil, errors.New("invalid api key")
	}

	hash := common.SHA256Hex(key)
	var token model.Token
	err := internal.DB.Preload("User").Where("key = ?", hash).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 兼容早期明文存量，验证成功后迁移为 hash。
		err = internal.DB.Preload("User").Where("key = ?", key).First(&token).Error
		if err == nil {
			_ = internal.DB.Model(&token).Update("key", hash).Error
			token.Key = hash
		}
	}
	if err != nil {
		return nil, errors.New("invalid api key")
	}
	if token.Status != common.TokenStatusEnabled {
		return nil, errors.New("api key is disabled")
	}
	if token.ExpiredAt != nil && token.ExpiredAt.Before(time.Now()) {
		return nil, errors.New("api key is expired")
	}
	if token.User == nil || token.User.Status != common.UserStatusEnabled {
		return nil, errors.New("user is disabled")
	}
	return &token, nil
}

// List 令牌列表 (管理员看全量, 用户看自己的)。
func (s *TokenService) List(userID uint, page, pageSize int) ([]model.Token, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.Token{})
	if userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var tokens []model.Token
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tokens).Error
	return tokens, total, err
}

func (s *TokenService) GetByIDForUser(id, userID uint) (*model.Token, error) {
	var token model.Token
	if err := internal.DB.Where("id = ? AND user_id = ?", id, userID).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

// Create 创建 API Token, 生成 sk-xxxx 格式 Key。
func (s *TokenService) Create(userID uint, name string, remainQuota int64, unlimited bool, expiredAt *int64) (*model.Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	if unlimited {
		remainQuota = common.QuotaUnlimited
	}
	var expires *time.Time
	if expiredAt != nil && *expiredAt > 0 {
		t := time.Unix(*expiredAt, 0)
		expires = &t
	}
	var user model.User
	if err := internal.DB.First(&user, userID).Error; err != nil {
		return nil, err
	}

	for i := 0; i < 3; i++ {
		plain, err := common.GenerateTokenKey()
		if err != nil {
			return nil, err
		}
		token := &model.Token{
			UserID:      userID,
			Name:        name,
			Key:         common.SHA256Hex(plain),
			Status:      common.TokenStatusEnabled,
			ExpiredAt:   expires,
			RemainQuota: remainQuota,
			Unlimited:   unlimited,
		}
		if err := internal.DB.Create(token).Error; err != nil {
			if i < 2 {
				continue
			}
			return nil, err
		}
		token.Key = plain
		return token, nil
	}
	return nil, errors.New("failed to generate api key")
}

// Update 编辑 Token。
func (s *TokenService) Update(id uint, updates map[string]interface{}) error {
	allowed := filterUpdates(updates, "name", "status", "remain_quota", "unlimited", "expired_at")
	if unlimited, ok := allowed["unlimited"].(bool); ok && unlimited {
		allowed["remain_quota"] = common.QuotaUnlimited
	}
	if len(allowed) == 0 {
		return nil
	}
	return internal.DB.Model(&model.Token{}).Where("id = ?", id).Updates(allowed).Error
}

// Delete 软删除 Token。
func (s *TokenService) Delete(id uint) error {
	return internal.DB.Delete(&model.Token{}, id).Error
}

// DeductQuota 扣减 Token / User 额度。
// 先扣 Token.RemainQuota, Token.remain_quota=-1 时只扣 User.Quota。
func (s *TokenService) DeductQuota(tokenID uint, quota int64) error {
	if quota <= 0 {
		return nil
	}
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		var token model.Token
		if err := tx.Preload("User").First(&token, tokenID).Error; err != nil {
			return err
		}
		if token.Unlimited || token.RemainQuota == common.QuotaUnlimited {
			res := tx.Model(&model.User{}).
				Where("id = ? AND status = ? AND quota >= ?", token.UserID, common.UserStatusEnabled, quota).
				Update("quota", gorm.Expr("quota - ?", quota))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errors.New("insufficient user quota")
			}
			return nil
		}
		res := tx.Model(&model.Token{}).
			Where("id = ? AND status = ? AND remain_quota >= ?", token.ID, common.TokenStatusEnabled, quota).
			Update("remain_quota", gorm.Expr("remain_quota - ?", quota))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("insufficient token quota")
		}
		return nil
	})
}

func (s *TokenService) HasAvailableQuota(token *model.Token) bool {
	if token == nil {
		return false
	}
	if token.Unlimited || token.RemainQuota == common.QuotaUnlimited {
		if token.User == nil {
			var user model.User
			if err := internal.DB.First(&user, token.UserID).Error; err != nil {
				return false
			}
			token.User = &user
		}
		return token.User.Status == common.UserStatusEnabled && token.User.Quota > 0
	}
	return token.RemainQuota > 0
}
