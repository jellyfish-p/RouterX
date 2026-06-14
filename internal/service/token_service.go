package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type TokenService struct{}

func NewTokenService() *TokenService {
	return &TokenService{}
}

var (
	ErrInvalidAPIKey           = errors.New("invalid api key")
	ErrAPIKeyDisabled          = errors.New("api key is disabled")
	ErrAPIKeyExpired           = errors.New("api key is expired")
	ErrAPIUserDisabled         = errors.New("user is disabled")
	ErrInsufficientUserQuota   = errors.New("insufficient user quota")
	ErrInsufficientTokenQuota  = errors.New("insufficient token quota")
	ErrBatchDisableNoFilter    = errors.New("batch disable requires token_ids or user_id")
	ErrModelNotAllowed         = errors.New("model not allowed by api key scope")
	ErrAPINotAllowed           = errors.New("api type not allowed by api key scope")
	ErrChannelGroupNotAllowed  = errors.New("channel group not allowed by api key scope")
	ErrEntryProtocolNotAllowed = errors.New("entry protocol not allowed by api key scope")
	ErrIPNotAllowed            = errors.New("ip not allowed by api key scope")
	ErrMethodNotAllowed        = errors.New("method not allowed by api key scope")
	ErrDailyQuotaExceeded      = errors.New("daily quota exceeded by api key scope")
	ErrMonthlyQuotaExceeded    = errors.New("monthly quota exceeded by api key scope")
	ErrMaxConcurrencyExceeded  = errors.New("max concurrency exceeded by api key scope")
	ErrRPMExceeded             = errors.New("rpm exceeded by api key scope")
	ErrTPMExceeded             = errors.New("tpm exceeded by api key scope")
)

const (
	maxTokenScopeModels         = 200
	maxTokenScopeAPITypes       = 64
	maxTokenScopeChannelGroups  = 64
	maxTokenScopeEntryProtocols = 8
	maxTokenScopeIPCIDRs        = 64
	maxTokenScopeMethods        = 128
)

type TokenScope struct {
	AllowModels    []string `json:"allow_models,omitempty"`
	APITypes       []string `json:"api_types,omitempty"`       // 入口能力白名单, 如 openai.chat/openai.embeddings
	ChannelGroups  []string `json:"channel_groups,omitempty"`  // 通道分组白名单, 空通道分组按 default 处理
	EntryProtocols []string `json:"entry_protocols,omitempty"` // 客户端入口协议白名单: openai/anthropic/gemini
	IPCIDRs        []string `json:"ip_cidrs,omitempty"`        // 来源 IP/CIDR 白名单
	Methods        []string `json:"methods,omitempty"`         // 请求方法和路径白名单, 如 POST /v1/chat/completions
	DailyQuota     *int64   `json:"daily_quota,omitempty"`     // 单 Key 每日最大成功消耗额度
	MonthlyQuota   *int64   `json:"monthly_quota,omitempty"`   // 单 Key 每月最大成功消耗额度
	MaxConcurrency *int64   `json:"max_concurrency,omitempty"` // 单 Key 同时在途请求上限
	RPM            *int64   `json:"rpm,omitempty"`             // 单 Key 每分钟请求上限
	TPM            *int64   `json:"tpm,omitempty"`             // 单 Key 每分钟模型 token 上限
}

var tokenConcurrencyScopes = newTokenConcurrencyTracker()

type TokenUsageStats struct {
	TokenID      uint
	CallCount    int64
	SuccessCount int64
	ErrorCount   int64
	TotalQuota   int64
	TotalTokens  int64
	LastUsedAt   *time.Time
	LastModel    string
	LastStatus   int
	LastErrorMsg string
}

type BatchDisableTokensInput struct {
	TokenIDs []uint
	UserID   *uint
	Reason   string
}

type BatchDisableTokensResult struct {
	MatchedCount  int64
	DisabledCount int64
	Reason        string
	TokenIDs      []uint
}

