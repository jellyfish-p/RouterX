package router

import (
	"net/http"

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
		var setting model.Setting
		if err := internal.DB.Where("key = ?", "jwt.secret").First(&setting).Error; err != nil || setting.Value == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "jwt": "missing"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
