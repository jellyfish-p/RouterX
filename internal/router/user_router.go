package router

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/handler"
	"routerx/internal/middleware"
)

// setupUserRoutes 注册 User 端 Web API + /v1 转发路由。
// /v0/user  需要 UserJwtAuth + SetupCheck
// /v1       需要 ApiKeyAuth + SetupCheck
func setupUserRoutes(
	r *gin.Engine,
	authH *handler.AuthHandler,
	userH *handler.UserHandler,
	logH *handler.LogHandler,
) {
	// User Web API (v0)
	api := r.Group("/v0/user")
	api.Use(middleware.SetupCheck())
	{
		api.POST("/register", authH.Register)
		api.POST("/login", authH.UserLogin)

		jwtRequired := api.Group("")
		jwtRequired.Use(middleware.UserJwtAuthRequired())
		{
			jwtRequired.GET("/self", userH.Self)
			jwtRequired.PUT("/self", userH.UpdateSelf)
			jwtRequired.POST("/self/password", authH.ChangePassword)

			jwtRequired.GET("/log", logH.UserList)
			jwtRequired.GET("/billing", logH.UserBilling)
		}
	}
}

// setupV1Routes 注册 OpenAI-Compatible 转发路由。
// 路径前缀: /v1  需要: ApiKeyAuthRequired + SetupCheck
func setupV1Routes(
	r *gin.Engine,
	relayH *handler.RelayHandler,
) {
	v1 := r.Group("/v1")
	v1.Use(middleware.SetupCheck())
	v1.Use(middleware.ApiKeyAuthRequired())
	{
		v1.POST("/chat/completions", relayH.ChatCompletions)
		v1.POST("/completions", relayH.Completions)
		v1.POST("/embeddings", relayH.Embeddings)
		v1.POST("/images/generations", relayH.ImageGenerations)
		v1.POST("/audio/transcriptions", relayH.AudioTranscriptions)
		v1.POST("/audio/speech", relayH.AudioSpeech)
		v1.GET("/models", relayH.ListModels)
	}
}
