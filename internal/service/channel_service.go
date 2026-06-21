package service

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
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

	channelCandidateCacheRedisHash                = "routing:channel_candidate_cache"
	channelCandidateCacheRedisField               = "snapshot"
	channelCandidateCacheInvalidationRedisChannel = "routing:channel_candidate_cache:invalidate"

	routeFilterReasonAccessDenied    = "access_denied"
	routeFilterReasonDisabled        = "disabled"
	routeFilterReasonHealthBlocked   = "health_blocked"
	routeFilterReasonModelMismatch   = "model_mismatch"
	routeFilterReasonRoutePreference = "route_preference"
)

type ChannelService struct {
	candidateCacheMu sync.Mutex
	candidateCache   channelCandidateCache
}

type circuitBreakerConfig struct {
	autoBan   bool
	threshold int
	cooldown  time.Duration
}

type breakerProbeConfig struct {
	enabled   bool
	interval  time.Duration
	batchSize int
}

type ChannelProbeSummary struct {
	Checked   int
	Succeeded int
	Failed    int
}

// ChannelHealthSummary is the operator-facing view of status plus breaker state.
type ChannelHealthSummary struct {
	Status                   string
	Reason                   string
	CooldownRemainingSeconds int64
}

type RouteSelectionFacts struct {
	FilteredReasons map[string]int
	BreakerSnapshot map[string]interface{}
}

type channelCandidateCache struct {
	loaded    bool
	version   int
	expiresAt time.Time
	channels  []model.Channel
}

type channelCandidateCacheConfig struct {
	enabled bool
	preload bool
	version int
	ttl     time.Duration
}

type channelCandidateCacheRedisSnapshot struct {
	Version       int             `json:"version"`
	ExpiresAtUnix int64           `json:"expires_at_unix"`
	Channels      []model.Channel `json:"channels"`
}

type channelCandidateCacheInvalidationMessage struct {
	Version    int   `json:"version"`
	SentAtUnix int64 `json:"sent_at_unix"`
}

type ChannelUpstreamTarget struct {
	BaseURL       string
	APIKey        string
	BaseURLSource string
	BaseURLIndex  int
}

type ChannelSecretRotationResult struct {
	ScannedChannels int
	RotatedChannels int
	ScannedSettings int
	RotatedSettings int
	RotatedSecrets  int
	SkippedSecrets  int
}

// RoutePreference describes optional internal route filters after policy checks.
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

// SelectChannelWithRoute 在管理员允许的候选集中应用内部路由过滤条件。
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

// SelectChannelCandidatesWithRoute 返回经过系统过滤和内部路由过滤后的有序候选通道。
func (s *ChannelService) SelectChannelCandidatesWithRoute(modelName string, route RoutePreference) ([]model.Channel, error) {
	candidates, _, err := s.SelectChannelCandidatesWithRouteFacts(modelName, route)
	return candidates, err
}

// SelectChannelCandidatesWithRouteFacts 同时返回候选和按首个过滤原因汇总的数量，供路由快照解释选择过程。
func (s *ChannelService) SelectChannelCandidatesWithRouteFacts(modelName string, route RoutePreference) ([]model.Channel, map[string]int, error) {
	candidates, facts, err := s.SelectChannelCandidatesWithRouteDetailedFacts(modelName, route)
	return candidates, facts.FilteredReasons, err
}

