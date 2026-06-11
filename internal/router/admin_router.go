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
	adminH *handler.AdminHandler,
	channelH *handler.ChannelHandler,
	relayH *handler.RelayHandler,
	logH *handler.LogHandler,
	settingH *handler.SettingHandler,
) {
	admin := r.Group("/v0/admin")
	admin.Use(middleware.SetupCheck())
	{
		admin.POST("/login", authH.AdminLogin)

		authRequired := admin.Group("")
		authRequired.Use(middleware.AdminAuthRequired())
		{
			authRequired.POST("/logout", authH.AdminLogout)

			// 用户管理 (Admin+)
			authRequired.GET("/user", userH.List)
			authRequired.POST("/user", userH.Create)
			authRequired.PUT("/user/:id", userH.Update)
			authRequired.DELETE("/user/:id", userH.Delete)
			authRequired.PATCH("/user/:id/quota", userH.UpdateQuota)

			// 管理员账户管理 (仅 SuperAdmin)
			adminMgmt := authRequired.Group("/admin")
			adminMgmt.Use(middleware.RequireSuperAdmin())
			{
				adminMgmt.GET("", adminH.List)
				adminMgmt.POST("", adminH.Create)
				adminMgmt.PUT("/:id", adminH.Update)
				adminMgmt.DELETE("/:id", adminH.Delete)
			}

			authRequired.GET("/channel", channelH.List)
			authRequired.POST("/channel", channelH.Create)
			authRequired.PUT("/channel/:id", channelH.Update)
			authRequired.DELETE("/channel/:id", channelH.Delete)
			authRequired.POST("/channel/:id/test", channelH.Test)

			authRequired.GET("/log", logH.AdminList)
			authRequired.DELETE("/log", logH.AdminClear)
			authRequired.GET("/dashboard", logH.Dashboard)

			authRequired.GET("/setting", settingH.GetAll)
			authRequired.PUT("/setting", settingH.BatchSet)
		}
	}
}
