package relay

import (
	"context"
	"net/http"

	"routerx/internal/common"
)

// QwenAdapter 通义千问 (DashScope) 厂商适配器。
// 兼容 OpenAI 格式，但请求/响应路径和鉴权方式有所不同。
type QwenAdapter struct{}

func init() {
	Register(common.ChannelTypeQwen, func() Adapter { return &QwenAdapter{} })
}

func (a *QwenAdapter) GetChannelType() int {
	return common.ChannelTypeQwen
}

func (a *QwenAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	return (&OpenAIAdapter{}).ConvertRequest(apiType, body)
}

func (a *QwenAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	return (&OpenAIAdapter{}).GetAPIEndpoint(apiType, model)
}

func (a *QwenAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	return (&OpenAIAdapter{}).DoRequest(ctx, baseURL, endpoint, apiKey, body)
}

func (a *QwenAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	return (&OpenAIAdapter{}).ConvertResponse(apiType, body)
}

func (a *QwenAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	return (&OpenAIAdapter{}).GetModelList(ctx, baseURL, apiKey)
}
