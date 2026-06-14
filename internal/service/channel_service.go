package service

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/relay"
)

const (
	keySelectionRoundRobin = "round_robin"
	keySelectionRandom     = "random"
)

type ChannelService struct{}

type circuitBreakerConfig struct {
	autoBan   bool
	threshold int
}

type ChannelUpstreamTarget struct {
	BaseURL string
	APIKey  string
}

// RoutePreference describes request-level routerx.route filters after policy checks.
type RoutePreference struct {
	ChannelGroup     string
	ChannelID        uint
	ChannelName      string
	Provider         string
	DisabledProvider []string
}

type channelUpstreamConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func NewChannelService() *ChannelService {
	return &ChannelService{}
}

// SelectChannel 根据模型名 + 优先级 + 权重 + 健康状态选择最优上游通道。
func (s *ChannelService) SelectChannel(modelName string) (*model.Channel, error) {
	return s.SelectChannelWithRoute(modelName, RoutePreference{})
}

// SelectChannelWithRoute 在管理员允许的候选集中应用请求级 routerx.route 偏好。
func (s *ChannelService) SelectChannelWithRoute(modelName string, route RoutePreference) (*model.Channel, error) {
	candidates, err := s.SelectChannelCandidatesWithRoute(modelName, route)
	if err != nil {
		return nil, err
	}
	bestPriority := candidates[0].Priority
	bestPriorityCandidates := make([]model.Channel, 0, len(candidates))
	for _, channel := range candidates {
		if channel.Priority != bestPriority {
			break
		}
		bestPriorityCandidates = append(bestPriorityCandidates, channel)
	}
	return weightedPick(bestPriorityCandidates), nil
}

// SelectChannelCandidatesWithRoute 返回经过系统过滤和 routerx.route 收窄后的有序候选通道。
func (s *ChannelService) SelectChannelCandidatesWithRoute(modelName string, route RoutePreference) ([]model.Channel, error) {
	modelName = strings.TrimSpace(modelName)
	var channels []model.Channel
	query := internal.DB.Where("status = ?", common.ChannelStatusEnabled)
	breaker := s.circuitBreakerConfig()
	if breaker.autoBan {
		query = query.Where("error_count < ?", breaker.threshold)
	}
	if err := query.Order("priority DESC, idx ASC, error_count ASC, response_ms ASC, id ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	candidates := make([]model.Channel, 0, len(channels))
	for _, channel := range channels {
		if !channelSupportsModel(channel.Models, modelName) {
			continue
		}
		if !channelMatchesRoute(channel, route) {
			continue
		}
		candidates = append(candidates, channel)
	}
	if len(candidates) == 0 {
		return nil, errors.New("no available channel")
	}
	return candidates, nil
}

func (s *ChannelService) circuitBreakerConfig() circuitBreakerConfig {
	cfg := circuitBreakerConfig{
		autoBan:   true,
		threshold: 10,
	}
	if internal.DB == nil {
		return cfg
	}
	settingSvc := NewSettingService()
	if enabled, err := settingSvc.GetBool("relay.error_auto_ban"); err == nil {
		cfg.autoBan = enabled
	}
	if threshold, err := settingSvc.GetInt("relay.error_ban_threshold"); err == nil && threshold > 0 {
		cfg.threshold = threshold
	}
	return cfg
}

// ResolveUpstream 解析某个通道本次请求应该使用的 base_url/api_key。
func (s *ChannelService) ResolveUpstream(channel *model.Channel) (*ChannelUpstreamTarget, error) {
	if channel == nil {
		return nil, errors.New("channel is required")
	}
	if upstreams := decodeUpstreamConfigs(channel.Upstreams); len(upstreams) > 0 {
		upstream := upstreams[randomIndex(len(upstreams))]
		apiKey, err := common.DecryptSecret(upstream.APIKey)
		if err != nil {
			return nil, err
		}
		return &ChannelUpstreamTarget{
			BaseURL: normalizeBaseURL(upstream.BaseURL, channel.Type),
			APIKey:  strings.TrimSpace(apiKey),
		}, nil
	}

	keys := decodeStringSlice(channel.APIKeys)
	if strings.TrimSpace(channel.APIKey) != "" {
		keys = append([]string{channel.APIKey}, keys...)
	}
	if len(keys) == 0 {
		return nil, errors.New("channel api key is required")
	}
	selectedKey := s.selectAPIKey(channel, keys)
	apiKey, err := common.DecryptSecret(selectedKey)
	if err != nil {
		return nil, err
	}
	baseURLs := decodeStringSlice(channel.BaseURLs)
	baseURL := channel.BaseURL
	if len(baseURLs) > 0 {
		baseURL = baseURLs[randomIndex(len(baseURLs))]
	}
	return &ChannelUpstreamTarget{
		BaseURL: normalizeBaseURL(baseURL, channel.Type),
		APIKey:  strings.TrimSpace(apiKey),
	}, nil
}

// List 通道分页列表。
func (s *ChannelService) List(page, pageSize int, channelType, status *int) ([]model.Channel, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.Channel{})
	if channelType != nil {
		query = query.Where("type = ?", *channelType)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var channels []model.Channel
	err := query.Order("idx ASC, priority DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&channels).Error
	return channels, total, err
}

// GetByID 按 ID 获取通道。
func (s *ChannelService) GetByID(id uint) (*model.Channel, error) {
	var channel model.Channel
	if err := internal.DB.First(&channel, id).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

// Create 创建通道。
func (s *ChannelService) Create(channel *model.Channel) error {
	if channel == nil {
		return errors.New("channel is required")
	}
	if err := normalizeChannel(channel, true); err != nil {
		return err
	}
	if err := encryptChannelSecrets(channel); err != nil {
		return err
	}
	return internal.DB.Create(channel).Error
}

// Update 编辑通道。
func (s *ChannelService) Update(id uint, updates map[string]interface{}) error {
	allowed := filterUpdates(
		updates,
		"idx", "type", "name", "models", "base_url", "base_urls", "api_key", "api_keys",
		"key_selection_mode", "upstreams", "model_rewrites", "channel_group", "upstream_options",
		"priority", "weight", "status",
	)
	if len(allowed) == 0 {
		return nil
	}
	if err := normalizeUpdateValues(allowed); err != nil {
		return err
	}
	return internal.DB.Model(&model.Channel{}).Where("id = ?", id).Updates(allowed).Error
}

// Delete 完全删除通道。历史日志保留，但解除 channel_id 引用。
func (s *ChannelService) Delete(id uint) error {
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Log{}).Where("channel_id = ?", id).Update("channel_id", nil).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.Channel{}, id).Error
	})
}

