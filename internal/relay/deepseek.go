package relay

import (
	"context"
	"net/http"

	"routerx/internal/common"
)

// DeepSeekAdapter DeepSeek 厂商适配器。
// DeepSeek API 完全兼容 OpenAI 格式，可直接复用 OpenAIAdapter 行为。
type DeepSeekAdapter struct{}

func init() {
	Register(common.ChannelTypeDeepSeek, func() Adapter { return &DeepSeekAdapter{} })
}

func (a *DeepSeekAdapter) GetChannelType() int {
	return common.ChannelTypeDeepSeek
}

func (a *DeepSeekAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	// TODO: Phase 7 实现
	// DeepSeek 完全兼容 OpenAI 格式，透传即可
	return body, nil
}

func (a *DeepSeekAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	// TODO: Phase 7 实现
	// 标准 OpenAI 路径: /v1/chat/completions
	return ""
}

func (a *DeepSeekAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	// TODO: Phase 7 实现
	// Authorization: Bearer {apiKey}
	return nil, nil
}

func (a *DeepSeekAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	// TODO: Phase 7 实现
	return nil, nil, nil
}

func (a *DeepSeekAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	// TODO: Phase 7 实现
	// GET /v1/models
	return nil, nil
}