// ValidateAndGetToken 验证 API Key 有效性：
// 1. 查 tokens 表匹配 key
// 2. 校验 status=1 且未过期
// 3. 校验所属用户状态
// 4. 返回关联 User 信息
func (s *TokenService) ValidateAndGetToken(key string) (*model.Token, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "sk-") {
		return nil, ErrInvalidAPIKey
	}

	hash := common.SHA256Hex(key)
	var token model.Token
	err := internal.DB.Preload("User").Preload("User.Group").Where("key = ?", hash).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 兼容早期明文存量，验证成功后迁移为 hash。
		err = internal.DB.Preload("User").Preload("User.Group").Where("key = ?", key).First(&token).Error
		if err == nil {
			_ = internal.DB.Model(&token).Update("key", hash).Error
			token.Key = hash
		}
	}
	if err != nil {
		return nil, ErrInvalidAPIKey
	}
	if token.Status != common.TokenStatusEnabled {
		return nil, ErrAPIKeyDisabled
	}
	if token.ExpiredAt != nil && token.ExpiredAt.Before(time.Now()) {
		return nil, ErrAPIKeyExpired
	}
	if token.User == nil || token.User.Status != common.UserStatusEnabled {
		return nil, ErrAPIUserDisabled
	}
	return &token, nil
}

// List 令牌列表 (管理员看全量, 用户看自己的)。
func (s *TokenService) List(userID uint, page, pageSize int) ([]model.Token, int64, error) {
	var userIDPtr *uint
	if userID > 0 {
		userIDPtr = &userID
	}
	return s.ListFiltered(userIDPtr, nil, page, pageSize)
}

func (s *TokenService) ListFiltered(userID *uint, status *int, page, pageSize int) ([]model.Token, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.Token{})
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
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
	} else if remainQuota < 0 {
		return nil, errors.New("token quota cannot be negative")
	}
	var expires *time.Time
	if expiredAt != nil && *expiredAt > 0 {
		t := time.Unix(*expiredAt, 0)
		expires = &t
	}

	var created *model.Token
	var plainKey string
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrAPIUserDisabled
		}
		token, plain, err := createTokenWithPlain(tx, model.Token{
			UserID:      userID,
			Name:        name,
			Status:      common.TokenStatusEnabled,
			ExpiredAt:   expires,
			RemainQuota: remainQuota,
			Unlimited:   unlimited,
		})
		if err != nil {
			return err
		}
		plainKey = plain
		created = token
		return nil
	})
	if err != nil {
		return nil, err
	}
	created.Key = plainKey
	return created, nil
}

func createTokenWithPlain(tx *gorm.DB, base model.Token) (*model.Token, string, error) {
	for i := 0; i < 3; i++ {
		plain, err := common.GenerateTokenKey()
		if err != nil {
			return nil, "", err
		}
		token := base
		token.Key = common.SHA256Hex(plain)
		if err := tx.Create(&token).Error; err != nil {
			if i < 2 {
				continue
			}
			return nil, "", err
		}
		return &token, plain, nil
	}
	return nil, "", errors.New("failed to generate api key")
}

func (s *TokenService) RotateForUser(id, userID uint) (*model.Token, *model.Token, error) {
	var oldAfter *model.Token
	var created *model.Token
	var plainKey string
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var old model.Token
		if err := tx.Where("id = ? AND user_id = ?", id, userID).First(&old).Error; err != nil {
			return err
		}
		if old.Status != common.TokenStatusEnabled {
			return ErrAPIKeyDisabled
		}
		if err := tx.Model(&model.Token{}).
			Where("id = ? AND user_id = ?", id, userID).
			Updates(map[string]interface{}{
				"status":         common.TokenStatusDisabled,
				"revoked_reason": "rotated",
			}).Error; err != nil {
			return err
		}

		rotatedFromID := old.ID
		var expires *time.Time
		if old.ExpiredAt != nil {
			t := *old.ExpiredAt
			expires = &t
		}
		token, plain, err := createTokenWithPlain(tx, model.Token{
			UserID:        old.UserID,
			Name:          old.Name,
			Status:        common.TokenStatusEnabled,
			ExpiredAt:     expires,
			RemainQuota:   old.RemainQuota,
			Unlimited:     old.Unlimited,
			RotatedFromID: &rotatedFromID,
			ScopeJSON:     append(model.JSONValue(nil), old.ScopeJSON...),
		})
		if err != nil {
			return err
		}
		old.Status = common.TokenStatusDisabled
		old.RevokedReason = "rotated"
		oldAfter = &old
		created = token
		plainKey = plain
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	created.Key = plainKey
	return oldAfter, created, nil
}