func (s *ChannelService) Disable(id uint) error {
	return internal.DB.Model(&model.Channel{}).Where("id = ?", id).Update("status", common.ChannelStatusDisabled).Error
}

func (s *ChannelService) Enable(id uint) error {
	return internal.DB.Model(&model.Channel{}).Where("id = ?", id).Update("status", common.ChannelStatusEnabled).Error
}

// Test 测试通道连通性：向厂商 API 发探测请求, 记录 response_ms + model_count。
func (s *ChannelService) Test(channelID uint) (bool, int64, int, error) {
	channel, err := s.GetByID(channelID)
	if err != nil {
		return false, 0, 0, err
	}
	start := time.Now()
	models, err := s.FetchUpstreamModels(channelID)
	responseMs := time.Since(start).Milliseconds()
	if err != nil {
		_ = internal.DB.Model(channel).Updates(map[string]interface{}{
			"response_ms": responseMs,
			"error_count": gorm.Expr("error_count + ?", 1),
		}).Error
		return false, responseMs, 0, err
	}
	_ = internal.DB.Model(channel).Updates(map[string]interface{}{
		"response_ms": responseMs,
		"error_count": 0,
	}).Error
	return true, responseMs, len(models), nil
}

func (s *ChannelService) FetchUpstreamModels(channelID uint) ([]string, error) {
	channel, err := s.GetByID(channelID)
	if err != nil {
		return nil, err
	}
	if len(decodeUpstreamConfigs(channel.Upstreams)) > 0 || len(decodeStringSlice(channel.BaseURLs)) > 1 {
		return nil, errors.New("model list fetch is supported only for single upstream url channels")
	}
	adapter, ok := relay.GetAdapter(channel.Type)
	if !ok {
		return nil, errors.New("unsupported channel type")
	}
	target, err := s.ResolveUpstream(channel)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return adapter.GetModelList(ctx, target.BaseURL, target.APIKey)
}

