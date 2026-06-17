package router

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/handler"
	"routerx/internal/middleware"
	"routerx/internal/model"
	"routerx/internal/service"
)

// SetupRouter 创建并配置所有路由。
// 返回配置好的 *gin.Engine。
//
// 依赖注入：所有 handler 在 cmd/server/main.go 中初始化后传入。
func SetupRouter(
	authH *handler.AuthHandler,
	userH *handler.UserHandler,
	tokenH *handler.TokenHandler,
	adminH *handler.AdminHandler,
	channelH *handler.ChannelHandler,
	relayH *handler.RelayHandler,
	logH *handler.LogHandler,
	settingH *handler.SettingHandler,
	setupH *handler.SetupHandler,
) *gin.Engine {
	middleware.ResetHTTPMetrics()
	service.ResetRelayMetrics()

	r := gin.New()

	// 全局中间件
	r.Use(middleware.Recovery())
	r.Use(middleware.Logger())

	// 健康检查 (无需初始化)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "healthy"})
	})
	r.GET("/ready", readyHandler)
	r.GET("/metrics", metricsHandler)

	// Setup 初始化路由 (无需鉴权, 无需系统已初始化)
	setupPublicRoutes(r, setupH)

	// Admin 管理端路由组 (需要 AdminAuth + 系统已初始化)
	setupAdminRoutes(r, authH, userH, tokenH, adminH, channelH, relayH, logH, settingH)

	// User Web API (需要 UserJwtAuth + 系统已初始化)
	setupUserRoutes(r, authH, userH, tokenH, logH)

	// Payment Webhook (需要系统已初始化, 由 provider 签名鉴权)
	setupPaymentRoutes(r, userH)

	// /v1 OpenAI-Compatible 转发路由 (需要 ApiKeyAuth + 系统已初始化)
	setupV1Routes(r, relayH)

	return r
}

func readyHandler(c *gin.Context) {
	if internal.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "database": "unavailable"})
		return
	}
	sqlDB, err := internal.DB.DB()
	if err != nil || sqlDB.Ping() != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "database": "unavailable"})
		return
	}
	if problem := readinessRedisProblem(); problem != "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "redis": problem})
		return
	}
	if internal.IsInitialized() {
		if problem := readinessSettingProblem(); problem != "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "setting": problem})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func metricsHandler(c *gin.Context) {
	if !metricsEnabled() {
		c.String(http.StatusNotFound, "404 page not found")
		return
	}
	if internal.DB == nil {
		c.String(http.StatusServiceUnavailable, "routerx_ready 0\n")
		return
	}
	userCount, channelCount, tokenCount, todayCalls, todayQuota, activeChannels, err := service.NewLogService().GetDashboardStats()
	if err != nil {
		c.String(http.StatusInternalServerError, "metrics unavailable\n")
		return
	}
	extended, err := collectExtendedMetrics()
	if err != nil {
		c.String(http.StatusInternalServerError, "metrics unavailable\n")
		return
	}
	ready := int64(1)
	if sqlDB, err := internal.DB.DB(); err != nil || sqlDB.Ping() != nil {
		ready = 0
	} else if readinessRedisProblem() != "" {
		ready = 0
	} else if internal.IsInitialized() && readinessSettingProblem() != "" {
		ready = 0
	}
	var b strings.Builder
	writeGauge(&b, "routerx_users_total", "Total users.", userCount)
	writeGauge(&b, "routerx_channels_total", "Total channels.", channelCount)
	writeGauge(&b, "routerx_tokens_total", "Total API keys.", tokenCount)
	writeGauge(&b, "routerx_channels_active", "Enabled channels.", activeChannels)
	writeGauge(&b, "routerx_ready", "Service readiness status.", ready)
	writeCounter(&b, "routerx_today_calls_total", "Successful calls since local midnight.", todayCalls)
	writeCounter(&b, "routerx_today_quota_total", "Quota used since local midnight.", todayQuota)
	writeGauge(&b, "routerx_db_up", "Database ping status.", extended.DBUp)
	writeGauge(&b, "routerx_redis_up", "Redis ping status.", extended.RedisUp)
	writeLabeledCounter(&b, "routerx_http_requests_total", "HTTP requests by method, path group and status.", extended.HTTPRequests)
	writeLabeledHistogram(&b, "routerx_http_request_duration_seconds", "HTTP request duration in seconds by method and path group.", extended.HTTPRequestDurations)
	writeLabeledHistogram(&b, "routerx_relay_duration_seconds", "Relay duration in seconds by protocol, API type and provider.", extended.RelayDurations)
	writeLabeledHistogram(&b, "routerx_upstream_duration_seconds", "Upstream duration in seconds by provider, channel and status.", extended.UpstreamDurations)
	writeLabeledCounter(&b, "routerx_logs_total", "Relay logs by status.", extended.Logs)
	writeLabeledCounter(&b, "routerx_relay_requests_total", "Relay requests by protocol, API type, model and status.", extended.RelayRequests)
	writeLabeledCounter(&b, "routerx_relay_errors_total", "Relay errors by protocol, API type, code and source.", extended.RelayErrors)
	writeLabeledCounter(&b, "routerx_tokens_used_total", "Model tokens used by model, provider and usage source.", extended.TokensUsed)
	writeLabeledCounter(&b, "routerx_quota_used_total", "Quota used by model, provider and user group.", extended.QuotaUsed)
	writeLabeledGauge(&b, "routerx_channel_available", "Channel availability by channel and provider.", extended.ChannelAvailable)
	writeLabeledGauge(&b, "routerx_channel_error_count", "Channel error counters by channel and provider.", extended.ChannelErrorCounts)
	writeLabeledCounter(&b, "routerx_rate_limit_rejections_total", "Rate limit rejections by dimension.", extended.RateLimitRejections)
	writeLabeledCounter(&b, "routerx_billing_failures_total", "Billing failures by reason.", extended.BillingFailures)
	writeLabeledGauge(&b, "routerx_payment_orders_total", "Payment orders by provider and status.", extended.PaymentOrders)
	writeLabeledGauge(&b, "routerx_payment_events_total", "Payment events by provider, event type and processed state.", extended.PaymentEvents)
	writeLabeledCounter(&b, "routerx_audit_events_total", "Admin audit events by action, resource type and result.", extended.AuditEvents)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
}

