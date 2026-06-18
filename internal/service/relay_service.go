package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/relay"
)

type RelayService struct {
	channelService *ChannelService
	tokenService   *TokenService
	logService     *LogService
	settingService *SettingService
}

type RelayStreamResult struct {
	ContentType string
	forward     func(write func([]byte) error, flush func()) (*relay.Usage, error)
}

type RelayRawResult struct {
	Body        []byte
	ContentType string
	Usage       *relay.Usage
}

type relayUserAgentContextKey struct{}
type relayRequestIDContextKey struct{}
type relayRouterXOptionsContextKey struct{}
type relayRouterXHopContextKey struct{}
type relayRouterXChainContextKey struct{}
type relayRequestSnapshotContextKey struct{}
type relayPolicySnapshotContextKey struct{}
type relayRouteSnapshotContextKey struct{}
type relayBillingSnapshotContextKey struct{}

type contentTypeRelayAdapter interface {
	DoRequestWithContentType(ctx context.Context, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error)
}

const (
	defaultRouterXMaxHops = 3

	usageMissingStrategyMinimum = "minimum"
	usageMissingStrategyReject  = "reject"

	defaultRelayMaxRequestBodyBytes  int64 = 20 << 20
	defaultRelayMaxResponseBodyBytes int64 = 20 << 20
)

var errRelayResponseBodyTooLarge = errors.New("relay upstream response body too large")

func (r *RelayStreamResult) Forward(write func([]byte) error, flush func()) (*relay.Usage, error) {
	if r == nil || r.forward == nil {
		return nil, errors.New("stream result is not initialized")
	}
	return r.forward(write, flush)
}

func NewRelayService(ch *ChannelService, tokenSvc *TokenService, logSvc *LogService, settingSvc *SettingService) *RelayService {
	return &RelayService{channelService: ch, tokenService: tokenSvc, logService: logSvc, settingService: settingSvc}
}

func ContextWithRelayUserAgent(ctx context.Context, userAgent string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayUserAgentContextKey{}, strings.TrimSpace(userAgent))
}

func ContextWithRelayRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = relay.ContextWithRequestID(ctx, requestID)
	return context.WithValue(ctx, relayRequestIDContextKey{}, strings.TrimSpace(requestID))
}

// ContextWithRelayRouterXOptions stores the optional X-RouterX-Options header.
// Request body or multipart form routerx fields still take precedence.
func ContextWithRelayRouterXOptions(ctx context.Context, options string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayRouterXOptionsContextKey{}, strings.TrimSpace(options))
}

// ContextWithRelayRouterXHop stores the inbound hop count from X-RouterX-Hop.
// The value is validated only when the selected upstream is RouterX-compatible.
func ContextWithRelayRouterXHop(ctx context.Context, hop string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayRouterXHopContextKey{}, strings.TrimSpace(hop))
}

// ContextWithRelayRouterXChain stores the inbound chain summary from
// X-RouterX-Chain. It is forwarded only to RouterX-compatible upstreams.
func ContextWithRelayRouterXChain(ctx context.Context, chain string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayRouterXChainContextKey{}, strings.TrimSpace(chain))
}

func ContextWithRelayRequestSnapshot(ctx context.Context, snapshot string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayRequestSnapshotContextKey{}, strings.TrimSpace(snapshot))
}

func ContextWithRelayPolicySnapshot(ctx context.Context, snapshot string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayPolicySnapshotContextKey{}, strings.TrimSpace(snapshot))
}

func ContextWithRelayRouteSnapshot(ctx context.Context, snapshot string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayRouteSnapshotContextKey{}, strings.TrimSpace(snapshot))
}

func ContextWithRelayBillingSnapshot(ctx context.Context, snapshot string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayBillingSnapshotContextKey{}, strings.TrimSpace(snapshot))
}

func relayUserAgentFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayUserAgentContextKey{}).(string)
	return strings.TrimSpace(value)
}

func relayRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayRequestIDContextKey{}).(string)
	return strings.TrimSpace(value)
}

func relayRouterXOptionsFromContext(ctx context.Context) json.RawMessage {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(relayRouterXOptionsContextKey{}).(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return json.RawMessage(value)
}

func relayRouterXHopFromContext(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, nil
	}
	value, _ := ctx.Value(relayRouterXHopContextKey{}).(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	hop, err := strconv.Atoi(value)
	if err != nil || hop < 0 {
		return 0, errors.New("invalid routerx hop")
	}
	return hop, nil
}

func relayRequestSnapshotFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayRequestSnapshotContextKey{}).(string)
	return strings.TrimSpace(value)
}

func relayPolicySnapshotFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayPolicySnapshotContextKey{}).(string)
	return strings.TrimSpace(value)
}

func relayRouteSnapshotFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayRouteSnapshotContextKey{}).(string)
	return strings.TrimSpace(value)
}

func relayBillingSnapshotFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayBillingSnapshotContextKey{}).(string)
	return strings.TrimSpace(value)
}

type HTTPError struct {
	Status  int
	Message string
	Type    string
	Code    string
}

type channelGroupAccessRule struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type channelGroupAccessPolicy struct {
	allowAll bool
	allowed  map[string]struct{}
	denied   map[string]struct{}
}

var (
	errInvalidJSONBody       = errors.New("invalid json body")
	errInvalidMultipartBody  = errors.New("invalid multipart body")
	errModelRequired         = errors.New("model is required")
	errUnsupportedMultipart  = errors.New("multipart relay is not supported for selected upstream channel")
	errInvalidRouterXOptions = errors.New("invalid routerx options")
	errInvalidRouterXRoute   = errors.New("invalid routerx route")
)

func (e *HTTPError) Error() string {
	return e.Message
}

// RelayChatCompletion 核心转发：Chat Completions。
// 流程 (详见 DESIGN.md 5.3):
// 1. 从 context 获取已鉴权的 Token + User
// 2. 调用 ChannelService.SelectChannel 选择通道
// 3. 根据 channel.Type 找到对应 relay.Adapter
// 4. adapter.ConvertRequest 转换请求体
// 5. adapter.DoRequest 发送请求
// 6. adapter.ConvertResponse 解析响应 + 提取 Usage
// 7. 计费: 计算消耗额度 → TokenService.DeductQuota
// 8. 写 Log
// 9. 返回 OpenAI 格式响应
func (s *RelayService) RelayChatCompletion(ctx context.Context, token *model.Token, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	return s.Relay(ctx, token, relay.APIChatCompletions, body, clientIP)
}

// RelayCompletions 转发 Text Completions (Legacy)。
func (s *RelayService) RelayCompletions(ctx context.Context, token *model.Token, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	return s.Relay(ctx, token, relay.APICompletions, body, clientIP)
}

func (s *RelayService) RelayAnthropicMessages(ctx context.Context, token *model.Token, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	canonical, err := anthropicMessagesToOpenAI(body)
	if err != nil {
		return nil, nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
	}
	resp, usage, err := s.Relay(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIChatToAnthropic(resp)
	if err != nil {
		return nil, usage, &HTTPError{Status: 502, Message: "response conversion failed", Type: "upstream_error", Code: "response_conversion_failed"}
	}
	return converted, usage, nil
}

func (s *RelayService) RelayAnthropicMessagesStream(ctx context.Context, token *model.Token, body []byte, clientIP string) (*RelayStreamResult, error) {
	canonical, err := anthropicMessagesToOpenAI(body)
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
	}
	result, err := s.RelayStream(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, err
	}
	state := &anthropicStreamState{}
	return &RelayStreamResult{
		ContentType: "text/event-stream",
		forward: func(write func([]byte) error, flush func()) (*relay.Usage, error) {
			return result.Forward(func(chunk []byte) error {
				converted, ok, err := openAIStreamChunkToAnthropic(chunk, state)
				if err != nil || !ok {
					return err
				}
				return write(converted)
			}, flush)
		},
	}, nil
}

func (s *RelayService) RelayGeminiGenerateContent(ctx context.Context, token *model.Token, modelName string, body []byte, stream bool, clientIP string) ([]byte, *relay.Usage, error) {
	canonical, err := geminiGenerateToOpenAI(modelName, body, stream)
	if err != nil {
		return nil, nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
	}
	resp, usage, err := s.Relay(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIChatToGemini(resp)
	if err != nil {
		return nil, usage, &HTTPError{Status: 502, Message: "response conversion failed", Type: "upstream_error", Code: "response_conversion_failed"}
	}
	return converted, usage, nil
}

func (s *RelayService) RelayGeminiEmbedContent(ctx context.Context, token *model.Token, modelName string, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	canonical, err := geminiEmbedContentToOpenAI(modelName, body)
	if err != nil {
		return nil, nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
	}
	resp, usage, err := s.Relay(ctx, token, relay.APIEmbeddings, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIEmbeddingsToGemini(resp)
	if err != nil {
		return nil, usage, &HTTPError{Status: 502, Message: "response conversion failed", Type: "upstream_error", Code: "response_conversion_failed"}
	}
	return converted, usage, nil
}

func (s *RelayService) RelayGeminiBatchEmbedContents(ctx context.Context, token *model.Token, modelName string, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	canonical, err := geminiBatchEmbedContentsToOpenAI(modelName, body)
	if err != nil {
		return nil, nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
	}
	resp, usage, err := s.Relay(ctx, token, relay.APIEmbeddings, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIEmbeddingsToGeminiBatch(resp)
	if err != nil {
		return nil, usage, &HTTPError{Status: 502, Message: "response conversion failed", Type: "upstream_error", Code: "response_conversion_failed"}
	}
	return converted, usage, nil
}

func (s *RelayService) RelayGeminiGenerateContentStream(ctx context.Context, token *model.Token, modelName string, body []byte, clientIP string) (*RelayStreamResult, error) {
	canonical, err := geminiGenerateToOpenAI(modelName, body, true)
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
	}
	result, err := s.RelayStream(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, err
	}
	return &RelayStreamResult{
		ContentType: "text/event-stream",
		forward: func(write func([]byte) error, flush func()) (*relay.Usage, error) {
			return result.Forward(func(chunk []byte) error {
				converted, ok, err := openAIStreamChunkToGemini(chunk)
				if err != nil || !ok {
					return err
				}
				return write(converted)
			}, flush)
		},
	}, nil
}

func (s *RelayService) AnthropicCountTokens(body []byte) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"input_tokens": approximateTokenCount(body),
	})
}

func (s *RelayService) GeminiCountTokens(body []byte) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"totalTokens": approximateTokenCount(body),
	})
}

// GetAdapter 根据通道类型返回对应的适配器实例。
func (s *RelayService) GetAdapter(channelType int) (relay.Adapter, error) {
	adapter, ok := relay.GetAdapter(channelType)
	if !ok {
		return nil, errors.New("unsupported channel type")
	}
	return adapter, nil
}

// Relay 通用转发入口。
func (s *RelayService) Relay(ctx context.Context, token *model.Token, apiType relay.APIType, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	result, usage, err := s.relayNonStream(ctx, token, apiType, body, "", clientIP, false)
	if err != nil {
		return nil, usage, err
	}
	return result.Body, result.Usage, nil
}

// RelayMultipart 处理 OpenAI-compatible multipart/form-data 请求，保留文件字段并剥离 RouterX 私有表单字段。
func (s *RelayService) RelayMultipart(ctx context.Context, token *model.Token, apiType relay.APIType, body []byte, contentType string, clientIP string) ([]byte, *relay.Usage, error) {
	result, usage, err := s.relayNonStream(ctx, token, apiType, body, contentType, clientIP, false)
	if err != nil {
		return nil, usage, err
	}
	return result.Body, result.Usage, nil
}

// RelayRaw 用于返回非 JSON 响应体的接口，例如 /v1/audio/speech 的音频字节流。
func (s *RelayService) RelayRaw(ctx context.Context, token *model.Token, apiType relay.APIType, body []byte, clientIP string) (*RelayRawResult, error) {
	result, _, err := s.relayNonStream(ctx, token, apiType, body, "", clientIP, true)
	return result, err
}

