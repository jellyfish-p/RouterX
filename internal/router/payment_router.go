package router

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/handler"
	"routerx/internal/middleware"
)

// setupPaymentRoutes 注册 provider 回调路由。
// Webhook 不使用用户 JWT，可信度由 provider 签名和本地订单校验保证。
func setupPaymentRoutes(r *gin.Engine, userH *handler.UserHandler) {
	payment := r.Group("/v0/payment")
	payment.Use(middleware.SetupCheck())
	{
		payment.POST("/epay/notify", userH.EpayNotify)
	}
}