type metricLabel struct {
	Name  string
	Value string
}

type metricSample struct {
	Labels []metricLabel
	Value  int64
}

type metricHistogramBucket struct {
	Le    string
	Count int64
}

type metricHistogramSample struct {
	Labels  []metricLabel
	Buckets []metricHistogramBucket
	Sum     float64
	Count   int64
}

type extendedMetrics struct {
	DBUp                 int64
	RedisUp              int64
	HTTPRequests         []metricSample
	HTTPRequestDurations []metricHistogramSample
	RelayDurations       []metricHistogramSample
	UpstreamDurations    []metricHistogramSample
	Logs                 []metricSample
	RelayRequests        []metricSample
	RelayErrors          []metricSample
	TokensUsed           []metricSample
	QuotaUsed            []metricSample
	ChannelErrorCounts   []metricSample
	ChannelAvailable     []metricSample
	RateLimitRejections  []metricSample
	BillingFailures      []metricSample
	PaymentOrders        []metricSample
	PaymentEvents        []metricSample
	AuditEvents          []metricSample
}

func collectExtendedMetrics() (extendedMetrics, error) {
	metrics := extendedMetrics{
		DBUp:    dbUp(),
		RedisUp: redisUp(),
	}
	httpRequests, httpRequestDurations := collectHTTPMetrics()
	metrics.HTTPRequests = httpRequests
	metrics.HTTPRequestDurations = httpRequestDurations
	relayDurations, upstreamDurations := collectRelayDurationMetrics()
	metrics.RelayDurations = relayDurations
	metrics.UpstreamDurations = upstreamDurations

	var logRows []struct {
		Status int
		Count  int64
	}
	if err := internal.DB.Model(&model.Log{}).
		Select("status, COUNT(*) AS count").
		Group("status").
		Order("status ASC").
		Scan(&logRows).Error; err != nil {
		return extendedMetrics{}, err
	}
	for _, row := range logRows {
		metrics.Logs = append(metrics.Logs, metricSample{
			Labels: []metricLabel{{Name: "status", Value: logStatusLabel(row.Status)}},
			Value:  row.Count,
		})
	}
	relayRequests, err := collectRelayRequestMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.RelayRequests = relayRequests

	relayErrors, err := collectRelayErrorMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.RelayErrors = relayErrors

	tokensUsed, err := collectTokenUsageMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.TokensUsed = tokensUsed

	quotaUsed, err := collectQuotaUsageMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.QuotaUsed = quotaUsed

	channelAvailable, err := collectChannelAvailabilityMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.ChannelAvailable = channelAvailable

	channelErrorCounts, err := collectChannelErrorCountMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.ChannelErrorCounts = channelErrorCounts

	rateLimitRejections, err := collectRateLimitRejectionMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.RateLimitRejections = rateLimitRejections

	billingFailures, err := collectBillingFailureMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.BillingFailures = billingFailures

	var orderRows []struct {
		Provider string
		Status   string
		Count    int64
	}
	if err := internal.DB.Model(&model.PaymentOrder{}).
		Select("provider, status, COUNT(*) AS count").
		Group("provider, status").
		Order("provider ASC, status ASC").
		Scan(&orderRows).Error; err != nil {
		return extendedMetrics{}, err
	}
	for _, row := range orderRows {
		metrics.PaymentOrders = append(metrics.PaymentOrders, metricSample{
			Labels: []metricLabel{
				{Name: "provider", Value: row.Provider},
				{Name: "status", Value: row.Status},
			},
			Value: row.Count,
		})
	}

	var eventRows []struct {
		Provider  string
		EventType string
		Processed bool
		Count     int64
	}
	if err := internal.DB.Model(&model.PaymentEvent{}).
		Select("provider, event_type, processed, COUNT(*) AS count").
		Group("provider, event_type, processed").
		Order("provider ASC, event_type ASC, processed ASC").
		Scan(&eventRows).Error; err != nil {
		return extendedMetrics{}, err
	}
	for _, row := range eventRows {
		metrics.PaymentEvents = append(metrics.PaymentEvents, metricSample{
			Labels: []metricLabel{
				{Name: "provider", Value: row.Provider},
				{Name: "event_type", Value: row.EventType},
				{Name: "processed", Value: strconv.FormatBool(row.Processed)},
			},
			Value: row.Count,
		})
	}
	auditEvents, err := collectAuditEventMetrics()
	if err != nil {
		return extendedMetrics{}, err
	}
	metrics.AuditEvents = auditEvents
	return metrics, nil
}