func (s *RelayService) relayNonStream(ctx context.Context, token *model.Token, apiType relay.APIType, body []byte, contentType string, clientIP string, rawResponse bool) (*RelayRawResult, *relay.Usage, error) {
	if token == nil {
		return nil, nil, &HTTPError{Status: 401, Message: "invalid api key", Type: "authentication_error", Code: "invalid_api_key"}
	}
	reqInfo, err := parseRelayRequestWithContentType(apiType, body, contentType, relayRouterXOptionsFromContext(ctx))
	if err != nil {
		return nil, nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: relayRequestErrorCode(err)}
	}
	if reqInfo.Stream {
		return nil, nil, &HTTPError{Status: 400, Message: "stream is not supported in P0 relay", Type: "invalid_request_error", Code: "unsupported_stream"}
	}
	ctx = ContextWithRelayRequestSnapshot(ctx, buildRelayRequestSnapshot(ctx, apiType, reqInfo))
	if err := s.enforceTokenScope(ctx, token, apiType, reqInfo.Model, clientIP); err != nil {
		return nil, nil, err
	}
	if err := s.enforceTokenRouteChannelGroupScope(ctx, token, reqInfo.Route, reqInfo.Model, clientIP); err != nil {
		return nil, nil, err
	}
	if !s.tokenService.HasAvailableQuota(token) {
		_ = s.recordLog(ctx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, "insufficient quota", clientIP)
		return nil, nil, &HTTPError{Status: 429, Message: "insufficient quota", Type: "insufficient_quota", Code: "insufficient_quota"}
	}
	if err := s.enforceModelRateLimit(ctx, token, reqInfo.Model, clientIP); err != nil {
		return nil, nil, err
	}
	ctx = ContextWithRelayPolicySnapshot(ctx, buildRelayPolicySnapshot(ctx, token, reqInfo))

	filteredReasons := map[string]int{}
	candidates, selectionFacts, err := s.channelService.SelectChannelCandidatesWithRouteDetailedFacts(reqInfo.Model, reqInfo.Route)
	mergeRouteFilterReasons(filteredReasons, selectionFacts.FilteredReasons)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, nil, nil, nil, filteredReasons))
		logCtx = ContextWithRelayPolicySnapshot(logCtx, buildRelayNoAvailableChannelPolicySnapshot(ctx, token, selectionFacts.BreakerSnapshot))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, "no available channel", clientIP)
		return nil, nil, &HTTPError{Status: 502, Message: "no available upstream channel", Type: "upstream_error", Code: "no_available_channel"}
	}
	candidates, removed, err := s.filterUserChannelModelAccess(token, candidates, reqInfo.Model)
	addRouteFilterReason(filteredReasons, routeFilterReasonAccessDenied, removed)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, nil, nil, filteredReasons))
		logCtx = ContextWithRelayPolicySnapshot(logCtx, buildRelayChannelModelAccessDenyPolicySnapshot(ctx, token))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, nil, err
	}
	candidates, removed, err = s.filterUserChannelGroupAccess(ctx, token, candidates, reqInfo.Model, clientIP)
	addRouteFilterReason(filteredReasons, routeFilterReasonAccessDenied, removed)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, nil, nil, filteredReasons))
		logCtx = ContextWithRelayPolicySnapshot(logCtx, buildRelayUserGroupAccessDenyPolicySnapshot(ctx, token))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, nil, err
	}
	candidates, removed, err = s.filterTokenChannelGroupScope(ctx, token, candidates, reqInfo.Model, clientIP)
	addRouteFilterReason(filteredReasons, routeFilterReasonAccessDenied, removed)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, nil, nil, filteredReasons))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, nil, err
	}
	selected := pickRelayChannelCandidate(candidates)
	attemptCandidates := orderRelayAttemptCandidates(candidates, selected)
	maxAttempts := 1 + s.relayRetryCount()
	if maxAttempts > len(attemptCandidates) {
		maxAttempts = len(attemptCandidates)
	}
	retryAttemptCapacity := 0
	if maxAttempts > 1 {
		retryAttemptCapacity = maxAttempts - 1
	}
	retryAttempts := make([]map[string]interface{}, 0, retryAttemptCapacity)
	var lastUsage *relay.Usage
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		channel := attemptCandidates[i]
		attemptCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, &channel, retryAttempts, filteredReasons))
		result, usage, retryable, err := s.relayNonStreamAttempt(attemptCtx, token, apiType, reqInfo, body, contentType, clientIP, &channel, rawResponse)
		if err == nil {
			return result, usage, nil
		}
		lastUsage = usage
		lastErr = err
		if !retryable {
			return nil, usage, err
		}
		retryAttempts = append(retryAttempts, buildRelayRetryAttemptSnapshot(i+1, &channel, err))
	}
	return nil, lastUsage, lastErr
}

func (s *RelayService) relayNonStreamAttempt(ctx context.Context, token *model.Token, apiType relay.APIType, reqInfo relayRequestInfo, body []byte, contentType string, clientIP string, channel *model.Channel, rawResponse bool) (*RelayRawResult, *relay.Usage, bool, error) {
	relayStart := time.Now()
	defer func() {
		s.recordRelayDuration(apiType, channel, time.Since(relayStart))
	}()

	if err := s.enforceChannelRateLimit(ctx, token, channel, reqInfo.Model, clientIP); err != nil {
		return nil, nil, false, err
	}
	adapter, err := s.GetAdapter(channel.Type)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, nil, false, &HTTPError{Status: 502, Message: "unsupported upstream channel", Type: "upstream_error", Code: "unsupported_channel"}
	}
	target, err := s.channelService.ResolveUpstream(channel)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream secret decrypt failed", clientIP)
		return nil, nil, false, &HTTPError{Status: 502, Message: "upstream channel secret is not available", Type: "upstream_error", Code: "upstream_secret_error"}
	}
	upstreamModel := s.channelService.ApplyModelRewrite(channel, reqInfo.Model)
	outBody := body
	outContentType := ""
	if isMultipartRelayContentType(contentType) {
		outBody, outContentType, err = rewriteMultipartRelayBody(body, contentType, upstreamModel)
		if err != nil {
			return nil, nil, false, &HTTPError{Status: 400, Message: "invalid multipart body", Type: "invalid_request_error", Code: "invalid_multipart"}
		}
	} else {
		outInputBody, err := mergeRelayUpstreamBody(body, relayUpstreamBodyForChannel(reqInfo.Upstream, channel.Type))
		if err != nil {
			return nil, nil, false, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
		}
		outInputBody, err = replaceRequestModel(outInputBody, upstreamModel)
		if err != nil {
			return nil, nil, false, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
		}
		outBody, err = adapter.ConvertRequest(apiType, outInputBody)
		if err != nil {
			return nil, nil, false, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
		}
	}
	routerXHop, forwardRouterXHop, err := s.nextRouterXHop(ctx, channel)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, nil, false, routerXHopHTTPError(err)
	}
	endpoint := adapter.GetAPIEndpoint(apiType, upstreamModel)
	timeout := s.relayTimeout()
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	reqCtx = relay.ContextWithUpstreamOptions(reqCtx, reqInfo.Upstream.Transport)
	if forwardRouterXHop {
		reqCtx = relay.ContextWithRouterXHop(reqCtx, routerXHop)
	}
	if routerXChain, forwardRouterXChain := routerXChainForUpstream(ctx, channel); forwardRouterXChain {
		reqCtx = relay.ContextWithRouterXChain(reqCtx, routerXChain)
	}
	defer cancel()
	start := time.Now()
	resp, err := doRelayAdapterRequest(reqCtx, adapter, target.BaseURL, endpoint, target.APIKey, outBody, outContentType)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		s.recordUpstreamDuration(channel, "failed", time.Since(start))
		_ = s.markChannelFailure(channel, latencyMs)
		if errors.Is(err, errUnsupportedMultipart) {
			_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "multipart relay is not supported for selected upstream channel", clientIP)
			return nil, nil, false, &HTTPError{Status: 502, Message: "multipart relay is not supported for selected upstream channel", Type: "upstream_error", Code: "unsupported_multipart_channel"}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream timeout", clientIP)
			return nil, nil, true, &HTTPError{Status: 504, Message: "upstream request timed out", Type: "upstream_error", Code: "upstream_timeout"}
		}
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream request failed", clientIP)
		return nil, nil, true, &HTTPError{Status: 502, Message: "upstream request failed", Type: "upstream_error", Code: "upstream_request_failed"}
	}
	defer resp.Body.Close()

	respBody, err := s.readUpstreamResponseBody(resp.Body)
	if err != nil {
		s.recordUpstreamDuration(channel, "failed", time.Since(start))
		_ = s.markChannelFailure(channel, latencyMs)
		if errors.Is(err, errRelayResponseBodyTooLarge) {
			_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream response body too large", clientIP)
			return nil, nil, true, &HTTPError{Status: 502, Message: "upstream response body too large", Type: "upstream_error", Code: "upstream_response_too_large"}
		}
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream response read failed", clientIP)
		return nil, nil, true, &HTTPError{Status: 502, Message: "upstream response read failed", Type: "upstream_error", Code: "upstream_response_failed"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.recordUpstreamDuration(channel, "failed", time.Since(start))
		_ = s.markChannelFailure(channel, latencyMs)
		message := fmt.Sprintf("upstream returned status %d", resp.StatusCode)
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, message, clientIP)
		return nil, nil, s.retryableUpstreamStatus(resp.StatusCode), &HTTPError{
			Status:  clientStatusFromUpstream(resp.StatusCode),
			Message: message,
			Type:    upstreamErrorType(resp.StatusCode),
			Code:    fmt.Sprintf("upstream_%d", resp.StatusCode),
		}
	}
	s.recordUpstreamDuration(channel, "success", time.Since(start))

	if rawResponse {
		if httpErr := s.rejectMissingUsage(ctx, token, channel, reqInfo.Model, nil, clientIP); httpErr != nil {
			_ = s.markChannelSuccess(channel, latencyMs)
			return nil, nil, false, httpErr
		}
		billing := s.calculateRelayBilling(token, channel, reqInfo.Model, nil)
		deduction, err := s.tokenService.DeductQuotaWithSnapshot(token.ID, billing.QuotaUsed)
		if err != nil {
			logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingFailureSnapshot(nil, billing, deduction, err))
			_ = s.recordLog(logCtx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "insufficient quota", clientIP)
			return nil, nil, false, &HTTPError{Status: 429, Message: "insufficient quota", Type: "insufficient_quota", Code: "insufficient_quota"}
		}
		contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		_ = s.markChannelSuccess(channel, latencyMs)
		logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingSnapshot(nil, billing, deduction))
		_ = s.recordLog(logCtx, token, channel, reqInfo.Model, nil, common.LogStatusSuccess, billing.QuotaUsed, "", clientIP)
		return &RelayRawResult{Body: respBody, ContentType: contentType}, nil, false, nil
	}

	converted, usage, err := adapter.ConvertResponse(apiType, respBody)
	if err != nil {
		_ = s.markChannelFailure(channel, latencyMs)
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream response conversion failed", clientIP)
		return nil, nil, false, &HTTPError{Status: 502, Message: "upstream response conversion failed", Type: "upstream_error", Code: "upstream_conversion_failed"}
	}
	if httpErr := s.rejectMissingUsage(ctx, token, channel, reqInfo.Model, usage, clientIP); httpErr != nil {
		_ = s.markChannelSuccess(channel, latencyMs)
		return nil, usage, false, httpErr
	}
	billing := s.calculateRelayBilling(token, channel, reqInfo.Model, usage)
	deduction, err := s.tokenService.DeductQuotaWithSnapshot(token.ID, billing.QuotaUsed)
	if err != nil {
		logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingFailureSnapshot(usage, billing, deduction, err))
		_ = s.recordLog(logCtx, token, channel, reqInfo.Model, usage, common.LogStatusFailed, 0, "insufficient quota", clientIP)
		return nil, usage, false, &HTTPError{Status: 429, Message: "insufficient quota", Type: "insufficient_quota", Code: "insufficient_quota"}
	}
	_ = s.markChannelSuccess(channel, latencyMs)
	logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingSnapshot(usage, billing, deduction))
	_ = s.recordLog(logCtx, token, channel, reqInfo.Model, usage, common.LogStatusSuccess, billing.QuotaUsed, "", clientIP)
	return &RelayRawResult{Body: converted, ContentType: "application/json; charset=utf-8", Usage: usage}, usage, false, nil
}

