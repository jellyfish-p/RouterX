package main

import (
	"context"
	"log"
	"os"
	"time"

	"routerx/internal"
	"routerx/internal/handler"
	"routerx/internal/router"
	"routerx/internal/service"
)

func main() {
	// 1. 初始化数据库 (GORM + PostgreSQL)
	if err := internal.InitDB(); err != nil {
		log.Fatalf("[FATAL] database init failed: %v", err)
	}
	if err := internal.InitLogDB(); err != nil {
		log.Fatalf("[FATAL] log database init failed: %v", err)
	}

	// 2. 初始化 Redis
	if err := internal.InitRedis(); err != nil {
		log.Printf("[WARN] redis init failed (non-fatal): %v", err)
	}

	// 3. 依赖注入: Service 层
	adminSvc := service.NewAdminService()
	settingSvc := service.NewSettingService()
	userSvc := service.NewUserService()
	authSvc := service.NewAuthService()
	channelSvc := service.NewChannelService()
	tokenSvc := service.NewTokenService()
	logSvc := service.NewLogService()
	alertSvc := service.NewAlertService()
	setupSvc := service.NewSetupService(userSvc, settingSvc)
	relaySvc := service.NewRelayService(channelSvc, tokenSvc, logSvc, settingSvc)
	if err := settingSvc.EnsureDefaults(); err != nil {
		log.Fatalf("[FATAL] settings defaults failed: %v", err)
	}
	if err := channelSvc.PreloadCandidateCache(); err != nil {
		log.Printf("[WARN] channel candidate cache preload failed: %v", err)
	}
	logSvc.StartLogReplicationWorker(context.Background(), time.Minute, 100)
	alertSvc.StartAlertDeliveryWorker(context.Background())
	channelSvc.StartBreakerProbeWorker(context.Background())
	channelSvc.StartCandidateCacheInvalidationSubscriber(context.Background())

	// 4. 依赖注入: Handler 层
	adminH := handler.NewAdminHandler(adminSvc)
	authH := handler.NewAuthHandler(authSvc)
	userH := handler.NewUserHandler(userSvc)
	tokenH := handler.NewTokenHandler(tokenSvc)
	channelH := handler.NewChannelHandler(channelSvc)
	relayH := handler.NewRelayHandler(relaySvc)
	logH := handler.NewLogHandler(logSvc)
	alertH := handler.NewAlertHandler(alertSvc)
	settingH := handler.NewSettingHandler(settingSvc)
	setupH := handler.NewSetupHandler(setupSvc)

	// 5. 配置路由
	r := router.SetupRouter(authH, userH, tokenH, adminH, channelH, relayH, logH, alertH, settingH, setupH)

	// 6. 启动服务
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("[Server] starting on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("[FATAL] server failed: %v", err)
	}
}
