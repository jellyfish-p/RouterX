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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type TokenService struct{}

func NewTokenService() *TokenService {
	return &TokenService{}
}

type QuotaDeductionResult struct {
	TokenQuotaBefore int64
	TokenQuotaAfter  int64
	UserQuotaBefore  int64
	UserQuotaAfter   int64
	TokenUnlimited   bool
}

var (
	ErrInvalidAPIKey           = errors.New("invalid api key")
	ErrAPIKeyDisabled          = errors.New("api key is disabled")
	ErrAPIKeyExpired           = errors.New("api key is expired")
	ErrAPIUserDisabled         = errors.New("user is disabled")
	ErrInsufficientUserQuota   = errors.New("insufficient user quota")
	ErrInsufficientTokenQuota  = errors.New("insufficient token quota")
	ErrBatchDisableNoFilter    = errors.New("batch disable requires token_ids or user_id")
	ErrBatchExpireNoFilter     = errors.New("batch expire requires token_ids or user_id")
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

var tokenNow = time.Now

const (
	apiKeyAuthCachePrefix = "api_key_auth:"
	apiKeyAuthCacheTTL    = time.Minute

	maxTokenScopeModels         = 200
	maxTokenScopeAPITypes       = 64
	maxTokenScopeChannelGroups  = 64
	maxTokenScopeEntryProtocols = 8
	maxTokenScopeIPCIDRs        = 64
	maxTokenScopeMethods        = 128
	maxTokenMetadataTags        = 20
	maxTokenMetadataValueLength = 128
	maxTokenMetadataNoteLength  = 256
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

type TokenMetadata struct {
	Environment   string   `json:"environment,omitempty"`
	Team          string   `json:"team,omitempty"`
	App           string   `json:"app,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	ExternalID    string   `json:"external_id,omitempty"`
	Note          string   `json:"note,omitempty"`
	PrincipalType string   `json:"principal_type,omitempty"` // 非登录主体类型, 如 service_account
	PrincipalID   string   `json:"principal_id,omitempty"`   // 机器身份或外部主体 ID
	PrincipalName string   `json:"principal_name,omitempty"` // 人类可读主体名称
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

type TokenRiskFilter struct {
	UserID        *uint
	WindowHours   int
	MinErrorCount int64
	LowQuotaBelow int64
	Page          int
	PageSize      int
}

type TokenListFilter struct {
	UserID        *uint
	Status        *int
	Environment   string
	Team          string
	App           string
	Tag           string
	PrincipalType string
	PrincipalID   string
	Page          int
	PageSize      int
}

type TokenRiskItem struct {
	Token               model.Token
	CallCount           int64
	SuccessCount        int64
	ErrorCount          int64
	TotalQuota          int64
	TotalTokens         int64
	LastUsedAt          *time.Time
	LastModel           string
	LastStatus          int
	LastErrorCode       string
	RiskLevel           string
	RiskReasons         []string
	RecommendedAction   string
	RotationRecommended bool
	RotationReason      string
	WindowStart         time.Time
}

type TokenLeakWindowStats struct {
	Token             model.Token
	WindowHours       int
	WindowStart       time.Time
	WindowEnd         time.Time
	CallCount         int64
	SuccessCount      int64
	ErrorCount        int64
	TotalQuota        int64
	TotalTokens       int64
	FirstUsedAt       *time.Time
	LastUsedAt        *time.Time
	Models            []TokenLeakWindowCounter
	ErrorCodes        []TokenLeakWindowCounter
	SourceIPHashes    []TokenLeakWindowCounter
	LastUsedIPHash    string
	LastUserAgentHash string
}

type TokenLeakWindowCounter struct {
	Value      string
	Count      int64
	LastSeenAt *time.Time
}

type BatchDisableTokensInput struct {
	TokenIDs []uint
	UserID   *uint
	Reason   string
}

type BatchExpireTokensInput struct {
	TokenIDs  []uint
	UserID    *uint
	Reason    string
	ExpiredAt time.Time
}

type BatchDisableTokensResult struct {
	MatchedCount  int64
	DisabledCount int64
	Reason        string
	TokenIDs      []uint
}

type BatchExpireTokensResult struct {
	MatchedCount int64
	ExpiredCount int64
	Reason       string
	ExpiredAt    time.Time
	TokenIDs     []uint
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
	if token, err, ok := s.loadAPIKeyAuthCache(hash); ok {
		return token, err
	}
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
	if err := validateAPIKeyAuthToken(&token); err != nil {
		return nil, err
	}
	s.storeAPIKeyAuthCache(hash, &token)
	return &token, nil
}

func apiKeyAuthCacheKey(hash string) string {
	return apiKeyAuthCachePrefix + strings.TrimSpace(hash)
}

func validateAPIKeyAuthToken(token *model.Token) error {
	if token == nil || token.ID == 0 {
		return ErrInvalidAPIKey
	}
	if token.Status != common.TokenStatusEnabled {
		return ErrAPIKeyDisabled
	}
	if token.ExpiredAt != nil && !token.ExpiredAt.After(tokenNow()) {
		return ErrAPIKeyExpired
	}
	if token.User == nil || token.User.Status != common.UserStatusEnabled {
		return ErrAPIUserDisabled
	}
	return nil
}

func (s *TokenService) loadAPIKeyAuthCache(hash string) (*model.Token, error, bool) {
	if internal.RDB == nil || internal.DB == nil {
		return nil, nil, false
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil, nil, false
	}
	cacheKey := apiKeyAuthCacheKey(hash)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := internal.RDB.Get(ctx, cacheKey).Result()
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, nil, false
	}
	tokenID, parseErr := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if parseErr != nil || tokenID == 0 {
		_ = internal.RDB.Del(ctx, cacheKey).Err()
		return nil, nil, false
	}
	var token model.Token
	err = internal.DB.Preload("User").Preload("User.Group").First(&token, uint(tokenID)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		_ = internal.RDB.Del(ctx, cacheKey).Err()
		return nil, nil, false
	}
	if err != nil {
		return nil, nil, false
	}
	if err := validateAPIKeyAuthToken(&token); err != nil {
		_ = internal.RDB.Del(ctx, cacheKey).Err()
		return nil, err, true
	}
	return &token, nil, true
}

func (s *TokenService) storeAPIKeyAuthCache(hash string, token *model.Token) {
	if internal.RDB == nil || token == nil || token.ID == 0 {
		return
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return
	}
	ttl := apiKeyAuthCacheTTL
	if token.ExpiredAt != nil {
		remaining := token.ExpiredAt.Sub(tokenNow())
		if remaining <= 0 {
			return
		}
		if remaining < ttl {
			ttl = remaining
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = internal.RDB.Set(ctx, apiKeyAuthCacheKey(hash), strconv.FormatUint(uint64(token.ID), 10), ttl).Err()
}

func (s *TokenService) invalidateAPIKeyAuthCacheByHashes(hashes ...string) {
	if internal.RDB == nil || len(hashes) == 0 {
		return
	}
	keys := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		hash = strings.TrimSpace(hash)
		if hash == "" {
			continue
		}
		if strings.HasPrefix(hash, "sk-") {
			hash = common.SHA256Hex(hash)
		}
		keys = append(keys, apiKeyAuthCacheKey(hash))
	}
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = internal.RDB.Del(ctx, keys...).Err()
}

func (s *TokenService) invalidateAPIKeyAuthCacheByIDs(ids ...uint) {
	if internal.RDB == nil || internal.DB == nil || len(ids) == 0 {
		return
	}
	ids = uniquePositiveUint(ids)
	if len(ids) == 0 {
		return
	}
	var tokens []model.Token
	if err := internal.DB.Unscoped().Select("id", "key").Where("id IN ?", ids).Find(&tokens).Error; err != nil {
		return
	}
	hashes := make([]string, 0, len(tokens))
	for _, token := range tokens {
		hashes = append(hashes, token.Key)
	}
	s.invalidateAPIKeyAuthCacheByHashes(hashes...)
}

func (s *TokenService) InvalidateUserAPIKeyAuthCache(userID uint) {
	if internal.RDB == nil || internal.DB == nil || userID == 0 {
		return
	}
	var tokens []model.Token
	if err := internal.DB.Unscoped().Select("id", "key").Where("user_id = ?", userID).Find(&tokens).Error; err != nil {
		return
	}
	hashes := make([]string, 0, len(tokens))
	for _, token := range tokens {
		hashes = append(hashes, token.Key)
	}
	s.invalidateAPIKeyAuthCacheByHashes(hashes...)
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
	return s.ListByFilter(TokenListFilter{UserID: userID, Status: status, Page: page, PageSize: pageSize})
}

func (s *TokenService) ListByFilter(filter TokenListFilter) ([]model.Token, int64, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	query := internal.DB.Model(&model.Token{})
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if filter.Status != nil {
		query = query.Where("status = ?", *filter.Status)
	}
	if filter.hasMetadataFilter() {
		var all []model.Token
		if err := query.Order("id DESC").Find(&all).Error; err != nil {
			return nil, 0, err
		}
		filtered := filterTokensByMetadata(all, filter)
		total := int64(len(filtered))
		start := (page - 1) * pageSize
		if start >= len(filtered) {
			return []model.Token{}, total, nil
		}
		end := start + pageSize
		if end > len(filtered) {
			end = len(filtered)
		}
		return filtered[start:end], total, nil
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
func (s *TokenService) Create(userID uint, name string, remainQuota int64, unlimited bool, expiredAt *int64, metadata ...TokenMetadata) (*model.Token, error) {
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
	tokenMetadata, err := optionalTokenMetadata(metadata...)
	if err != nil {
		return nil, err
	}

	var created *model.Token
	var plainKey string
	err = internal.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrAPIUserDisabled
		}
		token, plain, err := createTokenWithPlain(tx, model.Token{
			UserID:       userID,
			Name:         name,
			Status:       common.TokenStatusEnabled,
			ExpiredAt:    expires,
			RemainQuota:  remainQuota,
			Unlimited:    unlimited,
			MetadataJSON: tokenMetadata,
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

func optionalTokenMetadata(items ...TokenMetadata) (model.JSONValue, error) {
	if len(items) == 0 {
		return nil, nil
	}
	metadata, err := NormalizeTokenMetadata(items[0])
	if err != nil {
		return nil, err
	}
	if tokenMetadataEmpty(metadata) {
		return nil, nil
	}
	return model.NewJSONValue(metadata), nil
}

func NormalizeTokenMetadata(input TokenMetadata) (TokenMetadata, error) {
	metadata := TokenMetadata{
		Environment:   strings.ToLower(strings.TrimSpace(input.Environment)),
		Team:          strings.ToLower(strings.TrimSpace(input.Team)),
		App:           strings.ToLower(strings.TrimSpace(input.App)),
		ExternalID:    strings.TrimSpace(input.ExternalID),
		Note:          strings.TrimSpace(input.Note),
		PrincipalType: normalizeTokenPrincipalType(input.PrincipalType),
		PrincipalID:   strings.ToLower(strings.TrimSpace(input.PrincipalID)),
		PrincipalName: strings.TrimSpace(input.PrincipalName),
	}
	if err := validateTokenMetadataValue("environment", metadata.Environment, maxTokenMetadataValueLength); err != nil {
		return TokenMetadata{}, err
	}
	if err := validateTokenMetadataValue("team", metadata.Team, maxTokenMetadataValueLength); err != nil {
		return TokenMetadata{}, err
	}
	if err := validateTokenMetadataValue("app", metadata.App, maxTokenMetadataValueLength); err != nil {
		return TokenMetadata{}, err
	}
	if err := validateTokenMetadataValue("external_id", metadata.ExternalID, maxTokenMetadataValueLength); err != nil {
		return TokenMetadata{}, err
	}
	if err := validateTokenMetadataValue("note", metadata.Note, maxTokenMetadataNoteLength); err != nil {
		return TokenMetadata{}, err
	}
	if err := validateTokenPrincipalType(metadata.PrincipalType); err != nil {
		return TokenMetadata{}, err
	}
	if err := validateTokenMetadataValue("principal_id", metadata.PrincipalID, maxTokenMetadataValueLength); err != nil {
		return TokenMetadata{}, err
	}
	if err := validateTokenMetadataValue("principal_name", metadata.PrincipalName, maxTokenMetadataValueLength); err != nil {
		return TokenMetadata{}, err
	}

	seen := map[string]struct{}{}
	for _, tag := range input.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if err := validateTokenMetadataValue("tags", tag, maxTokenMetadataValueLength); err != nil {
			return TokenMetadata{}, err
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		metadata.Tags = append(metadata.Tags, tag)
		if len(metadata.Tags) > maxTokenMetadataTags {
			return TokenMetadata{}, errors.New("metadata tags exceeds limit")
		}
	}
	return metadata, nil
}

func ParseTokenMetadata(raw model.JSONValue) TokenMetadata {
	if len(raw) == 0 {
		return TokenMetadata{}
	}
	var metadata TokenMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return TokenMetadata{}
	}
	metadata, err := NormalizeTokenMetadata(metadata)
	if err != nil {
		return TokenMetadata{}
	}
	return metadata
}

func validateTokenMetadataValue(field, value string, maxLen int) error {
	if len(value) > maxLen {
		return fmt.Errorf("metadata %s is too long", field)
	}
	if strings.Contains(strings.ToLower(value), "sk-") {
		return fmt.Errorf("metadata %s must not contain api keys", field)
	}
	return nil
}

func tokenMetadataEmpty(metadata TokenMetadata) bool {
	return metadata.Environment == "" && metadata.Team == "" && metadata.App == "" &&
		metadata.ExternalID == "" && metadata.Note == "" && metadata.PrincipalType == "" &&
		metadata.PrincipalID == "" && metadata.PrincipalName == "" && len(metadata.Tags) == 0
}

func (filter TokenListFilter) hasMetadataFilter() bool {
	return strings.TrimSpace(filter.Environment) != "" ||
		strings.TrimSpace(filter.Team) != "" ||
		strings.TrimSpace(filter.App) != "" ||
		strings.TrimSpace(filter.Tag) != "" ||
		strings.TrimSpace(filter.PrincipalType) != "" ||
		strings.TrimSpace(filter.PrincipalID) != ""
}

func filterTokensByMetadata(tokens []model.Token, filter TokenListFilter) []model.Token {
	wantEnvironment := strings.ToLower(strings.TrimSpace(filter.Environment))
	wantTeam := strings.ToLower(strings.TrimSpace(filter.Team))
	wantApp := strings.ToLower(strings.TrimSpace(filter.App))
	wantTag := strings.ToLower(strings.TrimSpace(filter.Tag))
	wantPrincipalType := normalizeTokenPrincipalType(filter.PrincipalType)
	wantPrincipalID := strings.ToLower(strings.TrimSpace(filter.PrincipalID))
	filtered := make([]model.Token, 0, len(tokens))
	for _, token := range tokens {
		metadata := ParseTokenMetadata(token.MetadataJSON)
		if wantEnvironment != "" && metadata.Environment != wantEnvironment {
			continue
		}
		if wantTeam != "" && metadata.Team != wantTeam {
			continue
		}
		if wantApp != "" && metadata.App != wantApp {
			continue
		}
		if wantTag != "" && !metadataHasTag(metadata, wantTag) {
			continue
		}
		if wantPrincipalType != "" && metadata.PrincipalType != wantPrincipalType {
			continue
		}
		if wantPrincipalID != "" && metadata.PrincipalID != wantPrincipalID {
			continue
		}
		filtered = append(filtered, token)
	}
	return filtered
}

func normalizeTokenPrincipalType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateTokenPrincipalType(value string) error {
	switch value {
	case "", "user", "service_account":
		return nil
	default:
		return errors.New("metadata principal_type must be user or service_account")
	}
}

func metadataHasTag(metadata TokenMetadata, tag string) bool {
	for _, item := range metadata.Tags {
		if item == tag {
			return true
		}
	}
	return false
}

func (s *TokenService) RotateForUser(id, userID uint) (*model.Token, *model.Token, error) {
	var oldAfter *model.Token
	var created *model.Token
	var plainKey string
	var oldKey string
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var old model.Token
		if err := tx.Where("id = ? AND user_id = ?", id, userID).First(&old).Error; err != nil {
			return err
		}
		oldKey = old.Key
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
			MetadataJSON:  append(model.JSONValue(nil), old.MetadataJSON...),
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
	s.invalidateAPIKeyAuthCacheByHashes(oldKey)
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
	s.invalidateAPIKeyAuthCacheByHashes(token.Key)
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
	s.invalidateAPIKeyAuthCacheByHashes(token.Key)
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

func (s *TokenService) RecordScopeDeniedLog(token *model.Token, errorMsg, clientIP, userAgent, requestID string) {
	s.recordScopeDeniedLog(token, errorMsg, clientIP, userAgent, requestID, "")
}

func (s *TokenService) RecordScopeDeniedPolicyLog(token *model.Token, errorMsg, clientIP, userAgent, requestID, rejectCode, quotaPrecheck string, scopeResult map[string]interface{}) {
	ctx := ContextWithRelayRequestID(context.Background(), requestID)
	s.recordScopeDeniedLog(token, errorMsg, clientIP, userAgent, requestID, buildRelayPolicyDenySnapshot(ctx, token, rejectCode, quotaPrecheck, scopeResult))
}

func (s *TokenService) RecordRateLimitDeniedPolicyLog(token *model.Token, dimension string, limit, current int64, clientIP, userAgent, requestID string) {
	dimension = strings.ToLower(strings.TrimSpace(dimension))
	if dimension == "" {
		dimension = "unknown"
	}
	scopeResult := map[string]interface{}{
		"api_type":             "not_evaluated",
		"model":                "not_evaluated",
		"channel_group":        "not_evaluated",
		"rate_limit":           "deny",
		"rate_limit_dimension": dimension,
	}
	ctx := ContextWithRelayRequestID(context.Background(), requestID)
	s.recordScopeDeniedLog(token, dimension+" rate limit exceeded", clientIP, userAgent, requestID, buildRelayRateLimitDenySnapshot(ctx, token, dimension, limit, current, scopeResult))
}

func (s *TokenService) recordScopeDeniedLog(token *model.Token, errorMsg, clientIP, userAgent, requestID, policySnapshot string) {
	if token == nil {
		return
	}
	tokenID := token.ID
	_ = NewLogService().Record(&model.Log{
		UserID:         token.UserID,
		TokenID:        &tokenID,
		Model:          "",
		Status:         common.LogStatusFailed,
		QuotaUsed:      0,
		ErrorMsg:       errorMsg,
		IP:             clientIP,
		UserAgent:      userAgent,
		RequestID:      requestID,
		PolicySnapshot: strings.TrimSpace(policySnapshot),
	})
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

func (s *TokenService) GetLeakWindowForUser(id, userID uint, windowHours int) (TokenLeakWindowStats, error) {
	return s.getLeakWindow(id, &userID, windowHours)
}

func (s *TokenService) GetLeakWindow(id uint, windowHours int) (TokenLeakWindowStats, error) {
	return s.getLeakWindow(id, nil, windowHours)
}

func (s *TokenService) getLeakWindow(id uint, userID *uint, windowHours int) (TokenLeakWindowStats, error) {
	windowHours = normalizeLeakWindowHours(windowHours)
	var token model.Token
	query := internal.DB.Where("id = ?", id)
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}
	if err := query.First(&token).Error; err != nil {
		return TokenLeakWindowStats{}, err
	}

	windowEnd := tokenNow()
	windowStart := windowEnd.Add(-time.Duration(windowHours) * time.Hour)
	stats := TokenLeakWindowStats{
		Token:             token,
		WindowHours:       windowHours,
		WindowStart:       windowStart,
		WindowEnd:         windowEnd,
		Models:            []TokenLeakWindowCounter{},
		ErrorCodes:        []TokenLeakWindowCounter{},
		SourceIPHashes:    []TokenLeakWindowCounter{},
		LastUsedIPHash:    token.LastUsedIPHash,
		LastUserAgentHash: token.LastUserAgentHash,
	}

	var logs []model.Log
	if err := internal.DB.
		Where("token_id = ? AND created_at >= ? AND created_at <= ?", id, windowStart, windowEnd).
		Order("created_at ASC, id ASC").
		Find(&logs).Error; err != nil {
		return TokenLeakWindowStats{}, err
	}
	modelCounters := map[string]TokenLeakWindowCounter{}
	errorCounters := map[string]TokenLeakWindowCounter{}
	sourceCounters := map[string]TokenLeakWindowCounter{}
	for i := range logs {
		log := logs[i]
		stats.CallCount++
		if log.Status == common.LogStatusSuccess {
			stats.SuccessCount++
		}
		if log.Status == common.LogStatusFailed {
			stats.ErrorCount++
		}
		stats.TotalQuota += log.QuotaUsed
		stats.TotalTokens += int64(log.TotalTokens)
		if stats.FirstUsedAt == nil {
			first := log.CreatedAt
			stats.FirstUsedAt = &first
		}
		last := log.CreatedAt
		stats.LastUsedAt = &last
		addLeakWindowCounter(modelCounters, log.Model, log.CreatedAt)
		if log.Status == common.LogStatusFailed {
			addLeakWindowCounter(errorCounters, normalizeLogErrorCode(&log), log.CreatedAt)
		}
		addLeakWindowCounter(sourceCounters, usageSourceHash(log.IP), log.CreatedAt)
	}
	stats.Models = sortedLeakWindowCounters(modelCounters)
	stats.ErrorCodes = sortedLeakWindowCounters(errorCounters)
	stats.SourceIPHashes = sortedLeakWindowCounters(sourceCounters)
	return stats, nil
}

func normalizeLeakWindowHours(windowHours int) int {
	switch {
	case windowHours <= 0:
		return 24
	case windowHours > 720:
		return 720
	default:
		return windowHours
	}
}

func addLeakWindowCounter(counters map[string]TokenLeakWindowCounter, value string, seenAt time.Time) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	counter := counters[value]
	counter.Value = value
	counter.Count++
	if counter.LastSeenAt == nil || seenAt.After(*counter.LastSeenAt) {
		last := seenAt
		counter.LastSeenAt = &last
	}
	counters[value] = counter
}

func sortedLeakWindowCounters(counters map[string]TokenLeakWindowCounter) []TokenLeakWindowCounter {
	items := make([]TokenLeakWindowCounter, 0, len(counters))
	for _, counter := range counters {
		items = append(items, counter)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		if items[i].LastSeenAt != nil && items[j].LastSeenAt != nil && !items[i].LastSeenAt.Equal(*items[j].LastSeenAt) {
			return items[i].LastSeenAt.After(*items[j].LastSeenAt)
		}
		return items[i].Value < items[j].Value
	})
	return items
}

func (s *TokenService) ListRisk(filter TokenRiskFilter) ([]TokenRiskItem, int64, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	filter = normalizeTokenRiskFilter(filter)
	windowStart := time.Now().Add(-time.Duration(filter.WindowHours) * time.Hour)
	query := internal.DB.Model(&model.Token{})
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	var tokens []model.Token
	if err := query.Order("id DESC").Find(&tokens).Error; err != nil {
		return nil, 0, err
	}
	if len(tokens) == 0 {
		return nil, 0, nil
	}
	tokenIDs := make([]uint, 0, len(tokens))
	for _, token := range tokens {
		tokenIDs = append(tokenIDs, token.ID)
	}
	aggregates, err := tokenRiskAggregates(tokenIDs, windowStart)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	items := make([]TokenRiskItem, 0, len(tokens))
	for _, token := range tokens {
		agg := aggregates[token.ID]
		reasons := tokenRiskReasons(token, agg, filter, now)
		if len(reasons) == 0 {
			continue
		}
		item := TokenRiskItem{
			Token:               token,
			CallCount:           agg.CallCount,
			SuccessCount:        agg.SuccessCount,
			ErrorCount:          agg.ErrorCount,
			TotalQuota:          agg.TotalQuota,
			TotalTokens:         agg.TotalTokens,
			RiskReasons:         reasons,
			RiskLevel:           tokenRiskLevel(reasons),
			RecommendedAction:   tokenRiskAction(reasons),
			RotationRecommended: tokenRotationRecommended(reasons),
			RotationReason:      tokenRotationReason(reasons),
			WindowStart:         windowStart,
		}
		if last, ok, err := tokenRiskLastLog(token.ID, windowStart); err != nil {
			return nil, 0, err
		} else if ok {
			lastUsedAt := last.CreatedAt
			item.LastUsedAt = &lastUsedAt
			item.LastModel = last.Model
			item.LastStatus = last.Status
			item.LastErrorCode = last.ErrorCode
		} else {
			item.LastUsedAt = token.LastUsedAt
			item.LastModel = token.LastModel
			item.LastErrorCode = token.LastErrorCode
		}
		items = append(items, item)
	}
	sortTokenRiskItems(items)
	total := int64(len(items))
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []TokenRiskItem{}, total, nil
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

type tokenRiskAggregate struct {
	TokenID      uint
	CallCount    int64
	SuccessCount int64
	ErrorCount   int64
	TotalQuota   int64
	TotalTokens  int64
}

func normalizeTokenRiskFilter(filter TokenRiskFilter) TokenRiskFilter {
	if filter.WindowHours <= 0 {
		filter.WindowHours = 24
	}
	if filter.WindowHours > 24*30 {
		filter.WindowHours = 24 * 30
	}
	if filter.MinErrorCount <= 0 {
		filter.MinErrorCount = 3
	}
	if filter.LowQuotaBelow <= 0 {
		filter.LowQuotaBelow = 100
	}
	return filter
}

func tokenRiskAggregates(tokenIDs []uint, windowStart time.Time) (map[uint]tokenRiskAggregate, error) {
	aggregates := map[uint]tokenRiskAggregate{}
	var rows []tokenRiskAggregate
	err := internal.DB.Model(&model.Log{}).
		Where("token_id IN ? AND created_at >= ?", tokenIDs, windowStart).
		Select(
			"token_id, COUNT(*) AS call_count, "+
				"COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS success_count, "+
				"COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS error_count, "+
				"COALESCE(SUM(quota_used), 0) AS total_quota, "+
				"COALESCE(SUM(total_tokens), 0) AS total_tokens",
			common.LogStatusSuccess,
			common.LogStatusFailed,
		).
		Group("token_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		aggregates[row.TokenID] = row
	}
	return aggregates, nil
}

func tokenRiskLastLog(tokenID uint, windowStart time.Time) (model.Log, bool, error) {
	var last model.Log
	err := internal.DB.
		Where("token_id = ? AND created_at >= ?", tokenID, windowStart).
		Order("created_at DESC, id DESC").
		First(&last).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Log{}, false, nil
	}
	if err != nil {
		return model.Log{}, false, err
	}
	return last, true, nil
}

func tokenRiskReasons(token model.Token, agg tokenRiskAggregate, filter TokenRiskFilter, now time.Time) []string {
	var reasons []string
	revokedReason := strings.ToLower(strings.TrimSpace(token.RevokedReason))
	if strings.Contains(revokedReason, "leak") || strings.Contains(revokedReason, "public") {
		reasons = append(reasons, "leak_reported")
	}
	if token.Status != common.TokenStatusEnabled {
		reasons = append(reasons, "disabled")
	}
	if token.ExpiredAt != nil && !token.ExpiredAt.After(now) {
		reasons = append(reasons, "expired")
	}
	if token.RemainQuota >= 0 && token.RemainQuota <= filter.LowQuotaBelow {
		reasons = append(reasons, "low_quota")
	}
	if agg.ErrorCount >= filter.MinErrorCount {
		reasons = append(reasons, "error_spike")
	}
	if token.LastErrorCode != "" && !containsString(reasons, "error_spike") {
		reasons = append(reasons, "recent_error")
	}
	return reasons
}

func tokenRiskLevel(reasons []string) string {
	for _, reason := range reasons {
		if reason == "leak_reported" || reason == "error_spike" {
			return "high"
		}
	}
	if len(reasons) > 0 {
		return "medium"
	}
	return "low"
}

func tokenRiskAction(reasons []string) string {
	switch {
	case containsString(reasons, "leak_reported"):
		return "rotate_key"
	case containsString(reasons, "error_spike") || containsString(reasons, "recent_error"):
		return "review_errors"
	case containsString(reasons, "low_quota"):
		return "top_up_or_disable"
	case containsString(reasons, "expired"):
		return "review_expiration"
	case containsString(reasons, "disabled"):
		return "review_disabled_key"
	default:
		return "review"
	}
}

func tokenRotationRecommended(reasons []string) bool {
	return tokenRotationReason(reasons) != ""
}

func tokenRotationReason(reasons []string) string {
	if containsString(reasons, "leak_reported") {
		return "leak_reported"
	}
	return ""
}

func sortTokenRiskItems(items []TokenRiskItem) {
	rank := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(items, func(i, j int) bool {
		leftRank := rank[items[i].RiskLevel]
		rightRank := rank[items[j].RiskLevel]
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if items[i].ErrorCount != items[j].ErrorCount {
			return items[i].ErrorCount > items[j].ErrorCount
		}
		if items[i].Token.RemainQuota != items[j].Token.RemainQuota {
			return items[i].Token.RemainQuota < items[j].Token.RemainQuota
		}
		return items[i].Token.ID > items[j].Token.ID
	})
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	s.invalidateAPIKeyAuthCacheByIDs(disabledIDs...)
	return BatchDisableTokensResult{
		MatchedCount:  int64(len(matched)),
		DisabledCount: disabledCount,
		Reason:        reason,
		TokenIDs:      disabledIDs,
	}, matched, nil
}

func (s *TokenService) BatchExpire(input BatchExpireTokensInput) (BatchExpireTokensResult, []model.Token, error) {
	tokenIDs := uniquePositiveUint(input.TokenIDs)
	if len(tokenIDs) == 0 && input.UserID == nil {
		return BatchExpireTokensResult{}, nil, ErrBatchExpireNoFilter
	}
	reason := normalizeRevokedReason(input.Reason, "admin_batch_expire")
	expiredAt := input.ExpiredAt
	if expiredAt.IsZero() {
		expiredAt = time.Now()
	}
	var matched []model.Token
	var expiredIDs []uint
	var expiredCount int64
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
			if token.ExpiredAt == nil || token.ExpiredAt.After(expiredAt) {
				expiredIDs = append(expiredIDs, token.ID)
			}
		}
		if len(expiredIDs) == 0 {
			return nil
		}
		res := tx.Model(&model.Token{}).
			Where("id IN ?", expiredIDs).
			Update("expired_at", expiredAt)
		if res.Error != nil {
			return res.Error
		}
		expiredCount = res.RowsAffected
		return nil
	})
	if err != nil {
		return BatchExpireTokensResult{}, nil, err
	}
	s.invalidateAPIKeyAuthCacheByIDs(expiredIDs...)
	return BatchExpireTokensResult{
		MatchedCount: int64(len(matched)),
		ExpiredCount: expiredCount,
		Reason:       reason,
		ExpiredAt:    expiredAt,
		TokenIDs:     expiredIDs,
	}, matched, nil
}

// Update 编辑 Token。
func (s *TokenService) Update(id uint, updates map[string]interface{}) error {
	allowed := filterUpdates(updates, "name", "status", "expired_at", "metadata_json")
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
	err := internal.DB.Model(&model.Token{}).Where("id = ?", id).Updates(allowed).Error
	if err == nil {
		s.invalidateAPIKeyAuthCacheByIDs(id)
	}
	return err
}

// Delete 软删除 Token。
func (s *TokenService) Delete(id uint) error {
	err := internal.DB.Delete(&model.Token{}, id).Error
	if err == nil {
		s.invalidateAPIKeyAuthCacheByIDs(id)
	}
	return err
}

// DeductQuota 扣减 Token / User 额度。
// 先扣 Token.RemainQuota, Token.remain_quota=-1 时只扣 User.Quota。
func (s *TokenService) DeductQuota(tokenID uint, quota int64) error {
	_, err := s.DeductQuotaWithSnapshot(tokenID, quota)
	return err
}

func (s *TokenService) DeductQuotaWithSnapshot(tokenID uint, quota int64) (QuotaDeductionResult, error) {
	result := QuotaDeductionResult{}
	if quota <= 0 {
		return result, nil
	}
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var token model.Token
		if err := tx.Preload("User").First(&token, tokenID).Error; err != nil {
			return err
		}
		result.TokenUnlimited = token.Unlimited || token.RemainQuota == common.QuotaUnlimited
		result.TokenQuotaBefore = token.RemainQuota
		result.TokenQuotaAfter = token.RemainQuota
		if result.TokenUnlimited {
			result.TokenQuotaBefore = common.QuotaUnlimited
			result.TokenQuotaAfter = common.QuotaUnlimited
		}
		if token.User != nil {
			result.UserQuotaBefore = token.User.Quota
			result.UserQuotaAfter = token.User.Quota - quota
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
		result.TokenQuotaAfter = token.RemainQuota - quota
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
	if err != nil {
		return result, err
	}
	s.invalidateAPIKeyAuthCacheByIDs(tokenID)
	return result, nil
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
