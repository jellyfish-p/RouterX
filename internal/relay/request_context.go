package relay

import (
	"context"
	"net/http"
	"strings"

	"routerx/internal/common"
)

type requestIDContextKey struct{}

// ContextWithRequestID stores the RouterX request id for outbound adapter calls.
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDContextKey{}, strings.TrimSpace(requestID))
}

// RequestIDFromContext returns the request id previously attached to the context.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return strings.TrimSpace(value)
}

// SetRequestIDHeader copies RouterX's request id into outbound upstream calls.
// It deliberately uses the configured public header name, so deployments that
// rename observability.request_id_header keep the same trace boundary.
func SetRequestIDHeader(req *http.Request) {
	if req == nil {
		return
	}
	if requestID := RequestIDFromContext(req.Context()); requestID != "" {
		req.Header.Set(common.RequestIDHeaderName(), requestID)
	}
}
