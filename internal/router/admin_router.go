package router

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/handler"
	"routerx/internal/middleware"
)

// setupAdminRoutes 注册 Admin 管理端路由组。
// 路径前缀: /v0/admin
// 需要: AdminAuthRequired + SetupCheck
func setupAdminRoutes(
	r *gin.Engine,
	authH *handler.AuthHandler,
	userH *handler.UserHandler,
	tokenH *handler.TokenHandler,
	adminH *handler.AdminHandler,
	channelH *handler.ChannelHandler,
	relayH *handler.RelayHandler,
	logH *handler.LogHandler,
	settingH *handler.SettingHandler,
) {
	admin := r.Group("/v0/admin")
	admin.Use(middleware.SetupCheck())
	{
		authRequired := admin.Group("")
		authRequired.Use(middleware.AdminAuthRequired())
		{
			// 用户管理 (Admin+)
			authRequired.GET("/user", userH.List)
			authRequired.POST("/user", userH.Create)
			authRequired.PUT("/user/:id", userH.Update)
			authRequired.DELETE("/user/:id", userH.Delete)
			authRequired.PATCH("/user/:id/quota", userH.UpdateQuota)
			authRequired.GET("/redem", userH.ListRedemCodes)
			authRequired.POST("/redem", userH.CreateRedemCodes)
			authRequired.PATCH("/redem/:id/disable", userH.DisableRedemCode)
			authRequired.GET("/audit", middleware.RequireSuperAdmin(), userH.ListAdminAuditLogs)
			authRequired.GET("/payment/products", userH.ListPaymentProductsAdmin)
			authRequired.POST("/payment/products", userH.CreatePaymentProduct)
			authRequired.PUT("/payment/products/:id", userH.UpdatePaymentProduct)
			authRequired.PATCH("/payment/products/:id/disable", userH.DisablePaymentProduct)
			authRequired.PATCH("/payment/products/:id/enable", userH.EnablePaymentProduct)
			authRequired.GET("/model-prices", userH.ListModelPricesAdmin)
			authRequired.POST("/model-prices", userH.CreateModelPrice)
			authRequired.PUT("/model-prices/:id", userH.UpdateModelPrice)
			authRequired.PATCH("/model-prices/:id/disable", userH.DisableModelPrice)
			authRequired.PATCH("/model-prices/:id/enable", userH.EnableModelPrice)
			authRequired.GET("/channel-model-prices", userH.ListChannelModelPricesAdmin)
			authRequired.POST("/channel-model-prices", userH.CreateChannelModelPrice)
			authRequired.PUT("/channel-model-prices/:id", userH.UpdateChannelModelPrice)
			authRequired.PATCH("/channel-model-prices/:id/disable", userH.DisableChannelModelPrice)
			authRequired.PATCH("/channel-model-prices/:id/enable", userH.EnableChannelModelPrice)
			authRequired.POST("/payment/adjustments", userH.CreatePaymentManualAdjustment)
			authRequired.POST("/payment/refunds", userH.CreatePaymentManualRefund)
			authRequired.POST("/payment/refund-requests", userH.CreatePaymentProviderRefundRequest)
			authRequired.GET("/token", tokenH.AdminList)
			authRequired.POST("/token/batch-disable", tokenH.BatchDisable)
			authRequired.POST("/token/batch-expire", tokenH.BatchExpire)

			// 管理员账户查看 (Admin+)；写操作仅 SuperAdmin。
			authRequired.GET("/admin", adminH.List)
			adminMgmt := authRequired.Group("/admin")
			adminMgmt.Use(middleware.RequireSuperAdmin())
			{
				adminMgmt.POST("", adminH.Create)
				adminMgmt.PUT("/:id", adminH.Update)
				adminMgmt.DELETE("/:id", adminH.Delete)
			}

			authRequired.GET("/channel", channelH.List)
			authRequired.POST("/channel", channelH.Create)
			authRequired.PUT("/channel/:id", channelH.Update)
			authRequired.DELETE("/channel/:id", channelH.Delete)
			authRequired.PATCH("/channel/:id/disable", channelH.Disable)
			authRequired.PATCH("/channel/:id/enable", channelH.Enable)
			authRequired.POST("/channel/:id/test", channelH.Test)
			authRequired.GET("/channel/:id/models", channelH.FetchModels)

			authRequired.GET("/log", logH.AdminList)
			authRequired.GET("/log/export", logH.AdminExport)
			authRequired.DELETE("/log", logH.AdminClear)
			authRequired.GET("/dashboard", logH.Dashboard)

			settingMgmt := authRequired.Group("/setting")
			settingMgmt.Use(middleware.RequireSuperAdmin())
			{
				settingMgmt.GET("", settingH.GetAll)
				settingMgmt.PUT("", settingH.BatchSet)
			}
		}
	}
}
