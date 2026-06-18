package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProtocolWrapperRequestErrorsUseStableCodes(t *testing.T) {
	svc := NewRelayService(nil, nil, nil, nil)
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
		code string
	}{
		{
			name: "anthropic missing model",
			call: func() error {
				_, _, err := svc.RelayAnthropicMessages(ctx, nil, []byte(`{"messages":[{"role":"user","content":"hello"}]}`), "192.0.2.1")
				return err
			},
			code: "model_required",
		},
		{
			name: "anthropic invalid json",
			call: func() error {
				_, _, err := svc.RelayAnthropicMessages(ctx, nil, []byte(`{"model":`), "192.0.2.1")
				return err
			},
			code: "invalid_json",
		},
		{
			name: "gemini generate missing model",
			call: func() error {
				_, _, err := svc.RelayGeminiGenerateContent(ctx, nil, "", []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`), false, "192.0.2.1")
				return err
			},
			code: "model_required",
		},
		{
			name: "gemini generate invalid json",
			call: func() error {
				_, _, err := svc.RelayGeminiGenerateContent(ctx, nil, "gemini-test", []byte(`{"contents":`), false, "192.0.2.1")
				return err
			},
			code: "invalid_json",
		},
		{
			name: "gemini embed missing model",
			call: func() error {
				_, _, err := svc.RelayGeminiEmbedContent(ctx, nil, "", []byte(`{"content":{"parts":[{"text":"hello"}]}}`), "192.0.2.1")
				return err
			},
			code: "model_required",
		},
		{
			name: "gemini batch embed missing model",
			call: func() error {
				_, _, err := svc.RelayGeminiBatchEmbedContents(ctx, nil, "", []byte(`{"requests":[{"content":{"parts":[{"text":"hello"}]}}]}`), "192.0.2.1")
				return err
			},
			code: "model_required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			httpErr, ok := err.(*HTTPError)
			if !ok {
				t.Fatalf("expected HTTPError, got %T %v", err, err)
			}
			if httpErr.Code != tt.code {
				t.Fatalf("expected code %q, got %q (%v)", tt.code, httpErr.Code, httpErr)
			}
		})
	}
}

func TestGeminiEmbeddingOutputDimensionalityValidation(t *testing.T) {
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "embed content rejects non-positive dimensions",
			call: func() error {
				_, err := geminiEmbedContentToOpenAI("text-embedding-test", []byte(`{"content":{"parts":[{"text":"hello"}]},"outputDimensionality":0}`))
				return err
			},
			want: "outputDimensionality must be positive",
		},
		{
			name: "batch embed rejects non-positive dimensions",
			call: func() error {
				_, _, err := geminiBatchEmbedContentsToOpenAI("text-embedding-test", []byte(`{"requests":[{"content":{"parts":[{"text":"hello"}]},"outputDimensionality":0}]}`))
				return err
			},
			want: "outputDimensionality must be positive",
		},
		{
			name: "batch embed rejects mismatched dimensions",
			call: func() error {
				_, _, err := geminiBatchEmbedContentsToOpenAI("text-embedding-test", []byte(`{"requests":[{"content":{"parts":[{"text":"hello"}]},"outputDimensionality":128},{"content":{"parts":[{"text":"world"}]},"outputDimensionality":256}]}`))
				return err
			},
			want: "outputDimensionality must match for batch requests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestGeminiCountTokensUsesPromptTextInsteadOfJSONEnvelope(t *testing.T) {
	svc := NewRelayService(nil, nil, nil, nil)
	resp, err := svc.GeminiCountTokens([]byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"hello world"},{"text":"again"}]},
			{"role":"model","parts":[{"text":"ok"}]}
		],
		"systemInstruction": {"parts":[{"text":"be concise"}]}
	}`))
	if err != nil {
		t.Fatalf("GeminiCountTokens returned error: %v", err)
	}

	var out struct {
		TotalTokens int `json:"totalTokens"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("GeminiCountTokens response should be JSON: %v", err)
	}
	if out.TotalTokens != 6 {
		t.Fatalf("GeminiCountTokens should count prompt text only, got %d", out.TotalTokens)
	}
}

func TestGeminiCountTokensUsesGenerateContentRequestWhenPresent(t *testing.T) {
	svc := NewRelayService(nil, nil, nil, nil)
	resp, err := svc.GeminiCountTokens([]byte(`{
		"contents": [{"parts":[{"text":"ignored top level"}]}],
		"generateContentRequest": {
			"contents": [{"parts":[{"text":"wrapped prompt"}]}],
			"systemInstruction": {"parts":[{"text":"stay brief"}]}
		}
	}`))
	if err != nil {
		t.Fatalf("GeminiCountTokens returned error: %v", err)
	}

	var out struct {
		TotalTokens int `json:"totalTokens"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("GeminiCountTokens response should be JSON: %v", err)
	}
	if out.TotalTokens != 4 {
		t.Fatalf("GeminiCountTokens should prefer generateContentRequest over top-level contents, got %d", out.TotalTokens)
	}
}

func TestGeminiCountTokensRejectsInvalidJSON(t *testing.T) {
	svc := NewRelayService(nil, nil, nil, nil)
	_, err := svc.GeminiCountTokens([]byte(`{"contents":`))
	if err == nil {
		t.Fatal("GeminiCountTokens should reject invalid JSON")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T %v", err, err)
	}
	if httpErr.Status != 400 || httpErr.Code != "invalid_json" {
		t.Fatalf("expected invalid_json HTTP 400, got status=%d code=%q", httpErr.Status, httpErr.Code)
	}
}
