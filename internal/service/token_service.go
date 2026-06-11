package service

import (
	"routerx/internal"
	"routerx/internal/model"
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
	// TODO: Phase 2 实现
	_ = internal.DB
	return nil, nil
}

// List 令牌列表 (管理员看全量, 用户看自己的)。
func (s *TokenService) List(userID uint, page, pageSize int) ([]model.Token, int64, error) {
	// TODO: Phase 4/5 实现
	_ = internal.DB
	return nil, 0, nil
}

// Create 创建 API Token, 生成 sk-xxxx 格式 Key。
func (s *TokenService) Create(userID uint, name string, remainQuota int64, unlimited bool, expiredAt *int64) (*model.Token, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, nil
}

// Update 编辑 Token。
func (s *TokenService) Update(id uint, updates map[string]interface{}) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// Delete 软删除 Token。
func (s *TokenService) Delete(id uint) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// DeductQuota 扣减 Token / User 额度。
// 先扣 Token.RemainQuota, Token.remain_quota=-1 时只扣 User.Quota。
func (s *TokenService) DeductQuota(tokenID uint, quota int64) error {
	// TODO: Phase 3 实现
	_ = internal.DB
	return nil
}
