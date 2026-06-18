package service

import (
	"context"
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
