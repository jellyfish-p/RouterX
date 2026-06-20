package relay

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"routerx/internal/common"
)

type requestIDContextKey struct{}
type traceparentContextKey struct{}
type tracestateContextKey struct{}
type upstreamOptionsContextKey struct{}
type routerXHopContextKey struct{}
type routerXChainContextKey struct{}

const RouterXHopHeaderName = "X-RouterX-Hop"
const RouterXChainHeaderName = "X-RouterX-Chain"

// UpstreamOptions carries caller-supplied, policy-safe additions for the next
// upstream HTTP request. Sensitive authentication material is filtered before
// this value is stored, and adapters apply these values without replacing their
// own required headers or query parameters.
type UpstreamOptions struct {
	Headers map[string]string
	Query   map[string]string
}

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

// ContextWithTraceparent stores a validated W3C traceparent for outbound calls.
// Invalid values are dropped at the boundary instead of being forwarded.
func ContextWithTraceparent(ctx context.Context, traceparent string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, _, ok := common.NormalizeTraceparent(traceparent)
	if !ok {
		normalized = ""
	}
	return context.WithValue(ctx, traceparentContextKey{}, normalized)
}

// TraceparentFromContext returns the outbound W3C trace context, if present.
func TraceparentFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(traceparentContextKey{}).(string)
	return strings.TrimSpace(value)
}

// ContextWithTracestate stores a validated W3C tracestate for outbound calls.
// It is meaningful only together with a valid traceparent.
func ContextWithTracestate(ctx context.Context, tracestate string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, ok := common.NormalizeTracestate(tracestate)
	if !ok {
		normalized = ""
	}
	return context.WithValue(ctx, tracestateContextKey{}, normalized)
}

// TracestateFromContext returns the outbound W3C tracestate, if present.
func TracestateFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(tracestateContextKey{}).(string)
	return strings.TrimSpace(value)
}

// ContextWithUpstreamOptions stores sanitized outbound request additions.
func ContextWithUpstreamOptions(ctx context.Context, opts UpstreamOptions) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, upstreamOptionsContextKey{}, cloneUpstreamOptions(opts))
}

// UpstreamOptionsFromContext returns sanitized outbound request additions.
func UpstreamOptionsFromContext(ctx context.Context) UpstreamOptions {
	if ctx == nil {
		return UpstreamOptions{}
	}
	opts, _ := ctx.Value(upstreamOptionsContextKey{}).(UpstreamOptions)
	return cloneUpstreamOptions(opts)
}

// ContextWithRouterXHop stores the hop count to send to a RouterX-compatible
// upstream. The service computes this only after selecting a RouterX channel.
func ContextWithRouterXHop(ctx context.Context, hop int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, routerXHopContextKey{}, hop)
}

// RouterXHopFromContext returns the outbound RouterX hop value, if one exists.
func RouterXHopFromContext(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	hop, ok := ctx.Value(routerXHopContextKey{}).(int)
	return hop, ok
}

// ContextWithRouterXChain stores the outbound chain summary for RouterX hops.
func ContextWithRouterXChain(ctx context.Context, chain string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, routerXChainContextKey{}, strings.TrimSpace(chain))
}

// RouterXChainFromContext returns the outbound RouterX chain summary.
func RouterXChainFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	chain, _ := ctx.Value(routerXChainContextKey{}).(string)
	return strings.TrimSpace(chain)
}

// SetRequestIDHeader copies RouterX's request id into outbound upstream calls.
// It deliberately uses the configured public header name, so deployments that
// rename observability.request_id_header keep the same lookup boundary.
func SetRequestIDHeader(req *http.Request) {
	if req == nil {
		return
	}
	if requestID := RequestIDFromContext(req.Context()); requestID != "" {
		req.Header.Set(common.RequestIDHeaderName(), requestID)
	}
	SetTraceparentHeader(req)
}

// SetTraceparentHeader forwards an existing W3C trace context without
// inventing one. request_id remains the RouterX-local lookup handle.
func SetTraceparentHeader(req *http.Request) {
	if req == nil {
		return
	}
	if traceparent := TraceparentFromContext(req.Context()); traceparent != "" {
		req.Header.Set(common.TraceparentHeaderName, traceparent)
		if tracestate := TracestateFromContext(req.Context()); tracestate != "" {
			req.Header.Set(common.TracestateHeaderName, tracestate)
		}
	}
}

// SetRouterXHopHeader forwards the loop-prevention hop count to RouterX
// compatible upstreams. Non-RouterX adapters simply never receive this context.
func SetRouterXHopHeader(req *http.Request) {
	if req == nil {
		return
	}
	if hop, ok := RouterXHopFromContext(req.Context()); ok && hop > 0 {
		req.Header.Set(RouterXHopHeaderName, strconv.Itoa(hop))
	}
}

// SetRouterXChainHeader forwards a compact chain summary to RouterX-compatible
// upstreams. The service decides when this is safe to emit.
func SetRouterXChainHeader(req *http.Request) {
	if req == nil {
		return
	}
	if chain := RouterXChainFromContext(req.Context()); chain != "" {
		req.Header.Set(RouterXChainHeaderName, chain)
	}
}

// ApplyUpstreamOptions supplements outbound requests with caller-provided
// headers and query parameters. Existing adapter values win, so channel
// credentials, provider API keys and required content negotiation stay intact.
func ApplyUpstreamOptions(req *http.Request) {
	if req == nil {
		return
	}
	opts := UpstreamOptionsFromContext(req.Context())
	if len(opts.Query) > 0 && req.URL != nil {
		query := req.URL.Query()
		for key, value := range opts.Query {
			if _, exists := query[key]; !exists {
				query.Set(key, value)
			}
		}
		req.URL.RawQuery = query.Encode()
	}
	for key, value := range opts.Headers {
		if req.Header.Get(key) == "" {
			req.Header.Set(key, value)
		}
	}
}

func cloneUpstreamOptions(opts UpstreamOptions) UpstreamOptions {
	cloned := UpstreamOptions{}
	if len(opts.Headers) > 0 {
		cloned.Headers = make(map[string]string, len(opts.Headers))
		for key, value := range opts.Headers {
			cloned.Headers[key] = value
		}
	}
	if len(opts.Query) > 0 {
		cloned.Query = make(map[string]string, len(opts.Query))
		for key, value := range opts.Query {
			cloned.Query[key] = value
		}
	}
	return cloned
}