func (s *RelayService) RelayStream(ctx context.Context, token *model.Token, apiType relay.APIType, body []byte, clientIP string) (*RelayStreamResult, error) {
	if token == nil {
		return nil, &HTTPError{Status: 401, Message: "invalid api key", Type: "authentication_error", Code: "invalid_api_key"}
	}
	reqInfo, err := parseRelayRequest(apiType, body, relayRouterXOptionsFromContext(ctx))
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: relayRequestErrorCode(err)}
	}
	if !reqInfo.Stream {
		return nil, &HTTPError{Status: 400, Message: "stream is required", Type: "invalid_request_error", Code: "stream_required"}
	}
	ctx = ContextWithRelayRequestSnapshot(ctx, buildRelayRequestSnapshot(ctx, apiType, reqInfo))
	if err := s.enforceTokenScope(ctx, token, apiType, reqInfo.Model, clientIP); err != nil {
		return nil, err
	}
	if err := s.enforceTokenRouteChannelGroupScope(ctx, token, reqInfo.Route, reqInfo.Model, clientIP); err != nil {
		return nil, err
	}
	if !s.tokenService.HasAvailableQuota(token) {
		_ = s.recordLog(ctx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, "insufficient quota", clientIP)
		return nil, &HTTPError{Status: 429, Message: "insufficient quota", Type: "insufficient_quota", Code: "insufficient_quota"}
	}
	if err := s.enforceModelRateLimit(ctx, token, reqInfo.Model, clientIP); err != nil {
		return nil, err
	}
	ctx = ContextWithRelayPolicySnapshot(ctx, buildRelayPolicySnapshot(ctx, token, reqInfo))

	filteredReasons := map[string]int{}
	candidates, selectionFacts, err := s.channelService.SelectChannelCandidatesWithRouteDetailedFacts(reqInfo.Model, reqInfo.Route)
	mergeRouteFilterReasons(filteredReasons, selectionFacts.FilteredReasons)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, nil, nil, nil, filteredReasons))
		logCtx = ContextWithRelayPolicySnapshot(logCtx, buildRelayNoAvailableChannelPolicySnapshot(ctx, token, selectionFacts.BreakerSnapshot))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, "no available channel", clientIP)
		return nil, &HTTPError{Status: 502, Message: "no available upstream channel", Type: "upstream_error", Code: "no_available_channel"}
	}
	candidates, removed, err := s.filterUserChannelModelAccess(token, candidates, reqInfo.Model)
	addRouteFilterReason(filteredReasons, routeFilterReasonAccessDenied, removed)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, nil, nil, filteredReasons))
		logCtx = ContextWithRelayPolicySnapshot(logCtx, buildRelayChannelModelAccessDenyPolicySnapshot(ctx, token))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, err
	}
	candidates, removed, err = s.filterUserChannelGroupAccess(ctx, token, candidates, reqInfo.Model, clientIP)
	addRouteFilterReason(filteredReasons, routeFilterReasonAccessDenied, removed)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, nil, nil, filteredReasons))
		logCtx = ContextWithRelayPolicySnapshot(logCtx, buildRelayUserGroupAccessDenyPolicySnapshot(ctx, token))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, err
	}
	candidates, removed, err = s.filterTokenChannelGroupScope(ctx, token, candidates, reqInfo.Model, clientIP)
	addRouteFilterReason(filteredReasons, routeFilterReasonAccessDenied, removed)
	if err != nil {
		logCtx := ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, nil, nil, filteredReasons))
		_ = s.recordLog(logCtx, token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, err
	}
	channel := pickRelayChannelCandidate(candidates)
	ctx = ContextWithRelayRouteSnapshot(ctx, s.buildRelayRouteSnapshot(reqInfo, candidates, channel, nil, filteredReasons))
	if err := s.enforceChannelRateLimit(ctx, token, channel, reqInfo.Model, clientIP); err != nil {
		return nil, err
	}
	if !supportsOpenAICompatibleStream(channel.Type) {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "streaming is not supported for selected upstream channel", clientIP)
		return nil, &HTTPError{Status: 502, Message: "streaming is not supported for selected upstream channel", Type: "upstream_error", Code: "unsupported_stream_channel"}
	}
	adapter, err := s.GetAdapter(channel.Type)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, &HTTPError{Status: 502, Message: "unsupported upstream channel", Type: "upstream_error", Code: "unsupported_channel"}
	}
	target, err := s.channelService.ResolveUpstream(channel)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream secret decrypt failed", clientIP)
		return nil, &HTTPError{Status: 502, Message: "upstream channel secret is not available", Type: "upstream_error", Code: "upstream_secret_error"}
	}
	upstreamModel := s.channelService.ApplyModelRewrite(channel, reqInfo.Model)
	outInputBody, err := mergeRelayUpstreamBody(body, relayUpstreamBodyForChannel(reqInfo.Upstream, channel.Type))
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
	}
	outInputBody, err = replaceRequestModel(outInputBody, upstreamModel)
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
	}
	outBody, err := adapter.ConvertRequest(apiType, outInputBody)
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
	}
	routerXHop, forwardRouterXHop, err := s.nextRouterXHop(ctx, channel)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, routerXHopHTTPError(err)
	}

	endpoint := adapter.GetAPIEndpoint(apiType, upstreamModel)
	reqCtx, cancel := context.WithTimeout(ctx, s.relayTimeout())
	reqCtx = relay.ContextWithUpstreamOptions(reqCtx, reqInfo.Upstream.Transport)
	if forwardRouterXHop {
		reqCtx = relay.ContextWithRouterXHop(reqCtx, routerXHop)
	}
	if routerXChain, forwardRouterXChain := routerXChainForUpstream(ctx, channel); forwardRouterXChain {
		reqCtx = relay.ContextWithRouterXChain(reqCtx, routerXChain)
	}
	relayStart := time.Now()
	start := time.Now()
	resp, err := adapter.DoRequest(reqCtx, target.BaseURL, endpoint, target.APIKey, outBody)
	if err != nil {
		cancel()
		latencyMs := int(time.Since(start).Milliseconds())
		s.recordRelayDuration(apiType, channel, time.Since(relayStart))
		s.recordUpstreamDuration(channel, "failed", time.Since(start))
		_ = s.markChannelFailure(channel, latencyMs)
		if errors.Is(err, context.DeadlineExceeded) {
			_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream timeout", clientIP)
			return nil, &HTTPError{Status: 504, Message: "upstream request timed out", Type: "upstream_error", Code: "upstream_timeout"}
		}
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream request failed", clientIP)
		return nil, &HTTPError{Status: 502, Message: "upstream request failed", Type: "upstream_error", Code: "upstream_request_failed"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		cancel()
		defer resp.Body.Close()
		latencyMs := int(time.Since(start).Milliseconds())
		s.recordRelayDuration(apiType, channel, time.Since(relayStart))
		s.recordUpstreamDuration(channel, "failed", time.Since(start))
		_ = s.markChannelFailure(channel, latencyMs)
		message := fmt.Sprintf("upstream returned status %d", resp.StatusCode)
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, message, clientIP)
		return nil, &HTTPError{
			Status:  clientStatusFromUpstream(resp.StatusCode),
			Message: message,
			Type:    upstreamErrorType(resp.StatusCode),
			Code:    fmt.Sprintf("upstream_%d", resp.StatusCode),
		}
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "text/event-stream"
	}
	s.recordUpstreamDuration(channel, "success", time.Since(start))
	return &RelayStreamResult{
		ContentType: contentType,
		forward: func(write func([]byte) error, flush func()) (*relay.Usage, error) {
			defer func() {
				s.recordRelayDuration(apiType, channel, time.Since(relayStart))
			}()
			defer cancel()
			defer resp.Body.Close()
			usage, err := forwardOpenAIStream(resp.Body, write, flush)
			latencyMs := int(time.Since(start).Milliseconds())
			if err != nil {
				_ = s.markChannelFailure(channel, latencyMs)
				_ = s.recordLog(ctx, token, channel, reqInfo.Model, usage, common.LogStatusFailed, 0, "stream forwarding failed", clientIP)
				return usage, err
			}
			if httpErr := s.rejectMissingUsage(ctx, token, channel, reqInfo.Model, usage, clientIP); httpErr != nil {
				_ = s.markChannelSuccess(channel, latencyMs)
				return usage, httpErr
			}
			billing := s.calculateRelayBilling(token, channel, reqInfo.Model, usage)
			deduction, err := s.tokenService.DeductQuotaWithSnapshot(token.ID, billing.QuotaUsed)
			if err != nil {
				logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingFailureSnapshot(usage, billing, deduction, err))
				_ = s.recordLog(logCtx, token, channel, reqInfo.Model, usage, common.LogStatusFailed, 0, "insufficient quota", clientIP)
				return usage, err
			}
			_ = s.markChannelSuccess(channel, latencyMs)
			logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingSnapshot(usage, billing, deduction))
			_ = s.recordLog(logCtx, token, channel, reqInfo.Model, usage, common.LogStatusSuccess, billing.QuotaUsed, "", clientIP)
			return usage, nil
		},
	}, nil
}

func (s *RelayService) ListModels() ([]byte, error) {
	models, err := s.channelService.ListModels()
	if err != nil {
		return nil, err
	}
	data := make([]map[string]interface{}, 0, len(models))
	for _, modelName := range models {
		data = append(data, map[string]interface{}{
			"id":       modelName,
			"object":   "model",
			"created":  0,
			"owned_by": "routerx",
		})
	}
	return json.Marshal(map[string]interface{}{
		"object": "list",
		"data":   data,
	})
}

func (s *RelayService) ModelDetail(modelName string) ([]byte, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, errors.New("model is required")
	}
	models, err := s.channelService.ListModels()
	if err != nil {
		return nil, err
	}
	for _, candidate := range models {
		if candidate == modelName || "models/"+candidate == modelName {
			return json.Marshal(map[string]interface{}{
				"id":       candidate,
				"object":   "model",
				"created":  0,
				"owned_by": "routerx",
			})
		}
	}
	return nil, &HTTPError{Status: 404, Message: "model not found", Type: "invalid_request_error", Code: "model_not_found"}
}

func (s *RelayService) ListGeminiModels() ([]byte, error) {
	models, err := s.channelService.ListModels()
	if err != nil {
		return nil, err
	}
	data := make([]map[string]interface{}, 0, len(models))
	for _, modelName := range models {
		data = append(data, map[string]interface{}{
			"name":                       "models/" + modelName,
			"version":                    "",
			"displayName":                modelName,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		})
	}
	return json.Marshal(map[string]interface{}{"models": data})
}

func (s *RelayService) ListAnthropicModels() ([]byte, error) {
	models, err := s.channelService.ListModels()
	if err != nil {
		return nil, err
	}
	data := make([]map[string]interface{}, 0, len(models))
	for _, modelName := range models {
		data = append(data, map[string]interface{}{
			"id":           modelName,
			"type":         "model",
			"display_name": modelName,
		})
	}
	return json.Marshal(map[string]interface{}{"data": data, "has_more": false})
}

type relayRequestInfo struct {
	Model    string
	Stream   bool
	Route    RoutePreference
	Upstream relayUpstreamOptions
}

type relayUpstreamOptions struct {
	Transport    relay.UpstreamOptions
	Body         map[string]json.RawMessage
	ProviderBody map[string]map[string]json.RawMessage
}

func parseRelayRequestWithContentType(apiType relay.APIType, body []byte, contentType string, headerRouterX json.RawMessage) (relayRequestInfo, error) {
	if isMultipartRelayContentType(contentType) {
		return parseMultipartRelayRequest(body, contentType, headerRouterX)
	}
	return parseRelayRequest(apiType, body, headerRouterX)
}