func (s *ChannelService) ListModels() ([]string, error) {
	var channels []model.Channel
	if err := internal.DB.Where("status = ?", common.ChannelStatusEnabled).Order("idx ASC, priority DESC, id ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, channel := range channels {
		for _, modelName := range splitModels(channel.Models) {
			if modelName != "*" && modelName != "" {
				seen[modelName] = struct{}{}
			}
		}
	}
	models := make([]string, 0, len(seen))
	for modelName := range seen {
		models = append(models, modelName)
	}
	sort.Strings(models)
	return models, nil
}

func (s *ChannelService) ApplyModelRewrite(channel *model.Channel, modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if channel == nil || len(channel.ModelRewrites) == 0 || modelName == "" {
		return modelName
	}
	var rewrites map[string]string
	if err := json.Unmarshal(channel.ModelRewrites, &rewrites); err != nil {
		return modelName
	}
	if rewritten := strings.TrimSpace(rewrites[modelName]); rewritten != "" {
		return rewritten
	}
	return modelName
}

func (s *ChannelService) selectAPIKey(channel *model.Channel, keys []string) string {
	if len(keys) == 1 {
		return keys[0]
	}
	if channel.KeySelectionMode == keySelectionRandom {
		return keys[randomIndex(len(keys))]
	}
	idx := channel.KeyCursor % len(keys)
	if idx < 0 {
		idx = 0
	}
	_ = internal.DB.Model(channel).UpdateColumn("key_cursor", gorm.Expr("key_cursor + ?", 1)).Error
	return keys[idx]
}

func normalizeChannel(channel *model.Channel, creating bool) error {
	channel.Name = strings.TrimSpace(channel.Name)
	channel.Models = normalizeModels(channel.Models)
	channel.BaseURL = normalizeBaseURL(channel.BaseURL, channel.Type)
	channel.KeySelectionMode = normalizeKeySelectionMode(channel.KeySelectionMode)
	channel.ChannelGroup = strings.TrimSpace(channel.ChannelGroup)
	channel.BaseURLs = normalizeStringSliceJSON(channel.BaseURLs, true)
	channel.APIKeys = normalizeStringSliceJSON(channel.APIKeys, false)
	channel.Upstreams = normalizeUpstreamsJSON(channel.Upstreams)
	channel.ModelRewrites = normalizeJSONObject(channel.ModelRewrites)
	channel.UpstreamOptions = normalizeJSONObject(channel.UpstreamOptions)
	if channel.Name == "" || channel.Models == "" {
		return errors.New("name and models are required")
	}
	if channel.Weight <= 0 {
		channel.Weight = 1
	}
	if creating && channel.Status == 0 {
		channel.Status = common.ChannelStatusEnabled
	}
	if !hasAnyChannelKey(channel) {
		return errors.New("api_key, api_keys or upstreams.api_key is required")
	}
	return nil
}

func normalizeUpdateValues(updates map[string]interface{}) error {
	if v, ok := updates["name"].(string); ok {
		updates["name"] = strings.TrimSpace(v)
	}
	if v, ok := updates["models"].(string); ok {
		updates["models"] = normalizeModels(v)
	}
	if v, ok := updates["type"].(int); ok && v <= 0 {
		return errors.New("invalid channel type")
	}
	if v, ok := updates["base_url"].(string); ok {
		channelType := 0
		if t, ok := updates["type"].(int); ok {
			channelType = t
		}
		updates["base_url"] = normalizeBaseURL(v, channelType)
	}
	if v, ok := updates["base_urls"].(model.JSONValue); ok {
		updates["base_urls"] = normalizeStringSliceJSON(v, true)
	}
	if v, ok := updates["api_key"].(string); ok {
		v = strings.TrimSpace(v)
		if v == "" {
			delete(updates, "api_key")
		} else {
			encrypted, err := common.EncryptSecret(v)
			if err != nil {
				return err
			}
			updates["api_key"] = encrypted
		}
	}
	if v, ok := updates["api_keys"].(model.JSONValue); ok {
		encrypted, err := encryptAPIKeysJSON(normalizeStringSliceJSON(v, false))
		if err != nil {
			return err
		}
		updates["api_keys"] = encrypted
	}
	if v, ok := updates["key_selection_mode"].(string); ok {
		updates["key_selection_mode"] = normalizeKeySelectionMode(v)
	}
	if v, ok := updates["upstreams"].(model.JSONValue); ok {
		encrypted, err := encryptUpstreamsJSON(normalizeUpstreamsJSON(v))
		if err != nil {
			return err
		}
		updates["upstreams"] = encrypted
	}
	if v, ok := updates["model_rewrites"].(model.JSONValue); ok {
		updates["model_rewrites"] = normalizeJSONObject(v)
	}
	if v, ok := updates["channel_group"].(string); ok {
		updates["channel_group"] = strings.TrimSpace(v)
	}
	if v, ok := updates["upstream_options"].(model.JSONValue); ok {
		updates["upstream_options"] = normalizeJSONObject(v)
	}
	if v, ok := updates["weight"].(int); ok && v <= 0 {
		updates["weight"] = 1
	}
	return nil
}

