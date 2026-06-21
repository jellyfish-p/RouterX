package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
	"routerx/internal/middleware"
	"routerx/internal/relay"
	"routerx/internal/service"
)

type RelayHandler struct {
	svc *service.RelayService
}

var errRelayRequestBodyTooLarge = errors.New("relay request body too large")

func NewRelayHandler(svc *service.RelayService) *RelayHandler {
	return &RelayHandler{svc: svc}
}

// POST /v1/chat/completions — 对话补全转发
func (h *RelayHandler) ChatCompletions(c *gin.Context) {
	h.relayOpenAI(c, relay.APIChatCompletions)
}

// POST /v1/responses — Responses API 转发
func (h *RelayHandler) Responses(c *gin.Context) {
	h.relayOpenAI(c, relay.APIResponses)
}

func (h *RelayHandler) relayOpenAI(c *gin.Context, apiType relay.APIType) {
	token, ok := middleware.CurrentAPIToken(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.OpenAIError("invalid api key", "authentication_error", "invalid_api_key"))
		return
	}
	body, err := h.readRelayBody(c)
	if err != nil {
		writeOpenAIReadBodyError(c, err)
		return
	}
	if openAIAPIAllowsMultipart(apiType) && requestIsMultipart(c.GetHeader("Content-Type")) {
		resp, _, err := h.svc.RelayMultipart(relayRequestContext(c), token, apiType, body, c.GetHeader("Content-Type"), c.ClientIP())
		if err != nil {
			writeRelayError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
		return
	}
	if openAIAPIAllowsStream(apiType) && requestWantsStream(body) {
		result, err := h.svc.RelayStream(relayRequestContext(c), token, apiType, body, c.ClientIP())
		if err != nil {
			writeRelayError(c, err)
			return
		}
		c.Header("Content-Type", result.ContentType)
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Status(http.StatusOK)
		_, _ = result.Forward(func(chunk []byte) error {
			_, writeErr := c.Writer.Write(chunk)
			return writeErr
		}, func() {
			c.Writer.Flush()
		})
		return
	}
	resp, _, err := h.svc.Relay(relayRequestContext(c), token, apiType, body, c.ClientIP())
	if err != nil {
		writeRelayError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
}

// POST /v1/completions — 文本补全转发 (Legacy)
func (h *RelayHandler) Completions(c *gin.Context) {
	h.relayOpenAI(c, relay.APICompletions)
}

// POST /v1/embeddings — 向量嵌入转发
func (h *RelayHandler) Embeddings(c *gin.Context) {
	h.relayOpenAI(c, relay.APIEmbeddings)
}

// POST /v1/images/generations — 图像生成转发
func (h *RelayHandler) ImageGenerations(c *gin.Context) {
	h.relayOpenAI(c, relay.APIImagesGenerations)
}

func (h *RelayHandler) ImageEdits(c *gin.Context) {
	h.relayOpenAI(c, relay.APIImagesEdits)
}

func (h *RelayHandler) ImageVariations(c *gin.Context) {
	h.relayOpenAI(c, relay.APIImagesVariations)
}

// POST /v1/audio/transcriptions — 语音转文字转发
func (h *RelayHandler) AudioTranscriptions(c *gin.Context) {
	h.relayOpenAI(c, relay.APIAudioTranscriptions)
}

func (h *RelayHandler) AudioTranslations(c *gin.Context) {
	h.relayOpenAI(c, relay.APIAudioTranslations)
}

// POST /v1/audio/speech — 文字转语音转发
func (h *RelayHandler) AudioSpeech(c *gin.Context) {
	token, ok := middleware.CurrentAPIToken(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.OpenAIError("invalid api key", "authentication_error", "invalid_api_key"))
		return
	}
	body, err := h.readRelayBody(c)
	if err != nil {
		writeOpenAIReadBodyError(c, err)
		return
	}
	result, err := h.svc.RelayRaw(relayRequestContext(c), token, relay.APIAudioSpeech, body, c.ClientIP())
	if err != nil {
		writeRelayError(c, err)
		return
	}
	c.Data(http.StatusOK, result.ContentType, result.Body)
}

func (h *RelayHandler) Moderations(c *gin.Context) {
	h.relayOpenAI(c, relay.APIModerations)
}