func parseRelayRequest(apiType relay.APIType, body []byte, headerRouterX json.RawMessage) (relayRequestInfo, error) {
	if apiType == relay.APIModels {
		return relayRequestInfo{}, nil
	}
	var payload struct {
		Model   string          `json:"model"`
		Stream  bool            `json:"stream"`
		RouterX json.RawMessage `json:"routerx"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return relayRequestInfo{}, errInvalidJSONBody
	}
	routerXRaw := payload.RouterX
	if len(routerXRaw) == 0 || isJSONNull(routerXRaw) {
		routerXRaw = headerRouterX
	}
	route, upstream, err := parseRouterXOptions(routerXRaw)
	if err != nil {
		return relayRequestInfo{}, err
	}
	payload.Model = strings.TrimSpace(payload.Model)
	if payload.Model == "" {
		return relayRequestInfo{}, errModelRequired
	}
	return relayRequestInfo{Model: payload.Model, Stream: payload.Stream, Route: route, Upstream: upstream}, nil
}

func parseMultipartRelayRequest(body []byte, contentType string, headerRouterX json.RawMessage) (relayRequestInfo, error) {
	boundary, err := multipartBoundary(contentType)
	if err != nil {
		return relayRequestInfo{}, err
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	info := relayRequestInfo{}
	hasRouterXFormField := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return relayRequestInfo{}, errInvalidMultipartBody
		}
		name := part.FormName()
		if name == "" {
			continue
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			return relayRequestInfo{}, errInvalidMultipartBody
		}
		switch name {
		case "model":
			info.Model = strings.TrimSpace(string(raw))
		case "stream":
			info.Stream = multipartBoolValue(raw)
		case "routerx":
			hasRouterXFormField = true
			route, upstream, err := parseRouterXOptions(bytes.TrimSpace(raw))
			if err != nil {
				return relayRequestInfo{}, err
			}
			info.Route = route
			info.Upstream = upstream
		}
	}
	if !hasRouterXFormField {
		route, upstream, err := parseRouterXOptions(headerRouterX)
		if err != nil {
			return relayRequestInfo{}, err
		}
		info.Route = route
		info.Upstream = upstream
	}
	if info.Model == "" {
		return relayRequestInfo{}, errModelRequired
	}
	return info, nil
}

func relayRequestErrorCode(err error) string {
	switch {
	case errors.Is(err, errInvalidJSONBody):
		return "invalid_json"
	case errors.Is(err, errInvalidMultipartBody):
		return "invalid_multipart"
	case errors.Is(err, errModelRequired):
		return "model_required"
	case errors.Is(err, errInvalidRouterXOptions):
		return "invalid_routerx_options"
	case errors.Is(err, errInvalidRouterXRoute):
		return "invalid_routerx_route"
	default:
		return "invalid_request"
	}
}

func parseRouterXRoute(raw json.RawMessage) (RoutePreference, error) {
	route, _, err := parseRouterXOptions(raw)
	return route, err
}

func parseRouterXOptions(raw json.RawMessage) (RoutePreference, relayUpstreamOptions, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return RoutePreference{}, relayUpstreamOptions{}, nil
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(raw, &options); err != nil {
		return RoutePreference{}, relayUpstreamOptions{}, errInvalidRouterXOptions
	}
	route, err := parseRouterXRouteObject(options["route"])
	if err != nil {
		return RoutePreference{}, relayUpstreamOptions{}, err
	}
	upstream, err := parseRouterXUpstreamOptions(options["upstream"])
	if err != nil {
		return RoutePreference{}, relayUpstreamOptions{}, err
	}
	providerBody, err := parseRouterXProviderOptions(options["provider"])
	if err != nil {
		return RoutePreference{}, relayUpstreamOptions{}, err
	}
	upstream.ProviderBody = providerBody
	return route, upstream, nil
}

func parseRouterXRouteObject(routeRaw json.RawMessage) (RoutePreference, error) {
	if len(routeRaw) == 0 || isJSONNull(routeRaw) {
		return RoutePreference{}, nil
	}
	var route map[string]json.RawMessage
	if err := json.Unmarshal(routeRaw, &route); err != nil {
		return RoutePreference{}, errInvalidRouterXRoute
	}
	preference := RoutePreference{}
	for key, value := range route {
		switch key {
		case "channel_group", "group":
			v, err := routeStringValue(value)
			if err != nil {
				return RoutePreference{}, errInvalidRouterXRoute
			}
			preference.ChannelGroup = v
		case "channel_id":
			v, err := routeUintValue(value)
			if err != nil {
				return RoutePreference{}, errInvalidRouterXRoute
			}
			preference.ChannelID = v
		case "channel", "channel_name":
			v, err := routeStringValue(value)
			if err != nil {
				return RoutePreference{}, errInvalidRouterXRoute
			}
			preference.ChannelName = v
		case "provider", "upstream_provider":
			v, err := routeStringValue(value)
			if err != nil {
				return RoutePreference{}, errInvalidRouterXRoute
			}
			preference.Provider = v
		case "disabled_providers", "exclude_providers":
			v, err := routeStringSliceValue(value)
			if err != nil {
				return RoutePreference{}, errInvalidRouterXRoute
			}
			preference.DisabledProvider = v
		}
	}
	return preference, nil
}

func parseRouterXUpstreamOptions(raw json.RawMessage) (relayUpstreamOptions, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return relayUpstreamOptions{}, nil
	}
	var upstream struct {
		Headers map[string]string          `json:"headers"`
		Query   map[string]string          `json:"query"`
		Body    map[string]json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return relayUpstreamOptions{}, errInvalidRouterXOptions
	}
	return relayUpstreamOptions{
		Transport: relay.UpstreamOptions{
			Headers: sanitizeRouterXUpstreamHeaders(upstream.Headers),
			Query:   sanitizeRouterXUpstreamQuery(upstream.Query),
		},
		Body: sanitizeRouterXUpstreamBody(upstream.Body),
	}, nil
}

func parseRouterXProviderOptions(raw json.RawMessage) (map[string]map[string]json.RawMessage, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &providers); err != nil {
		return nil, errInvalidRouterXOptions
	}
	clean := make(map[string]map[string]json.RawMessage, len(providers))
	for provider, rawBody := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" || isJSONNull(rawBody) {
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rawBody, &body); err != nil {
			return nil, errInvalidRouterXOptions
		}
		body = sanitizeRouterXUpstreamBody(body)
		if len(body) > 0 {
			clean[provider] = body
		}
	}
	if len(clean) == 0 {
		return nil, nil
	}
	return clean, nil
}

func sanitizeRouterXUpstreamHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clean := make(map[string]string, len(headers))
	for key, value := range headers {
		key = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" || disallowedRouterXUpstreamHeader(key) {
			continue
		}
		clean[key] = value
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func disallowedRouterXUpstreamHeader(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" || strings.HasPrefix(lower, "x-routerx-") || lower == strings.ToLower(common.RequestIDHeaderName()) {
		return true
	}
	switch lower {
	case "authorization", "proxy-authorization", "cookie", "set-cookie",
		"x-api-key", "api-key", "x-goog-api-key", "anthropic-api-key",
		"host", "content-length", "content-type":
		return true
	default:
		return false
	}
}

func sanitizeRouterXUpstreamQuery(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clean := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || disallowedRouterXUpstreamQuery(key) {
			continue
		}
		clean[key] = value
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func disallowedRouterXUpstreamQuery(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "key", "api_key", "apikey", "access_token":
		return true
	default:
		return false
	}
}

func sanitizeRouterXUpstreamBody(values map[string]json.RawMessage) map[string]json.RawMessage {
	if len(values) == 0 {
		return nil
	}
	clean := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || disallowedRouterXUpstreamBodyField(key) {
			continue
		}
		clean[key] = append(json.RawMessage(nil), value...)
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func disallowedRouterXUpstreamBodyField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "model", "routerx", "stream":
		return true
	default:
		return false
	}
}

func routeStringValue(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func routeStringSliceValue(raw json.RawMessage) ([]string, error) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result, nil
}

func routeUintValue(raw json.RawMessage) (uint, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		return uint(parsed), err
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return uint(parsed), err
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.EqualFold(strings.TrimSpace(string(raw)), "null")
}

func replaceRequestModel(body []byte, modelName string) ([]byte, error) {
	if strings.TrimSpace(modelName) == "" {
		return body, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	rawModel, err := json.Marshal(modelName)
	if err != nil {
		return nil, err
	}
	payload["model"] = rawModel
	return json.Marshal(payload)
}

func mergeRelayUpstreamBody(body []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return body, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	for key, value := range extra {
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = append(json.RawMessage(nil), value...)
	}
	return json.Marshal(payload)
}

func relayUpstreamBodyForChannel(options relayUpstreamOptions, channelType int) map[string]json.RawMessage {
	merged := cloneRawMessageMap(options.Body)
	for provider, body := range options.ProviderBody {
		if !channelMatchesProvider(channelType, provider) {
			continue
		}
		if merged == nil {
			merged = map[string]json.RawMessage{}
		}
		for key, value := range body {
			merged[key] = append(json.RawMessage(nil), value...)
		}
	}
	return merged
}

func cloneRawMessageMap(values map[string]json.RawMessage) map[string]json.RawMessage {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		cloned[key] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func (s *RelayService) nextRouterXHop(ctx context.Context, channel *model.Channel) (int, bool, error) {
	if channel == nil || channel.Type != common.ChannelTypeRouterX {
		return 0, false, nil
	}
	hop, err := relayRouterXHopFromContext(ctx)
	if err != nil {
		return 0, true, err
	}
	if hop >= s.RouterXMaxHops() {
		return 0, true, errors.New("routerx hop limit exceeded")
	}
	return hop + 1, true, nil
}

func relayRouterXChainFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayRouterXChainContextKey{}).(string)
	return strings.TrimSpace(value)
}

func routerXChainForUpstream(ctx context.Context, channel *model.Channel) (string, bool) {
	if channel == nil || channel.Type != common.ChannelTypeRouterX {
		return "", false
	}
	chain := sanitizeRouterXChain(relayRouterXChainFromContext(ctx))
	if chain == "" {
		return "routerx", true
	}
	return chain + ",routerx", true
}

func sanitizeRouterXChain(chain string) string {
	parts := strings.Split(chain, ",")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return -1
			}
			return r
		}, part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	joined := strings.Join(clean, ",")
	if len(joined) > 512 {
		return joined[:512]
	}
	return joined
}

func routerXHopHTTPError(err error) *HTTPError {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "routerx hop limit exceeded"
	}
	return &HTTPError{
		Status:  http.StatusBadRequest,
		Message: message,
		Type:    "invalid_request_error",
		Code:    "routerx_hop_exceeded",
	}
}

func rewriteMultipartRelayBody(body []byte, contentType string, modelName string) ([]byte, string, error) {
	boundary, err := multipartBoundary(contentType)
	if err != nil {
		return nil, "", err
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var out bytes.Buffer
	writer := multipart.NewWriter(&out)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", errInvalidMultipartBody
		}
		name := part.FormName()
		if name == "routerx" {
			_, _ = io.Copy(io.Discard, part)
			continue
		}
		header := cloneMIMEHeader(part.Header)
		dst, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", errInvalidMultipartBody
		}
		if name == "model" && strings.TrimSpace(modelName) != "" {
			if _, err := dst.Write([]byte(modelName)); err != nil {
				return nil, "", errInvalidMultipartBody
			}
			_, _ = io.Copy(io.Discard, part)
			continue
		}
		if _, err := io.Copy(dst, part); err != nil {
			return nil, "", errInvalidMultipartBody
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", errInvalidMultipartBody
	}
	return out.Bytes(), writer.FormDataContentType(), nil
}

func isMultipartRelayContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "multipart/form-data")
}

func multipartBoundary(contentType string) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || strings.TrimSpace(params["boundary"]) == "" {
		return "", errInvalidMultipartBody
	}
	return params["boundary"], nil
}

func multipartBoolValue(raw []byte) bool {
	switch strings.ToLower(strings.TrimSpace(string(raw))) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

func cloneMIMEHeader(header textproto.MIMEHeader) textproto.MIMEHeader {
	cloned := make(textproto.MIMEHeader, len(header))
	for key, values := range header {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func doRelayAdapterRequest(ctx context.Context, adapter relay.Adapter, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error) {
	if strings.TrimSpace(contentType) == "" {
		return adapter.DoRequest(ctx, baseURL, endpoint, apiKey, body)
	}
	contentTypeAdapter, ok := adapter.(contentTypeRelayAdapter)
	if !ok {
		return nil, errUnsupportedMultipart
	}
	return contentTypeAdapter.DoRequestWithContentType(ctx, baseURL, endpoint, apiKey, body, contentType)
}

func anthropicMessagesToOpenAI(body []byte) ([]byte, error) {
	var input struct {
		Model    string          `json:"model"`
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens     *int     `json:"max_tokens"`
		Temperature   *float64 `json:"temperature"`
		TopP          *float64 `json:"top_p"`
		StopSequences []string `json:"stop_sequences"`
		Stream        bool     `json:"stream"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, errors.New("invalid json body")
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" {
		return nil, errors.New("model is required")
	}
	messages := make([]map[string]interface{}, 0, len(input.Messages)+1)
	if system := strings.TrimSpace(relay.TextFromContent(input.System)); system != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": system})
	}
	for _, message := range input.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "assistant" {
			role = "user"
		}
		messages = append(messages, map[string]interface{}{
			"role":    role,
			"content": relay.TextFromContent(message.Content),
		})
	}
	output := map[string]interface{}{
		"model":    input.Model,
		"messages": messages,
		"stream":   input.Stream,
	}
	if input.MaxTokens != nil {
		output["max_tokens"] = *input.MaxTokens
	}
	if input.Temperature != nil {
		output["temperature"] = *input.Temperature
	}
	if input.TopP != nil {
		output["top_p"] = *input.TopP
	}
	if len(input.StopSequences) > 0 {
		output["stop"] = input.StopSequences
	}
	return json.Marshal(output)
}

func geminiGenerateToOpenAI(modelName string, body []byte, stream bool) ([]byte, error) {
	var input struct {
		Contents []struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		} `json:"contents"`
		SystemInstruction *struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"systemInstruction"`
		GenerationConfig map[string]interface{} `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, errors.New("invalid json body")
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, errors.New("model is required")
	}
	messages := make([]map[string]interface{}, 0, len(input.Contents)+1)
	if input.SystemInstruction != nil {
		parts := make([]string, 0, len(input.SystemInstruction.Parts))
		for _, part := range input.SystemInstruction.Parts {
			if text := strings.TrimSpace(relay.TextFromContent(part)); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			messages = append(messages, map[string]interface{}{"role": "system", "content": strings.Join(parts, "\n")})
		}
	}
	for _, content := range input.Contents {
		parts := make([]string, 0, len(content.Parts))
		for _, part := range content.Parts {
			if text := strings.TrimSpace(relay.TextFromContent(part)); text != "" {
				parts = append(parts, text)
			}
		}
		role := "user"
		if strings.EqualFold(content.Role, "model") {
			role = "assistant"
		}
		messages = append(messages, map[string]interface{}{"role": role, "content": strings.Join(parts, "\n")})
	}
	output := map[string]interface{}{
		"model":    modelName,
		"messages": messages,
		"stream":   stream,
	}
	if config := input.GenerationConfig; len(config) > 0 {
		if value, ok := config["maxOutputTokens"]; ok {
			output["max_tokens"] = value
		}
		if value, ok := config["temperature"]; ok {
			output["temperature"] = value
		}
		if value, ok := config["topP"]; ok {
			output["top_p"] = value
		}
		if value, ok := config["stopSequences"]; ok {
			output["stop"] = value
		}
	}
	return json.Marshal(output)
}