func encryptChannelSecrets(channel *model.Channel) error {
	if strings.TrimSpace(channel.APIKey) != "" {
		encrypted, err := common.EncryptSecret(strings.TrimSpace(channel.APIKey))
		if err != nil {
			return err
		}
		channel.APIKey = encrypted
	}
	encryptedKeys, err := encryptAPIKeysJSON(channel.APIKeys)
	if err != nil {
		return err
	}
	channel.APIKeys = encryptedKeys
	encryptedUpstreams, err := encryptUpstreamsJSON(channel.Upstreams)
	if err != nil {
		return err
	}
	channel.Upstreams = encryptedUpstreams
	return nil
}

func hasAnyChannelKey(channel *model.Channel) bool {
	if strings.TrimSpace(channel.APIKey) != "" {
		return true
	}
	for _, key := range decodeStringSlice(channel.APIKeys) {
		if strings.TrimSpace(key) != "" {
			return true
		}
	}
	for _, upstream := range decodeUpstreamConfigs(channel.Upstreams) {
		if strings.TrimSpace(upstream.APIKey) != "" {
			return true
		}
	}
	return false
}

func channelSupportsModel(models, modelName string) bool {
	if modelName == "" {
		return false
	}
	for _, candidate := range splitModels(models) {
		if candidate == "*" || candidate == modelName {
			return true
		}
	}
	return false
}

func channelMatchesRoute(channel model.Channel, route RoutePreference) bool {
	if route.ChannelGroup != "" && channel.ChannelGroup != route.ChannelGroup {
		return false
	}
	if route.ChannelID != 0 && channel.ID != route.ChannelID {
		return false
	}
	if route.ChannelName != "" && channel.Name != route.ChannelName {
		return false
	}
	if route.Provider != "" && !channelMatchesProvider(channel.Type, route.Provider) {
		return false
	}
	for _, provider := range route.DisabledProvider {
		if channelMatchesProvider(channel.Type, provider) {
			return false
		}
	}
	return true
}

func channelMatchesProvider(channelType int, provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return channelType == common.ChannelTypeOpenAI
	case "openai-compatible", "openai_compatible", "openai-compat", "openai_compat", "compat":
		return channelType == common.ChannelTypeOpenAICompat
	case "azure", "azure-openai", "azure_openai":
		return channelType == common.ChannelTypeAzure
	case "anthropic", "claude":
		return channelType == common.ChannelTypeClaude
	case "gemini", "google":
		return channelType == common.ChannelTypeGemini
	case "qwen", "dashscope":
		return channelType == common.ChannelTypeQwen
	case "deepseek":
		return channelType == common.ChannelTypeDeepSeek
	case "xai", "grok":
		return channelType == common.ChannelTypeXAI
	case "routerx", "routerx-compatible", "routerx_compatible":
		return channelType == common.ChannelTypeRouterX
	default:
		return false
	}
}

func normalizeModels(models string) string {
	return strings.Join(splitModels(models), ",")
}