// GET /v1/models — 模型列表
func (h *RelayHandler) ListModels(c *gin.Context) {
	protocol := modelListProtocol(c)
	token, ok := middleware.CurrentAPIToken(c)
	if ok {
		if err := h.svc.CheckTokenAPIScope(token, relay.APIModels, c.ClientIP()); err != nil {
			writeProtocolRelayError(c, protocol, err)
			return
		}
	}
	body, err := h.listModelsForRequest(c)
	if err != nil {
		writeProtocolRelayError(c, protocol, &service.HTTPError{
			Status:  http.StatusInternalServerError,
			Message: "failed to list models",
			Type:    "server_error",
			Code:    "model_list_failed",
		})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

func (h *RelayHandler) ModelDetail(c *gin.Context) {
	protocol := modelListProtocol(c)
	token, ok := middleware.CurrentAPIToken(c)
	if ok {
		if err := h.svc.CheckTokenAPIScope(token, relay.APIModels, c.ClientIP()); err != nil {
			writeProtocolRelayError(c, protocol, err)
			return
		}
	}
	var body []byte
	var err error
	switch protocol {
	case "gemini":
		body, err = h.svc.GeminiModelDetail(c.Param("model"))
	case "anthropic":
		body, err = h.svc.AnthropicModelDetail(c.Param("model"))
	default:
		body, err = h.svc.ModelDetail(c.Param("model"))
	}
	if err != nil {
		writeProtocolRelayError(c, protocol, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

func (h *RelayHandler) AnthropicMessages(c *gin.Context) {
	token, ok := middleware.CurrentAPIToken(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.AnthropicError("invalid api key", "authentication_error"))
		return
	}
	body, err := h.readRelayBody(c)
	if err != nil {
		writeAnthropicReadBodyError(c, err)
		return
	}
	if requestWantsStream(body) {
		result, err := h.svc.RelayAnthropicMessagesStream(relayRequestContext(c), token, body, c.ClientIP())
		if err != nil {
			writeAnthropicRelayError(c, err)
			return
		}
		c.Header("Content-Type", result.ContentType)
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Status(http.StatusOK)
		_, _ = result.Forward(func(chunk []byte) error {
			_, writeErr := c.Writer.Write(chunk)
			return writeErr
		}, func() {
			c.Writer.Flush()
		})
		return
	}
	resp, _, err := h.svc.RelayAnthropicMessages(relayRequestContext(c), token, body, c.ClientIP())
	if err != nil {
		writeAnthropicRelayError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
}

func (h *RelayHandler) AnthropicCountTokens(c *gin.Context) {
	body, err := h.readRelayBody(c)
	if err != nil {
		writeAnthropicReadBodyError(c, err)
		return
	}
	resp, err := h.svc.AnthropicCountTokens(body)
	if err != nil {
		writeAnthropicRelayError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
}

func (h *RelayHandler) GeminiModelAction(c *gin.Context) {
	modelName, action := splitGeminiModelAction(c.Param("model"))
	body, err := h.readRelayBody(c)
	if err != nil {
		writeGeminiReadBodyError(c, err)
		return
	}
	switch action {
	case "generateContent", "streamGenerateContent", "embedContent", "batchEmbedContents":
		token, ok := middleware.CurrentAPIToken(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, common.GeminiError(http.StatusUnauthorized, "invalid api key", geminiRelayStatusText(http.StatusUnauthorized)))
			return
		}
		if action == "embedContent" {
			resp, _, err := h.svc.RelayGeminiEmbedContent(relayRequestContext(c), token, modelName, body, c.ClientIP())
			if err != nil {
				writeGeminiRelayError(c, err)
				return
			}
			c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
			return
		}
		if action == "batchEmbedContents" {
			resp, _, err := h.svc.RelayGeminiBatchEmbedContents(relayRequestContext(c), token, modelName, body, c.ClientIP())
			if err != nil {
				writeGeminiRelayError(c, err)
				return
			}
			c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
			return
		}
		if action == "streamGenerateContent" {
			result, err := h.svc.RelayGeminiGenerateContentStream(relayRequestContext(c), token, modelName, body, c.ClientIP())
			if err != nil {
				writeGeminiRelayError(c, err)
				return
			}
			c.Header("Content-Type", result.ContentType)
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Status(http.StatusOK)
			_, _ = result.Forward(func(chunk []byte) error {
				_, writeErr := c.Writer.Write(chunk)
				return writeErr
			}, func() {
				c.Writer.Flush()
			})
			return
		}
		resp, _, err := h.svc.RelayGeminiGenerateContent(relayRequestContext(c), token, modelName, body, action == "streamGenerateContent", c.ClientIP())
		if err != nil {
			writeGeminiRelayError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
	case "countTokens":
		resp, err := h.svc.GeminiCountTokens(body)
		if err != nil {
			writeGeminiRelayError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", resp)
	default:
		c.JSON(http.StatusNotFound, common.GeminiError(http.StatusNotFound, "unsupported api", geminiRelayStatusText(http.StatusNotFound)))
	}
}

func (h *RelayHandler) listModelsForRequest(c *gin.Context) ([]byte, error) {
	switch modelListProtocol(c) {
	case "gemini":
		return h.svc.ListGeminiModels()
	case "anthropic":
		return h.svc.ListAnthropicModels()
	default:
		return h.svc.ListModels()
	}
}

func modelListProtocol(c *gin.Context) string {
	if protocol := normalizeModelListProtocol(c.Query("format")); protocol != "" {
		return protocol
	}
	if protocol := normalizeModelListProtocol(c.Query("routerx_protocol")); protocol != "" {
		return protocol
	}
	if protocol := normalizeModelListProtocol(c.GetHeader("X-RouterX-Protocol")); protocol != "" {
		return protocol
	}
	if strings.TrimSpace(c.GetHeader("anthropic-version")) != "" {
		return "anthropic"
	}
	return "openai"
}

func normalizeModelListProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "google", "gemini":
		return "gemini"
	case "claude", "anthropic":
		return "anthropic"
	case "openai", "openai-compatible", "openai_compatible":
		return "openai"
	default:
		return ""
	}
}

func relayRequestContext(c *gin.Context) context.Context {
	ctx := service.ContextWithRelayUserAgent(c.Request.Context(), c.GetHeader("User-Agent"))
	ctx = service.ContextWithRelayRouterXHop(ctx, c.GetHeader(relay.RouterXHopHeaderName))
	ctx = service.ContextWithRelayRouterXChain(ctx, c.GetHeader(relay.RouterXChainHeaderName))
	ctx = service.ContextWithRelayRequestID(ctx, c.GetString("request_id"))
	ctx = relay.ContextWithTraceparent(ctx, c.GetString("traceparent"))
	return relay.ContextWithTracestate(ctx, c.GetString("tracestate"))
}

func writeRelayError(c *gin.Context, err error) {
	var httpErr *service.HTTPError
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.Status, common.OpenAIError(httpErr.Message, httpErr.Type, httpErr.Code))
		return
	}
	c.JSON(http.StatusInternalServerError, common.OpenAIError("internal server error", "server_error", "internal_error"))
}

func writeProtocolRelayError(c *gin.Context, protocol string, err error) {
	switch protocol {
	case "anthropic":
		writeAnthropicRelayError(c, err)
	case "gemini":
		writeGeminiRelayError(c, err)
	default:
		writeRelayError(c, err)
	}
}

func writeAnthropicRelayError(c *gin.Context, err error) {
	var httpErr *service.HTTPError
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.Status, common.AnthropicError(httpErr.Message, httpErr.Type))
		return
	}
	c.JSON(http.StatusInternalServerError, common.AnthropicError("internal server error", "server_error"))
}

func writeGeminiRelayError(c *gin.Context, err error) {
	var httpErr *service.HTTPError
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.Status, common.GeminiError(httpErr.Status, httpErr.Message, geminiRelayStatusText(httpErr.Status)))
		return
	}
	c.JSON(http.StatusInternalServerError, common.GeminiError(http.StatusInternalServerError, "internal server error", geminiRelayStatusText(http.StatusInternalServerError)))
}

