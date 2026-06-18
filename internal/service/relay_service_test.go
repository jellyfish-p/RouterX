package service

import (
	"context"
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