func splitModels(models string) []string {
	parts := strings.Split(models, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		modelName := strings.TrimSpace(part)
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

func normalizeBaseURL(baseURL string, channelType int) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" || channelType == 0 {
		return baseURL
	}
	switch channelType {
	case common.ChannelTypeOpenAI, common.ChannelTypeOpenAICompat:
		return "https://api.openai.com"
	case common.ChannelTypeClaude:
		return "https://api.anthropic.com"
	case common.ChannelTypeGemini:
		return "https://generativelanguage.googleapis.com"
	case common.ChannelTypeDeepSeek:
		return "https://api.deepseek.com"
	case common.ChannelTypeQwen:
		return "https://dashscope.aliyuncs.com/compatible-mode"
	case common.ChannelTypeXAI:
		return "https://api.x.ai"
	default:
		return baseURL
	}
}

func normalizeKeySelectionMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == keySelectionRandom {
		return keySelectionRandom
	}
	return keySelectionRoundRobin
}

func normalizeStringSliceJSON(raw model.JSONValue, normalizeURL bool) model.JSONValue {
	values := decodeStringSlice(raw)
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if normalizeURL {
			value = strings.TrimRight(value, "/")
		}
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return model.NewJSONValue(result)
}

func normalizeUpstreamsJSON(raw model.JSONValue) model.JSONValue {
	upstreams := decodeUpstreamConfigs(raw)
	result := make([]channelUpstreamConfig, 0, len(upstreams))
	for _, upstream := range upstreams {
		upstream.BaseURL = strings.TrimRight(strings.TrimSpace(upstream.BaseURL), "/")
		upstream.APIKey = strings.TrimSpace(upstream.APIKey)
		if upstream.BaseURL == "" && upstream.APIKey == "" {
			continue
		}
		result = append(result, upstream)
	}
	return model.NewJSONValue(result)
}

func normalizeJSONObject(raw model.JSONValue) model.JSONValue {
	if len(raw) == 0 || string(raw) == "null" {
		return model.NewJSONValue(map[string]interface{}{})
	}
	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return model.NewJSONValue(map[string]interface{}{})
	}
	return model.NewJSONValue(value)
}

func encryptAPIKeysJSON(raw model.JSONValue) (model.JSONValue, error) {
	keys := decodeStringSlice(raw)
	encrypted := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value, err := common.EncryptSecret(key)
		if err != nil {
			return nil, err
		}
		encrypted = append(encrypted, value)
	}
	return model.NewJSONValue(encrypted), nil
}

func encryptUpstreamsJSON(raw model.JSONValue) (model.JSONValue, error) {
	upstreams := decodeUpstreamConfigs(raw)
	encrypted := make([]channelUpstreamConfig, 0, len(upstreams))
	for _, upstream := range upstreams {
		upstream.BaseURL = strings.TrimRight(strings.TrimSpace(upstream.BaseURL), "/")
		upstream.APIKey = strings.TrimSpace(upstream.APIKey)
		if upstream.BaseURL == "" && upstream.APIKey == "" {
			continue
		}
		if upstream.APIKey != "" {
			value, err := common.EncryptSecret(upstream.APIKey)
			if err != nil {
				return nil, err
			}
			upstream.APIKey = value
		}
		encrypted = append(encrypted, upstream)
	}
	return model.NewJSONValue(encrypted), nil
}

func decodeStringSlice(raw model.JSONValue) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

func decodeUpstreamConfigs(raw model.JSONValue) []channelUpstreamConfig {
	if len(raw) == 0 {
		return nil
	}
	var upstreams []channelUpstreamConfig
	if err := json.Unmarshal(raw, &upstreams); err != nil {
		return nil
	}
	return upstreams
}

func weightedPick(channels []model.Channel) *model.Channel {
	if len(channels) == 1 {
		channel := channels[0]
		return &channel
	}
	total := 0
	for _, channel := range channels {
		weight := channel.Weight
		if weight <= 0 {
			weight = 1
		}
		total += weight
	}
	if total <= 0 {
		channel := channels[0]
		return &channel
	}
	offset := randomIndex(total)
	for _, channel := range channels {
		weight := channel.Weight
		if weight <= 0 {
			weight = 1
		}
		if offset < weight {
			selected := channel
			return &selected
		}
		offset -= weight
	}
	channel := channels[0]
	return &channel
}

func randomIndex(max int) int {
	if max <= 1 {
		return 0
	}
	n, err := crand.Int(crand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(n.Int64())
}