func collectHTTPMetrics() ([]metricSample, []metricHistogramSample) {
	requestRows, durationRows := middleware.HTTPMetricsSnapshot()
	requests := make([]metricSample, 0, len(requestRows))
	for _, row := range requestRows {
		requests = append(requests, metricSample{
			Labels: []metricLabel{
				{Name: "method", Value: row.Method},
				{Name: "path_group", Value: row.PathGroup},
				{Name: "status", Value: row.Status},
			},
			Value: row.Count,
		})
	}

	durations := make([]metricHistogramSample, 0, len(durationRows))
	for _, row := range durationRows {
		buckets := make([]metricHistogramBucket, 0, len(row.Buckets))
		for _, bucket := range row.Buckets {
			buckets = append(buckets, metricHistogramBucket{
				Le:    bucket.Le,
				Count: bucket.Count,
			})
		}
		durations = append(durations, metricHistogramSample{
			Labels: []metricLabel{
				{Name: "method", Value: row.Method},
				{Name: "path_group", Value: row.PathGroup},
			},
			Buckets: buckets,
			Sum:     row.SumSeconds,
			Count:   row.Count,
		})
	}
	return requests, durations
}

func collectRelayDurationMetrics() ([]metricHistogramSample, []metricHistogramSample) {
	relayRows, upstreamRows := service.RelayMetricsSnapshot()
	relaySamples := make([]metricHistogramSample, 0, len(relayRows))
	for _, row := range relayRows {
		relaySamples = append(relaySamples, metricHistogramSample{
			Labels: []metricLabel{
				{Name: "protocol", Value: row.Protocol},
				{Name: "api_type", Value: row.APIType},
				{Name: "provider", Value: row.Provider},
			},
			Buckets: serviceBucketsToMetricBuckets(row.Buckets),
			Sum:     row.SumSeconds,
			Count:   row.Count,
		})
	}

	upstreamSamples := make([]metricHistogramSample, 0, len(upstreamRows))
	for _, row := range upstreamRows {
		upstreamSamples = append(upstreamSamples, metricHistogramSample{
			Labels: []metricLabel{
				{Name: "provider", Value: row.Provider},
				{Name: "channel_id", Value: row.ChannelID},
				{Name: "status", Value: row.Status},
			},
			Buckets: serviceBucketsToMetricBuckets(row.Buckets),
			Sum:     row.SumSeconds,
			Count:   row.Count,
		})
	}
	return relaySamples, upstreamSamples
}

