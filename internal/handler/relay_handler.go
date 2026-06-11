package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/middleware"
	"routerx/internal/service"
)

type RelayHandler struct {
	svc *service.RelayService
}

func NewRelayHandler(svc *service.RelayService) *RelayHandler {
	return &RelayHandler{svc: svc}
}

// POST /v1/chat/completions — 对话补全转发
func (h *RelayHandler) ChatCompletions(c *gin.Context) {
	token, ok := middleware.CurrentAPIToken(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.OpenAIError("invalid api key", "authentication_error", "invalid_api_key"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 10<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, common.OpenAIError("failed to read request body", "invalid_request_error", "invalid_request"))
		return
	}
	resp, _, err := h.svc.RelayChatCompletion(c.Request.Context(), token, body, c.ClientIP())
	if err != nil {
		writeRelayError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
}

// POST /v1/completions — 文本补全转发 (Legacy)
func (h *RelayHandler) Completions(c *gin.Context) {
	writeUnsupported(c)
}

// POST /v1/embeddings — 向量嵌入转发
func (h *RelayHandler) Embeddings(c *gin.Context) {
	writeUnsupported(c)
}

// POST /v1/images/generations — 图像生成转发
func (h *RelayHandler) ImageGenerations(c *gin.Context) {
	writeUnsupported(c)
}

// POST /v1/audio/transcriptions — 语音转文字转发
func (h *RelayHandler) AudioTranscriptions(c *gin.Context) {
	writeUnsupported(c)
}

// POST /v1/audio/speech — 文字转语音转发
func (h *RelayHandler) AudioSpeech(c *gin.Context) {
	writeUnsupported(c)
}

// GET /v1/models — 模型列表
func (h *RelayHandler) ListModels(c *gin.Context) {
	body, err := h.svc.ListModels()
	if err != nil {
		c.JSON(http.StatusInternalServerError, common.OpenAIError("failed to list models", "server_error", "model_list_failed"))
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

func writeRelayError(c *gin.Context, err error) {
	var httpErr *service.HTTPError
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.Status, common.OpenAIError(httpErr.Message, httpErr.Type, httpErr.Code))
		return
	}
	c.JSON(http.StatusInternalServerError, common.OpenAIError("internal server error", "server_error", "internal_error"))
}

func writeUnsupported(c *gin.Context) {
	c.JSON(http.StatusNotFound, common.OpenAIError("unsupported api", "invalid_request_error", "unsupported_api"))
}