func geminiEmbedContentToOpenAI(modelName string, body []byte) ([]byte, error) {
	var input struct {
		Content struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, errors.New("invalid json body")
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, errors.New("model is required")
	}
	text := geminiTextFromParts(input.Content.Parts)
	if text == "" {
		return nil, errors.New("content is required")
	}
	return json.Marshal(map[string]interface{}{
		"model": modelName,
		"input": text,
	})
}

func geminiBatchEmbedContentsToOpenAI(modelName string, body []byte) ([]byte, error) {
	var input struct {
		Requests []struct {
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, errors.New("invalid json body")
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, errors.New("model is required")
	}
	if len(input.Requests) == 0 {
		return nil, errors.New("requests are required")
	}
	values := make([]string, 0, len(input.Requests))
	for _, request := range input.Requests {
		text := geminiTextFromParts(request.Content.Parts)
		if text == "" {
			return nil, errors.New("content is required")
		}
		values = append(values, text)
	}
	return json.Marshal(map[string]interface{}{
		"model": modelName,
		"input": values,
	})
}

func geminiTextFromParts(parts []json.RawMessage) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if text := strings.TrimSpace(relay.TextFromContent(part)); text != "" {
			values = append(values, text)
		}
	}
	return strings.Join(values, "\n")
}

func openAIChatToAnthropic(body []byte) ([]byte, error) {
	var input struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *relay.Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}
	content := ""
	stopReason := "end_turn"
	if len(input.Choices) > 0 {
		content = input.Choices[0].Message.Content
		stopReason = anthropicStopReason(input.Choices[0].FinishReason)
	}
	usage := map[string]int{"input_tokens": 0, "output_tokens": 0}
	if input.Usage != nil {
		usage["input_tokens"] = input.Usage.PromptTokens
		usage["output_tokens"] = input.Usage.CompletionTokens
	}
	return json.Marshal(map[string]interface{}{
		"id":            input.ID,
		"type":          "message",
		"role":          "assistant",
		"model":         input.Model,
		"content":       []map[string]string{{"type": "text", "text": content}},
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	})
}

type anthropicStreamState struct {
	started        bool
	contentStopped bool
}

func openAIStreamChunkToAnthropic(chunk []byte, state *anthropicStreamState) ([]byte, bool, error) {
	line := bytes.TrimSpace(chunk)
	if len(line) == 0 {
		return nil, false, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return chunk, true, nil
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	var out bytes.Buffer
	if bytes.Equal(payload, []byte("[DONE]")) {
		if state.started && !state.contentStopped {
			if err := writeAnthropicStreamEvent(&out, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0}); err != nil {
				return nil, false, err
			}
			state.contentStopped = true
		}
		if state.started {
			if err := writeAnthropicStreamEvent(&out, "message_stop", map[string]interface{}{"type": "message_stop"}); err != nil {
				return nil, false, err
			}
		}
		return out.Bytes(), out.Len() > 0, nil
	}
	if !json.Valid(payload) {
		return nil, false, errors.New("invalid openai stream chunk")
	}
	var input struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *relay.Usage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return nil, false, err
	}
	if !state.started {
		if err := writeAnthropicStreamStart(&out); err != nil {
			return nil, false, err
		}
		state.started = true
	}
	for _, choice := range input.Choices {
		if choice.Delta.Content != "" {
			if err := writeAnthropicStreamEvent(&out, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]string{"type": "text_delta", "text": choice.Delta.Content},
			}); err != nil {
				return nil, false, err
			}
		}
		if choice.FinishReason != "" || input.Usage != nil {
			delta := map[string]interface{}{}
			if choice.FinishReason != "" {
				delta["stop_reason"] = anthropicStopReason(choice.FinishReason)
				delta["stop_sequence"] = nil
			}
			if err := writeAnthropicStreamEvent(&out, "message_delta", map[string]interface{}{
				"type":  "message_delta",
				"delta": delta,
				"usage": anthropicStreamUsage(input.Usage),
			}); err != nil {
				return nil, false, err
			}
		}
	}
	return out.Bytes(), out.Len() > 0, nil
}

func writeAnthropicStreamStart(out *bytes.Buffer) error {
	if err := writeAnthropicStreamEvent(out, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            "msg_routerx_stream",
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	}); err != nil {
		return err
	}
	return writeAnthropicStreamEvent(out, "content_block_start", map[string]interface{}{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]string{"type": "text", "text": ""},
	})
}

func writeAnthropicStreamEvent(out *bytes.Buffer, event string, payload interface{}) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	out.WriteString("event: ")
	out.WriteString(event)
	out.WriteByte('\n')
	out.WriteString("data: ")
	out.Write(raw)
	out.WriteString("\n\n")
	return nil
}

func anthropicStreamUsage(usage *relay.Usage) map[string]int {
	if usage == nil {
		return map[string]int{}
	}
	return map[string]int{
		"input_tokens":  usage.PromptTokens,
		"output_tokens": usage.CompletionTokens,
	}
}

func openAIChatToGemini(body []byte) ([]byte, error) {
	var input struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *relay.Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}
	candidates := make([]map[string]interface{}, 0, len(input.Choices))
	for _, choice := range input.Choices {
		candidates = append(candidates, map[string]interface{}{
			"content": map[string]interface{}{
				"role":  "model",
				"parts": []map[string]string{{"text": choice.Message.Content}},
			},
			"finishReason": geminiFinishReason(choice.FinishReason),
		})
	}
	return json.Marshal(map[string]interface{}{
		"candidates":    candidates,
		"usageMetadata": geminiUsageMetadata(input.Usage),
	})
}

func openAIEmbeddingsToGemini(body []byte) ([]byte, error) {
	var input struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}
	if len(input.Data) == 0 {
		return nil, errors.New("embedding response is empty")
	}
	return json.Marshal(map[string]interface{}{
		"embedding": map[string]interface{}{
			"values": input.Data[0].Embedding,
		},
	})
}

func openAIEmbeddingsToGeminiBatch(body []byte) ([]byte, error) {
	var input struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}
	if len(input.Data) == 0 {
		return nil, errors.New("embedding response is empty")
	}
	embeddings := make([]map[string]interface{}, 0, len(input.Data))
	for _, item := range input.Data {
		embeddings = append(embeddings, map[string]interface{}{"values": item.Embedding})
	}
	return json.Marshal(map[string]interface{}{"embeddings": embeddings})
}

func openAIStreamChunkToGemini(chunk []byte) ([]byte, bool, error) {
	line := bytes.TrimSpace(chunk)
	if len(line) == 0 {
		return []byte("\n"), true, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return chunk, true, nil
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false, nil
	}
	if !json.Valid(payload) {
		return nil, false, errors.New("invalid openai stream chunk")
	}
	var input struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *relay.Usage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return nil, false, err
	}
	output := map[string]interface{}{}
	candidates := make([]map[string]interface{}, 0, len(input.Choices))
	for _, choice := range input.Choices {
		candidate := map[string]interface{}{}
		if choice.Delta.Content != "" {
			candidate["content"] = map[string]interface{}{
				"role":  "model",
				"parts": []map[string]string{{"text": choice.Delta.Content}},
			}
		}
		if choice.FinishReason != "" {
			candidate["finishReason"] = geminiFinishReason(choice.FinishReason)
		}
		if len(candidate) > 0 {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) > 0 {
		output["candidates"] = candidates
	}
	if input.Usage != nil {
		output["usageMetadata"] = geminiUsageMetadata(input.Usage)
	}
	if len(output) == 0 {
		return nil, false, nil
	}
	converted, err := json.Marshal(output)
	if err != nil {
		return nil, false, err
	}
	return append(append([]byte("data: "), converted...), '\n'), true, nil
}

func geminiUsageMetadata(usage *relay.Usage) map[string]int {
	metadata := map[string]int{}
	if usage != nil {
		metadata["promptTokenCount"] = usage.PromptTokens
		metadata["candidatesTokenCount"] = usage.CompletionTokens
		metadata["totalTokenCount"] = usage.TotalTokens
	}
	return metadata
}

func anthropicStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "stop":
		return "end_turn"
	default:
		return reason
	}
}

func geminiFinishReason(reason string) string {
	switch reason {
	case "length":
		return "MAX_TOKENS"
	case "stop":
		return "STOP"
	default:
		return strings.ToUpper(reason)
	}
}

func approximateTokenCount(body []byte) int {
	text := string(body)
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == '"' || r == '\'' || r == ',' || r == ':' || r == '{' || r == '}' || r == '[' || r == ']'
	})
	count := 0
	for _, field := range fields {
		if strings.TrimSpace(field) != "" {
			count++
		}
	}
	if count == 0 && len(body) > 0 {
		return 1
	}
	return count
}

func (s *RelayService) relayTimeout() time.Duration {
	timeoutSeconds := 120
	if s.settingService != nil {
		if value, err := s.settingService.Get("relay.timeout"); err == nil {
			timeoutSeconds = common.ParsePositiveInt(value, 120)
		}
	}
	return time.Duration(timeoutSeconds) * time.Second
}

func (s *RelayService) MaxRequestBodyBytes() int64 {
	if s == nil || s.settingService == nil {
		return defaultRelayMaxRequestBodyBytes
	}
	value, err := s.settingService.GetInt("relay.max_request_body_bytes")
	if err != nil || value < 0 {
		return defaultRelayMaxRequestBodyBytes
	}
	return int64(value)
}

func (s *RelayService) MaxResponseBodyBytes() int64 {
	if s == nil || s.settingService == nil {
		return defaultRelayMaxResponseBodyBytes
	}
	value, err := s.settingService.GetInt("relay.max_response_body_bytes")
	if err != nil || value < 0 {
		return defaultRelayMaxResponseBodyBytes
	}
	return int64(value)
}

func (s *RelayService) RouterXMaxHops() int {
	if s == nil || s.settingService == nil {
		return defaultRouterXMaxHops
	}
	value, err := s.settingService.GetInt("relay.routerx_max_hops")
	if err != nil || value <= 0 {
		return defaultRouterXMaxHops
	}
	return value
}

func (s *RelayService) readUpstreamResponseBody(body io.Reader) ([]byte, error) {
	limit := s.MaxResponseBodyBytes()
	if limit <= 0 {
		return io.ReadAll(body)
	}
	payload, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errRelayResponseBodyTooLarge
	}
	return payload, nil
}