func serviceBucketsToMetricBuckets(buckets []service.HistogramBucket) []metricHistogramBucket {
	result := make([]metricHistogramBucket, 0, len(buckets))
	for _, bucket := range buckets {
		result = append(result, metricHistogramBucket{
			Le:    bucket.Le,
			Count: bucket.Count,
		})
	}
	return result
}

type relayRequestMetricKey struct {
	Protocol string
	APIType  string
	Model    string
	Status   string
}

func collectRelayRequestMetrics() ([]metricSample, error) {
	var rows []struct {
		Model           string
		Status          int
		RequestSnapshot string
	}
	if err := internal.DB.Model(&model.Log{}).
		Select("model, status, request_snapshot").
		Where("status <> ?", common.LogStatusUnknown).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[relayRequestMetricKey]int64{}
	for _, row := range rows {
		protocol, apiType := relayRequestMetricDimensions(row.RequestSnapshot)
		key := relayRequestMetricKey{
			Protocol: protocol,
			APIType:  apiType,
			Model:    metricDimensionOrUnknown(row.Model),
			Status:   logStatusLabel(row.Status),
		}
		counts[key]++
	}
	keys := make([]relayRequestMetricKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Protocol != keys[j].Protocol {
			return keys[i].Protocol < keys[j].Protocol
		}
		if keys[i].APIType != keys[j].APIType {
			return keys[i].APIType < keys[j].APIType
		}
		if keys[i].Model != keys[j].Model {
			return keys[i].Model < keys[j].Model
		}
		return keys[i].Status < keys[j].Status
	})
	samples := make([]metricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, metricSample{
			Labels: []metricLabel{
				{Name: "protocol", Value: key.Protocol},
				{Name: "api_type", Value: key.APIType},
				{Name: "model", Value: key.Model},
				{Name: "status", Value: key.Status},
			},
			Value: counts[key],
		})
	}
	return samples, nil
}

type tokenUsageMetricKey struct {
	Model       string
	Provider    string
	UsageSource string
}

func collectTokenUsageMetrics() ([]metricSample, error) {
	var rows []struct {
		Model         string
		TotalTokens   int
		UsageSource   string
		RouteSnapshot string
	}
	if err := internal.DB.Model(&model.Log{}).
		Select("model, total_tokens, usage_source, route_snapshot").
		Where("status = ? AND total_tokens > 0", common.LogStatusSuccess).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[tokenUsageMetricKey]int64{}
	for _, row := range rows {
		key := tokenUsageMetricKey{
			Model:       metricDimensionOrUnknown(row.Model),
			Provider:    providerFromRouteSnapshot(row.RouteSnapshot),
			UsageSource: metricDimensionOrUnknown(row.UsageSource),
		}
		counts[key] += int64(row.TotalTokens)
	}
	keys := make([]tokenUsageMetricKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Model != keys[j].Model {
			return keys[i].Model < keys[j].Model
		}
		if keys[i].Provider != keys[j].Provider {
			return keys[i].Provider < keys[j].Provider
		}
		return keys[i].UsageSource < keys[j].UsageSource
	})
	samples := make([]metricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, metricSample{
			Labels: []metricLabel{
				{Name: "model", Value: key.Model},
				{Name: "provider", Value: key.Provider},
				{Name: "usage_source", Value: key.UsageSource},
			},
			Value: counts[key],
		})
	}
	return samples, nil
}

