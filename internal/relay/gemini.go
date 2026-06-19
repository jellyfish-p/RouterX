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
	"time"

	"routerx/internal/common"
)

// GeminiAdapter Google Gemini 厂商适配器。
// Gemini API 格式与 OpenAI 不同，需要双向转换。
type GeminiAdapter struct{}

func init() {
	Register(common.ChannelTypeGemini, func() Adapter { return &GeminiAdapter{} })
}

func (a *GeminiAdapter) GetChannelType() int {
	return common.ChannelTypeGemini
}

func (a *GeminiAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	if apiType != APIChatCompletions && apiType != APIGeminiGenerateContent && apiType != APIGeminiStreamGenerateContent {
		return nil, errors.New("unsupported api type")
	}
	var input openAIChatRequest
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}
	type geminiPart struct {
		Text string `json:"text"`
	}
	type geminiContent struct {
		Role  string       `json:"role,omitempty"`
		Parts []geminiPart `json:"parts"`
	}
	output := struct {
		Contents          []geminiContent        `json:"contents"`
		SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
		GenerationConfig  map[string]interface{} `json:"generationConfig,omitempty"`
		SafetySettings    json.RawMessage        `json:"safetySettings,omitempty"`
	}{}
	systemParts := make([]geminiPart, 0)
	for _, message := range input.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		content := messageContentText(message.Content)
		if content == "" {
			continue
		}
		if role == "system" {
			systemParts = append(systemParts, geminiPart{Text: content})
			continue
		}
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		output.Contents = append(output.Contents, geminiContent{
			Role:  geminiRole,
			Parts: []geminiPart{{Text: content}},
		})
	}
	if len(systemParts) > 0 {
		output.SystemInstruction = &geminiContent{Parts: systemParts}
	}
	config := map[string]interface{}{}
	if input.MaxTokens != nil && *input.MaxTokens > 0 {
		config["maxOutputTokens"] = *input.MaxTokens
	}
	if input.Temperature != nil {
		config["temperature"] = *input.Temperature
	}
	if input.TopP != nil {
		config["topP"] = *input.TopP
	}
	if stops := parseStopStrings(input.Stop); len(stops) > 0 {
		config["stopSequences"] = stops
	}
	if len(config) > 0 {
		output.GenerationConfig = config
	}
	if raw := bytes.TrimSpace(input.SafetySettings); len(raw) > 0 && string(raw) != "null" {
		// Gemini safety settings are provider-native and intentionally live outside generationConfig.
		output.SafetySettings = append(json.RawMessage(nil), raw...)
	}
	return json.Marshal(output)
}

func (a *GeminiAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	model = strings.TrimPrefix(strings.TrimSpace(model), "models/")
	escapedModel := url.PathEscape(model)
	switch apiType {
	case APIChatCompletions, APIGeminiGenerateContent:
		return "/v1beta/models/" + escapedModel + ":generateContent"
	case APIGeminiStreamGenerateContent:
		return "/v1beta/models/" + escapedModel + ":streamGenerateContent"
	case APIModels:
		return "/v1beta/models"
	default:
		return ""
	}
}

func (a *GeminiAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
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
	targetURL, err := url.Parse(joinGeminiBaseURL(baseURL, endpoint))
	if err != nil {
		return nil, err
	}
	query := targetURL.Query()
	query.Set("key", apiKey)
	targetURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, targetURL.String(), reader)
	if err != nil {
		return nil, err
	}
	SetRequestIDHeader(req)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ApplyUpstreamOptions(req)
	return http.DefaultClient.Do(req)
}

func (a *GeminiAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	if apiType != APIChatCompletions && apiType != APIGeminiGenerateContent && apiType != APIGeminiStreamGenerateContent {
		return nil, nil, errors.New("unsupported api type")
	}
	var input struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, nil, err
	}
	choices := make([]map[string]interface{}, 0, len(input.Candidates))
	for idx, candidate := range input.Candidates {
		parts := make([]string, 0, len(candidate.Content.Parts))
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
		choices = append(choices, map[string]interface{}{
			"index": idx,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": strings.Join(parts, ""),
			},
			"finish_reason": normalizeGeminiFinishReason(candidate.FinishReason),
		})
	}
	usage := &Usage{
		PromptTokens:     input.UsageMetadata.PromptTokenCount,
		CompletionTokens: input.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      input.UsageMetadata.TotalTokenCount,
	}
	output := map[string]interface{}{
		"id":      "chatcmpl-gemini",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   input.ModelVersion,
		"choices": choices,
		"usage":   usage,
	}
	converted, err := json.Marshal(output)
	return converted, usage, err
}

func (a *GeminiAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
	resp, err := a.DoRequest(ctx, baseURL, "/v1beta/models", apiKey, nil)
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
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(result.Models))
	for _, item := range result.Models {
		if item.Name != "" {
			models = append(models, strings.TrimPrefix(item.Name, "models/"))
		}
	}
	return models, nil
}

func normalizeGeminiFinishReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return strings.ToLower(reason)
	}
}

func joinGeminiBaseURL(baseURL, endpoint string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return baseURL + endpoint
}