// SelectChannelCandidatesWithRouteDetailedFacts 返回候选以及用于路由/策略快照的结构化过滤事实。
func (s *ChannelService) SelectChannelCandidatesWithRouteDetailedFacts(modelName string, route RoutePreference) ([]model.Channel, RouteSelectionFacts, error) {
	modelName = strings.TrimSpace(modelName)
	breaker := s.circuitBreakerConfig()
	channels, err := s.channelsForCandidateSelection()
	if err != nil {
		return nil, RouteSelectionFacts{}, err
	}
	now := time.Now()
	facts := RouteSelectionFacts{FilteredReasons: map[string]int{}}
	candidates := make([]model.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			addRouteFilterReason(facts.FilteredReasons, routeFilterReasonDisabled, 1)
			continue
		}
		if channelHealthBlocked(channel, breaker, now) {
			addRouteFilterReason(facts.FilteredReasons, routeFilterReasonHealthBlocked, 1)
			facts.addHealthBlockedChannel(channel, breaker, now)
			continue
		}
		if !channelSupportsModel(channel.Models, modelName) {
			addRouteFilterReason(facts.FilteredReasons, routeFilterReasonModelMismatch, 1)
			continue
		}
		if !channelMatchesRoute(channel, route) {
			addRouteFilterReason(facts.FilteredReasons, routeFilterReasonRoutePreference, 1)
			continue
		}
		candidates = append(candidates, channel)
	}
	if len(candidates) == 0 {
		return nil, facts, errors.New("no available channel")
	}
	return candidates, facts, nil
}