type quotaUsageMetricKey struct {
	Model     string
	Provider  string
	UserGroup string
}

type quotaUsageMetricRow struct {
	UserID        uint
	Model         string
	QuotaUsed     int64
	RouteSnapshot string
}

func collectQuotaUsageMetrics() ([]metricSample, error) {
	var rows []quotaUsageMetricRow
	if err := internal.DB.Model(&model.Log{}).
		Select("user_id, model, quota_used, route_snapshot").
		Where("status = ? AND quota_used > 0", common.LogStatusSuccess).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	userGroups, err := quotaUsageUserGroups(rows)
	if err != nil {
		return nil, err
	}
	counts := map[quotaUsageMetricKey]int64{}
	for _, row := range rows {
		key := quotaUsageMetricKey{
			Model:     metricDimensionOrUnknown(row.Model),
			Provider:  providerFromRouteSnapshot(row.RouteSnapshot),
			UserGroup: userGroups[row.UserID],
		}
		counts[key] += row.QuotaUsed
	}
	keys := make([]quotaUsageMetricKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Model != keys[j].Model {
			return keys[i].Model < keys[j].Model
		}
		if keys[i].Provider != keys[j].Provider {
			return keys[i].Provider < keys[j].Provider
		}
		return keys[i].UserGroup < keys[j].UserGroup
	})
	samples := make([]metricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, metricSample{
			Labels: []metricLabel{
				{Name: "model", Value: key.Model},
				{Name: "provider", Value: key.Provider},
				{Name: "user_group", Value: key.UserGroup},
			},
			Value: counts[key],
		})
	}
	return samples, nil
}

func quotaUsageUserGroups(rows []quotaUsageMetricRow) (map[uint]string, error) {
	userIDs := make([]uint, 0, len(rows))
	seen := map[uint]struct{}{}
	for _, row := range rows {
		if _, ok := seen[row.UserID]; ok {
			continue
		}
		seen[row.UserID] = struct{}{}
		userIDs = append(userIDs, row.UserID)
	}
	labels := make(map[uint]string, len(userIDs))
	for _, id := range userIDs {
		// 没有绑定分组或用户记录缺失时，指标统一落到 default 分组，避免暴露 user_id 高基数标签。
		labels[id] = "default"
	}
	if len(userIDs) == 0 {
		return labels, nil
	}
	var users []model.User
	if err := internal.DB.Preload("Group").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		if user.Group != nil {
			labels[user.ID] = metricDimensionOrDefault(user.Group.Name, "default")
		}
	}
	return labels, nil
}

func providerFromRouteSnapshot(raw string) string {
	var snapshot struct {
		SelectedProvider string `json:"selected_provider"`
		Provider         string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return "unknown"
	}
	provider := snapshot.SelectedProvider
	if provider == "" {
		provider = snapshot.Provider
	}
	return metricDimensionOrUnknown(provider)
}

func collectAuditEventMetrics() ([]metricSample, error) {
	var rows []struct {
		Action       string
		ResourceType string
		Result       string
		Count        int64
	}
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Select("action, resource_type, result, COUNT(*) AS count").
		Group("action, resource_type, result").
		Order("action ASC, resource_type ASC, result ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	samples := make([]metricSample, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, metricSample{
			Labels: []metricLabel{
				{Name: "action", Value: metricDimensionOrUnknown(row.Action)},
				{Name: "resource_type", Value: metricDimensionOrUnknown(row.ResourceType)},
				{Name: "result", Value: metricDimensionOrUnknown(row.Result)},
			},
			Value: row.Count,
		})
	}
	return samples, nil
}