func geminiRelayStatusText(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusRequestEntityTooLarge:
		return "RESOURCE_EXHAUSTED"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

func writeUnsupported(c *gin.Context) {
	c.JSON(http.StatusNotFound, common.OpenAIError("unsupported api", "invalid_request_error", "unsupported_api"))
}

// Unsupported returns the OpenAI-compatible error used by authenticated unknown
// /v1 routes.
func (h *RelayHandler) Unsupported(c *gin.Context) {
	writeUnsupported(c)
}

func (h *RelayHandler) readRelayBody(c *gin.Context) ([]byte, error) {
	var reader io.Reader = c.Request.Body
	if h != nil && h.svc != nil {
		if limit := h.svc.MaxRequestBodyBytes(); limit > 0 {
			reader = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, errRelayRequestBodyTooLarge
		}
		return nil, err
	}
	return body, nil
}

func writeOpenAIReadBodyError(c *gin.Context, err error) {
	if errors.Is(err, errRelayRequestBodyTooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, common.OpenAIError("request body too large", "invalid_request_error", "request_body_too_large"))
		return
	}
	c.JSON(http.StatusBadRequest, common.OpenAIError("failed to read request body", "invalid_request_error", "invalid_request"))
}

func writeAnthropicReadBodyError(c *gin.Context, err error) {
	if errors.Is(err, errRelayRequestBodyTooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, common.AnthropicError("request body too large", "invalid_request_error"))
		return
	}
	c.JSON(http.StatusBadRequest, common.AnthropicError("failed to read request body", "invalid_request_error"))
}

func writeGeminiReadBodyError(c *gin.Context, err error) {
	if errors.Is(err, errRelayRequestBodyTooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, common.GeminiError(http.StatusRequestEntityTooLarge, "request body too large", geminiRelayStatusText(http.StatusRequestEntityTooLarge)))
		return
	}
	c.JSON(http.StatusBadRequest, common.GeminiError(http.StatusBadRequest, "failed to read request body", geminiRelayStatusText(http.StatusBadRequest)))
}

func requestWantsStream(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Stream
}

func openAIAPIAllowsStream(apiType relay.APIType) bool {
	return apiType == relay.APIChatCompletions ||
		apiType == relay.APICompletions ||
		apiType == relay.APIResponses
}

func openAIAPIAllowsMultipart(apiType relay.APIType) bool {
	return apiType == relay.APIImagesEdits ||
		apiType == relay.APIImagesVariations ||
		apiType == relay.APIAudioTranscriptions ||
		apiType == relay.APIAudioTranslations
}

func requestIsMultipart(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data")
}

func splitGeminiModelAction(value string) (string, string) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "/")
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return value, ""
	}
	return value[:idx], value[idx+1:]
}