func (f *RouteSelectionFacts) addHealthBlockedChannel(channel model.Channel, breaker circuitBreakerConfig, now time.Time) {
	if f == nil {
		return
	}
	if f.BreakerSnapshot == nil {
		f.BreakerSnapshot = map[string]interface{}{
			"decision":              "deny",
			"reason":                routeFilterReasonHealthBlocked,
			"auto_ban":              breaker.autoBan,
			"threshold":             breaker.threshold,
			"cooldown_seconds":      int64(breaker.cooldown.Seconds()),
			"blocked_channel_count": int64(0),
			"blocked_channels":      []map[string]interface{}{},
		}
	}
	blockedCount, _ := f.BreakerSnapshot["blocked_channel_count"].(int64)
	blockedCount++
	f.BreakerSnapshot["blocked_channel_count"] = blockedCount

	blockedChannels, _ := f.BreakerSnapshot["blocked_channels"].([]map[string]interface{})
	if len(blockedChannels) >= 10 {
		f.BreakerSnapshot["blocked_channels_truncated"] = true
		return
	}
	summary := map[string]interface{}{
		"channel_id":    channel.ID,
		"provider":      channelProviderName(channel.Type),
		"channel_group": normalizeChannelGroupName(channel.ChannelGroup),
		"error_count":   channel.ErrorCount,
	}
	if !channel.UpdatedAt.IsZero() {
		summary["updated_at"] = channel.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if breaker.cooldown > 0 && !channel.UpdatedAt.IsZero() {
		remaining := breaker.cooldown - now.Sub(channel.UpdatedAt)
		if remaining < 0 {
			remaining = 0
		}
		summary["cooldown_remaining_seconds"] = int64(remaining.Seconds())
	}
	f.BreakerSnapshot["blocked_channels"] = append(blockedChannels, summary)
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
	if cooldownSeconds, err := settingSvc.GetInt("relay.error_ban_cooldown_seconds"); err == nil && cooldownSeconds > 0 {
		cfg.cooldown = time.Duration(cooldownSeconds) * time.Second
	}
	return cfg
}

func channelHealthBlocked(channel model.Channel, breaker circuitBreakerConfig, now time.Time) bool {
	if !breaker.autoBan || channel.ErrorCount < breaker.threshold {
		return false
	}
	if breaker.cooldown <= 0 || channel.UpdatedAt.IsZero() {
		return true
	}
	return now.Sub(channel.UpdatedAt) < breaker.cooldown
}

func (s *ChannelService) ChannelHealthSummary(channel model.Channel) ChannelHealthSummary {
	breaker := s.circuitBreakerConfig()
	now := time.Now()
	if channel.Status != common.ChannelStatusEnabled {
		return ChannelHealthSummary{Status: "disabled", Reason: "manual_status"}
	}
	if !breaker.autoBan || channel.ErrorCount < breaker.threshold {
		return ChannelHealthSummary{Status: "healthy", Reason: "ok"}
	}
	if channelHealthBlocked(channel, breaker, now) {
		return ChannelHealthSummary{
			Status:                   "tripped",
			Reason:                   "error_count_threshold",
			CooldownRemainingSeconds: channelCooldownRemainingSeconds(channel, breaker, now),
		}
	}
	return ChannelHealthSummary{Status: "probing", Reason: "cooldown_elapsed"}
}

func channelCooldownRemainingSeconds(channel model.Channel, breaker circuitBreakerConfig, now time.Time) int64 {
	if breaker.cooldown <= 0 || channel.UpdatedAt.IsZero() {
		return 0
	}
	remaining := breaker.cooldown - now.Sub(channel.UpdatedAt)
	if remaining <= 0 {
		return 0
	}
	seconds := int64(remaining.Seconds())
	if seconds == 0 {
		return 1
	}
	return seconds
}

func (s *ChannelService) breakerProbeConfig() breakerProbeConfig {
	cfg := breakerProbeConfig{
		enabled:   true,
		interval:  time.Minute,
		batchSize: 20,
	}
	if internal.DB == nil {
		return cfg
	}
	settingSvc := NewSettingService()
	if enabled, err := settingSvc.GetBool("relay.error_probe_enabled"); err == nil {
		cfg.enabled = enabled
	}
	if intervalSeconds, err := settingSvc.GetInt("relay.error_probe_interval_seconds"); err == nil {
		cfg.interval = time.Duration(intervalSeconds) * time.Second
	}
	if batchSize, err := settingSvc.GetInt("relay.error_probe_batch_size"); err == nil && batchSize > 0 {
		cfg.batchSize = batchSize
	}
	return cfg
}

// ProbeTrippedChannelsOnce tests channels whose breaker cooldown has elapsed.
// It deliberately reuses Test so manual and background probes share accounting.
func (s *ChannelService) ProbeTrippedChannelsOnce(ctx context.Context, limit int) (ChannelProbeSummary, error) {
	summary := ChannelProbeSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	if internal.DB == nil {
		return summary, nil
	}
	breaker := s.circuitBreakerConfig()
	probeCfg := s.breakerProbeConfig()
	if !probeCfg.enabled || !breaker.autoBan || breaker.threshold <= 0 || breaker.cooldown <= 0 {
		return summary, nil
	}
	if limit <= 0 {
		limit = probeCfg.batchSize
	}
	cutoff := time.Now().Add(-breaker.cooldown)
	var channels []model.Channel
	if err := internal.DB.
		Where("status = ? AND error_count >= ? AND updated_at <= ?", common.ChannelStatusEnabled, breaker.threshold, cutoff).
		Order("updated_at ASC, error_count DESC, id ASC").
		Limit(limit).
		Find(&channels).Error; err != nil {
		return summary, err
	}
	for _, channel := range channels {
		select {
		case <-ctx.Done():
			return summary, ctx.Err()
		default:
		}
		summary.Checked++
		ok, _, _, err := s.Test(channel.ID)
		if ok && err == nil {
			summary.Succeeded++
			recordChannelProbeResult(true)
			continue
		}
		summary.Failed++
		recordChannelProbeResult(false)
	}
	return summary, nil
}

func (s *ChannelService) StartBreakerProbeWorker(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		for {
			cfg := s.breakerProbeConfig()
			interval := cfg.interval
			if interval <= 0 {
				interval = time.Minute
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			cfg = s.breakerProbeConfig()
			if !cfg.enabled || cfg.interval <= 0 {
				continue
			}
			_, _ = s.ProbeTrippedChannelsOnce(ctx, cfg.batchSize)
		}
	}()
}

func (s *ChannelService) StartCandidateCacheInvalidationSubscriber(ctx context.Context) {
	if s == nil || internal.RDB == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		pubsub := internal.RDB.Subscribe(ctx, channelCandidateCacheInvalidationRedisChannel)
		defer pubsub.Close()
		messages := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}
				if msg != nil {
					s.handleCandidateCacheInvalidationMessage(msg.Payload)
				}
			}
		}
	}()
}