func collectBillingFailureMetrics() ([]metricSample, error) {
	var rows []struct {
		ErrorCode       string
		BillingSnapshot string
	}
	if err := internal.DB.Model(&model.Log{}).
		Select("error_code, billing_snapshot").
		Where("status = ? AND error_source = ?", common.LogStatusFailed, common.LogErrorSourceBilling).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, row := range rows {
		counts[billingFailureReason(row.BillingSnapshot, row.ErrorCode)]++
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	samples := make([]metricSample, 0, len(reasons))
	for _, reason := range reasons {
		samples = append(samples, metricSample{
			Labels: []metricLabel{{Name: "reason", Value: reason}},
			Value:  counts[reason],
		})
	}
	return samples, nil
}

func billingFailureReason(rawSnapshot, fallbackCode string) string {
	var snapshot struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(rawSnapshot), &snapshot); err == nil {
		if reason := metricDimensionOrUnknown(snapshot.Reason); reason != "unknown" {
			return reason
		}
	}
	return metricDimensionOrUnknown(fallbackCode)
}

type relayErrorMetricKey struct {
	Protocol  string
	APIType   string
	ErrorCode string
	Source    string
}

func collectRelayErrorMetrics() ([]metricSample, error) {
	var rows []struct {
		ErrorCode       string
		ErrorSource     string
		RequestSnapshot string
	}
	if err := internal.DB.Model(&model.Log{}).
		Select("error_code, error_source, request_snapshot").
		Where("status = ? AND error_code <> ?", common.LogStatusFailed, "").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[relayErrorMetricKey]int64{}
	for _, row := range rows {
		protocol, apiType := relayRequestMetricDimensions(row.RequestSnapshot)
		key := relayErrorMetricKey{
			Protocol:  protocol,
			APIType:   apiType,
			ErrorCode: metricDimensionOrUnknown(row.ErrorCode),
			Source:    metricDimensionOrUnknown(row.ErrorSource),
		}
		counts[key]++
	}
	keys := make([]relayErrorMetricKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Protocol != keys[j].Protocol {
			return keys[i].Protocol < keys[j].Protocol
		}
		if keys[i].APIType != keys[j].APIType {
			return keys[i].APIType < keys[j].APIType
		}
		if keys[i].ErrorCode != keys[j].ErrorCode {
			return keys[i].ErrorCode < keys[j].ErrorCode
		}
		return keys[i].Source < keys[j].Source
	})
	samples := make([]metricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, metricSample{
			Labels: []metricLabel{
				{Name: "protocol", Value: key.Protocol},
				{Name: "api_type", Value: key.APIType},
				{Name: "error_code", Value: key.ErrorCode},
				{Name: "source", Value: key.Source},
			},
			Value: counts[key],
		})
	}
	return samples, nil
}

