package relay

import (
	"context"
	"net/http"

	"routerx/internal/common"
)

// AzureAdapter Azure OpenAI 厂商适配器。
// Azure 的 API 路径格式: /openai/deployments/{model}/chat/completions?api-version=2024-02-15-preview
// 使用 api-key header 而非 Bearer token。
type AzureAdapter struct{}

func init() {
	Register(common.ChannelTypeAzure, func() Adapter { return &AzureAdapter{} })
}

func (a *AzureAdapter) GetChannelType() int {
	return common.ChannelTypeAzure
}

func (a *AzureAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	// TODO: Phase 7 实现
	// Azure 请求体与 OpenAI 基本一致，可能需处理 api-version 差异
	return body, nil
}

func (a *AzureAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	// TODO: Phase 7 实现
	// 构建 Azure 格式端点: /openai/deployments/{model}/...?api-version=...
	return ""
}

func (a *AzureAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	// TODO: Phase 7 实现
	// 使用 Header: api-key: {apiKey} (非 Bearer)
	return nil, nil
}

func (a *AzureAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	// TODO: Phase 7 实现
	return nil, nil, nil
}

func (a *AzureAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	// TODO: Phase 7 实现
	// Azure 通过 deployments API 获取
	return nil, nil
}