func (s *TokenService) DisableForUser(id, userID uint, reason string) (*model.Token, error) {
	reason = normalizeRevokedReason(reason, "user_disabled")
	var token model.Token
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND user_id = ?", id, userID).First(&token).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Token{}).
			Where("id = ? AND user_id = ?", id, userID).
			Updates(map[string]interface{}{
				"status":         common.TokenStatusDisabled,
				"revoked_reason": reason,
			}).Error; err != nil {
			return err
		}
		token.Status = common.TokenStatusDisabled
		token.RevokedReason = reason
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (s *TokenService) ReportLeakForUser(id, userID uint, reason string) (*model.Token, error) {
	return s.DisableForUser(id, userID, normalizeRevokedReason(reason, "reported_leak"))
}

func (s *TokenService) UpdateScopeForUser(id, userID uint, scope TokenScope) (*model.Token, error) {
	scope.AllowModels = normalizeScopeModels(scope.AllowModels)
	scope.APITypes = normalizeScopeAPITypes(scope.APITypes)
	scope.ChannelGroups = normalizeScopeChannelGroups(scope.ChannelGroups)
	scope.EntryProtocols = normalizeScopeEntryProtocols(scope.EntryProtocols)
	scope.IPCIDRs = normalizeScopeIPCIDRs(scope.IPCIDRs)
	scope.Methods = normalizeScopeMethods(scope.Methods)
	if len(scope.AllowModels) > maxTokenScopeModels {
		return nil, errors.New("allow_models exceeds limit")
	}
	if len(scope.APITypes) > maxTokenScopeAPITypes {
		return nil, errors.New("api_types exceeds limit")
	}
	if len(scope.ChannelGroups) > maxTokenScopeChannelGroups {
		return nil, errors.New("channel_groups exceeds limit")
	}
	if len(scope.EntryProtocols) > maxTokenScopeEntryProtocols {
		return nil, errors.New("entry_protocols exceeds limit")
	}
	if len(scope.IPCIDRs) > maxTokenScopeIPCIDRs {
		return nil, errors.New("ip_cidrs exceeds limit")
	}
	if len(scope.Methods) > maxTokenScopeMethods {
		return nil, errors.New("methods exceeds limit")
	}
	if scope.DailyQuota != nil && *scope.DailyQuota < 0 {
		return nil, errors.New("daily_quota cannot be negative")
	}
	if scope.MonthlyQuota != nil && *scope.MonthlyQuota < 0 {
		return nil, errors.New("monthly_quota cannot be negative")
	}
	if scope.MaxConcurrency != nil && *scope.MaxConcurrency < 0 {
		return nil, errors.New("max_concurrency cannot be negative")
	}
	if scope.RPM != nil && *scope.RPM < 0 {
		return nil, errors.New("rpm cannot be negative")
	}
	if scope.TPM != nil && *scope.TPM < 0 {
		return nil, errors.New("tpm cannot be negative")
	}
	if err := validateScopeIPCIDRs(scope.IPCIDRs); err != nil {
		return nil, err
	}
	if err := validateScopeEntryProtocols(scope.EntryProtocols); err != nil {
		return nil, err
	}
	if err := validateScopeMethods(scope.Methods); err != nil {
		return nil, err
	}
	scopeJSON := model.NewJSONValue(scope)
	var token model.Token
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND user_id = ?", id, userID).First(&token).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Token{}).
			Where("id = ? AND user_id = ?", id, userID).
			Update("scope_json", scopeJSON).Error; err != nil {
			return err
		}
		token.ScopeJSON = scopeJSON
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (s *TokenService) CheckModelScope(token *model.Token, modelName string) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrModelNotAllowed
	}
	if len(scope.AllowModels) == 0 {
		return nil
	}
	modelName = strings.TrimSpace(modelName)
	for _, allowed := range scope.AllowModels {
		if allowed == "*" || allowed == modelName {
			return nil
		}
	}
	return ErrModelNotAllowed
}

func (s *TokenService) CheckAPIScope(token *model.Token, apiType string) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrAPINotAllowed
	}
	if len(scope.APITypes) == 0 {
		return nil
	}
	apiType = normalizeScopeAPIType(apiType)
	for _, allowed := range scope.APITypes {
		if allowed == "*" || allowed == apiType {
			return nil
		}
	}
	return ErrAPINotAllowed
}