func (s *RelayService) relayRetryCount() int {
	if s.settingService == nil {
		return 0
	}
	value, err := s.settingService.GetInt("relay.retry_count")
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (s *RelayService) retryableUpstreamStatus(status int) bool {
	statuses := s.retryableUpstreamStatuses()
	_, ok := statuses[status]
	return ok
}

func (s *RelayService) retryableUpstreamStatuses() map[int]struct{} {
	statuses := defaultRetryableUpstreamStatuses()
	if s == nil || s.settingService == nil {
		return statuses
	}
	raw, err := s.settingService.Get("relay.retry_on_status")
	if err != nil {
		return statuses
	}
	// Settings validation rejects bad values on write; this fallback protects older or manually edited databases.
	values, err := parseHTTPErrorStatusArraySetting("relay.retry_on_status", raw)
	if err != nil {
		return statuses
	}
	statuses = make(map[int]struct{}, len(values))
	for _, value := range values {
		statuses[value] = struct{}{}
	}
	return statuses
}

func defaultRetryableUpstreamStatuses() map[int]struct{} {
	return map[int]struct{}{
		429: {},
		500: {},
		502: {},
		503: {},
		504: {},
	}
}

func (s *RelayService) CheckTokenAPIScope(token *model.Token, apiType relay.APIType, clientIP string) error {
	return s.enforceTokenAPIScope(context.Background(), token, apiType, "", clientIP)
}

func (s *RelayService) enforceTokenScope(ctx context.Context, token *model.Token, apiType relay.APIType, modelName, clientIP string) error {
	if err := s.enforceTokenAPIScope(ctx, token, apiType, modelName, clientIP); err != nil {
		return err
	}
	if err := s.enforceTokenModelScope(ctx, token, modelName, clientIP); err != nil {
		return err
	}
	return s.enforceTokenTPMScope(ctx, token, modelName, clientIP)
}

func (s *RelayService) enforceTokenRouteChannelGroupScope(ctx context.Context, token *model.Token, route RoutePreference, modelName, clientIP string) error {
	if strings.TrimSpace(route.ChannelGroup) == "" {
		return nil
	}
	return s.enforceTokenChannelGroupValueScope(ctx, token, route.ChannelGroup, modelName, clientIP)
}

func (s *RelayService) filterTokenChannelGroupScope(ctx context.Context, token *model.Token, candidates []model.Channel, modelName, clientIP string) ([]model.Channel, int, error) {
	if s.tokenService == nil || len(candidates) == 0 {
		return candidates, 0, nil
	}
	filtered := make([]model.Channel, 0, len(candidates))
	for _, candidate := range candidates {
		if err := s.tokenService.CheckChannelGroupScope(token, candidate.ChannelGroup); err == nil {
			filtered = append(filtered, candidate)
		}
	}
	removed := len(candidates) - len(filtered)
	if len(filtered) == 0 {
		return nil, removed, channelGroupScopeHTTPError()
	}
	return filtered, removed, nil
}

func (s *RelayService) filterUserChannelGroupAccess(ctx context.Context, token *model.Token, candidates []model.Channel, modelName, clientIP string) ([]model.Channel, int, error) {
	if len(candidates) == 0 {
		return candidates, 0, nil
	}
	policy := s.channelGroupAccessPolicy(token)
	filtered := make([]model.Channel, 0, len(candidates))
	for _, candidate := range candidates {
		if policy.allows(candidate.ChannelGroup) {
			filtered = append(filtered, candidate)
		}
	}
	removed := len(candidates) - len(filtered)
	if len(filtered) == 0 {
		return nil, removed, &HTTPError{Status: 403, Message: "channel group is not allowed by user group access", Type: "permission_error", Code: "route_forbidden"}
	}
	return filtered, removed, nil
}

func (s *RelayService) filterUserChannelModelAccess(token *model.Token, candidates []model.Channel, modelName string) ([]model.Channel, int, error) {
	if len(candidates) == 0 || tokenAllowsHiddenChannelModels(token) {
		return candidates, 0, nil
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return candidates, 0, nil
	}
	channelIDs := make([]uint, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID > 0 {
			channelIDs = append(channelIDs, candidate.ID)
		}
	}
	if len(channelIDs) == 0 {
		return candidates, 0, nil
	}
	var prices []model.ChannelModelPrice
	if err := internal.DB.
		Where("channel_id IN ? AND model = ? AND user_enabled = ?", channelIDs, modelName, false).
		Find(&prices).Error; err != nil {
		return candidates, 0, err
	}
	if len(prices) == 0 {
		return candidates, 0, nil
	}
	hiddenByChannel := make(map[uint]struct{}, len(prices))
	for _, price := range prices {
		hiddenByChannel[price.ChannelID] = struct{}{}
	}
	filtered := make([]model.Channel, 0, len(candidates))
	for _, candidate := range candidates {
		if _, hidden := hiddenByChannel[candidate.ID]; hidden {
			continue
		}
		filtered = append(filtered, candidate)
	}
	removed := len(candidates) - len(filtered)
	if len(filtered) == 0 {
		return nil, removed, &HTTPError{Status: 403, Message: "channel model is not enabled for ordinary users", Type: "permission_error", Code: "route_forbidden"}
	}
	return filtered, removed, nil
}

func tokenAllowsHiddenChannelModels(token *model.Token) bool {
	return token != nil && token.User != nil && token.User.Role > common.RoleUser
}

func (s *RelayService) channelGroupAccessPolicy(token *model.Token) channelGroupAccessPolicy {
	policy := newChannelGroupAccessPolicy(s.defaultUserChannelGroupAccess())
	overrides := s.userGroupChannelGroupAccess()
	// 策略合成顺序与 docs/POLICIES.md 一致：默认允许列表先给普通访问范围，再叠加用户分组 allow/deny。
	for _, key := range userGroupAccessKeys(token) {
		if rule, ok := overrides[key]; ok {
			policy.applyAllow(rule.Allow)
			policy.applyDeny(rule.Deny)
		}
	}
	return policy
}

func newChannelGroupAccessPolicy(defaultAllowed []string) channelGroupAccessPolicy {
	policy := channelGroupAccessPolicy{
		allowed: map[string]struct{}{},
		denied:  map[string]struct{}{},
	}
	policy.applyAllow(defaultAllowed)
	return policy
}

func (p *channelGroupAccessPolicy) applyAllow(values []string) {
	for _, group := range normalizeChannelGroupAccessValues(values) {
		if group == "*" {
			p.allowAll = true
			continue
		}
		p.allowed[group] = struct{}{}
	}
}

func (p *channelGroupAccessPolicy) applyDeny(values []string) {
	for _, group := range normalizeChannelGroupAccessValues(values) {
		p.denied[group] = struct{}{}
	}
}

func (p channelGroupAccessPolicy) allows(group string) bool {
	group = normalizeChannelGroupName(group)
	if _, ok := p.denied["*"]; ok {
		return false
	}
	if _, ok := p.denied[group]; ok {
		return false
	}
	if p.allowAll {
		return true
	}
	_, ok := p.allowed[group]
	return ok
}

func (s *RelayService) defaultUserChannelGroupAccess() []string {
	fallback := []string{"default"}
	if s.settingService == nil {
		return fallback
	}
	raw, err := s.settingService.Get("billing.default_user_channel_group_access")
	if err != nil {
		return fallback
	}
	values, err := parseChannelGroupListSetting(raw)
	if err != nil {
		return fallback
	}
	return values
}

func (s *RelayService) userGroupChannelGroupAccess() map[string]channelGroupAccessRule {
	if s.settingService == nil {
		return nil
	}
	raw, err := s.settingService.Get("billing.user_group_channel_group_access")
	if err != nil {
		return nil
	}
	var rules map[string]channelGroupAccessRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil
	}
	normalized := make(map[string]channelGroupAccessRule, len(rules))
	for key, rule := range rules {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		normalized[key] = channelGroupAccessRule{
			Allow: normalizeChannelGroupAccessValues(rule.Allow),
			Deny:  normalizeChannelGroupAccessValues(rule.Deny),
		}
	}
	return normalized
}

func parseChannelGroupListSetting(raw string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return normalizeChannelGroupAccessValues(values), nil
}

func normalizeChannelGroupAccessValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeChannelGroupName(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func userGroupAccessKeys(token *model.Token) []string {
	keys := make([]string, 0, 2)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range keys {
			if existing == value {
				return
			}
		}
		keys = append(keys, value)
	}
	if token != nil && token.User != nil {
		if token.User.Group != nil {
			add(token.User.Group.Name)
		}
		if token.User.GroupID != nil && *token.User.GroupID > 0 {
			add(strconv.FormatUint(uint64(*token.User.GroupID), 10))
		}
	}
	if len(keys) == 0 {
		add("default")
	}
	return keys
}

func (s *RelayService) enforceTokenChannelGroupValueScope(ctx context.Context, token *model.Token, channelGroup, modelName, clientIP string) error {
	if s.tokenService == nil {
		return nil
	}
	if err := s.tokenService.CheckChannelGroupScope(token, channelGroup); err != nil {
		return s.channelGroupScopeError(ctx, token, modelName, clientIP)
	}
	return nil
}

func (s *RelayService) channelGroupScopeError(ctx context.Context, token *model.Token, modelName, clientIP string) error {
	logCtx := ContextWithRelayPolicySnapshot(ctx, buildRelayPolicyDenySnapshot(ctx, token, "route_forbidden", "not_evaluated", map[string]interface{}{
		"api_type":      "allow",
		"model":         "allow",
		"channel_group": "deny",
	}))
	_ = s.recordLog(logCtx, token, nil, modelName, nil, common.LogStatusFailed, 0, "channel group not allowed by api key scope", clientIP)
	return channelGroupScopeHTTPError()
}

func channelGroupScopeHTTPError() *HTTPError {
	return &HTTPError{Status: 403, Message: "channel group is not allowed by api key scope", Type: "permission_error", Code: "route_forbidden"}
}

func (s *RelayService) enforceTokenAPIScope(ctx context.Context, token *model.Token, apiType relay.APIType, modelName, clientIP string) error {
	if s.tokenService == nil {
		return nil
	}
	scopeName := relayAPITypeScopeName(apiType)
	if scopeName == "" {
		return nil
	}
	if err := s.tokenService.CheckAPIScope(token, scopeName); err != nil {
		logCtx := ContextWithRelayPolicySnapshot(ctx, buildRelayPolicyDenySnapshot(ctx, token, "token_forbidden", "not_evaluated", map[string]interface{}{
			"api_type":      "deny",
			"model":         "not_evaluated",
			"channel_group": "not_evaluated",
		}))
		_ = s.recordLog(logCtx, token, nil, modelName, nil, common.LogStatusFailed, 0, "api type not allowed by api key scope", clientIP)
		return &HTTPError{Status: 403, Message: "api type is not allowed by api key scope", Type: "permission_error", Code: "token_forbidden"}
	}
	return nil
}

func (s *RelayService) enforceTokenModelScope(ctx context.Context, token *model.Token, modelName, clientIP string) error {
	if s.tokenService == nil || strings.TrimSpace(modelName) == "" {
		return nil
	}
	if err := s.tokenService.CheckModelScope(token, modelName); err != nil {
		logCtx := ContextWithRelayPolicySnapshot(ctx, buildRelayPolicyDenySnapshot(ctx, token, "model_not_allowed", "not_evaluated", map[string]interface{}{
			"api_type":      "allow",
			"model":         "deny",
			"channel_group": "not_evaluated",
		}))
		_ = s.recordLog(logCtx, token, nil, modelName, nil, common.LogStatusFailed, 0, "model not allowed by api key scope", clientIP)
		return &HTTPError{Status: 403, Message: "model is not allowed by api key scope", Type: "permission_error", Code: "model_not_allowed"}
	}
	return nil
}

func (s *RelayService) enforceTokenTPMScope(ctx context.Context, token *model.Token, modelName, clientIP string) error {
	if s.tokenService == nil {
		return nil
	}
	if err := s.tokenService.CheckTPMScope(token); err != nil {
		logCtx := ContextWithRelayPolicySnapshot(ctx, buildRelayPolicyDenySnapshot(ctx, token, "rate_limit_exceeded", "rate_limit_exceeded", map[string]interface{}{
			"api_type":      "allow",
			"model":         "allow",
			"channel_group": "not_evaluated",
			"tpm":           "deny",
		}))
		_ = s.recordLog(logCtx, token, nil, modelName, nil, common.LogStatusFailed, 0, "tpm limit exceeded by api key scope", clientIP)
		return &HTTPError{Status: 429, Message: "rate limit exceeded", Type: "rate_limit_error", Code: "rate_limit_exceeded"}
	}
	return nil
}

func (s *RelayService) enforceModelRateLimit(ctx context.Context, token *model.Token, modelName, clientIP string) error {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || internal.RDB == nil {
		return nil
	}
	settingSvc := s.settingService
	if settingSvc == nil {
		settingSvc = NewSettingService()
	}
	if enabled, err := settingSvc.GetBool("rate_limit.enabled"); err == nil && !enabled {
		return nil
	}
	limit, err := settingSvc.GetInt("rate_limit.per_model_per_min")
	if err != nil || limit <= 0 {
		return nil
	}
	now := time.Now().Unix() / 60
	exceeded, current := relayRateLimitExceeded(fmt.Sprintf("rl:model:%s:%d", modelName, now), int64(limit))
	if !exceeded {
		return nil
	}
	logCtx := ContextWithRelayPolicySnapshot(ctx, buildRelayRateLimitDenySnapshot(ctx, token, "model", int64(limit), current, map[string]interface{}{
		"api_type":             "allow",
		"model":                "allow",
		"channel_group":        "not_evaluated",
		"rate_limit":           "deny",
		"rate_limit_dimension": "model",
	}))
	_ = s.recordLog(logCtx, token, nil, modelName, nil, common.LogStatusFailed, 0, "model rate limit exceeded", clientIP)
	return &HTTPError{Status: 429, Message: "rate limit exceeded", Type: "rate_limit_error", Code: "rate_limit_exceeded"}
}

