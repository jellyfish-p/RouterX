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

func init() {
	Register(common.ChannelTypeOpenAI, func() Adapter { return &OpenAIAdapter{} })
	Register(common.ChannelTypeOpenAICompat, func() Adapter { return &OpenAIAdapter{} })
	Register(common.ChannelTypeXAI, func() Adapter { return &OpenAIAdapter{} })
	Register(common.ChannelTypeRouterX, func() Adapter { return &OpenAIAdapter{} })
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return http.DefaultClient.Do(req)
}

func (a *OpenAIAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	if !json.Valid(body) {
		return nil, nil, errors.New("upstream returned invalid json")
	}
	var envelope struct {
		Usage *Usage `json:"usage"`
	}
	_ = json.Unmarshal(body, &envelope)
	return body, envelope.Usage, nil
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