func (s *TokenService) CheckChannelGroupScope(token *model.Token, channelGroup string) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrChannelGroupNotAllowed
	}
	if len(scope.ChannelGroups) == 0 {
		return nil
	}
	channelGroup = normalizeChannelGroupForScope(channelGroup)
	for _, allowed := range scope.ChannelGroups {
		if allowed == "*" || allowed == channelGroup {
			return nil
		}
	}
	return ErrChannelGroupNotAllowed
}

// CheckEntryProtocolScope verifies the client-facing protocol before request parsing and routing.
func (s *TokenService) CheckEntryProtocolScope(token *model.Token, protocol string) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrEntryProtocolNotAllowed
	}
	if len(scope.EntryProtocols) == 0 {
		return nil
	}
	protocol = normalizeScopeEntryProtocol(protocol)
	for _, allowed := range scope.EntryProtocols {
		if allowed == "*" || allowed == protocol {
			return nil
		}
	}
	return ErrEntryProtocolNotAllowed
}

func (s *TokenService) CheckIPScope(token *model.Token, ip string) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrIPNotAllowed
	}
	if len(scope.IPCIDRs) == 0 {
		return nil
	}
	clientIP := net.ParseIP(strings.TrimSpace(ip))
	if clientIP == nil {
		return ErrIPNotAllowed
	}
	for _, allowed := range scope.IPCIDRs {
		if allowed == "*" {
			return nil
		}
		if strings.Contains(allowed, "/") {
			_, network, err := net.ParseCIDR(allowed)
			if err == nil && network.Contains(clientIP) {
				return nil
			}
			continue
		}
		if allowedIP := net.ParseIP(allowed); allowedIP != nil && allowedIP.Equal(clientIP) {
			return nil
		}
	}
	return ErrIPNotAllowed
}

func (s *TokenService) CheckMethodScope(token *model.Token, method, path string) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrMethodNotAllowed
	}
	if len(scope.Methods) == 0 {
		return nil
	}
	target := normalizeRequestMethodScope(method, path)
	if target == "" {
		return ErrMethodNotAllowed
	}
	for _, allowed := range scope.Methods {
		if allowed == "*" || allowed == target {
			return nil
		}
	}
	return ErrMethodNotAllowed
}

func (s *TokenService) CheckDailyQuotaScope(token *model.Token) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrDailyQuotaExceeded
	}
	if scope.DailyQuota == nil {
		return nil
	}
	if *scope.DailyQuota < 0 {
		return ErrDailyQuotaExceeded
	}
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	used, err := sumSuccessfulTokenQuotaSince(token.ID, startOfDay)
	if err != nil {
		return err
	}
	if used >= *scope.DailyQuota {
		return ErrDailyQuotaExceeded
	}
	return nil
}

func (s *TokenService) CheckMonthlyQuotaScope(token *model.Token) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrMonthlyQuotaExceeded
	}
	if scope.MonthlyQuota == nil {
		return nil
	}
	if *scope.MonthlyQuota < 0 {
		return ErrMonthlyQuotaExceeded
	}
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	used, err := sumSuccessfulTokenQuotaSince(token.ID, startOfMonth)
	if err != nil {
		return err
	}
	if used >= *scope.MonthlyQuota {
		return ErrMonthlyQuotaExceeded
	}
	return nil
}

func sumSuccessfulTokenQuotaSince(tokenID uint, since time.Time) (int64, error) {
	var used int64
	err := internal.DB.Model(&model.Log{}).
		Where("token_id = ? AND status = ? AND created_at >= ?", tokenID, common.LogStatusSuccess, since).
		Select("COALESCE(SUM(quota_used), 0)").
		Scan(&used).Error
	return used, err
}

func (s *TokenService) CheckRPMScope(token *model.Token) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrRPMExceeded
	}
	if scope.RPM == nil {
		return nil
	}
	if *scope.RPM < 0 {
		return ErrRPMExceeded
	}
	now := time.Now()
	startOfMinute := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), 0, 0, now.Location())
	count, err := countTokenLogsSince(token.ID, startOfMinute)
	if err != nil {
		return err
	}
	if count >= *scope.RPM {
		return ErrRPMExceeded
	}
	return nil
}

