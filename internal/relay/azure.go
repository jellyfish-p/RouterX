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
// Chat/Completions/Embeddings 继续使用 deployment-path 形态；
// Image Generations/Audio Speech 使用 Azure /openai/v1 形态，model 字段就是部署名。
// Azure 使用 api-key header 而非 Bearer token。
type AzureAdapter struct{}

const defaultAzureAPIVersion = "2024-02-15-preview"
const azureV1PreviewAPIVersion = "preview"

func init() {
	Register(common.ChannelTypeAzure, func() Adapter { return &AzureAdapter{} })
}

func (a *AzureAdapter) GetChannelType() int {
	return common.ChannelTypeAzure
}

func (a *AzureAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	if apiType != APIChatCompletions && apiType != APICompletions && apiType != APIEmbeddings && !azureUsesV1Endpoint(apiType) {
		return nil, errors.New("unsupported api type")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	// Azure deployment-path API 已经由路径表达 model；/openai/v1 API 仍需要 model 选择部署。
	if !azureUsesV1Endpoint(apiType) {
		delete(payload, "model")
	}
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
	case APICompletions:
		return "/openai/deployments/" + deployment + "/completions?api-version=" + defaultAzureAPIVersion
	case APIEmbeddings:
		return "/openai/deployments/" + deployment + "/embeddings?api-version=" + defaultAzureAPIVersion
	case APIImagesGenerations:
		return "/openai/v1/images/generations?api-version=" + azureV1PreviewAPIVersion
	case APIAudioSpeech:
		return "/openai/v1/audio/speech?api-version=" + azureV1PreviewAPIVersion
	case APIAudioTranscriptions:
		return "/openai/v1/audio/transcriptions?api-version=" + azureV1PreviewAPIVersion
	case APIAudioTranslations:
		return "/openai/v1/audio/translations?api-version=" + azureV1PreviewAPIVersion
	default:
		return ""
	}
}

func (a *AzureAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	return a.doRequest(ctx, baseURL, endpoint, apiKey, body, "application/json")
}

func (a *AzureAdapter) DoRequestWithContentType(ctx context.Context, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error) {
	return a.doRequest(ctx, baseURL, endpoint, apiKey, body, contentType)
}

func (a *AzureAdapter) doRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error) {
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
	SetRequestIDHeader(req)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		contentType = strings.TrimSpace(contentType)
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	ApplyUpstreamOptions(req)
	return http.DefaultClient.Do(req)
}

func (a *AzureAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	if apiType != APIChatCompletions && apiType != APICompletions && apiType != APIEmbeddings && apiType != APIImagesGenerations && apiType != APIAudioTranscriptions && apiType != APIAudioTranslations {
		return nil, nil, errors.New("unsupported api type")
	}
	if !json.Valid(body) {
		return nil, nil, errors.New("upstream returned invalid json")
	}
	return body, extractOpenAIUsage(body), nil
}

func azureUsesV1Endpoint(apiType APIType) bool {
	return apiType == APIImagesGenerations || apiType == APIAudioSpeech
}

// GetModelList returns Azure deployment IDs because RouterX uses request model
// names as the deployment path segment for Azure OpenAI Chat Completions.
func (a *AzureAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	resp, err := a.DoRequest(ctx, baseURL, "/openai/deployments?api-version="+defaultAzureAPIVersion, apiKey, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New(common.FormatHTTPError(resp.StatusCode, "model list request failed"))
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(result.Data))
	for _, item := range result.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}