func (s *ChannelService) channelsForCandidateSelection() ([]model.Channel, error) {
	cfg := s.channelCandidateCacheConfig()
	if !cfg.enabled {
		return loadChannelsForCandidateSelection()
	}

	now := time.Now()
	s.candidateCacheMu.Lock()
	defer s.candidateCacheMu.Unlock()
	if s.candidateCache.loaded &&
		s.candidateCache.version == cfg.version &&
		(cfg.ttl == 0 || now.Before(s.candidateCache.expiresAt)) {
		return cloneChannels(s.candidateCache.channels), nil
	}
	if channels, expiresAt, ok := s.loadCandidateCacheFromRedis(cfg, now); ok {
		s.candidateCache = channelCandidateCache{
			loaded:    true,
			version:   cfg.version,
			expiresAt: expiresAt,
			channels:  cloneChannels(channels),
		}
		return cloneChannels(channels), nil
	}

	channels, err := loadChannelsForCandidateSelection()
	if err != nil {
		return nil, err
	}
	s.candidateCache = channelCandidateCache{
		loaded:    true,
		version:   cfg.version,
		expiresAt: now.Add(cfg.ttl),
		channels:  cloneChannels(channels),
	}
	s.storeCandidateCacheInRedis(cfg, now, channels)
	return channels, nil
}

func (s *ChannelService) channelCandidateCacheConfig() channelCandidateCacheConfig {
	cfg := channelCandidateCacheConfig{
		enabled: true,
		preload: true,
		version: 1,
		ttl:     60 * time.Second,
	}
	if internal.DB == nil {
		return cfg
	}
	settingSvc := NewSettingService()
	if enabled, err := settingSvc.GetBool("routing.channel_cache.enabled"); err == nil {
		cfg.enabled = enabled
	}
	if preload, err := settingSvc.GetBool("routing.channel_cache.preload"); err == nil {
		cfg.preload = preload
	}
	if ttlSeconds, err := settingSvc.GetInt("routing.channel_cache.ttl_seconds"); err == nil && ttlSeconds >= 0 {
		cfg.ttl = time.Duration(ttlSeconds) * time.Second
	}
	if version, err := settingSvc.GetInt("routing.channel_cache.version"); err == nil && version > 0 {
		cfg.version = version
	}
	return cfg
}

func (s *ChannelService) PreloadCandidateCache() error {
	if s == nil || internal.DB == nil {
		return nil
	}
	cfg := s.channelCandidateCacheConfig()
	if !cfg.enabled || !cfg.preload {
		return nil
	}
	channels, err := loadChannelsForCandidateSelection()
	if err != nil {
		return err
	}
	now := time.Now()
	s.candidateCacheMu.Lock()
	defer s.candidateCacheMu.Unlock()
	s.candidateCache = channelCandidateCache{
		loaded:    true,
		version:   cfg.version,
		expiresAt: now.Add(cfg.ttl),
		channels:  cloneChannels(channels),
	}
	s.storeCandidateCacheInRedis(cfg, now, channels)
	return nil
}

func (s *ChannelService) loadCandidateCacheFromRedis(cfg channelCandidateCacheConfig, now time.Time) ([]model.Channel, time.Time, bool) {
	if internal.RDB == nil {
		return nil, time.Time{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := internal.RDB.HGet(ctx, channelCandidateCacheRedisHash, channelCandidateCacheRedisField).Result()
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, time.Time{}, false
	}
	var snapshot channelCandidateCacheRedisSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, time.Time{}, false
	}
	if snapshot.Version != cfg.version {
		return nil, time.Time{}, false
	}
	expiresAt := time.Unix(snapshot.ExpiresAtUnix, 0)
	if cfg.ttl > 0 && (snapshot.ExpiresAtUnix <= 0 || !now.Before(expiresAt)) {
		return nil, time.Time{}, false
	}
	if cfg.ttl == 0 {
		expiresAt = time.Time{}
	}
	return cloneChannels(snapshot.Channels), expiresAt, true
}

