package relay

import (
	"context"
	"net/http"

	"routerx/internal/common"
)

// OpenAIAdapter OpenAI / OpenAI-compatible 厂商适配器。
// 覆盖 ChannelTypeOpenAI (1) 和 ChannelTypeOpenAICompat (100)。
// OpenAI 原生 API 格式与 RouterX 一致，无需转换请求/响应格式。
type OpenAIAdapter struct{}

func init() {
	Register(common.ChannelTypeOpenAI, func() Adapter { return &OpenAIAdapter{} })
	Register(common.ChannelTypeOpenAICompat, func() Adapter { return &OpenAIAdapter{} })
}

func (a *OpenAIAdapter) GetChannelType() int {
	return common.ChannelTypeOpenAI
}

func (a *OpenAIAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	// TODO: Phase 3 实现
	// OpenAI 格式无需转换，直接透传
	// 可能需要根据 apiType 调整路由
	return body, nil
}

func (a *OpenAIAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	// TODO: Phase 3 实现
	// 返回 OpenAI 标准 API 路径
	// 如: /v1/chat/completions
	return ""
}

func (a *OpenAIAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	// TODO: Phase 3 实现
	// HTTP POST + Authorization: Bearer {apiKey}
	// 支持流式和非流式
	// 超时控制从 SettingService 读取 relay.timeout
	return nil, nil
}

func (a *OpenAIAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	// TODO: Phase 3 实现
	// 直接解析 OpenAI 响应，提取 Usage
	return nil, nil, nil
}

func (a *OpenAIAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	// TODO: Phase 3 实现
	// GET {baseURL}/v1/models
	return nil, nil
}