func relayRequestMetricDimensions(raw string) (string, string) {
	var snapshot struct {
		EntryProtocol string `json:"entry_protocol"`
		Protocol      string `json:"protocol"`
		APIType       string `json:"api_type"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return "unknown", "unknown"
	}
	protocol := snapshot.EntryProtocol
	if protocol == "" {
		protocol = snapshot.Protocol
	}
	return metricDimensionOrUnknown(protocol), metricDimensionOrUnknown(snapshot.APIType)
}

func collectChannelAvailabilityMetrics() ([]metricSample, error) {
	var rows []struct {
		ID     uint
		Type   int
		Status int
	}
	if err := internal.DB.Model(&model.Channel{}).
		Select("id, type, status").
		Order("id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	samples := make([]metricSample, 0, len(rows))
	for _, row := range rows {
		value := int64(0)
		if row.Status == common.ChannelStatusEnabled {
			value = 1
		}
		samples = append(samples, metricSample{
			Labels: []metricLabel{
				{Name: "channel_id", Value: strconv.FormatUint(uint64(row.ID), 10)},
				{Name: "provider", Value: channelMetricProviderName(row.Type)},
			},
			Value: value,
		})
	}
	return samples, nil
}

func collectChannelErrorCountMetrics() ([]metricSample, error) {
	var rows []struct {
		ID         uint
		Type       int
		ErrorCount int
	}
	if err := internal.DB.Model(&model.Channel{}).
		Select("id, type, error_count").
		Order("id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	samples := make([]metricSample, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, metricSample{
			Labels: []metricLabel{
				{Name: "channel_id", Value: strconv.FormatUint(uint64(row.ID), 10)},
				{Name: "provider", Value: channelMetricProviderName(row.Type)},
			},
			Value: int64(row.ErrorCount),
		})
	}
	return samples, nil
}

func channelMetricProviderName(channelType int) string {
	switch channelType {
	case common.ChannelTypeOpenAI:
		return "openai"
	case common.ChannelTypeAzure:
		return "azure"
	case common.ChannelTypeClaude:
		return "anthropic"
	case common.ChannelTypeGemini:
		return "gemini"
	case common.ChannelTypeQwen:
		return "qwen"
	case common.ChannelTypeDeepSeek:
		return "deepseek"
	case common.ChannelTypeXAI:
		return "xai"
	case common.ChannelTypeRouterX:
		return "routerx"
	case common.ChannelTypeOpenAICompat:
		return "openai-compatible"
	default:
		return "unknown"
	}
}

func collectRateLimitRejectionMetrics() ([]metricSample, error) {
	var rows []struct {
		PolicySnapshot string
	}
	if err := internal.DB.Model(&model.Log{}).
		Select("policy_snapshot").
		Where("status = ? AND error_code = ?", common.LogStatusFailed, "rate_limit_exceeded").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, row := range rows {
		counts[rateLimitDimensionFromPolicySnapshot(row.PolicySnapshot)]++
	}
	dimensions := make([]string, 0, len(counts))
	for dimension := range counts {
		dimensions = append(dimensions, dimension)
	}
	sort.Strings(dimensions)
	samples := make([]metricSample, 0, len(dimensions))
	for _, dimension := range dimensions {
		samples = append(samples, metricSample{
			Labels: []metricLabel{{Name: "dimension", Value: dimension}},
			Value:  counts[dimension],
		})
	}
	return samples, nil
}

func rateLimitDimensionFromPolicySnapshot(raw string) string {
	var snapshot struct {
		ScopeResult map[string]interface{} `json:"scope_result"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return "unknown"
	}
	dimension, _ := snapshot.ScopeResult["rate_limit_dimension"].(string)
	return normalizedRateLimitDimension(dimension)
}

func normalizedRateLimitDimension(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "global":
		return "global"
	case "ip":
		return "ip"
	case "token":
		return "token"
	case "user":
		return "user"
	case "model":
		return "model"
	case "channel":
		return "channel"
	default:
		return "unknown"
	}
}

func metricDimensionOrUnknown(value string) string {
	return metricDimensionOrDefault(value, "unknown")
}