func (s *ChannelService) storeCandidateCacheInRedis(cfg channelCandidateCacheConfig, now time.Time, channels []model.Channel) {
	if internal.RDB == nil {
		return
	}
	expiresAtUnix := int64(0)
	if cfg.ttl > 0 {
		expiresAtUnix = now.Add(cfg.ttl).Unix()
	}
	raw, err := json.Marshal(channelCandidateCacheRedisSnapshot{
		Version:       cfg.version,
		ExpiresAtUnix: expiresAtUnix,
		Channels:      cloneChannels(channels),
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = internal.RDB.HSet(ctx, channelCandidateCacheRedisHash, channelCandidateCacheRedisField, string(raw)).Err()
}

func (s *ChannelService) publishCandidateCacheInvalidation(version int) {
	if internal.RDB == nil || version <= 0 {
		return
	}
	raw, err := json.Marshal(channelCandidateCacheInvalidationMessage{
		Version:    version,
		SentAtUnix: time.Now().Unix(),
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = internal.RDB.Publish(ctx, channelCandidateCacheInvalidationRedisChannel, string(raw)).Err()
}

func (s *ChannelService) handleCandidateCacheInvalidationMessage(raw string) {
	if s == nil || strings.TrimSpace(raw) == "" {
		return
	}
	var message channelCandidateCacheInvalidationMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil || message.Version <= 0 {
		return
	}
	s.candidateCacheMu.Lock()
	defer s.candidateCacheMu.Unlock()
	if !s.candidateCache.loaded {
		return
	}
	if message.Version >= s.candidateCache.version {
		s.candidateCache = channelCandidateCache{}
	}
}

func loadChannelsForCandidateSelection() ([]model.Channel, error) {
	var channels []model.Channel
	err := internal.DB.Order("priority DESC, idx ASC, error_count ASC, response_ms ASC, id ASC").Find(&channels).Error
	return channels, err
}

func cloneChannels(channels []model.Channel) []model.Channel {
	if len(channels) == 0 {
		return nil
	}
	cloned := make([]model.Channel, len(channels))
	copy(cloned, channels)
	return cloned
}

func (s *ChannelService) InvalidateCandidateCache() {
	if s == nil {
		return
	}
	s.candidateCacheMu.Lock()
	defer s.candidateCacheMu.Unlock()
	s.candidateCache = channelCandidateCache{}
}

func (s *ChannelService) touchCandidateCacheVersion() {
	s.InvalidateCandidateCache()
	if internal.DB == nil {
		return
	}
	settingSvc := NewSettingService()
	version, err := settingSvc.GetInt("routing.channel_cache.version")
	if err != nil || version < 1 {
		version = 1
	}
	nextVersion := version + 1
	if err := settingSvc.Set("routing.channel_cache.version", strconv.Itoa(nextVersion)); err == nil {
		s.publishCandidateCacheInvalidation(nextVersion)
	}
	// Preload is an optimization; request-time cache reload remains authoritative if warmup fails.
	_ = s.PreloadCandidateCache()
}

// ResolveUpstream 解析某个通道本次请求应该使用的 base_url/api_key。
func (s *ChannelService) ResolveUpstream(channel *model.Channel) (*ChannelUpstreamTarget, error) {
	if channel == nil {
		return nil, errors.New("channel is required")
	}
	if upstreams := decodeUpstreamConfigs(channel.Upstreams); len(upstreams) > 0 {
		upstreamIndex := randomIndex(len(upstreams))
		upstream := upstreams[upstreamIndex]
		apiKey, err := common.DecryptSecret(upstream.APIKey)
		if err != nil {
			return nil, err
		}
		return &ChannelUpstreamTarget{
			BaseURL:       normalizeBaseURL(upstream.BaseURL, channel.Type),
			APIKey:        strings.TrimSpace(apiKey),
			BaseURLSource: "upstreams",
			BaseURLIndex:  upstreamIndex,
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
	baseURLSource := "base_url"
	baseURLIndex := -1
	if len(baseURLs) > 0 {
		baseURLIndex = randomIndex(len(baseURLs))
		baseURL = baseURLs[baseURLIndex]
		baseURLSource = "base_urls"
	}
	return &ChannelUpstreamTarget{
		BaseURL:       normalizeBaseURL(baseURL, channel.Type),
		APIKey:        strings.TrimSpace(apiKey),
		BaseURLSource: baseURLSource,
		BaseURLIndex:  baseURLIndex,
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
	if err := internal.DB.Create(channel).Error; err != nil {
		return err
	}
	s.touchCandidateCacheVersion()
	return nil
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
	if err := internal.DB.Model(&model.Channel{}).Where("id = ?", id).Updates(allowed).Error; err != nil {
		return err
	}
	s.touchCandidateCacheVersion()
	return nil
}

// Delete 完全删除通道。历史日志保留，但解除 channel_id 引用。
func (s *ChannelService) Delete(id uint) error {
	if err := internal.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Log{}).Where("channel_id = ?", id).Update("channel_id", nil).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.Channel{}, id).Error
	}); err != nil {
		return err
	}
	s.touchCandidateCacheVersion()
	return nil
}

func (s *ChannelService) Disable(id uint) error {
	if err := internal.DB.Model(&model.Channel{}).Where("id = ?", id).Update("status", common.ChannelStatusDisabled).Error; err != nil {
		return err
	}
	s.touchCandidateCacheVersion()
	return nil
}

func (s *ChannelService) Enable(id uint) error {
	if err := internal.DB.Model(&model.Channel{}).Where("id = ?", id).Update("status", common.ChannelStatusEnabled).Error; err != nil {
		return err
	}
	s.touchCandidateCacheVersion()
	return nil
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
		s.InvalidateCandidateCache()
		return false, responseMs, 0, err
	}
	_ = internal.DB.Model(channel).Updates(map[string]interface{}{
		"response_ms": responseMs,
		"error_count": 0,
	}).Error
	s.InvalidateCandidateCache()
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

func (s *ChannelService) RotateEncryptedSecrets(previousKey string) (ChannelSecretRotationResult, error) {
	result := ChannelSecretRotationResult{}
	previousKey = strings.TrimSpace(previousKey)
	currentKey := strings.TrimSpace(os.Getenv("ENCRYPTION_KEY"))
	rotatedSettingValues := map[string]string{}
	if previousKey == "" {
		return result, errors.New("previous_encryption_key is required")
	}
	if currentKey == "" {
		return result, errors.New("ENCRYPTION_KEY is required")
	}
	if previousKey == currentKey {
		return result, errors.New("previous_encryption_key must differ from ENCRYPTION_KEY")
	}

	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := tx.Select("id", "api_key", "api_keys", "upstreams").Find(&channels).Error; err != nil {
			return err
		}
		result.ScannedChannels = len(channels)
		for _, channel := range channels {
			updates, counts, err := rotateChannelSecretFields(channel, previousKey, currentKey)
			if err != nil {
				return fmt.Errorf("channel %d secret rotation failed: %w", channel.ID, err)
			}
			result.RotatedSecrets += counts.rotated
			result.SkippedSecrets += counts.skipped
			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&model.Channel{}).Where("id = ?", channel.ID).Updates(updates).Error; err != nil {
				return err
			}
			result.RotatedChannels++
		}
		if err := rotateEncryptedProviderSettings(tx, previousKey, currentKey, &result, rotatedSettingValues); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		refreshRotatedSettingCache(rotatedSettingValues)
	}
	return result, err
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
	channel.ChannelGroup = normalizeChannelGroupName(channel.ChannelGroup)
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
		updates["channel_group"] = normalizeChannelGroupName(v)
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

type channelSecretRotationCounts struct {
	rotated int
	skipped int
}

func rotateChannelSecretFields(channel model.Channel, previousKey, currentKey string) (map[string]interface{}, channelSecretRotationCounts, error) {
	updates := map[string]interface{}{}
	counts := channelSecretRotationCounts{}
	if common.IsEncryptedSecret(channel.APIKey) {
		rotated, err := rotateEncryptedSecretValue(channel.APIKey, previousKey, currentKey)
		if err != nil {
			return nil, counts, err
		}
		updates["api_key"] = rotated
		counts.rotated++
	} else if strings.TrimSpace(channel.APIKey) != "" {
		counts.skipped++
	}

	if common.ContainsEncryptedSecret(string(channel.APIKeys)) {
		rotated, nestedCounts, err := rotateEncryptedAPIKeysJSON(channel.APIKeys, previousKey, currentKey)
		if err != nil {
			return nil, counts, err
		}
		updates["api_keys"] = rotated
		counts.rotated += nestedCounts.rotated
		counts.skipped += nestedCounts.skipped
	}
	if common.ContainsEncryptedSecret(string(channel.Upstreams)) {
		rotated, nestedCounts, err := rotateEncryptedUpstreamsJSON(channel.Upstreams, previousKey, currentKey)
		if err != nil {
			return nil, counts, err
		}
		updates["upstreams"] = rotated
		counts.rotated += nestedCounts.rotated
		counts.skipped += nestedCounts.skipped
	}
	return updates, counts, nil
}

func rotateEncryptedAPIKeysJSON(raw model.JSONValue, previousKey, currentKey string) (model.JSONValue, channelSecretRotationCounts, error) {
	counts := channelSecretRotationCounts{}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, counts, err
	}
	for i, key := range keys {
		if common.IsEncryptedSecret(key) {
			rotated, err := rotateEncryptedSecretValue(key, previousKey, currentKey)
			if err != nil {
				return nil, counts, err
			}
			keys[i] = rotated
			counts.rotated++
			continue
		}
		if strings.TrimSpace(key) != "" {
			counts.skipped++
		}
	}
	return model.NewJSONValue(keys), counts, nil
}

func rotateEncryptedUpstreamsJSON(raw model.JSONValue, previousKey, currentKey string) (model.JSONValue, channelSecretRotationCounts, error) {
	counts := channelSecretRotationCounts{}
	var upstreams []channelUpstreamConfig
	if err := json.Unmarshal(raw, &upstreams); err != nil {
		return nil, counts, err
	}
	for i, upstream := range upstreams {
		if common.IsEncryptedSecret(upstream.APIKey) {
			rotated, err := rotateEncryptedSecretValue(upstream.APIKey, previousKey, currentKey)
			if err != nil {
				return nil, counts, err
			}
			upstreams[i].APIKey = rotated
			counts.rotated++
			continue
		}
		if strings.TrimSpace(upstream.APIKey) != "" {
			counts.skipped++
		}
	}
	return model.NewJSONValue(upstreams), counts, nil
}

func rotateEncryptedSecretValue(value, previousKey, currentKey string) (string, error) {
	plain, err := common.DecryptSecretWithKey(value, previousKey)
	if err != nil {
		return "", err
	}
	return common.EncryptSecretWithKey(plain, currentKey)
}

func rotateEncryptedProviderSettings(tx *gorm.DB, previousKey, currentKey string, result *ChannelSecretRotationResult, rotatedSettingValues map[string]string) error {
	var settings []model.Setting
	if err := tx.Select("id", "key", "value").Find(&settings).Error; err != nil {
		return err
	}
	for _, setting := range settings {
		if !SettingKeyRequiresSecretEncryption(setting.Key) {
			continue
		}
		result.ScannedSettings++
		if !common.IsEncryptedSecret(setting.Value) {
			if strings.TrimSpace(setting.Value) != "" {
				result.SkippedSecrets++
			}
			continue
		}
		rotated, err := rotateEncryptedSecretValue(setting.Value, previousKey, currentKey)
		if err != nil {
			return fmt.Errorf("setting %s secret rotation failed: %w", setting.Key, err)
		}
		if err := tx.Model(&model.Setting{}).Where("id = ?", setting.ID).Update("value", rotated).Error; err != nil {
			return err
		}
		result.RotatedSettings++
		result.RotatedSecrets++
		rotatedSettingValues[setting.Key] = rotated
	}
	return nil
}

func refreshRotatedSettingCache(values map[string]string) {
	if internal.RDB == nil || len(values) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	args := make(map[string]interface{}, len(values))
	for key, value := range values {
		args[key] = value
	}
	_ = internal.RDB.HSet(ctx, "settings", args).Err()
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

func normalizeChannelGroupName(group string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return "default"
	}
	return group
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
