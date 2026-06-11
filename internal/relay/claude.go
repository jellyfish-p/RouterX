package relay

import (
	"context"
	"net/http"

	"routerx/internal/common"
)

// ClaudeAdapter Anthropic Claude 厂商适配器。
// Claude Messages API 格式与 OpenAI Chat Completions 格式不同，需要双向转换。
type ClaudeAdapter struct{}

func init() {
	Register(common.ChannelTypeClaude, func() Adapter { return &ClaudeAdapter{} })
}

func (a *ClaudeAdapter) GetChannelType() int {
	return common.ChannelTypeClaude
}

func (a *ClaudeAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	// TODO: Phase 7 实现
	// OpenAI ChatCompletionRequest → Claude MessagesRequest
	// 格式差异:
	//   - role: system/assistant/user → Claude 支持
	//   - max_tokens 含义相同
	//   - temperature, top_p, stop 映射相似
	//   - Authorization: x-api-key:{key}
	return nil, nil
}

func (a *ClaudeAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	// TODO: Phase 7 实现
	// POST /v1/messages
	return ""
}

func (a *ClaudeAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	// TODO: Phase 7 实现
	// Header: x-api-key: {apiKey}, anthropic-version: 2023-06-01
	return nil, nil
}

func (a *ClaudeAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	// TODO: Phase 7 实现
	// Claude MessagesResponse → OpenAI ChatCompletionResponse
	return nil, nil, nil
}

func (a *ClaudeAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	// TODO: Phase 7 实现
	// GET /v1/models (Anthropic)
	return nil, nil
}