func (s *RelayService) enforceChannelRateLimit(ctx context.Context, token *model.Token, channel *model.Channel, modelName, clientIP string) error {
	if channel == nil || channel.ID == 0 || internal.RDB == nil {
		return nil
	}
	settingSvc := s.settingService
	if settingSvc == nil {
		settingSvc = NewSettingService()
	}
	if enabled, err := settingSvc.GetBool("rate_limit.enabled"); err == nil && !enabled {
		return nil
	}
	limit, err := settingSvc.GetInt("rate_limit.per_channel_per_min")
	if err != nil || limit <= 0 {
		return nil
	}
	now := time.Now().Unix() / 60
	exceeded, current := relayRateLimitExceeded(fmt.Sprintf("rl:channel:%d:%d", channel.ID, now), int64(limit))
	if !exceeded {
		return nil
	}
	logCtx := ContextWithRelayPolicySnapshot(ctx, buildRelayRateLimitDenySnapshot(ctx, token, "channel", int64(limit), current, map[string]interface{}{
		"api_type":             "allow",
		"model":                "allow",
		"channel_group":        "allow",
		"rate_limit":           "deny",
		"rate_limit_dimension": "channel",
	}))
	_ = s.recordLog(logCtx, token, channel, modelName, nil, common.LogStatusFailed, 0, "channel rate limit exceeded", clientIP)
	return &HTTPError{Status: 429, Message: "rate limit exceeded", Type: "rate_limit_error", Code: "rate_limit_exceeded"}
}

