package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"routerx/internal/common"
)

// OpenAIAdapter OpenAI / OpenAI-compatible 厂商适配器。
// 覆盖 ChannelTypeOpenAI (1) 和 ChannelTypeOpenAICompat (100)。
// OpenAI 原生 API 格式与 RouterX 一致，无需转换请求/响应格式。
type OpenAIAdapter struct{}
type RouterXAdapter struct{ OpenAIAdapter }

func init() {
	Register(common.ChannelTypeOpenAI, func() Adapter { return &OpenAIAdapter{} })
	Register(common.ChannelTypeOpenAICompat, func() Adapter { return &OpenAIAdapter{} })
	Register(common.ChannelTypeXAI, func() Adapter { return &OpenAIAdapter{} })
	Register(common.ChannelTypeRouterX, func() Adapter { return &RouterXAdapter{} })
}

func (a *OpenAIAdapter) GetChannelType() int {
	return common.ChannelTypeOpenAI
}

func (a *OpenAIAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	if apiType == APIModels {
		return nil, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	delete(payload, "routerx")
	return json.Marshal(payload)
}

func (a *RouterXAdapter) GetChannelType() int {
	return common.ChannelTypeRouterX
}

func (a *RouterXAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	if apiType == APIModels {
		return nil, nil
	}
	if !json.Valid(body) {
		return nil, errors.New("invalid json")
	}
	return body, nil
}

func (a *OpenAIAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	switch apiType {
	case APIResponses:
		return "/v1/responses"
	case APIChatCompletions:
		return "/v1/chat/completions"
	case APICompletions:
		return "/v1/completions"
	case APIEmbeddings:
		return "/v1/embeddings"
	case APIImagesGenerations:
		return "/v1/images/generations"
	case APIImagesEdits:
		return "/v1/images/edits"
	case APIImagesVariations:
		return "/v1/images/variations"
	case APIAudioTranscriptions:
		return "/v1/audio/transcriptions"
	case APIAudioTranslations:
		return "/v1/audio/translations"
	case APIAudioSpeech:
		return "/v1/audio/speech"
	case APIModerations:
		return "/v1/moderations"
	case APIModels:
		return "/v1/models"
	default:
		return ""
	}
}

func (a *OpenAIAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
	return a.doRequest(ctx, baseURL, endpoint, apiKey, body, "application/json")
}

func (a *OpenAIAdapter) DoRequestWithContentType(ctx context.Context, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error) {
	return a.doRequest(ctx, baseURL, endpoint, apiKey, body, contentType)
}

func (a *OpenAIAdapter) doRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error) {
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
	req.Header.Set("Authorization", "Bearer "+apiKey)
	SetRequestIDHeader(req)
	SetRouterXHopHeader(req)
	SetRouterXChainHeader(req)
	if body != nil {
		contentType = strings.TrimSpace(contentType)
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	ApplyUpstreamOptions(req)
	return http.DefaultClient.Do(req)
}

func (a *OpenAIAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	if !json.Valid(body) {
		return nil, nil, errors.New("upstream returned invalid json")
	}
	return body, extractOpenAIUsage(body), nil
}

func extractOpenAIUsage(body []byte) *Usage {
	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Usage) == 0 || string(envelope.Usage) == "null" {
		return nil
	}
	var usage Usage
	_ = json.Unmarshal(envelope.Usage, &usage)
	var responsesUsage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	}
	_ = json.Unmarshal(envelope.Usage, &responsesUsage)
	if usage.PromptTokens == 0 {
		usage.PromptTokens = responsesUsage.InputTokens
	}
	if usage.CompletionTokens == 0 {
		usage.CompletionTokens = responsesUsage.OutputTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = responsesUsage.TotalTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return &usage
}

func (a *OpenAIAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	resp, err := a.DoRequest(ctx, baseURL, "/v1/models", apiKey, nil)
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
		if item.ID != "" {
			models = append(models, item.ID)
		}
	}
	return models, nil
}

func joinBaseURL(baseURL, endpoint string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	if endpoint == "" {
		return baseURL
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return baseURL + endpoint
}