func countTokenLogsSince(tokenID uint, since time.Time) (int64, error) {
	var count int64
	err := internal.DB.Model(&model.Log{}).
		Where("token_id = ? AND created_at >= ?", tokenID, since).
		Count(&count).Error
	return count, err
}

func (s *TokenService) CheckTPMScope(token *model.Token) error {
	if token == nil {
		return ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return ErrTPMExceeded
	}
	if scope.TPM == nil {
		return nil
	}
	if *scope.TPM < 0 {
		return ErrTPMExceeded
	}
	now := time.Now()
	startOfMinute := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), 0, 0, now.Location())
	used, err := sumSuccessfulTokenTokensSince(token.ID, startOfMinute)
	if err != nil {
		return err
	}
	if used >= *scope.TPM {
		return ErrTPMExceeded
	}
	return nil
}

func sumSuccessfulTokenTokensSince(tokenID uint, since time.Time) (int64, error) {
	var used int64
	err := internal.DB.Model(&model.Log{}).
		Where("token_id = ? AND status = ? AND created_at >= ?", tokenID, common.LogStatusSuccess, since).
		Select("COALESCE(SUM(total_tokens), 0)").
		Scan(&used).Error
	return used, err
}

func (s *TokenService) AcquireConcurrencyScope(token *model.Token) (func(), error) {
	if token == nil {
		return func() {}, ErrInvalidAPIKey
	}
	scope, err := ParseTokenScope(token.ScopeJSON)
	if err != nil {
		return func() {}, ErrMaxConcurrencyExceeded
	}
	if scope.MaxConcurrency == nil {
		return func() {}, nil
	}
	limit := *scope.MaxConcurrency
	if limit < 0 {
		return func() {}, ErrMaxConcurrencyExceeded
	}
	if internal.RDB != nil {
		release, err := acquireRedisTokenConcurrency(token.ID, limit)
		if err == nil {
			return release, nil
		}
		if errors.Is(err, ErrMaxConcurrencyExceeded) {
			return func() {}, err
		}
		// Redis 是可降级依赖；单机或测试环境回落到进程内计数。
	}
	return tokenConcurrencyScopes.acquire(token.ID, limit)
}

func acquireRedisTokenConcurrency(tokenID uint, limit int64) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	key := fmt.Sprintf("api_key:concurrency:%d", tokenID)
	count, err := internal.RDB.Incr(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if count == 1 {
		_ = internal.RDB.Expire(ctx, key, time.Hour).Err()
	}
	if count > limit {
		releaseRedisTokenConcurrency(ctx, key)
		return nil, ErrMaxConcurrencyExceeded
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			releaseRedisTokenConcurrency(ctx, key)
		})
	}, nil
}

func releaseRedisTokenConcurrency(ctx context.Context, key string) {
	count, err := internal.RDB.Decr(ctx, key).Result()
	if err == nil && count <= 0 {
		_ = internal.RDB.Del(ctx, key).Err()
	}
}

type tokenConcurrencyTracker struct {
	mu     sync.Mutex
	counts map[uint]int64
}

func newTokenConcurrencyTracker() *tokenConcurrencyTracker {
	return &tokenConcurrencyTracker{counts: map[uint]int64{}}
}

func (t *tokenConcurrencyTracker) acquire(tokenID uint, limit int64) (func(), error) {
	t.mu.Lock()
	if t.counts[tokenID] >= limit {
		t.mu.Unlock()
		return nil, ErrMaxConcurrencyExceeded
	}
	t.counts[tokenID]++
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.counts[tokenID] <= 1 {
				delete(t.counts, tokenID)
				return
			}
			t.counts[tokenID]--
		})
	}, nil
}

func (s *TokenService) RecordScopeDeniedLog(token *model.Token, errorMsg, clientIP string) {
	if token == nil {
		return
	}
	tokenID := token.ID
	_ = internal.DB.Create(&model.Log{
		UserID:    token.UserID,
		TokenID:   &tokenID,
		Model:     "",
		Status:    common.LogStatusFailed,
		QuotaUsed: 0,
		ErrorMsg:  errorMsg,
		IP:        clientIP,
	}).Error
}

