package router

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal"
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
	setupAdminRoutes(r, authH, userH, adminH, channelH, relayH, logH, settingH)

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
	ready := int64(1)
	if sqlDB, err := internal.DB.DB(); err != nil || sqlDB.Ping() != nil {
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
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
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
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(strconv.FormatInt(value, 10))
	b.WriteByte('\n')
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
