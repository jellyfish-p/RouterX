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
	// TODO: Phase 7 实现
	// 通义千问兼容 OpenAI 格式，基本透传
	// 注意: 部分模型可能使用 DashScope 特定字段 (如 result_format)
	return body, nil
}

func (a *QwenAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	// TODO: Phase 7 实现
	// DashScope: POST /compatible-mode/v1/chat/completions
	return ""
}

func (a *QwenAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	// TODO: Phase 7 实现
	// Authorization: Bearer {apiKey}
	return nil, nil
}

func (a *QwenAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	// TODO: Phase 7 实现
	return nil, nil, nil
}

func (a *QwenAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	// TODO: Phase 7 实现
	return nil, nil
}
