package handler

import (
	"github.com/gin-gonic/gin"
	"routerx/internal/common"
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
	// TODO: Phase 3 实现
	// 1. 从 context 获取 token (由 ApiKeyAuth 中间件注入)
	// 2. 读取请求体
	// 3. 调用 svc.RelayChatCompletion
	// 4. 流式 / 非流式分别处理
	common.Fail(c, "not implemented")
}

// POST /v1/completions — 文本补全转发 (Legacy)
func (h *RelayHandler) Completions(c *gin.Context) {
	// TODO: Phase 6 实现
	common.Fail(c, "not implemented")
}

// POST /v1/embeddings — 向量嵌入转发
func (h *RelayHandler) Embeddings(c *gin.Context) {
	// TODO: Phase 6 实现
	common.Fail(c, "not implemented")
}

// POST /v1/images/generations — 图像生成转发
func (h *RelayHandler) ImageGenerations(c *gin.Context) {
	// TODO: Phase 6 实现
	common.Fail(c, "not implemented")
}

// POST /v1/audio/transcriptions — 语音转文字转发
func (h *RelayHandler) AudioTranscriptions(c *gin.Context) {
	// TODO: Phase 6 实现
	common.Fail(c, "not implemented")
}

// POST /v1/audio/speech — 文字转语音转发
func (h *RelayHandler) AudioSpeech(c *gin.Context) {
	// TODO: Phase 6 实现
	common.Fail(c, "not implemented")
}

// GET /v1/models — 模型列表
func (h *RelayHandler) ListModels(c *gin.Context) {
	// TODO: Phase 3 实现
	common.Fail(c, "not implemented")
}
