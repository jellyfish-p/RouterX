package common

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Response 统一响应结构
type Response struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

// Success 返回成功响应 (HTTP 200)
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Success: true,
		Data:    data,
		Message: "",
	})
}

// SuccessMsg 返回带消息的成功响应
func SuccessMsg(c *gin.Context, message string) {
	c.JSON(http.StatusOK, Response{
		Success: true,
		Data:    nil,
		Message: message,
	})
}

// Fail 返回失败响应 (HTTP 200, success=false)
func Fail(c *gin.Context, message string) {
	c.JSON(http.StatusOK, Response{
		Success: false,
		Data:    nil,
		Message: message,
	})
}

// FailWithStatus 返回带 HTTP 状态码的失败响应
func FailWithStatus(c *gin.Context, httpStatus int, message string) {
	c.JSON(httpStatus, Response{
		Success: false,
		Data:    nil,
		Message: message,
	})
}

// AnthropicError wraps an error in the Anthropic Messages error envelope.
func AnthropicError(message, typ string) gin.H {
	return gin.H{
		"type": "error",
		"error": gin.H{
			"type":    typ,
			"message": message,
		},
	}
}

// GeminiError wraps an error in the Google/Gemini error envelope.
func GeminiError(status int, message, statusText string) gin.H {
	return gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  statusText,
		},
	}
}