func ParseTokenScope(raw model.JSONValue) (TokenScope, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return TokenScope{}, nil
	}
	var scope TokenScope
	if err := json.Unmarshal(raw, &scope); err != nil {
		return TokenScope{}, err
	}
	scope.AllowModels = normalizeScopeModels(scope.AllowModels)
	scope.APITypes = normalizeScopeAPITypes(scope.APITypes)
	scope.ChannelGroups = normalizeScopeChannelGroups(scope.ChannelGroups)
	scope.EntryProtocols = normalizeScopeEntryProtocols(scope.EntryProtocols)
	scope.IPCIDRs = normalizeScopeIPCIDRs(scope.IPCIDRs)
	scope.Methods = normalizeScopeMethods(scope.Methods)
	return scope, nil
}

func (s *TokenService) GetUsageForUser(id, userID uint) (TokenUsageStats, error) {
	if _, err := s.GetByIDForUser(id, userID); err != nil {
		return TokenUsageStats{}, err
	}
	type aggregate struct {
		CallCount    int64
		SuccessCount int64
		ErrorCount   int64
		TotalQuota   int64
		TotalTokens  int64
	}
	var agg aggregate
	err := internal.DB.Model(&model.Log{}).
		Where("user_id = ? AND token_id = ?", userID, id).
		Select(
			"COUNT(*) AS call_count, "+
				"COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS success_count, "+
				"COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS error_count, "+
				"COALESCE(SUM(quota_used), 0) AS total_quota, "+
				"COALESCE(SUM(total_tokens), 0) AS total_tokens",
			common.LogStatusSuccess,
			common.LogStatusFailed,
		).
		Scan(&agg).Error
	if err != nil {
		return TokenUsageStats{}, err
	}
	stats := TokenUsageStats{
		TokenID:      id,
		CallCount:    agg.CallCount,
		SuccessCount: agg.SuccessCount,
		ErrorCount:   agg.ErrorCount,
		TotalQuota:   agg.TotalQuota,
		TotalTokens:  agg.TotalTokens,
	}
	if agg.CallCount == 0 {
		return stats, nil
	}
	var last model.Log
	err = internal.DB.
		Where("user_id = ? AND token_id = ?", userID, id).
		Order("created_at DESC, id DESC").
		First(&last).Error
	if err != nil {
		return TokenUsageStats{}, err
	}
	lastUsedAt := last.CreatedAt
	stats.LastUsedAt = &lastUsedAt
	stats.LastModel = last.Model
	stats.LastStatus = last.Status
	stats.LastErrorMsg = last.ErrorMsg
	return stats, nil
}

func (s *TokenService) BatchDisable(input BatchDisableTokensInput) (BatchDisableTokensResult, []model.Token, error) {
	tokenIDs := uniquePositiveUint(input.TokenIDs)
	if len(tokenIDs) == 0 && input.UserID == nil {
		return BatchDisableTokensResult{}, nil, ErrBatchDisableNoFilter
	}
	reason := normalizeRevokedReason(input.Reason, "admin_batch_disable")
	var matched []model.Token
	var disabledIDs []uint
	var disabledCount int64
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&model.Token{})
		if len(tokenIDs) > 0 {
			query = query.Where("id IN ?", tokenIDs)
		}
		if input.UserID != nil {
			query = query.Where("user_id = ?", *input.UserID)
		}
		if err := query.Order("id ASC").Find(&matched).Error; err != nil {
			return err
		}
		for _, token := range matched {
			if token.Status == common.TokenStatusEnabled {
				disabledIDs = append(disabledIDs, token.ID)
			}
		}
		if len(disabledIDs) == 0 {
			return nil
		}
		res := tx.Model(&model.Token{}).
			Where("id IN ?", disabledIDs).
			Updates(map[string]interface{}{
				"status":         common.TokenStatusDisabled,
				"revoked_reason": reason,
			})
		if res.Error != nil {
			return res.Error
		}
		disabledCount = res.RowsAffected
		return nil
	})
	if err != nil {
		return BatchDisableTokensResult{}, nil, err
	}
	return BatchDisableTokensResult{
		MatchedCount:  int64(len(matched)),
		DisabledCount: disabledCount,
		Reason:        reason,
		TokenIDs:      disabledIDs,
	}, matched, nil
}

