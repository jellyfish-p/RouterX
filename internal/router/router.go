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
