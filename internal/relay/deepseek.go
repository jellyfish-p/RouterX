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
	return (&OpenAIAdapter{}).ConvertRequest(apiType, body)
}

func (a *DeepSeekAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	return (&OpenAIAdapter{}).GetAPIEndpoint(apiType, model)
}

func (a *DeepSeekAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	return (&OpenAIAdapter{}).DoRequest(ctx, baseURL, endpoint, apiKey, body)
}

func (a *DeepSeekAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	return (&OpenAIAdapter{}).ConvertResponse(apiType, body)
}

func (a *DeepSeekAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	return (&OpenAIAdapter{}).GetModelList(ctx, baseURL, apiKey)
}