func metricDimensionOrDefault(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func dbUp() int64 {
	if internal.DB == nil {
		return 0
	}
	sqlDB, err := internal.DB.DB()
	if err != nil || sqlDB.Ping() != nil {
		return 0
	}
	return 1
}

func redisUp() int64 {
	if internal.RDB == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := internal.RDB.Ping(ctx).Err(); err != nil {
		return 0
	}
	return 1
}

func readinessRedisProblem() string {
	if !redisRequiredForCurrentMode() {
		return ""
	}
	if internal.RDB == nil {
		return "required"
	}
	if redisUp() == 0 {
		return "unavailable"
	}
	return ""
}

func redisRequiredForCurrentMode() bool {
	dsn := strings.TrimSpace(os.Getenv("SQL_DSN"))
	if dsn == "" || strings.HasPrefix(dsn, "sqlite://") || strings.HasPrefix(dsn, "file:") {
		return false
	}
	return true
}

func metricsEnabled() bool {
	if internal.DB == nil {
		return false
	}
	raw, ok := settingValue("observability.metrics_enabled")
	if !ok {
		return false
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && enabled
}

func writeGauge(b *strings.Builder, name, help string, value int64) {
	writeMetric(b, name, help, "gauge", value)
}

func writeCounter(b *strings.Builder, name, help string, value int64) {
	writeMetric(b, name, help, "counter", value)
}

func writeMetric(b *strings.Builder, name, help, metricType string, value int64) {
	writeMetricHeader(b, name, help, metricType)
	writeMetricSample(b, name, nil, value)
}

func writeLabeledGauge(b *strings.Builder, name, help string, samples []metricSample) {
	writeMetricHeader(b, name, help, "gauge")
	for _, sample := range samples {
		writeMetricSample(b, name, sample.Labels, sample.Value)
	}
}

func writeLabeledCounter(b *strings.Builder, name, help string, samples []metricSample) {
	writeMetricHeader(b, name, help, "counter")
	for _, sample := range samples {
		writeMetricSample(b, name, sample.Labels, sample.Value)
	}
}

func writeLabeledHistogram(b *strings.Builder, name, help string, samples []metricHistogramSample) {
	writeMetricHeader(b, name, help, "histogram")
	for _, sample := range samples {
		for _, bucket := range sample.Buckets {
			labels := append([]metricLabel{}, sample.Labels...)
			labels = append(labels, metricLabel{Name: "le", Value: bucket.Le})
			writeMetricSample(b, name+"_bucket", labels, bucket.Count)
		}
		writeMetricFloatSample(b, name+"_sum", sample.Labels, sample.Sum)
		writeMetricSample(b, name+"_count", sample.Labels, sample.Count)
	}
}

func writeMetricHeader(b *strings.Builder, name, help, metricType string) {
	b.WriteString("# HELP ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(help)
	b.WriteByte('\n')
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(metricType)
	b.WriteByte('\n')
}

func writeMetricFloatSample(b *strings.Builder, name string, labels []metricLabel, value float64) {
	b.WriteString(name)
	writeMetricLabels(b, labels)
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
	b.WriteByte('\n')
}

func writeMetricSample(b *strings.Builder, name string, labels []metricLabel, value int64) {
	b.WriteString(name)
	writeMetricLabels(b, labels)
	b.WriteByte(' ')
	b.WriteString(strconv.FormatInt(value, 10))
	b.WriteByte('\n')
}

func writeMetricLabels(b *strings.Builder, labels []metricLabel) {
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, label := range labels {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(label.Name)
			b.WriteString("=\"")
			b.WriteString(escapeMetricLabel(label.Value))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
}

func escapeMetricLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func logStatusLabel(status int) string {
	switch status {
	case common.LogStatusSuccess:
		return "success"
	case common.LogStatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func readinessSettingProblem() string {
	jwtSecret, ok := settingValue("jwt.secret")
	if !ok || len(strings.TrimSpace(jwtSecret)) < 32 {
		return "jwt.secret"
	}
	strict := true
	if raw, ok := settingValue("ready.production_strict"); ok {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return "ready.production_strict"
		}
		strict = value
	}
	if !strict {
		return ""
	}
	relayTimeout, ok := settingValue("relay.timeout")
	if !ok {
		return "relay.timeout"
	}
	timeout, err := strconv.Atoi(strings.TrimSpace(relayTimeout))
	if err != nil || timeout <= 0 {
		return "relay.timeout"
	}
	epayEnabled, problem := readinessBoolSetting("payment.epay.enabled")
	if problem != "" {
		return problem
	}
	if epayEnabled && strings.TrimSpace(os.Getenv("PAYMENT_EPAY_KEY")) == "" {
		return "PAYMENT_EPAY_KEY"
	}
	stripeEnabled, problem := readinessBoolSetting("payment.stripe.enabled")
	if problem != "" {
		return problem
	}
	if stripeEnabled && strings.TrimSpace(os.Getenv("PAYMENT_STRIPE_SECRET_KEY")) == "" {
		return "PAYMENT_STRIPE_SECRET_KEY"
	}
	if stripeEnabled && strings.TrimSpace(os.Getenv("PAYMENT_STRIPE_WEBHOOK_SECRET")) == "" {
		return "PAYMENT_STRIPE_WEBHOOK_SECRET"
	}
	return ""
}

func settingValue(key string) (string, bool) {
	var setting model.Setting
	if err := internal.DB.Where("key = ?", key).First(&setting).Error; err != nil {
		return "", false
	}
	return setting.Value, true
}

func readinessBoolSetting(key string) (bool, string) {
	raw, ok := settingValue(key)
	if !ok {
		return false, ""
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, key
	}
	return value, ""
}
