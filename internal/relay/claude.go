package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"routerx/internal/common"
)

// ClaudeAdapter Anthropic Claude 厂商适配器。
// Claude Messages API 格式与 OpenAI Chat Completions 格式不同，需要双向转换。
type ClaudeAdapter struct{}

func init() {
	Register(common.ChannelTypeClaude, func() Adapter { return &ClaudeAdapter{} })
}

func (a *ClaudeAdapter) GetChannelType() int {
	return common.ChannelTypeClaude
}

func (a *ClaudeAdapter) ConvertRequest(apiType APIType, body []byte) ([]byte, error) {
	if apiType != APIChatCompletions && apiType != APIAnthropicMessages {
		return nil, errors.New("unsupported api type")
	}
	var input openAIChatRequest
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}
	type claudeMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	output := struct {
		Model       string          `json:"model"`
		System      string          `json:"system,omitempty"`
		Messages    []claudeMessage `json:"messages"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature,omitempty"`
		TopP        *float64        `json:"top_p,omitempty"`
		Stop        []string        `json:"stop_sequences,omitempty"`
		Stream      bool            `json:"stream,omitempty"`
	}{
		Model:       input.Model,
		MaxTokens:   1024,
		Temperature: input.Temperature,
		TopP:        input.TopP,
		Stream:      input.Stream,
	}
	if input.MaxTokens != nil && *input.MaxTokens > 0 {
		output.MaxTokens = *input.MaxTokens
	}
	output.Stop = parseStopStrings(input.Stop)
	systemParts := make([]string, 0)
	for _, message := range input.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		content := messageContentText(message.Content)
		if content == "" {
			continue
		}
		switch role {
		case "system":
			systemParts = append(systemParts, content)
		case "assistant":
			output.Messages = append(output.Messages, claudeMessage{Role: "assistant", Content: content})
		default:
			output.Messages = append(output.Messages, claudeMessage{Role: "user", Content: content})
		}
	}
	output.System = strings.Join(systemParts, "\n")
	return json.Marshal(output)
}

func (a *ClaudeAdapter) GetAPIEndpoint(apiType APIType, model string) string {
	switch apiType {
	case APIChatCompletions, APIAnthropicMessages:
		return "/v1/messages"
	case APIModels:
		return "/v1/models"
	default:
		return ""
	}
}

func (a *ClaudeAdapter) DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error) {
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
	req, err := http.NewRequestWithContext(ctx, method, joinClaudeBaseURL(baseURL, endpoint), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	SetRequestIDHeader(req)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return http.DefaultClient.Do(req)
}

func (a *ClaudeAdapter) ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error) {
	if apiType != APIChatCompletions && apiType != APIAnthropicMessages {
		return nil, nil, errors.New("unsupported api type")
	}
	var input struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, nil, err
	}
	parts := make([]string, 0, len(input.Content))
	for _, content := range input.Content {
		if content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	usage := &Usage{
		PromptTokens:     input.Usage.InputTokens,
		CompletionTokens: input.Usage.OutputTokens,
		TotalTokens:      input.Usage.InputTokens + input.Usage.OutputTokens,
	}
	output := map[string]interface{}{
		"id":      input.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   input.Model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": strings.Join(parts, ""),
				},
				"finish_reason": normalizeClaudeFinishReason(input.StopReason),
			},
		},
		"usage": usage,
	}
	converted, err := json.Marshal(output)
	return converted, usage, err
}

func (a *ClaudeAdapter) GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error) {
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

func parseStopStrings(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && single != "" {
		return []string{single}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	return nil
}

func normalizeClaudeFinishReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return reason
	}
}

func joinClaudeBaseURL(baseURL, endpoint string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return baseURL + endpoint
}