func relayRateLimitExceeded(key string, limit int64) (bool, int64) {
	if internal.RDB == nil || limit <= 0 {
		return false, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	count, err := internal.RDB.Incr(ctx, key).Result()
	if err != nil {
		return false, 0
	}
	if count == 1 {
		_ = internal.RDB.Expire(ctx, key, 2*time.Minute).Err()
	}
	return count > limit, count
}

// relayAPITypeScopeName keeps API Key scope names stable across internal enum values.
func relayAPITypeScopeName(apiType relay.APIType) string {
	switch apiType {
	case relay.APIChatCompletions:
		return "openai.chat"
	case relay.APICompletions:
		return "openai.completions"
	case relay.APIResponses:
		return "openai.responses"
	case relay.APIImagesGenerations:
		return "openai.images.generations"
	case relay.APIImagesEdits:
		return "openai.images.edits"
	case relay.APIImagesVariations:
		return "openai.images.variations"
	case relay.APIAudioTranscriptions:
		return "openai.audio.transcriptions"
	case relay.APIAudioTranslations:
		return "openai.audio.translations"
	case relay.APIAudioSpeech:
		return "openai.audio.speech"
	case relay.APIEmbeddings:
		return "openai.embeddings"
	case relay.APIModels:
		return "openai.models"
	case relay.APIFiles:
		return "openai.files"
	case relay.APIFineTuning:
		return "openai.fine_tuning"
	case relay.APIModerations:
		return "openai.moderations"
	case relay.APIGeminiGenerateContent:
		return "gemini.generate_content"
	case relay.APIGeminiStreamGenerateContent:
		return "gemini.stream_generate_content"
	case relay.APIGeminiCountTokens:
		return "gemini.count_tokens"
	case relay.APIGeminiEmbedContent:
		return "gemini.embed_content"
	case relay.APIGeminiBatchEmbedContents:
		return "gemini.batch_embed_contents"
	case relay.APIAnthropicMessages:
		return "anthropic.messages"
	case relay.APIAnthropicCountTokens:
		return "anthropic.count_tokens"
	default:
		return ""
	}
}

func pickRelayChannelCandidate(candidates []model.Channel) *model.Channel {
	if len(candidates) == 0 {
		return nil
	}
	bestPriority := candidates[0].Priority
	bestPriorityCandidates := make([]model.Channel, 0, len(candidates))
	for _, channel := range candidates {
		if channel.Priority != bestPriority {
			break
		}
		bestPriorityCandidates = append(bestPriorityCandidates, channel)
	}
	return weightedPick(bestPriorityCandidates)
}

func quotaFromUsage(usage *relay.Usage) int64 {
	if usage == nil || usage.TotalTokens <= 0 {
		return 1
	}
	return int64(usage.TotalTokens)
}

func (s *RelayService) usageMissingStrategy() string {
	if s == nil || s.settingService == nil {
		return usageMissingStrategyMinimum
	}
	value, err := s.settingService.Get("billing.usage_missing_strategy")
	if err != nil {
		return usageMissingStrategyMinimum
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case usageMissingStrategyReject:
		return usageMissingStrategyReject
	default:
		return usageMissingStrategyMinimum
	}
}

func (s *RelayService) rejectMissingUsage(ctx context.Context, token *model.Token, channel *model.Channel, modelName string, usage *relay.Usage, clientIP string) *HTTPError {
	if usage != nil && usage.TotalTokens > 0 {
		return nil
	}
	if s.usageMissingStrategy() != usageMissingStrategyReject {
		return nil
	}
	const message = "usage missing rejected by billing policy"
	_ = s.recordLog(ctx, token, channel, modelName, usage, common.LogStatusFailed, 0, message, clientIP)
	return &HTTPError{
		Status:  http.StatusBadGateway,
		Message: "upstream usage missing",
		Type:    "upstream_error",
		Code:    "usage_missing",
	}
}

func logUsageSource(usage *relay.Usage, status int, quotaUsed int64) string {
	if usage != nil && usage.TotalTokens > 0 {
		return common.LogUsageSourceUpstream
	}
	if status == common.LogStatusSuccess && quotaUsed > 0 {
		return common.LogUsageSourceMinimum
	}
	return ""
}

func buildRelayRequestSnapshot(ctx context.Context, apiType relay.APIType, reqInfo relayRequestInfo) string {
	apiTypeName := relayAPITypeScopeName(apiType)
	snapshot := map[string]interface{}{
		"schema":           "routerx.snapshot.v1",
		"kind":             "request",
		"stage":            "p1",
		"source":           "relay",
		"redacted":         true,
		"request_id":       relayRequestIDFromContext(ctx),
		"ingress_protocol": relayIngressProtocolFromAPIType(apiType),
		"api_type":         apiTypeName,
		"requested_model":  strings.TrimSpace(reqInfo.Model),
		"stream":           reqInfo.Stream,
	}
	if preference := routePreferenceSnapshot(reqInfo.Route); len(preference) > 0 {
		snapshot["routerx_summary"] = map[string]interface{}{
			"route": preference,
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

func relayIngressProtocolFromAPIType(apiType relay.APIType) string {
	apiTypeName := relayAPITypeScopeName(apiType)
	if idx := strings.Index(apiTypeName, "."); idx > 0 {
		return apiTypeName[:idx]
	}
	return strings.TrimSpace(apiTypeName)
}

func buildRelayPolicySnapshot(ctx context.Context, token *model.Token, reqInfo relayRequestInfo) string {
	snapshot := map[string]interface{}{
		"schema":          "routerx.snapshot.v1",
		"kind":            "policy",
		"stage":           "p1",
		"source":          "policy",
		"redacted":        true,
		"request_id":      relayRequestIDFromContext(ctx),
		"access_decision": "allow",
		"quota_precheck":  "available",
		"scope_result": map[string]interface{}{
			"api_type":      "allow",
			"model":         "allow",
			"channel_group": "allow",
		},
		"policy_version": "p0_scope",
	}
	if token != nil {
		snapshot["token_status"] = map[string]interface{}{
			"id":        token.ID,
			"status":    tokenStatusSnapshot(token.Status),
			"unlimited": token.Unlimited || token.RemainQuota == common.QuotaUnlimited,
		}
		if token.User != nil {
			snapshot["user_status"] = map[string]interface{}{
				"id":     token.User.ID,
				"status": userStatusSnapshot(token.User.Status),
			}
		}
	}
	if preference := routePreferenceSnapshot(reqInfo.Route); len(preference) > 0 {
		snapshot["route_preference"] = preference
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

func buildRelayPolicyDenySnapshot(ctx context.Context, token *model.Token, rejectCode, quotaPrecheck string, scopeResult map[string]interface{}) string {
	snapshot := map[string]interface{}{
		"schema":          "routerx.snapshot.v1",
		"kind":            "policy",
		"stage":           "p1",
		"source":          "policy",
		"redacted":        true,
		"request_id":      relayRequestIDFromContext(ctx),
		"access_decision": "deny",
		"quota_precheck":  strings.TrimSpace(quotaPrecheck),
		"scope_result":    scopeResult,
		"reject_code":     strings.TrimSpace(rejectCode),
		"policy_version":  "p0_scope",
	}
	if snapshot["quota_precheck"] == "" {
		snapshot["quota_precheck"] = "not_evaluated"
	}
	if token != nil {
		snapshot["token_status"] = map[string]interface{}{
			"id":        token.ID,
			"status":    tokenStatusSnapshot(token.Status),
			"unlimited": token.Unlimited || token.RemainQuota == common.QuotaUnlimited,
		}
		if token.User != nil {
			snapshot["user_status"] = map[string]interface{}{
				"id":     token.User.ID,
				"status": userStatusSnapshot(token.User.Status),
			}
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

func buildRelayRateLimitDenySnapshot(ctx context.Context, token *model.Token, dimension string, limit, current int64, scopeResult map[string]interface{}) string {
	raw := buildRelayPolicyDenySnapshot(ctx, token, "rate_limit_exceeded", "rate_limit_exceeded", scopeResult)
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return raw
	}
	remaining := limit - current
	if remaining < 0 {
		remaining = 0
	}
	snapshot["rate_limit_snapshot"] = map[string]interface{}{
		"dimension": strings.ToLower(strings.TrimSpace(dimension)),
		"window":    "minute",
		"threshold": limit,
		"current":   current,
		"remaining": remaining,
		"decision":  "deny",
	}
	withRateLimit, err := json.Marshal(snapshot)
	if err != nil {
		return raw
	}
	return string(withRateLimit)
}

func buildRelayUserGroupAccessDenyPolicySnapshot(ctx context.Context, token *model.Token) string {
	return buildRelayPolicyDenySnapshot(ctx, token, "route_forbidden", "available", map[string]interface{}{
		"api_type":                 "allow",
		"model":                    "allow",
		"channel_group":            "allow",
		"user_group_channel_group": "deny",
	})
}

func buildRelayChannelModelAccessDenyPolicySnapshot(ctx context.Context, token *model.Token) string {
	return buildRelayPolicyDenySnapshot(ctx, token, "route_forbidden", "available", map[string]interface{}{
		"api_type":      "allow",
		"model":         "allow",
		"channel_group": "allow",
		"channel_model": "deny",
	})
}

func buildRelayNoAvailableChannelPolicySnapshot(ctx context.Context, token *model.Token, breakerSnapshot map[string]interface{}) string {
	raw := buildRelayPolicyDenySnapshot(ctx, token, "no_available_channel", "available", map[string]interface{}{
		"api_type":        "allow",
		"model":           "allow",
		"channel_group":   "allow",
		"route_candidate": "deny",
	})
	if len(breakerSnapshot) == 0 || strings.TrimSpace(raw) == "" {
		return raw
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return raw
	}
	snapshot["breaker_snapshot"] = breakerSnapshot
	withBreaker, err := json.Marshal(snapshot)
	if err != nil {
		return raw
	}
	return string(withBreaker)
}

func tokenStatusSnapshot(status int) string {
	switch status {
	case common.TokenStatusEnabled:
		return "enabled"
	case common.TokenStatusDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

func userStatusSnapshot(status int) string {
	switch status {
	case common.UserStatusEnabled:
		return "enabled"
	case common.UserStatusDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

func (s *RelayService) buildRelayRouteSnapshot(reqInfo relayRequestInfo, candidates []model.Channel, selected *model.Channel, retryAttempts []map[string]interface{}, filteredReasons map[string]int) string {
	requestedModel := strings.TrimSpace(reqInfo.Model)
	snapshot := map[string]interface{}{
		"schema":          "routerx.snapshot.v1",
		"kind":            "route",
		"stage":           "p1",
		"source":          "relay",
		"redacted":        true,
		"requested_model": requestedModel,
		"candidate_count": len(candidates),
	}
	if preference := routePreferenceSnapshot(reqInfo.Route); len(preference) > 0 {
		snapshot["route_preference"] = preference
	}
	if reasons := compactRouteFilterReasons(filteredReasons); len(reasons) > 0 {
		snapshot["filtered_reasons"] = reasons
	}
	if len(retryAttempts) > 0 {
		snapshot["retry_attempts"] = retryAttempts
	}
	if selected != nil {
		snapshot["selected_channel_id"] = selected.ID
		snapshot["selected_provider"] = channelProviderName(selected.Type)
		snapshot["selected_channel_group"] = strings.TrimSpace(selected.ChannelGroup)
		snapshot["priority"] = selected.Priority
		snapshot["weight"] = selected.Weight
		if s != nil && s.channelService != nil {
			upstreamModel := strings.TrimSpace(s.channelService.ApplyModelRewrite(selected, requestedModel))
			if requestedModel != "" && upstreamModel != "" && upstreamModel != requestedModel {
				snapshot["model_rewrite"] = map[string]interface{}{
					"from": requestedModel,
					"to":   upstreamModel,
				}
			}
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

func addRouteFilterReason(reasons map[string]int, reason string, count int) {
	if reasons == nil || count <= 0 {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	reasons[reason] += count
}

func mergeRouteFilterReasons(target, source map[string]int) {
	for reason, count := range source {
		addRouteFilterReason(target, reason, count)
	}
}

func compactRouteFilterReasons(reasons map[string]int) map[string]int {
	result := map[string]int{}
	for reason, count := range reasons {
		if count <= 0 {
			continue
		}
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		result[reason] = count
	}
	return result
}

func buildRelayRetryAttemptSnapshot(attempt int, channel *model.Channel, err error) map[string]interface{} {
	snapshot := map[string]interface{}{
		"attempt":   attempt,
		"status":    "failed",
		"retryable": true,
	}
	if channel != nil {
		snapshot["channel_id"] = channel.ID
		snapshot["provider"] = channelProviderName(channel.Type)
		snapshot["channel_group"] = strings.TrimSpace(channel.ChannelGroup)
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if code := strings.TrimSpace(httpErr.Code); code != "" {
			snapshot["error_code"] = code
		}
		if status := upstreamStatusFromHTTPError(httpErr); status > 0 {
			snapshot["upstream_status"] = status
		}
	}
	return snapshot
}

func upstreamStatusFromHTTPError(err *HTTPError) int {
	if err == nil {
		return 0
	}
	code := strings.TrimSpace(err.Code)
	if !strings.HasPrefix(code, "upstream_") {
		return 0
	}
	status, parseErr := strconv.Atoi(strings.TrimPrefix(code, "upstream_"))
	if parseErr != nil {
		return 0
	}
	return status
}

func buildRelayBillingSnapshot(usage *relay.Usage, billing relayBillingResult, deduction QuotaDeductionResult) string {
	quotaUsed := billing.QuotaUsed
	usageSource := logUsageSource(usage, common.LogStatusSuccess, quotaUsed)
	payer := "token_and_user"
	if deduction.TokenUnlimited {
		payer = "user"
	}
	expressionSource := strings.TrimSpace(billing.ExpressionSource)
	if expressionSource == "" {
		expressionSource = "p0_usage"
		if usageSource == common.LogUsageSourceMinimum {
			expressionSource = "minimum"
		}
	}
	priceSource := strings.TrimSpace(billing.PriceSource)
	if priceSource == "" {
		priceSource = expressionSource
	}
	expressionSnapshot := billing.ExpressionSnapshot
	if expressionSnapshot == nil {
		expressionSnapshot = buildP0BillingExpressionSnapshot(usage, usageSource, quotaUsed)
	}
	snapshot := map[string]interface{}{
		"schema":                      "routerx.snapshot.v1",
		"kind":                        "billing",
		"stage":                       "p1",
		"source":                      "billing",
		"redacted":                    true,
		"billing_status":              "settled",
		"price_source":                priceSource,
		"billing_expression_source":   expressionSource,
		"billing_expression_snapshot": expressionSnapshot,
		"multiplier_snapshot":         billingMultiplierSnapshot(billing),
		"usage_source":                usageSource,
		"payer":                       payer,
		"final_quota_used":            quotaUsed,
		"deduction_result":            "applied",
		"key_budget_before":           deduction.TokenQuotaBefore,
		"key_budget_after":            deduction.TokenQuotaAfter,
		"user_balance_before":         deduction.UserQuotaBefore,
		"user_balance_after":          deduction.UserQuotaAfter,
	}
	if usage != nil {
		snapshot["prompt_tokens"] = usage.PromptTokens
		snapshot["completion_tokens"] = usage.CompletionTokens
		snapshot["total_tokens"] = usage.TotalTokens
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

func buildRelayBillingFailureSnapshot(usage *relay.Usage, billing relayBillingResult, deduction QuotaDeductionResult, err error) string {
	quotaUsed := billing.QuotaUsed
	usageSource := logUsageSource(usage, common.LogStatusSuccess, quotaUsed)
	payer := "token_and_user"
	if deduction.TokenUnlimited {
		payer = "user"
	}
	expressionSource := strings.TrimSpace(billing.ExpressionSource)
	if expressionSource == "" {
		expressionSource = "p0_usage"
		if usageSource == common.LogUsageSourceMinimum {
			expressionSource = "minimum"
		}
	}
	priceSource := strings.TrimSpace(billing.PriceSource)
	if priceSource == "" {
		priceSource = expressionSource
	}
	expressionSnapshot := billing.ExpressionSnapshot
	if expressionSnapshot == nil {
		expressionSnapshot = buildP0BillingExpressionSnapshot(usage, usageSource, quotaUsed)
	}
	snapshot := map[string]interface{}{
		"schema":                      "routerx.snapshot.v1",
		"kind":                        "billing",
		"stage":                       "p1",
		"source":                      "billing",
		"redacted":                    true,
		"billing_status":              "failed",
		"price_source":                priceSource,
		"billing_expression_source":   expressionSource,
		"billing_expression_snapshot": expressionSnapshot,
		"multiplier_snapshot":         billingMultiplierSnapshot(billing),
		"usage_source":                usageSource,
		"payer":                       payer,
		"attempted_quota_used":        quotaUsed,
		"final_quota_used":            int64(0),
		"deduction_result":            "failed",
		"deduction_error_code":        quotaDeductionErrorCode(err),
		"key_budget_before":           deduction.TokenQuotaBefore,
		"key_budget_after":            deduction.TokenQuotaBefore,
		"user_balance_before":         deduction.UserQuotaBefore,
		"user_balance_after":          deduction.UserQuotaBefore,
	}
	if usage != nil {
		snapshot["prompt_tokens"] = usage.PromptTokens
		snapshot["completion_tokens"] = usage.CompletionTokens
		snapshot["total_tokens"] = usage.TotalTokens
	}
	raw, marshalErr := json.Marshal(snapshot)
	if marshalErr != nil {
		return ""
	}
	return string(raw)
}

func quotaDeductionErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInsufficientUserQuota):
		return "insufficient_user_quota"
	case errors.Is(err, ErrInsufficientTokenQuota):
		return "insufficient_token_quota"
	default:
		return "deduction_failed"
	}
}

func buildP0BillingExpressionSnapshot(usage *relay.Usage, usageSource string, quotaUsed int64) map[string]interface{} {
	if usageSource == common.LogUsageSourceMinimum {
		return map[string]interface{}{
			"source":     "minimum",
			"price_mode": "minimum",
			"expression": "minimum_charge",
			"base_quota": quotaUsed,
			"variables": map[string]interface{}{
				"minimum_quota": quotaUsed,
			},
		}
	}
	variables := map[string]interface{}{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
	if usage != nil {
		variables["prompt_tokens"] = usage.PromptTokens
		variables["completion_tokens"] = usage.CompletionTokens
		variables["total_tokens"] = usage.TotalTokens
	}
	return map[string]interface{}{
		"source":     "p0_usage",
		"price_mode": "token",
		"expression": "total_tokens",
		"base_quota": quotaUsed,
		"variables":  variables,
	}
}

func defaultMultiplierSnapshot() map[string]interface{} {
	return map[string]interface{}{
		"source":                   "p0_default",
		"default_ratio":            1.0,
		"user_group":               "default",
		"user_group_ratio":         1.0,
		"channel_group":            "default",
		"channel_group_ratio":      1.0,
		"user_group_channel_ratio": 1.0,
		"ratio_mode":               "separate_factors",
		"effective_ratio":          1.0,
	}
}

func routePreferenceSnapshot(route RoutePreference) map[string]interface{} {
	preference := map[string]interface{}{}
	if route.ChannelGroup != "" {
		preference["channel_group"] = route.ChannelGroup
	}
	if route.ChannelID != 0 {
		preference["channel_id"] = route.ChannelID
	}
	if route.ChannelName != "" {
		preference["channel_name"] = route.ChannelName
	}
	if route.Provider != "" {
		preference["provider"] = route.Provider
	}
	if len(route.DisabledProvider) > 0 {
		preference["disabled_providers"] = route.DisabledProvider
	}
	return preference
}

func channelProviderName(channelType int) string {
	switch channelType {
	case common.ChannelTypeOpenAI:
		return "openai"
	case common.ChannelTypeAzure:
		return "azure"
	case common.ChannelTypeClaude:
		return "anthropic"
	case common.ChannelTypeGemini:
		return "gemini"
	case common.ChannelTypeQwen:
		return "qwen"
	case common.ChannelTypeDeepSeek:
		return "deepseek"
	case common.ChannelTypeXAI:
		return "xai"
	case common.ChannelTypeRouterX:
		return "routerx"
	case common.ChannelTypeOpenAICompat:
		return "openai-compatible"
	default:
		return "unknown"
	}
}

func orderRelayAttemptCandidates(candidates []model.Channel, selected *model.Channel) []model.Channel {
	if selected == nil || selected.ID == 0 {
		return candidates
	}
	ordered := make([]model.Channel, 0, len(candidates))
	ordered = append(ordered, *selected)
	for _, candidate := range candidates {
		if candidate.ID == selected.ID {
			continue
		}
		ordered = append(ordered, candidate)
	}
	return ordered
}

func forwardOpenAIStream(r io.Reader, write func([]byte) error, flush func()) (*relay.Usage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var usage *relay.Usage
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if parsed := usageFromOpenAIStreamLine(line); parsed != nil {
			usage = parsed
		}
		chunk := append(line, '\n')
		if err := write(chunk); err != nil {
			return usage, err
		}
		if len(bytes.TrimSpace(line)) == 0 && flush != nil {
			flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	if flush != nil {
		flush()
	}
	return usage, nil
}

func usageFromOpenAIStreamLine(line []byte) *relay.Usage {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return nil
	}
	var envelope struct {
		Usage *relay.Usage `json:"usage"`
	}
	_ = json.Unmarshal(payload, &envelope)
	return envelope.Usage
}

func supportsOpenAICompatibleStream(channelType int) bool {
	switch channelType {
	case common.ChannelTypeOpenAI,
		common.ChannelTypeOpenAICompat,
		common.ChannelTypeXAI,
		common.ChannelTypeQwen,
		common.ChannelTypeDeepSeek,
		common.ChannelTypeRouterX:
		return true
	default:
		return false
	}
}

func clientStatusFromUpstream(status int) int {
	switch {
	case status == 400:
		return 400
	case status == 429:
		return 429
	default:
		return 502
	}
}

func upstreamErrorType(status int) string {
	if status == 400 {
		return "invalid_request_error"
	}
	return "upstream_error"
}

func (s *RelayService) recordLog(ctx context.Context, token *model.Token, channel *model.Channel, modelName string, usage *relay.Usage, status int, quotaUsed int64, errMsg, ip string) error {
	if s.logService == nil || token == nil {
		return nil
	}
	var tokenID *uint
	if token.ID > 0 {
		id := token.ID
		tokenID = &id
	}
	var channelID *uint
	if channel != nil && channel.ID > 0 {
		id := channel.ID
		channelID = &id
	}
	log := &model.Log{
		UserID:          token.UserID,
		TokenID:         tokenID,
		ChannelID:       channelID,
		Model:           modelName,
		Status:          status,
		QuotaUsed:       quotaUsed,
		UsageSource:     logUsageSource(usage, status, quotaUsed),
		ErrorMsg:        errMsg,
		IP:              ip,
		UserAgent:       relayUserAgentFromContext(ctx),
		RequestID:       relayRequestIDFromContext(ctx),
		RequestSnapshot: relayRequestSnapshotFromContext(ctx),
		PolicySnapshot:  relayPolicySnapshotFromContext(ctx),
		RouteSnapshot:   relayRouteSnapshotFromContext(ctx),
		BillingSnapshot: relayBillingSnapshotFromContext(ctx),
	}
	if usage != nil {
		log.PromptTokens = usage.PromptTokens
		log.CompletionTokens = usage.CompletionTokens
		log.TotalTokens = usage.TotalTokens
	}
	return s.logService.Record(log)
}

func (s *RelayService) markChannelFailure(channel *model.Channel, responseMs int) error {
	if channel == nil {
		return nil
	}
	err := internal.DB.Model(channel).Updates(map[string]interface{}{
		"response_ms": responseMs,
		"error_count": gorm.Expr("error_count + ?", 1),
	}).Error
	if err == nil && s.channelService != nil {
		s.channelService.InvalidateCandidateCache()
	}
	return err
}

func (s *RelayService) markChannelSuccess(channel *model.Channel, responseMs int) error {
	if channel == nil {
		return nil
	}
	return internal.DB.Model(channel).Updates(map[string]interface{}{
		"response_ms": responseMs,
		"error_count": 0,
	}).Error
}
