package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"routerx/internal/common"
)

// AzureAdapter Azure OpenAI 厂商适配器。
// Azure 的 API 路径格式: /openai/deployments/{model}/chat/completions?api-version=2024-02-15-preview
// 使用 api-key header 而非 Bearer token。
type AzureAdapter struct{}

const defaultAzureAPIVersion = "2024-02-15-preview"

func init() {
	Register(common.ChannelTypeAzure, func() Adapter { return &AzureAdapter{} })
}

func (a *AzureAdapter) GetChannelType() int {
	return common.ChannelTypeAzure
}

func (a *AzureAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	if apiType != APIChatCompletions {
		return nil, errors.New("unsupported api type")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	// Azure deployment 已经由路径表达，真实上游请求体不再携带 RouterX 私有字段或 model。
	delete(payload, "model")
	delete(payload, "routerx")
	return json.Marshal(payload)
}

func (a *AzureAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	deployment := url.PathEscape(model)
	switch apiType {
	case APIChatCompletions:
		return "/openai/deployments/" + deployment + "/chat/completions?api-version=" + defaultAzureAPIVersion
	default:
		return ""
	}
}

func (a *AzureAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	if endpoint == "" {
		return nil, errors.New("unsupported api type")
	}
	method := http.MethodPost
	var reader io.Reader
	if body == nil {
		method = http.MethodGet
	} else {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, joinBaseURL(baseURL, endpoint), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return http.DefaultClient.Do(req)
}

func (a *AzureAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	if apiType != APIChatCompletions {
		return nil, nil, errors.New("unsupported api type")
	}
	if !json.Valid(body) {
		return nil, nil, errors.New("upstream returned invalid json")
	}
	return body, extractOpenAIUsage(body), nil
}

func (a *AzureAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	return nil, errors.New("azure model list is not implemented")
}
