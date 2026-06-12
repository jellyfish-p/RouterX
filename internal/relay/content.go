package relay

import (
	"bytes"
	"encoding/json"
	"strings"
)

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        json.RawMessage `json:"stop,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func messageContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err == nil {
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			if text := strings.TrimSpace(contentPartText(part)); text != "" {
				values = append(values, text)
			}
		}
		return strings.Join(values, "\n")
	}
	var object struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		if strings.TrimSpace(object.Text) != "" {
			return object.Text
		}
		return compactJSON(raw)
	}
	return string(raw)
}

func TextFromContent(raw json.RawMessage) string {
	return messageContentText(raw)
}

func contentPartText(raw json.RawMessage) string {
	var object struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && strings.TrimSpace(object.Text) != "" {
		return object.Text
	}
	return compactJSON(raw)
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		return buf.String()
	}
	return string(raw)
}

func jsonString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
