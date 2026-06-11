package router

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/handler"
)

// setupPublicRoutes 注册公共路由 (无需鉴权，不受 SetupCheck 限制)。
// 路径: /v0/setup/*, /health
func setupPublicRoutes(r *gin.Engine, setupH *handler.SetupHandler) {
	setup := r.Group("/v0/setup")
	{
		setup.GET("/status", setupH.Status)
		setup.POST("/init", setupH.Init)
	}
}