// Update 编辑 Token。
func (s *TokenService) Update(id uint, updates map[string]interface{}) error {
	allowed := filterUpdates(updates, "name", "status", "expired_at")
	if status, ok := allowed["status"].(int); ok {
		if status != common.TokenStatusDisabled && status != common.TokenStatusEnabled {
			return errors.New("invalid token status")
		}
		if status == common.TokenStatusDisabled {
			allowed["revoked_reason"] = normalizeRevokedReason("", "user_disabled")
		} else {
			allowed["revoked_reason"] = ""
		}
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
				return ErrInsufficientUserQuota
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
			return ErrInsufficientTokenQuota
		}
		res = tx.Model(&model.User{}).
			Where("id = ? AND status = ? AND quota >= ?", token.UserID, common.UserStatusEnabled, quota).
			Update("quota", gorm.Expr("quota - ?", quota))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInsufficientUserQuota
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
	if token.RemainQuota <= 0 {
		return false
	}
	if token.User == nil {
		var user model.User
		if err := internal.DB.First(&user, token.UserID).Error; err != nil {
			return false
		}
		token.User = &user
	}
	return token.User.Status == common.UserStatusEnabled && token.User.Quota > 0
}

func normalizeRevokedReason(reason, fallback string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = fallback
	}
	reason = strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, reason)
	if reason == "" {
		reason = fallback
	}
	runes := []rune(reason)
	if len(runes) > 128 {
		reason = string(runes[:128])
	}
	return reason
}

func uniquePositiveUint(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeScopeModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, modelName := range models {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			continue
		}
		if _, ok := seen[modelName]; ok {
			continue
		}
		seen[modelName] = struct{}{}
		result = append(result, modelName)
	}
	return result
}

func normalizeScopeAPITypes(apiTypes []string) []string {
	seen := make(map[string]struct{}, len(apiTypes))
	result := make([]string, 0, len(apiTypes))
	for _, apiType := range apiTypes {
		apiType = normalizeScopeAPIType(apiType)
		if apiType == "" {
			continue
		}
		if _, ok := seen[apiType]; ok {
			continue
		}
		seen[apiType] = struct{}{}
		result = append(result, apiType)
	}
	return result
}

func normalizeScopeAPIType(apiType string) string {
	return strings.ToLower(strings.TrimSpace(apiType))
}

func normalizeScopeChannelGroups(groups []string) []string {
	seen := make(map[string]struct{}, len(groups))
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		result = append(result, group)
	}
	return result
}

func normalizeChannelGroupForScope(group string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return "default"
	}
	return group
}

func normalizeScopeEntryProtocols(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeScopeEntryProtocol(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeScopeEntryProtocol(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateScopeEntryProtocols(values []string) error {
	for _, value := range values {
		switch value {
		case "*", "openai", "anthropic", "gemini":
			continue
		default:
			return errors.New("entry_protocols contains invalid protocol")
		}
	}
	return nil
}

func normalizeScopeIPCIDRs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validateScopeIPCIDRs(values []string) error {
	for _, value := range values {
		if value == "*" {
			continue
		}
		if strings.Contains(value, "/") {
			if _, _, err := net.ParseCIDR(value); err != nil {
				return errors.New("ip_cidrs contains invalid cidr")
			}
			continue
		}
		if net.ParseIP(value) == nil {
			return errors.New("ip_cidrs contains invalid ip")
		}
	}
	return nil
}

func normalizeScopeMethods(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeScopeMethod(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeScopeMethod(value string) string {
	value = strings.TrimSpace(value)
	if value == "*" {
		return value
	}
	parts := strings.Fields(value)
	if len(parts) != 2 {
		return value
	}
	return normalizeRequestMethodScope(parts[0], parts[1])
}

func normalizeRequestMethodScope(method, path string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	if method == "" || path == "" {
		return ""
	}
	return method + " " + path
}

func validateScopeMethods(values []string) error {
	for _, value := range values {
		if value == "*" {
			continue
		}
		parts := strings.Fields(value)
		if len(parts) != 2 || parts[0] == "" || !strings.HasPrefix(parts[1], "/") {
			return errors.New("methods contains invalid method path")
		}
	}
	return nil
}
