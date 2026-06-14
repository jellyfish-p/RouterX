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
	tokenH *handler.TokenHandler,
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

			jwtRequired.GET("/token", tokenH.List)
			jwtRequired.POST("/token", tokenH.Create)
			jwtRequired.PUT("/token/:id", tokenH.Update)
			jwtRequired.DELETE("/token/:id", tokenH.Delete)

			jwtRequired.GET("/log", logH.UserList)
			jwtRequired.GET("/billing", logH.UserBilling)
			jwtRequired.POST("/redem", userH.RedeemCode)
			jwtRequired.GET("/models", userH.Models)
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
	v1.Use(middleware.RateLimit())
	{
		v1.POST("/responses", relayH.Responses)
		v1.POST("/chat/completions", relayH.ChatCompletions)
		v1.POST("/completions", relayH.Completions)
		v1.POST("/embeddings", relayH.Embeddings)
		v1.POST("/images/generations", relayH.ImageGenerations)
		v1.POST("/images/edits", relayH.ImageEdits)
		v1.POST("/images/variations", relayH.ImageVariations)
		v1.POST("/audio/transcriptions", relayH.AudioTranscriptions)
		v1.POST("/audio/translations", relayH.AudioTranslations)
		v1.POST("/audio/speech", relayH.AudioSpeech)
		v1.POST("/moderations", relayH.Moderations)
		v1.POST("/messages", relayH.AnthropicMessages)
		v1.POST("/messages/count_tokens", relayH.AnthropicCountTokens)
		v1.GET("/models", relayH.ListModels)
		v1.GET("/models/:model", relayH.ModelDetail)
		v1.POST("/models/:model", relayH.GeminiModelAction)
	}
}
