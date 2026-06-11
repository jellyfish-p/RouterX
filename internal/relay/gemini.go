package relay

import (
	"context"
	"net/http"

	"routerx/internal/common"
)

// GeminiAdapter Google Gemini 厂商适配器。
// Gemini API 格式与 OpenAI 不同，需要双向转换。
type GeminiAdapter struct{}

func init() {
	Register(common.ChannelTypeGemini, func() Adapter { return &GeminiAdapter{} })
}

func (a *GeminiAdapter) GetChannelType() int {
	return common.ChannelTypeGemini
}

func (a *GeminiAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	// TODO: Phase 7 实现
	// OpenAI ChatCompletionRequest → Gemini GenerateContentRequest
	// 格式差异:
	//   - messages → contents[] (parts[])
	//   - system message → system_instruction
	//   - stop → stopSequences
	//   - max_tokens → maxOutputTokens
	return nil, nil
}

func (a *GeminiAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	// TODO: Phase 7 实现
	// POST /v1beta/models/{model}:generateContent?key={apiKey}
	return ""
}

func (a *GeminiAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	// TODO: Phase 7 实现
	// API Key 通过 URL query 参数传递，非 Header
	return nil, nil
}

func (a *GeminiAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	// TODO: Phase 7 实现
	// Gemini GenerateContentResponse → OpenAI ChatCompletionResponse
	return nil, nil, nil
}

func (a *GeminiAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	// TODO: Phase 7 实现
	// GET /v1beta/models?key={apiKey}
	return nil, nil
}
