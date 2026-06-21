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
	"path/filepath"
	"regexp"
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
	ContentType    string
	outputProtocol string
	forward        func(write func([]byte) error, flush func()) (*relay.Usage, error)
}

type RelayRawResult struct {
	Body        []byte
	ContentType string
	Usage       *relay.Usage
}

type relayUserAgentContextKey struct{}
type relayRequestIDContextKey struct{}
type relayIngressProtocolContextKey struct{}
type relayRouterXHopContextKey struct{}
type relayRouterXChainContextKey struct{}
type relayRequestSnapshotContextKey struct{}
type relayAdapterDegradationsContextKey struct{}
type relayAnthropicNativeBodyContextKey struct{}
type relayGeminiNativeBodyContextKey struct{}
type relayGeminiNativeEmbeddingContextKey struct{}
type relayPolicySnapshotContextKey struct{}
type relayRouteSnapshotContextKey struct{}
type relayBillingSnapshotContextKey struct{}
type relayLogRequestBodyContextKey struct{}
type relayLogResponseBodyContextKey struct{}

type relayAdapterDegradation struct {
	Protocol string `json:"protocol"`
	Field    string `json:"field"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
}

type contentTypeRelayAdapter interface {
	DoRequestWithContentType(ctx context.Context, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error)
}

const (
	defaultRouterXMaxHops          = 3
	relaySnapshotHashPrefixLength  = 16
	relaySnapshotTokenPrefixLength = 12
	relaySnapshotUserAgentMaxRunes = 128

	inputProtocolAnthropic = "anthropic"
	inputProtocolGemini    = "gemini"

	relayGeminiNativeEmbedContentKind       = "embed_content"
	relayGeminiNativeBatchEmbedContentsKind = "batch_embed_contents"
	usageMissingStrategyMinimum             = "minimum"
	usageMissingStrategyReject              = "reject"

	defaultRelayMaxRequestBodyBytes   int64 = 20 << 20
	defaultRelayMaxMultipartFileBytes int64 = 10 << 20
	defaultRelayMaxResponseBodyBytes  int64 = 20 << 20
)

var errRelayResponseBodyTooLarge = errors.New("relay upstream response body too large")

var relayBodyLogRedactors = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern:     regexp.MustCompile(`(?i)("?(?:api[_-]?key|authorization|cookie|set-cookie|token|secret|upstream_key)"?\s*:\s*")[^"]*(")`),
		replacement: `$1[REDACTED]$2`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`),
		replacement: `Bearer [REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`sk-[A-Za-z0-9._-]+`),
		replacement: `[REDACTED]`,
	},
}

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

func contextWithRelayIngressProtocol(ctx context.Context, protocol string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		return ctx
	}
	return context.WithValue(ctx, relayIngressProtocolContextKey{}, protocol)
}

func contextWithRelayAdapterDegradations(ctx context.Context, degradations []relayAdapterDegradation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(degradations) == 0 {
		return ctx
	}
	return context.WithValue(ctx, relayAdapterDegradationsContextKey{}, append([]relayAdapterDegradation(nil), degradations...))
}

func contextWithRelayAnthropicNativeBody(ctx context.Context, body []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return ctx
	}
	return context.WithValue(ctx, relayAnthropicNativeBodyContextKey{}, append([]byte(nil), body...))
}

func contextWithRelayGeminiNativeBody(ctx context.Context, body []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return ctx
	}
	return context.WithValue(ctx, relayGeminiNativeBodyContextKey{}, append([]byte(nil), body...))
}

func contextWithRelayGeminiNativeEmbedding(ctx context.Context, kind string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ctx
	}
	return context.WithValue(ctx, relayGeminiNativeEmbeddingContextKey{}, kind)
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

func ContextWithRelayLogRequestBody(ctx context.Context, body string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayLogRequestBodyContextKey{}, strings.TrimSpace(body))
}

func ContextWithRelayLogResponseBody(ctx context.Context, body string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, relayLogResponseBodyContextKey{}, strings.TrimSpace(body))
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

func relayIngressProtocolFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayIngressProtocolContextKey{}).(string)
	return strings.TrimSpace(value)
}

func relayAdapterDegradationsFromContext(ctx context.Context) []relayAdapterDegradation {
	if ctx == nil {
		return nil
	}
	values, _ := ctx.Value(relayAdapterDegradationsContextKey{}).([]relayAdapterDegradation)
	if len(values) == 0 {
		return nil
	}
	return append([]relayAdapterDegradation(nil), values...)
}

func relayAnthropicNativeBodyFromContext(ctx context.Context) []byte {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(relayAnthropicNativeBodyContextKey{}).([]byte)
	if len(value) == 0 {
		return nil
	}
	return append([]byte(nil), value...)
}

func relayGeminiNativeBodyFromContext(ctx context.Context) []byte {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(relayGeminiNativeBodyContextKey{}).([]byte)
	if len(value) == 0 {
		return nil
	}
	return append([]byte(nil), value...)
}

func relayGeminiNativeEmbeddingFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayGeminiNativeEmbeddingContextKey{}).(string)
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

func relayLogRequestBodyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayLogRequestBodyContextKey{}).(string)
	return strings.TrimSpace(value)
}

func relayLogResponseBodyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(relayLogResponseBodyContextKey{}).(string)
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
	errInvalidJSONBody        = errors.New("invalid json body")
	errInvalidMultipartBody   = errors.New("invalid multipart body")
	errMultipartFileTooLarge  = errors.New("multipart file exceeds maximum size")
	errMultipartFileRequired  = errors.New("multipart file field is required")
	errUnsafeMultipartFile    = errors.New("multipart file is not allowed")
	errModelRequired          = errors.New("model is required")
	errUnsupportedMultipart   = errors.New("multipart relay is not supported for selected upstream channel")
	errInvalidChatMessages    = errors.New("chat messages must be a non-empty array")
	errInvalidGeminiEmbedding = errors.New("invalid gemini embedding request")
	errInvalidEmbeddingInput  = errors.New("embeddings input must be a non-empty string, string array, token array, or token array batch")
	errEmbeddingBatchTooLarge = errors.New("embeddings input batch exceeds maximum size")
	errInvalidImagePrompt     = errors.New("image generation prompt must be a non-empty string")
	errInvalidImageCount      = errors.New("image generation count must be an integer greater than or equal to 1")
	errInvalidImageSize       = errors.New("image size must be auto or WIDTHxHEIGHT within configured bounds")
	errInvalidAudioFormat     = errors.New("audio response_format is not supported for this API")
	errInvalidAudioInput      = errors.New("audio speech input must be a non-empty string within configured bounds")
	errInvalidAudioVoice      = errors.New("audio speech voice must be a non-empty string")
	errInvalidModerationInput = errors.New("moderations input must be a non-empty string or string array")
)

const maxEmbeddingBatchSize = 2048
const maxImageGenerationDimension = 4096
const maxImageGenerationPixels = 4194304
const maxAudioSpeechInputRunes = 4096

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

func upstreamConversionHTTPError() *HTTPError {
	return &HTTPError{
		Status:  http.StatusBadGateway,
		Message: "upstream response conversion failed",
		Type:    "upstream_error",
		Code:    "upstream_conversion_failed",
	}
}

func relayInvalidRequestHTTPError(err error) *HTTPError {
	status := http.StatusBadRequest
	if errors.Is(err, errMultipartFileTooLarge) {
		status = http.StatusRequestEntityTooLarge
	}
	return &HTTPError{
		Status:  status,
		Message: err.Error(),
		Type:    "invalid_request_error",
		Code:    relayRequestErrorCode(err),
	}
}

func relayUnsupportedAPITypeHTTPError() *HTTPError {
	return &HTTPError{
		Status:  http.StatusBadGateway,
		Message: "selected upstream channel does not support this api type",
		Type:    "upstream_error",
		Code:    "unsupported_api_type",
	}
}

func isUnsupportedAPITypeError(err error) bool {
	return err != nil && strings.EqualFold(strings.TrimSpace(err.Error()), "unsupported api type")
}

func (s *RelayService) RelayAnthropicMessages(ctx context.Context, token *model.Token, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	canonical, err := anthropicMessagesToOpenAI(body)
	if err != nil {
		return nil, nil, relayInvalidRequestHTTPError(err)
	}
	ctx = contextWithRelayIngressProtocol(ctx, inputProtocolAnthropic)
	ctx = contextWithRelayAnthropicNativeBody(ctx, body)
	ctx = contextWithRelayAdapterDegradations(ctx, anthropicAdapterDegradations(body))
	resp, usage, err := s.Relay(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIChatToAnthropic(resp)
	if err != nil {
		return nil, usage, upstreamConversionHTTPError()
	}
	return converted, usage, nil
}

func (s *RelayService) RelayAnthropicMessagesStream(ctx context.Context, token *model.Token, body []byte, clientIP string) (*RelayStreamResult, error) {
	canonical, err := anthropicMessagesToOpenAI(body)
	if err != nil {
		return nil, relayInvalidRequestHTTPError(err)
	}
	ctx = contextWithRelayIngressProtocol(ctx, inputProtocolAnthropic)
	ctx = contextWithRelayAnthropicNativeBody(ctx, body)
	ctx = contextWithRelayAdapterDegradations(ctx, anthropicAdapterDegradations(body))
	result, err := s.RelayStream(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, err
	}
	if result.outputProtocol == inputProtocolAnthropic {
		return result, nil
	}
	state := &anthropicStreamState{}
	return &RelayStreamResult{
		ContentType:    "text/event-stream",
		outputProtocol: inputProtocolAnthropic,
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
		return nil, nil, relayInvalidRequestHTTPError(err)
	}
	ctx = contextWithRelayIngressProtocol(ctx, inputProtocolGemini)
	ctx = contextWithRelayGeminiNativeBody(ctx, body)
	ctx = contextWithRelayAdapterDegradations(ctx, geminiGenerateAdapterDegradations(body))
	resp, usage, err := s.Relay(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIChatToGemini(resp)
	if err != nil {
		return nil, usage, upstreamConversionHTTPError()
	}
	return converted, usage, nil
}

func (s *RelayService) RelayGeminiEmbedContent(ctx context.Context, token *model.Token, modelName string, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	canonical, err := geminiEmbedContentToOpenAI(modelName, body)
	if err != nil {
		return nil, nil, relayInvalidRequestHTTPError(err)
	}
	ctx = contextWithRelayIngressProtocol(ctx, inputProtocolGemini)
	ctx = contextWithRelayGeminiNativeBody(ctx, body)
	ctx = contextWithRelayGeminiNativeEmbedding(ctx, relayGeminiNativeEmbedContentKind)
	ctx = contextWithRelayAdapterDegradations(ctx, geminiEmbedContentAdapterDegradations(body))
	resp, usage, err := s.Relay(ctx, token, relay.APIEmbeddings, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIEmbeddingsToGemini(resp)
	if err != nil {
		return nil, usage, upstreamConversionHTTPError()
	}
	return converted, usage, nil
}

func (s *RelayService) RelayGeminiBatchEmbedContents(ctx context.Context, token *model.Token, modelName string, body []byte, clientIP string) ([]byte, *relay.Usage, error) {
	canonical, requestCount, err := geminiBatchEmbedContentsToOpenAI(modelName, body)
	if err != nil {
		return nil, nil, relayInvalidRequestHTTPError(err)
	}
	ctx = contextWithRelayIngressProtocol(ctx, inputProtocolGemini)
	ctx = contextWithRelayGeminiNativeBody(ctx, body)
	ctx = contextWithRelayGeminiNativeEmbedding(ctx, relayGeminiNativeBatchEmbedContentsKind)
	ctx = contextWithRelayAdapterDegradations(ctx, geminiBatchEmbedContentsAdapterDegradations(body))
	resp, usage, err := s.Relay(ctx, token, relay.APIEmbeddings, canonical, clientIP)
	if err != nil {
		return nil, usage, err
	}
	converted, err := openAIEmbeddingsToGeminiBatch(resp, requestCount)
	if err != nil {
		return nil, usage, upstreamConversionHTTPError()
	}
	return converted, usage, nil
}

func (s *RelayService) RelayGeminiGenerateContentStream(ctx context.Context, token *model.Token, modelName string, body []byte, clientIP string) (*RelayStreamResult, error) {
	canonical, err := geminiGenerateToOpenAI(modelName, body, true)
	if err != nil {
		return nil, relayInvalidRequestHTTPError(err)
	}
	ctx = contextWithRelayIngressProtocol(ctx, inputProtocolGemini)
	ctx = contextWithRelayGeminiNativeBody(ctx, body)
	ctx = contextWithRelayAdapterDegradations(ctx, geminiGenerateAdapterDegradations(body))
	result, err := s.RelayStream(ctx, token, relay.APIChatCompletions, canonical, clientIP)
	if err != nil {
		return nil, err
	}
	if result.outputProtocol == inputProtocolGemini {
		return result, nil
	}
	return &RelayStreamResult{
		ContentType:    "text/event-stream",
		outputProtocol: inputProtocolGemini,
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
	inputTokens, err := anthropicCountTokensFromBody(body)
	if err != nil {
		return nil, relayInvalidRequestHTTPError(err)
	}
	return json.Marshal(map[string]interface{}{
		"input_tokens": inputTokens,
	})
}

func (s *RelayService) GeminiCountTokens(body []byte) ([]byte, error) {
	totalTokens, err := geminiCountTokensFromBody(body)
	if err != nil {
		return nil, relayInvalidRequestHTTPError(err)
	}
	return json.Marshal(map[string]interface{}{
		"totalTokens": totalTokens,
	})
}

type anthropicCountTokenMessage struct {
	Content json.RawMessage `json:"content"`
}

func anthropicCountTokensFromBody(body []byte) (int, error) {
	var input struct {
		System   json.RawMessage              `json:"system"`
		Messages []anthropicCountTokenMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return 0, errInvalidJSONBody
	}
	text := anthropicCountTokenText(input.System, input.Messages)
	if strings.TrimSpace(text) == "" {
		return approximateTokenCount(body), nil
	}
	return approximateTokenCount([]byte(text)), nil
}

func anthropicCountTokenText(system json.RawMessage, messages []anthropicCountTokenMessage) string {
	values := make([]string, 0, len(messages)+1)
	if text := strings.TrimSpace(relay.TextFromContent(system)); text != "" {
		values = append(values, text)
	}
	for _, message := range messages {
		if text := strings.TrimSpace(relay.TextFromContent(message.Content)); text != "" {
			values = append(values, text)
		}
	}
	return strings.Join(values, "\n")
}

type geminiCountTokenContent struct {
	Parts []json.RawMessage `json:"parts"`
}

type geminiCountTokenGenerateRequest struct {
	Contents          []geminiCountTokenContent `json:"contents"`
	SystemInstruction *geminiCountTokenContent  `json:"systemInstruction"`
}

func geminiCountTokensFromBody(body []byte) (int, error) {
	var input struct {
		Contents               []geminiCountTokenContent        `json:"contents"`
		SystemInstruction      *geminiCountTokenContent         `json:"systemInstruction"`
		GenerateContentRequest *geminiCountTokenGenerateRequest `json:"generateContentRequest"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return 0, errInvalidJSONBody
	}

	contents := input.Contents
	systemInstruction := input.SystemInstruction
	fallbackBody := body
	if input.GenerateContentRequest != nil {
		// Gemini ignores top-level contents when generateContentRequest is set.
		contents = input.GenerateContentRequest.Contents
		systemInstruction = input.GenerateContentRequest.SystemInstruction
		if nestedBody, err := json.Marshal(input.GenerateContentRequest); err == nil {
			fallbackBody = nestedBody
		}
	}

	text := geminiCountTokenText(contents, systemInstruction)
	if strings.TrimSpace(text) == "" {
		return approximateTokenCount(fallbackBody), nil
	}
	return approximateTokenCount([]byte(text)), nil
}

func geminiCountTokenText(contents []geminiCountTokenContent, systemInstruction *geminiCountTokenContent) string {
	values := make([]string, 0, len(contents)+1)
	if systemInstruction != nil {
		if text := strings.TrimSpace(geminiTextFromParts(systemInstruction.Parts)); text != "" {
			values = append(values, text)
		}
	}
	for _, content := range contents {
		if text := strings.TrimSpace(geminiTextFromParts(content.Parts)); text != "" {
			values = append(values, text)
		}
	}
	return strings.Join(values, "\n")
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
	reqInfo, err := parseRelayRequestWithContentType(apiType, body, contentType, s.MaxMultipartFileBytes())
	if err != nil {
		return nil, nil, relayInvalidRequestHTTPError(err)
	}
	if reqInfo.Stream {
		return nil, nil, &HTTPError{Status: 400, Message: "stream is not supported in P0 relay", Type: "invalid_request_error", Code: "unsupported_stream"}
	}
	ctx = s.contextWithRelayLogRequestBody(ctx, body)
	ctx = ContextWithRelayRequestSnapshot(ctx, buildRelayRequestSnapshot(ctx, token, clientIP, apiType, reqInfo))
	if err := s.enforceTokenScope(ctx, token, apiType, reqInfo.Model, clientIP); err != nil {
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
	candidates, selectionFacts, err := s.channelService.SelectChannelCandidatesWithRouteDetailedFacts(reqInfo.Model, RoutePreference{})
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
		attemptCtx = ContextWithRelayRequestSnapshot(attemptCtx, buildRelayRequestSnapshotForChannel(attemptCtx, token, clientIP, apiType, reqInfo, &channel))
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
	ctx = ContextWithRelayRouteSnapshot(ctx, addRelayRouteUpstreamTargetSnapshot(relayRouteSnapshotFromContext(ctx), target))
	upstreamModel := s.channelService.ApplyModelRewrite(channel, reqInfo.Model)
	outBody := body
	outContentType := ""
	if isMultipartRelayContentType(contentType) {
		outBody, outContentType, err = rewriteMultipartRelayBody(apiType, body, contentType, upstreamModel, s.MaxMultipartFileBytes())
		if err != nil {
			return nil, nil, false, relayInvalidRequestHTTPError(err)
		}
	} else {
		outInputBody, err := relayUpstreamInputBody(ctx, apiType, body, channel.Type, upstreamModel)
		if err != nil {
			return nil, nil, false, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
		}
		convertAPIType := relayRequestConvertAPIType(ctx, apiType, channel.Type)
		outBody, err = adapter.ConvertRequest(convertAPIType, outInputBody)
		if err != nil {
			if isUnsupportedAPITypeError(err) {
				_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "unsupported api type", clientIP)
				return nil, nil, false, relayUnsupportedAPITypeHTTPError()
			}
			return nil, nil, false, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
		}
	}
	routerXHop, forwardRouterXHop, err := s.nextRouterXHop(ctx, channel)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, nil, false, routerXHopHTTPError(err)
	}
	upstreamAPIType := relayNonStreamUpstreamAPIType(ctx, apiType, channel.Type)
	endpoint := adapter.GetAPIEndpoint(upstreamAPIType, upstreamModel)
	timeout := s.relayTimeout()
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
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
		logCtx := s.contextWithRelayLogResponseBody(ctx, respBody, resp.Header.Get("Content-Type"))
		_ = s.recordLog(logCtx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, message, clientIP)
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
		logCtx = s.contextWithRelayLogResponseBody(logCtx, respBody, contentType)
		_ = s.recordLog(logCtx, token, channel, reqInfo.Model, nil, common.LogStatusSuccess, billing.QuotaUsed, "", clientIP)
		return &RelayRawResult{Body: respBody, ContentType: contentType}, nil, false, nil
	}

	responseAPIType := relayNonStreamResponseAPIType(ctx, apiType, channel.Type)
	converted, usage, err := adapter.ConvertResponse(responseAPIType, respBody)
	if err != nil {
		_ = s.markChannelFailure(channel, latencyMs)
		logCtx := s.contextWithRelayLogResponseBody(ctx, respBody, resp.Header.Get("Content-Type"))
		_ = s.recordLog(logCtx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream response conversion failed", clientIP)
		return nil, nil, false, &HTTPError{Status: 502, Message: "upstream response conversion failed", Type: "upstream_error", Code: "upstream_conversion_failed"}
	}
	ctx = s.contextWithRelayLogResponseBody(ctx, converted, "application/json")
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
	reqInfo, err := parseRelayRequest(apiType, body)
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: relayRequestErrorCode(err)}
	}
	if !reqInfo.Stream {
		return nil, &HTTPError{Status: 400, Message: "stream is required", Type: "invalid_request_error", Code: "stream_required"}
	}
	ctx = s.contextWithRelayLogRequestBody(ctx, body)
	ctx = ContextWithRelayRequestSnapshot(ctx, buildRelayRequestSnapshot(ctx, token, clientIP, apiType, reqInfo))
	if err := s.enforceTokenScope(ctx, token, apiType, reqInfo.Model, clientIP); err != nil {
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
	candidates, selectionFacts, err := s.channelService.SelectChannelCandidatesWithRouteDetailedFacts(reqInfo.Model, RoutePreference{})
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
	ctx = ContextWithRelayRequestSnapshot(ctx, buildRelayRequestSnapshotForChannel(ctx, token, clientIP, apiType, reqInfo, channel))
	if err := s.enforceChannelRateLimit(ctx, token, channel, reqInfo.Model, clientIP); err != nil {
		return nil, err
	}
	nativeGeminiStream := supportsGeminiNativeStream(ctx, apiType, channel.Type)
	nativeAnthropicStream := supportsAnthropicNativeStream(ctx, apiType, channel.Type)
	if !supportsOpenAICompatibleStream(channel.Type) && !nativeGeminiStream && !nativeAnthropicStream {
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
	ctx = ContextWithRelayRouteSnapshot(ctx, addRelayRouteUpstreamTargetSnapshot(relayRouteSnapshotFromContext(ctx), target))
	upstreamModel := s.channelService.ApplyModelRewrite(channel, reqInfo.Model)
	outInputBody, err := relayUpstreamInputBody(ctx, apiType, body, channel.Type, upstreamModel)
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
	}
	convertAPIType := relayRequestConvertAPIType(ctx, apiType, channel.Type)
	outBody, err := adapter.ConvertRequest(convertAPIType, outInputBody)
	if err != nil {
		if isUnsupportedAPITypeError(err) {
			_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "unsupported api type", clientIP)
			return nil, relayUnsupportedAPITypeHTTPError()
		}
		return nil, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
	}
	routerXHop, forwardRouterXHop, err := s.nextRouterXHop(ctx, channel)
	if err != nil {
		_ = s.recordLog(ctx, token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, routerXHopHTTPError(err)
	}

	upstreamAPIType := relayStreamUpstreamAPIType(ctx, apiType, channel.Type)
	endpoint := adapter.GetAPIEndpoint(upstreamAPIType, upstreamModel)
	reqCtx, cancel := context.WithTimeout(ctx, s.relayTimeout())
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
		ContentType:    contentType,
		outputProtocol: relayStreamOutputProtocol(ctx, apiType, channel.Type),
		forward: func(write func([]byte) error, flush func()) (*relay.Usage, error) {
			defer func() {
				s.recordRelayDuration(apiType, channel, time.Since(relayStart))
			}()
			defer cancel()
			defer resp.Body.Close()
			var usage *relay.Usage
			var err error
			switch {
			case nativeGeminiStream:
				usage, err = forwardGeminiStream(resp.Body, write, flush)
			case nativeAnthropicStream:
				usage, err = forwardAnthropicStream(resp.Body, write, flush)
			default:
				usage, err = forwardOpenAIStream(resp.Body, write, flush)
			}
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

func (s *RelayService) GeminiModelDetail(modelName string) ([]byte, error) {
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, errors.New("model is required")
	}
	models, err := s.channelService.ListModels()
	if err != nil {
		return nil, err
	}
	for _, candidate := range models {
		if candidate == modelName {
			return json.Marshal(geminiModelInfo(candidate))
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
		data = append(data, geminiModelInfo(modelName))
	}
	return json.Marshal(map[string]interface{}{"models": data})
}

func geminiModelInfo(modelName string) map[string]interface{} {
	// RouterX 的 Gemini 外形会把生成、计数和 Embeddings 都落到同一模型名上。
	return map[string]interface{}{
		"name":                       "models/" + modelName,
		"version":                    "",
		"displayName":                modelName,
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens", "embedContent", "batchEmbedContents"},
	}
}

func (s *RelayService) AnthropicModelDetail(modelName string) ([]byte, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, errors.New("model is required")
	}
	models, err := s.channelService.ListModels()
	if err != nil {
		return nil, err
	}
	for _, candidate := range models {
		if candidate == modelName {
			return json.Marshal(anthropicModelInfo(candidate))
		}
	}
	return nil, &HTTPError{Status: 404, Message: "model not found", Type: "invalid_request_error", Code: "model_not_found"}
}

func (s *RelayService) ListAnthropicModels() ([]byte, error) {
	models, err := s.channelService.ListModels()
	if err != nil {
		return nil, err
	}
	data := make([]map[string]interface{}, 0, len(models))
	for _, modelName := range models {
		data = append(data, anthropicModelInfo(modelName))
	}
	return json.Marshal(map[string]interface{}{"data": data, "has_more": false})
}

func anthropicModelInfo(modelName string) map[string]interface{} {
	return map[string]interface{}{
		"id":           modelName,
		"type":         "model",
		"display_name": modelName,
	}
}

type relayRequestInfo struct {
	Model  string
	Stream bool
}

func parseRelayRequestWithContentType(apiType relay.APIType, body []byte, contentType string, maxMultipartFileBytes int64) (relayRequestInfo, error) {
	if isMultipartRelayContentType(contentType) {
		return parseMultipartRelayRequest(apiType, body, contentType, maxMultipartFileBytes)
	}
	return parseRelayRequest(apiType, body)
}

func parseRelayRequest(apiType relay.APIType, body []byte) (relayRequestInfo, error) {
	if apiType == relay.APIModels {
		return relayRequestInfo{}, nil
	}
	var payload struct {
		Model    string          `json:"model"`
		Stream   bool            `json:"stream"`
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
		Prompt   json.RawMessage `json:"prompt"`
		N        json.RawMessage `json:"n"`
		Size     json.RawMessage `json:"size"`
		Voice    json.RawMessage `json:"voice"`
		Format   json.RawMessage `json:"response_format"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return relayRequestInfo{}, errInvalidJSONBody
	}
	payload.Model = strings.TrimSpace(payload.Model)
	if payload.Model == "" {
		return relayRequestInfo{}, errModelRequired
	}
	if apiType == relay.APIChatCompletions {
		if err := validateChatMessages(payload.Messages); err != nil {
			return relayRequestInfo{}, err
		}
	}
	if apiType == relay.APIEmbeddings {
		if err := validateEmbeddingInput(payload.Input); err != nil {
			return relayRequestInfo{}, err
		}
	}
	if apiType == relay.APIImagesGenerations {
		if err := validateImageGenerationPrompt(payload.Prompt); err != nil {
			return relayRequestInfo{}, err
		}
		if err := validateImageGenerationCount(payload.N); err != nil {
			return relayRequestInfo{}, err
		}
		if err := validateImageGenerationSize(payload.Size); err != nil {
			return relayRequestInfo{}, err
		}
	}
	if apiType == relay.APIAudioSpeech {
		if err := validateAudioSpeechInput(payload.Input); err != nil {
			return relayRequestInfo{}, err
		}
		if err := validateAudioSpeechVoice(payload.Voice); err != nil {
			return relayRequestInfo{}, err
		}
		if err := validateAudioSpeechResponseFormat(payload.Format); err != nil {
			return relayRequestInfo{}, err
		}
	}
	if apiType == relay.APIModerations {
		if err := validateModerationInput(payload.Input); err != nil {
			return relayRequestInfo{}, err
		}
	}
	return relayRequestInfo{Model: payload.Model, Stream: payload.Stream}, nil
}

// OpenAI-compatible Chat 的 messages 是对话主体；消息内部结构由适配器继续处理。
func validateChatMessages(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return errInvalidChatMessages
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return errInvalidChatMessages
	}
	if len(messages) == 0 {
		return errInvalidChatMessages
	}
	return nil
}

// Image Generations 的 prompt 是生成图片的必要文本输入，先在本地挡住空值和非字符串。
func validateImageGenerationPrompt(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return errInvalidImagePrompt
	}
	var prompt string
	if err := json.Unmarshal(raw, &prompt); err != nil {
		return errInvalidImagePrompt
	}
	if strings.TrimSpace(prompt) == "" {
		return errInvalidImagePrompt
	}
	return nil
}

// Image Generations 的 n 是可选数量字段；显式传入时只接受正整数。
func validateImageGenerationCount(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if isJSONNull(raw) || strings.ContainsAny(string(raw), ".eE") {
		return errInvalidImageCount
	}
	count, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || count < 1 {
		return errInvalidImageCount
	}
	return nil
}

// Image Generations 在本地挡住明显异常的尺寸，避免无效请求进入上游和计费链路。
func validateImageGenerationSize(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	var size string
	if err := json.Unmarshal(raw, &size); err != nil {
		return errInvalidImageSize
	}
	size = strings.ToLower(strings.TrimSpace(size))
	return validateImageSizeValue(size)
}

func validateImageSizeValue(size string) error {
	if size == "" || size == "auto" {
		return nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return errInvalidImageSize
	}
	width, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return errInvalidImageSize
	}
	height, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return errInvalidImageSize
	}
	if width <= 0 || height <= 0 || width > maxImageGenerationDimension || height > maxImageGenerationDimension {
		return errInvalidImageSize
	}
	if int64(width)*int64(height) > maxImageGenerationPixels {
		return errInvalidImageSize
	}
	return nil
}

// Audio Speech 只允许 OpenAI 兼容的音频容器格式，避免明显无效的格式请求进入上游。
func validateAudioSpeechResponseFormat(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	var format string
	if err := json.Unmarshal(raw, &format); err != nil {
		return errInvalidAudioFormat
	}
	if format == "" {
		return nil
	}
	switch format {
	case "mp3", "opus", "aac", "flac", "wav", "pcm":
		return nil
	default:
		return errInvalidAudioFormat
	}
}

// Audio Speech 的文本和 voice 是生成音频的必要输入，先在本地挡住空值和异常长文本。
func validateAudioSpeechInput(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return errInvalidAudioInput
	}
	var input string
	if err := json.Unmarshal(raw, &input); err != nil {
		return errInvalidAudioInput
	}
	if strings.TrimSpace(input) == "" || len([]rune(input)) > maxAudioSpeechInputRunes {
		return errInvalidAudioInput
	}
	return nil
}

func validateAudioSpeechVoice(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return errInvalidAudioVoice
	}
	var voice string
	if err := json.Unmarshal(raw, &voice); err != nil {
		return errInvalidAudioVoice
	}
	if strings.TrimSpace(voice) == "" {
		return errInvalidAudioVoice
	}
	return nil
}

// Moderations 只接受 OpenAI 兼容的文本输入形态，避免空审核请求进入上游和计费链路。
func validateModerationInput(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return errInvalidModerationInput
	}
	switch raw[0] {
	case '"':
		var input string
		if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input) == "" {
			return errInvalidModerationInput
		}
		return nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
			return errInvalidModerationInput
		}
		for _, item := range items {
			var input string
			if err := json.Unmarshal(item, &input); err != nil || strings.TrimSpace(input) == "" {
				return errInvalidModerationInput
			}
		}
		return nil
	default:
		return errInvalidModerationInput
	}
}

// Embeddings 在本地验证 OpenAI 支持的 input 形态，避免无效批量请求进入会计费的上游链路。
func validateEmbeddingInput(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isJSONNull(raw) {
		return errInvalidEmbeddingInput
	}
	switch raw[0] {
	case '"':
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
			return errInvalidEmbeddingInput
		}
		return nil
	case '[':
		return validateEmbeddingInputArray(raw)
	default:
		return errInvalidEmbeddingInput
	}
}

func validateEmbeddingInputArray(raw json.RawMessage) error {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return errInvalidEmbeddingInput
	}
	firstKind := embeddingInputItemKind(items[0])
	if firstKind == "" {
		return errInvalidEmbeddingInput
	}
	for _, item := range items {
		if embeddingInputItemKind(item) != firstKind {
			return errInvalidEmbeddingInput
		}
	}
	switch firstKind {
	case "string":
		if len(items) > maxEmbeddingBatchSize {
			return errEmbeddingBatchTooLarge
		}
		for _, item := range items {
			var value string
			if err := json.Unmarshal(item, &value); err != nil || strings.TrimSpace(value) == "" {
				return errInvalidEmbeddingInput
			}
		}
	case "token":
		for _, item := range items {
			if !validEmbeddingTokenID(item) {
				return errInvalidEmbeddingInput
			}
		}
	case "token_batch":
		if len(items) > maxEmbeddingBatchSize {
			return errEmbeddingBatchTooLarge
		}
		for _, item := range items {
			if err := validateEmbeddingTokenArray(item); err != nil {
				return err
			}
		}
	default:
		return errInvalidEmbeddingInput
	}
	return nil
}

func embeddingInputItemKind(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		return "string"
	case '[':
		return "token_batch"
	default:
		if validEmbeddingTokenID(raw) {
			return "token"
		}
		return ""
	}
}

func validateEmbeddingTokenArray(raw json.RawMessage) error {
	var tokens []json.RawMessage
	if err := json.Unmarshal(raw, &tokens); err != nil || len(tokens) == 0 {
		return errInvalidEmbeddingInput
	}
	for _, token := range tokens {
		if !validEmbeddingTokenID(token) {
			return errInvalidEmbeddingInput
		}
	}
	return nil
}

func validEmbeddingTokenID(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, ".eE") {
		return false
	}
	tokenID, err := strconv.ParseInt(value, 10, 64)
	return err == nil && tokenID >= 0
}

func parseMultipartRelayRequest(apiType relay.APIType, body []byte, contentType string, maxFileBytes int64) (relayRequestInfo, error) {
	boundary, err := multipartBoundary(contentType)
	if err != nil {
		return relayRequestInfo{}, err
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	info := relayRequestInfo{}
	requiredFileField := requiredMultipartFileField(apiType)
	hasRequiredFileField := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return relayRequestInfo{}, errInvalidMultipartBody
		}
		name := part.FormName()
		if part.FileName() != "" {
			if name == requiredFileField {
				hasRequiredFileField = true
			}
			if err := validateMultipartFile(apiType, part); err != nil {
				return relayRequestInfo{}, err
			}
			if err := discardMultipartFileWithLimit(part, maxFileBytes, apiType, part.FileName()); err != nil {
				return relayRequestInfo{}, err
			}
			continue
		}
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
		case "size":
			if isImageMultipartAPIType(apiType) {
				if err := validateImageMultipartSize(raw); err != nil {
					return relayRequestInfo{}, err
				}
			}
		case "response_format":
			if isAudioTextMultipartAPIType(apiType) {
				if err := validateAudioMultipartResponseFormat(raw); err != nil {
					return relayRequestInfo{}, err
				}
			}
		}
	}
	if info.Model == "" {
		return relayRequestInfo{}, errModelRequired
	}
	if requiredFileField != "" && !hasRequiredFileField {
		return relayRequestInfo{}, errMultipartFileRequired
	}
	return info, nil
}

func requiredMultipartFileField(apiType relay.APIType) string {
	switch apiType {
	case relay.APIImagesEdits, relay.APIImagesVariations:
		return "image"
	case relay.APIAudioTranscriptions, relay.APIAudioTranslations:
		return "file"
	default:
		return ""
	}
}

func isImageMultipartAPIType(apiType relay.APIType) bool {
	return apiType == relay.APIImagesEdits || apiType == relay.APIImagesVariations
}

func validateImageMultipartSize(raw []byte) error {
	return validateImageSizeValue(strings.ToLower(strings.TrimSpace(string(raw))))
}

func isAudioTextMultipartAPIType(apiType relay.APIType) bool {
	return apiType == relay.APIAudioTranscriptions || apiType == relay.APIAudioTranslations
}

func validateAudioMultipartResponseFormat(raw []byte) error {
	format := strings.TrimSpace(string(raw))
	if format == "" {
		return nil
	}
	switch format {
	case "json", "text", "srt", "verbose_json", "vtt":
		return nil
	default:
		return errInvalidAudioFormat
	}
}

func relayRequestErrorCode(err error) string {
	switch {
	case errors.Is(err, errInvalidJSONBody):
		return "invalid_json"
	case errors.Is(err, errInvalidMultipartBody):
		return "invalid_multipart"
	case errors.Is(err, errMultipartFileTooLarge):
		return "request_file_too_large"
	case errors.Is(err, errMultipartFileRequired):
		return "multipart_file_required"
	case errors.Is(err, errUnsafeMultipartFile):
		return "unsafe_multipart_file"
	case errors.Is(err, errModelRequired):
		return "model_required"
	case errors.Is(err, errInvalidChatMessages):
		return "invalid_chat_messages"
	case errors.Is(err, errInvalidGeminiEmbedding):
		return "invalid_gemini_embedding_request"
	case errors.Is(err, errInvalidEmbeddingInput):
		return "invalid_embedding_input"
	case errors.Is(err, errEmbeddingBatchTooLarge):
		return "embedding_batch_too_large"
	case errors.Is(err, errInvalidImagePrompt):
		return "invalid_image_prompt"
	case errors.Is(err, errInvalidImageCount):
		return "invalid_image_count"
	case errors.Is(err, errInvalidImageSize):
		return "invalid_image_size"
	case errors.Is(err, errInvalidAudioFormat):
		return "invalid_audio_response_format"
	case errors.Is(err, errInvalidAudioInput):
		return "invalid_audio_speech_input"
	case errors.Is(err, errInvalidAudioVoice):
		return "invalid_audio_speech_voice"
	case errors.Is(err, errInvalidModerationInput):
		return "invalid_moderation_input"
	default:
		return "invalid_request"
	}
}

func invalidGeminiEmbeddingRequest(reason string) error {
	return fmt.Errorf("%w: %s", errInvalidGeminiEmbedding, reason)
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

func relayUpstreamInputBody(ctx context.Context, apiType relay.APIType, body []byte, channelType int, upstreamModel string) ([]byte, error) {
	out := body
	var err error
	if supportsGeminiNativeRequest(ctx, apiType, channelType) {
		out, err = geminiNativeUpstreamBody(ctx, apiType)
		if err != nil {
			return nil, err
		}
	}
	if supportsAnthropicNativeRequest(ctx, apiType, channelType) {
		// Anthropic native fields are applied only after route selection so OpenAI-compatible upstreams keep the canonical Chat body.
		out, err = anthropicNativeMessagesUpstreamBody(relayAnthropicNativeBodyFromContext(ctx))
		if err != nil {
			return nil, err
		}
	}
	return replaceRequestModel(out, upstreamModel)
}

func geminiNativeUpstreamBody(ctx context.Context, apiType relay.APIType) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(relayGeminiNativeBodyFromContext(ctx), &payload); err != nil {
		return nil, err
	}
	var native map[string]json.RawMessage
	switch {
	case apiType == relay.APIChatCompletions:
		native = geminiNativeProviderBody(payload)
	case apiType == relay.APIEmbeddings && strings.EqualFold(relayGeminiNativeEmbeddingFromContext(ctx), relayGeminiNativeEmbedContentKind):
		native = geminiEmbedContentNativeProviderBody(payload)
	case apiType == relay.APIEmbeddings && strings.EqualFold(relayGeminiNativeEmbeddingFromContext(ctx), relayGeminiNativeBatchEmbedContentsKind):
		native = geminiBatchEmbedContentsNativeProviderBody(payload)
	default:
		native = nil
	}
	if len(native) == 0 {
		return nil, errInvalidJSONBody
	}
	return json.Marshal(native)
}

func anthropicNativeMessagesUpstreamBody(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	// Keep this whitelist explicit: it prevents RouterX private fields from leaking while preserving Anthropic Messages semantics.
	fields := []string{
		"model",
		"messages",
		"system",
		"max_tokens",
		"metadata",
		"stop_sequences",
		"stream",
		"temperature",
		"top_p",
		"top_k",
		"tools",
		"tool_choice",
		"thinking",
		"container",
		"context_management",
		"mcp_servers",
		"service_tier",
	}
	output := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		raw, ok := payload[field]
		if !ok || !rawJSONFieldPresent(raw) {
			continue
		}
		output[field] = append(json.RawMessage(nil), raw...)
	}
	if _, ok := output["max_tokens"]; !ok {
		output["max_tokens"] = json.RawMessage(`1024`)
	}
	return json.Marshal(output)
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

func rewriteMultipartRelayBody(apiType relay.APIType, body []byte, contentType string, modelName string, maxFileBytes int64) ([]byte, string, error) {
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
			if part.FileName() != "" {
				if err := validateMultipartFile(apiType, part); err != nil {
					return nil, "", err
				}
				if err := discardMultipartFileWithLimit(part, maxFileBytes, apiType, part.FileName()); err != nil {
					return nil, "", err
				}
			} else {
				_, _ = io.Copy(io.Discard, part)
			}
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
		if part.FileName() != "" {
			if err := validateMultipartFile(apiType, part); err != nil {
				return nil, "", err
			}
			if err := copyMultipartFileWithLimit(dst, part, maxFileBytes, apiType, part.FileName()); err != nil {
				return nil, "", err
			}
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

func validateMultipartFile(apiType relay.APIType, part *multipart.Part) error {
	if unsafeMultipartFilePathName(rawMultipartFileName(part)) {
		return errUnsafeMultipartFile
	}
	filename := strings.TrimSpace(part.FileName())
	if filename == "" {
		return nil
	}
	if unsafeMultipartFileExtension(filepath.Ext(filename)) {
		return errUnsafeMultipartFile
	}
	if !allowedMultipartFileExtension(apiType, filepath.Ext(filename)) {
		return errUnsafeMultipartFile
	}
	return nil
}

func rawMultipartFileName(part *multipart.Part) string {
	if part == nil {
		return ""
	}
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["filename"])
}

func unsafeMultipartFilePathName(filename string) bool {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return false
	}
	return filename == "." || filename == ".." || strings.ContainsAny(filename, `/\:`)
}

func unsafeMultipartFileExtension(ext string) bool {
	// 这里只做基础入口防护，完整内容扫描应由后续专门的安全策略承接。
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".bat", ".cmd", ".com", ".dll", ".exe", ".js", ".msi", ".php", ".ps1", ".scr", ".sh", ".vbs":
		return true
	default:
		return false
	}
}

func allowedMultipartFileExtension(apiType relay.APIType, ext string) bool {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return true
	}
	switch apiType {
	case relay.APIImagesEdits, relay.APIImagesVariations:
		return stringInSet(ext, ".gif", ".jpeg", ".jpg", ".png", ".webp")
	case relay.APIAudioTranscriptions, relay.APIAudioTranslations:
		return stringInSet(ext, ".aac", ".aif", ".aiff", ".flac", ".m4a", ".mp3", ".mp4", ".mpeg", ".mpga", ".oga", ".ogg", ".opus", ".wav", ".webm")
	default:
		return true
	}
}

func stringInSet(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func multipartFileSignatureMatches(apiType relay.APIType, ext string, prefix []byte) bool {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return true
	}
	switch apiType {
	case relay.APIImagesEdits, relay.APIImagesVariations:
		return imageFileSignatureMatches(ext, prefix)
	case relay.APIAudioTranscriptions, relay.APIAudioTranslations:
		return audioFileSignatureMatches(ext, prefix)
	default:
		return true
	}
}

func imageFileSignatureMatches(ext string, prefix []byte) bool {
	// Keep this as a cheap file header gate; full media decoding belongs in a
	// deeper safety pipeline, not the Relay hot path.
	switch ext {
	case ".png":
		return bytes.HasPrefix(prefix, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	case ".jpg", ".jpeg":
		return bytes.HasPrefix(prefix, []byte{0xff, 0xd8, 0xff})
	case ".gif":
		return bytes.HasPrefix(prefix, []byte("GIF87a")) || bytes.HasPrefix(prefix, []byte("GIF89a"))
	case ".webp":
		return len(prefix) >= 12 && bytes.HasPrefix(prefix, []byte("RIFF")) && bytes.Equal(prefix[8:12], []byte("WEBP"))
	default:
		return true
	}
}

func audioFileSignatureMatches(ext string, prefix []byte) bool {
	switch ext {
	case ".wav":
		return len(prefix) >= 12 && bytes.HasPrefix(prefix, []byte("RIFF")) && bytes.Equal(prefix[8:12], []byte("WAVE"))
	case ".aif", ".aiff":
		return len(prefix) >= 12 && bytes.HasPrefix(prefix, []byte("FORM")) && (bytes.Equal(prefix[8:12], []byte("AIFF")) || bytes.Equal(prefix[8:12], []byte("AIFC")))
	case ".flac":
		return bytes.HasPrefix(prefix, []byte("fLaC"))
	case ".mp3", ".mpeg", ".mpga":
		return bytes.HasPrefix(prefix, []byte("ID3")) || mp3FrameSignature(prefix)
	case ".mp4", ".m4a":
		return len(prefix) >= 12 && bytes.Equal(prefix[4:8], []byte("ftyp"))
	case ".oga", ".ogg", ".opus":
		return bytes.HasPrefix(prefix, []byte("OggS"))
	case ".webm":
		return bytes.HasPrefix(prefix, []byte{0x1a, 0x45, 0xdf, 0xa3})
	case ".aac":
		return len(prefix) >= 2 && prefix[0] == 0xff && prefix[1]&0xf0 == 0xf0
	default:
		return true
	}
}

func mp3FrameSignature(prefix []byte) bool {
	return len(prefix) >= 2 && prefix[0] == 0xff && prefix[1]&0xe0 == 0xe0
}

const multipartFileSignatureScanBytes = 512

func unsafeMultipartFileContent(prefix []byte) bool {
	if len(prefix) == 0 {
		return false
	}
	// 这层只拦截明显的可执行或脚本签名，避免把完整杀毒/内容审核耦合进 Relay 热路径。
	trimmed := bytes.TrimLeft(prefix, "\x00\t\r\n ")
	lower := bytes.ToLower(trimmed)
	switch {
	case bytes.HasPrefix(prefix, []byte{'M', 'Z'}): // Windows PE
		return true
	case bytes.HasPrefix(prefix, []byte{0x7f, 'E', 'L', 'F'}): // Linux ELF
		return true
	case bytes.HasPrefix(trimmed, []byte("#!")):
		return true
	case bytes.HasPrefix(lower, []byte("<script")):
		return true
	case bytes.HasPrefix(lower, []byte("<?php")):
		return true
	case bytes.HasPrefix(lower, []byte("<!doctype html")):
		return true
	case bytes.HasPrefix(lower, []byte("<html")):
		return true
	default:
		return false
	}
}

func discardMultipartFileWithLimit(src io.Reader, maxBytes int64, apiType relay.APIType, filename string) error {
	return copyMultipartFileWithLimit(io.Discard, src, maxBytes, apiType, filename)
}

func copyMultipartFileWithLimit(dst io.Writer, src io.Reader, maxBytes int64, apiType relay.APIType, filename string) error {
	reader := src
	if maxBytes > 0 {
		reader = &io.LimitedReader{R: src, N: maxBytes + 1}
	}
	prefix := make([]byte, multipartFileSignatureScanBytes)
	n, readErr := io.ReadFull(reader, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return errInvalidMultipartBody
	}
	if unsafeMultipartFileContent(prefix[:n]) {
		return errUnsafeMultipartFile
	}
	written := int64(n)
	if maxBytes > 0 && written > maxBytes {
		return errMultipartFileTooLarge
	}
	if !multipartFileSignatureMatches(apiType, filepath.Ext(filename), prefix[:n]) {
		return errUnsafeMultipartFile
	}
	if n > 0 {
		prefixWritten, err := dst.Write(prefix[:n])
		if err != nil || prefixWritten != n {
			return errInvalidMultipartBody
		}
	}
	// 多读 1 字节即可判断是否超限，同时避免把整个文件先读入内存。
	copied, err := io.Copy(dst, reader)
	if err != nil {
		return errInvalidMultipartBody
	}
	written += copied
	if maxBytes > 0 && written > maxBytes {
		return errMultipartFileTooLarge
	}
	return nil
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
		return nil, errInvalidJSONBody
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" {
		return nil, errModelRequired
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
		return nil, errInvalidJSONBody
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, errModelRequired
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

func geminiNativeProviderBody(payload map[string]json.RawMessage) map[string]json.RawMessage {
	if len(payload) == 0 {
		return nil
	}
	fields := []string{
		"contents",
		"systemInstruction",
		"generationConfig",
		"safetySettings",
		"tools",
		"toolConfig",
		"cachedContent",
	}
	native := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		raw, ok := payload[field]
		if !ok || !rawJSONFieldPresent(raw) {
			continue
		}
		native[field] = append(json.RawMessage(nil), raw...)
	}
	if len(native) == 0 {
		return nil
	}
	native["_routerx_source_protocol"] = json.RawMessage(`"gemini"`)
	return native
}

func rawJSONFieldPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.EqualFold(trimmed, []byte("null"))
}

func geminiEmbedContentToOpenAI(modelName string, body []byte) ([]byte, error) {
	var input struct {
		Content struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
		OutputDimensionality *int `json:"outputDimensionality"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, errInvalidJSONBody
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, errModelRequired
	}
	text := geminiTextFromParts(input.Content.Parts)
	if text == "" {
		return nil, invalidGeminiEmbeddingRequest("content is required")
	}
	output := map[string]interface{}{
		"model": modelName,
		"input": text,
	}
	if input.OutputDimensionality != nil {
		if *input.OutputDimensionality <= 0 {
			return nil, invalidGeminiEmbeddingRequest("outputDimensionality must be positive")
		}
		output["dimensions"] = *input.OutputDimensionality
	}
	return json.Marshal(output)
}

func geminiEmbedContentNativeProviderBody(payload map[string]json.RawMessage) map[string]json.RawMessage {
	if len(payload) == 0 {
		return nil
	}
	fields := []string{
		"content",
		"taskType",
		"title",
		"outputDimensionality",
	}
	native := make(map[string]json.RawMessage, len(fields)+1)
	for _, field := range fields {
		raw, ok := payload[field]
		if !ok || !rawJSONFieldPresent(raw) {
			continue
		}
		native[field] = append(json.RawMessage(nil), raw...)
	}
	if len(native) == 0 {
		return nil
	}
	native["_routerx_source_protocol"] = json.RawMessage(`"gemini_embed_content"`)
	return native
}

func geminiBatchEmbedContentsToOpenAI(modelName string, body []byte) ([]byte, int, error) {
	var input struct {
		Requests []struct {
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			OutputDimensionality *int `json:"outputDimensionality"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, 0, errInvalidJSONBody
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, 0, errModelRequired
	}
	if len(input.Requests) == 0 {
		return nil, 0, invalidGeminiEmbeddingRequest("requests are required")
	}
	values := make([]string, 0, len(input.Requests))
	// OpenAI-compatible embeddings accept one dimensions value for the whole batch.
	dimensions := 0
	hasDimensions := false
	for _, request := range input.Requests {
		text := geminiTextFromParts(request.Content.Parts)
		if text == "" {
			return nil, 0, invalidGeminiEmbeddingRequest("content is required")
		}
		values = append(values, text)
		if request.OutputDimensionality != nil {
			if *request.OutputDimensionality <= 0 {
				return nil, 0, invalidGeminiEmbeddingRequest("outputDimensionality must be positive")
			}
			if hasDimensions && dimensions != *request.OutputDimensionality {
				return nil, 0, invalidGeminiEmbeddingRequest("outputDimensionality must match for batch requests")
			}
			dimensions = *request.OutputDimensionality
			hasDimensions = true
		}
	}
	output := map[string]interface{}{
		"model": modelName,
		"input": values,
	}
	if hasDimensions {
		output["dimensions"] = dimensions
	}
	canonical, err := json.Marshal(output)
	if err != nil {
		return nil, 0, err
	}
	return canonical, len(values), nil
}

func geminiBatchEmbedContentsNativeProviderBody(payload map[string]json.RawMessage) map[string]json.RawMessage {
	if len(payload) == 0 {
		return nil
	}
	raw, ok := payload["requests"]
	if !ok || !rawJSONFieldPresent(raw) {
		return nil
	}
	return map[string]json.RawMessage{
		"requests":                 append(json.RawMessage(nil), raw...),
		"_routerx_source_protocol": json.RawMessage(`"gemini_batch_embed_contents"`),
	}
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

func anthropicAdapterDegradations(body []byte) []relayAdapterDegradation {
	var input struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools      json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage `json:"tool_choice"`
		Thinking   json.RawMessage `json:"thinking"`
		Metadata   json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil
	}
	degradations := make([]relayAdapterDegradation, 0)
	for _, message := range input.Messages {
		degradations = appendAnthropicContentDegradations(degradations, inputProtocolAnthropic, "messages.content", message.Content)
	}
	degradations = appendAnthropicContentDegradations(degradations, inputProtocolAnthropic, "system", input.System)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolAnthropic, "tools", input.Tools)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolAnthropic, "tool_choice", input.ToolChoice)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolAnthropic, "thinking", input.Thinking)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolAnthropic, "metadata", input.Metadata)
	return degradations
}

func appendAnthropicContentDegradations(degradations []relayAdapterDegradation, protocol, prefix string, raw json.RawMessage) []relayAdapterDegradation {
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return degradations
	}
	for _, block := range blocks {
		var blockType string
		_ = json.Unmarshal(block["type"], &blockType)
		blockType = strings.TrimSpace(blockType)
		if blockType == "" || blockType == "text" {
			continue
		}
		degradations = appendUniqueAdapterDegradation(degradations, relayAdapterDegradation{
			Protocol: protocol,
			Field:    prefix + "." + blockType,
			Action:   "serialized_as_text",
			Reason:   "non_text_content_block_serialized_as_compact_json",
		})
	}
	return degradations
}

func geminiGenerateAdapterDegradations(body []byte) []relayAdapterDegradation {
	var input struct {
		Contents []struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"contents"`
		SystemInstruction *struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"systemInstruction"`
		Tools            json.RawMessage            `json:"tools"`
		ToolConfig       json.RawMessage            `json:"toolConfig"`
		SafetySettings   json.RawMessage            `json:"safetySettings"`
		CachedContent    json.RawMessage            `json:"cachedContent"`
		GenerationConfig map[string]json.RawMessage `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil
	}
	degradations := make([]relayAdapterDegradation, 0)
	for _, content := range input.Contents {
		degradations = appendGeminiPartDegradations(degradations, "contents.parts", content.Parts)
	}
	if input.SystemInstruction != nil {
		degradations = appendGeminiPartDegradations(degradations, "systemInstruction.parts", input.SystemInstruction.Parts)
	}
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "tools", input.Tools)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "toolConfig", input.ToolConfig)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "safetySettings", input.SafetySettings)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "cachedContent", input.CachedContent)
	degradations = appendGeminiGenerationConfigDegradations(degradations, input.GenerationConfig)
	return degradations
}

func geminiEmbedContentAdapterDegradations(body []byte) []relayAdapterDegradation {
	var input struct {
		Content struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
		TaskType json.RawMessage `json:"taskType"`
		Title    json.RawMessage `json:"title"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil
	}
	degradations := make([]relayAdapterDegradation, 0)
	degradations = appendGeminiPartDegradations(degradations, "content.parts", input.Content.Parts)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "taskType", input.TaskType)
	degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "title", input.Title)
	return degradations
}

func geminiBatchEmbedContentsAdapterDegradations(body []byte) []relayAdapterDegradation {
	var input struct {
		Requests []struct {
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			TaskType json.RawMessage `json:"taskType"`
			Title    json.RawMessage `json:"title"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil
	}
	degradations := make([]relayAdapterDegradation, 0)
	for _, request := range input.Requests {
		degradations = appendGeminiPartDegradations(degradations, "requests.content.parts", request.Content.Parts)
		degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "requests.taskType", request.TaskType)
		degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "requests.title", request.Title)
	}
	return degradations
}

func appendGeminiGenerationConfigDegradations(degradations []relayAdapterDegradation, config map[string]json.RawMessage) []relayAdapterDegradation {
	for key, raw := range config {
		switch key {
		case "maxOutputTokens", "temperature", "topP", "stopSequences":
			continue
		}
		degradations = appendDroppedFieldDegradation(degradations, inputProtocolGemini, "generationConfig."+key, raw)
	}
	return degradations
}

func appendGeminiPartDegradations(degradations []relayAdapterDegradation, prefix string, parts []json.RawMessage) []relayAdapterDegradation {
	for _, part := range parts {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(part, &object); err != nil {
			continue
		}
		if rawText, ok := object["text"]; ok {
			var text string
			if err := json.Unmarshal(rawText, &text); err == nil && strings.TrimSpace(text) != "" {
				continue
			}
		}
		field := firstGeminiPartField(object)
		if field == "" {
			continue
		}
		degradations = appendUniqueAdapterDegradation(degradations, relayAdapterDegradation{
			Protocol: inputProtocolGemini,
			Field:    prefix + "." + field,
			Action:   "serialized_as_text",
			Reason:   "non_text_part_serialized_as_compact_json",
		})
	}
	return degradations
}

func firstGeminiPartField(object map[string]json.RawMessage) string {
	for _, key := range []string{"functionCall", "functionResponse", "inlineData", "fileData", "executableCode", "codeExecutionResult"} {
		if _, ok := object[key]; ok {
			return key
		}
	}
	for key := range object {
		if key != "text" {
			return key
		}
	}
	return ""
}

func appendDroppedFieldDegradation(degradations []relayAdapterDegradation, protocol, field string, raw json.RawMessage) []relayAdapterDegradation {
	if !rawJSONHasValue(raw) {
		return degradations
	}
	return appendUniqueAdapterDegradation(degradations, relayAdapterDegradation{
		Protocol: protocol,
		Field:    field,
		Action:   "dropped",
		Reason:   "no_equivalent_openai_chat_field",
	})
}

func appendUniqueAdapterDegradation(degradations []relayAdapterDegradation, next relayAdapterDegradation) []relayAdapterDegradation {
	if strings.TrimSpace(next.Protocol) == "" || strings.TrimSpace(next.Field) == "" || strings.TrimSpace(next.Action) == "" {
		return degradations
	}
	for _, existing := range degradations {
		if existing.Protocol == next.Protocol && existing.Field == next.Field && existing.Action == next.Action {
			return degradations
		}
	}
	return append(degradations, next)
}

func rawJSONHasValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" || trimmed == "{}" {
		return false
	}
	return true
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

func openAIEmbeddingsToGeminiBatch(body []byte, expectedCount int) ([]byte, error) {
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
	// Gemini batch 响应按请求顺序返回；数量不一致时不能静默降级成部分成功。
	if expectedCount > 0 && len(input.Data) != expectedCount {
		return nil, errors.New("embedding response count mismatch")
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

func (s *RelayService) MaxMultipartFileBytes() int64 {
	if s == nil || s.settingService == nil {
		return defaultRelayMaxMultipartFileBytes
	}
	value, err := s.settingService.GetInt("relay.max_multipart_file_bytes")
	if err != nil || value < 0 {
		return defaultRelayMaxMultipartFileBytes
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

func (s *RelayService) relayLogBodyMaxBytes() int {
	if s == nil || s.settingService == nil {
		return 0
	}
	for _, key := range []string{"relay.log_body_max_bytes", "log.body_max_bytes"} {
		value, err := s.settingService.GetInt(key)
		if err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func (s *RelayService) relayRequestBodyLoggingEnabled() bool {
	if s == nil || s.settingService == nil {
		return false
	}
	enabled, err := s.settingService.GetBool("log.request_body_enabled")
	return err == nil && enabled && s.relayLogBodyMaxBytes() > 0
}

func (s *RelayService) relayResponseBodyLoggingEnabled() bool {
	if s == nil || s.settingService == nil {
		return false
	}
	enabled, err := s.settingService.GetBool("log.response_body_enabled")
	return err == nil && enabled && s.relayLogBodyMaxBytes() > 0
}

func (s *RelayService) contextWithRelayLogRequestBody(ctx context.Context, body []byte) context.Context {
	if !s.relayRequestBodyLoggingEnabled() {
		return ctx
	}
	if snippet := sanitizeRelayLogBody(body, s.relayLogBodyMaxBytes()); snippet != "" {
		return ContextWithRelayLogRequestBody(ctx, snippet)
	}
	return ctx
}

func (s *RelayService) contextWithRelayLogResponseBody(ctx context.Context, body []byte, contentType string) context.Context {
	if !s.relayResponseBodyLoggingEnabled() || !relayLoggableContentType(contentType) {
		return ctx
	}
	if snippet := sanitizeRelayLogBody(body, s.relayLogBodyMaxBytes()); snippet != "" {
		return ContextWithRelayLogResponseBody(ctx, snippet)
	}
	return ctx
}

func sanitizeRelayLogBody(body []byte, limit int) string {
	if len(body) == 0 || limit <= 0 {
		return ""
	}
	value := strings.ToValidUTF8(string(body), "")
	for _, redactor := range relayBodyLogRedactors {
		value = redactor.pattern.ReplaceAllString(value, redactor.replacement)
	}
	if len(value) > limit {
		value = string([]byte(value)[:limit])
		value = strings.ToValidUTF8(value, "")
	}
	return strings.TrimSpace(value)
}

func relayLoggableContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		mediaType = strings.TrimSpace(contentType)
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType == "" {
		return true
	}
	return strings.HasPrefix(mediaType, "text/") ||
		strings.Contains(mediaType, "json") ||
		strings.Contains(mediaType, "xml") ||
		strings.Contains(mediaType, "javascript") ||
		strings.Contains(mediaType, "event-stream")
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
	if modelName == "" {
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
	exceeded, current, unavailable := relayRateLimitExceeded(fmt.Sprintf("rl:model:%s:%d", modelName, now), int64(limit))
	if unavailable {
		return s.recordRateLimitUnavailable(ctx, token, nil, modelName, clientIP, "model", "incr_failed")
	}
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
	if channel == nil || channel.ID == 0 {
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
	exceeded, current, unavailable := relayRateLimitExceeded(fmt.Sprintf("rl:channel:%d:%d", channel.ID, now), int64(limit))
	if unavailable {
		return s.recordRateLimitUnavailable(ctx, token, channel, modelName, clientIP, "channel", "incr_failed")
	}
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

func (s *RelayService) recordRateLimitUnavailable(ctx context.Context, token *model.Token, channel *model.Channel, modelName, clientIP, dimension, reason string) error {
	scopeResult := map[string]interface{}{
		"api_type":                   "allow",
		"model":                      "allow",
		"channel_group":              "not_evaluated",
		"rate_limit":                 "error",
		"rate_limit_dimension":       strings.ToLower(strings.TrimSpace(dimension)),
		"rate_limit_dependency":      "redis",
		"rate_limit_dependency_mode": "required",
	}
	if channel != nil {
		scopeResult["channel_group"] = "allow"
	}
	logCtx := ContextWithRelayPolicySnapshot(ctx, buildRelayRateLimitUnavailableSnapshot(ctx, token, dimension, reason, scopeResult))
	_ = s.recordLog(logCtx, token, channel, modelName, nil, common.LogStatusFailed, 0, "rate limit unavailable: "+strings.TrimSpace(reason), clientIP)
	return &HTTPError{Status: http.StatusServiceUnavailable, Message: "rate limit unavailable", Type: "server_error", Code: "rate_limit_unavailable"}
}

func relayRateLimitExceeded(key string, limit int64) (bool, int64, bool) {
	if limit <= 0 {
		return false, 0, false
	}
	if internal.RDB == nil {
		if RedisRequiredForCurrentMode() {
			RecordRedisError("rate_limit_required")
			return false, 0, true
		}
		return false, 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	count, err := internal.RDB.Incr(ctx, key).Result()
	if err != nil {
		RecordRedisError("rate_limit_incr")
		return false, 0, RedisRequiredForCurrentMode()
	}
	if count == 1 {
		if err := internal.RDB.Expire(ctx, key, 2*time.Minute).Err(); err != nil {
			RecordRedisError("rate_limit_expire")
			return false, count, RedisRequiredForCurrentMode()
		}
	}
	return count > limit, count, false
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

func buildRelayRequestSnapshot(ctx context.Context, token *model.Token, clientIP string, apiType relay.APIType, reqInfo relayRequestInfo) string {
	return buildRelayRequestSnapshotForChannel(ctx, token, clientIP, apiType, reqInfo, nil)
}

func buildRelayRequestSnapshotForChannel(ctx context.Context, token *model.Token, clientIP string, apiType relay.APIType, reqInfo relayRequestInfo, channel *model.Channel) string {
	apiTypeName := relayAPITypeScopeName(apiType)
	ingressProtocol := relayIngressProtocolFromContext(ctx)
	if ingressProtocol == "" {
		ingressProtocol = relayIngressProtocolFromAPIType(apiType)
	}
	snapshot := map[string]interface{}{
		"schema":           "routerx.snapshot.v1",
		"kind":             "request",
		"stage":            "p1",
		"source":           "relay",
		"redacted":         true,
		"request_id":       relayRequestIDFromContext(ctx),
		"ingress_protocol": ingressProtocol,
		"api_type":         apiTypeName,
		"requested_model":  strings.TrimSpace(reqInfo.Model),
		"stream":           reqInfo.Stream,
	}
	if token != nil {
		if token.UserID > 0 {
			snapshot["user_id"] = token.UserID
		}
		if token.ID > 0 {
			snapshot["token_id"] = token.ID
		}
		if tokenPrefix := relayTokenPrefixForSnapshot(token); tokenPrefix != "" {
			snapshot["token_prefix"] = tokenPrefix
		}
	}
	if clientIPSummary := relayHashSummaryForSnapshot(clientIP, relaySnapshotHashPrefixLength); clientIPSummary != "" {
		snapshot["client_ip_summary"] = clientIPSummary
	}
	if userAgentSummary := relayUserAgentSummaryForSnapshot(relayUserAgentFromContext(ctx)); userAgentSummary != "" {
		snapshot["user_agent_summary"] = userAgentSummary
	}
	if traceparent, traceID, ok := common.NormalizeTraceparent(relay.TraceparentFromContext(ctx)); ok {
		// Re-normalize before persistence so only bounded W3C trace context reaches audit logs.
		snapshot["trace_id"] = traceID
		snapshot["traceparent"] = traceparent
		if tracestate := relay.TracestateFromContext(ctx); tracestate != "" {
			snapshot["tracestate"] = tracestate
		}
	}
	if degradations := relayAdapterDegradationsForSnapshot(ctx, apiType, channel); len(degradations) > 0 {
		snapshot["adapter_degradations"] = degradations
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

func relayTokenPrefixForSnapshot(token *model.Token) string {
	if token == nil {
		return ""
	}
	keyHash := strings.TrimSpace(token.Key)
	if keyHash == "" {
		return ""
	}
	if strings.HasPrefix(keyHash, "sk-") {
		keyHash = common.SHA256Hex(keyHash)
	}
	if len(keyHash) > relaySnapshotTokenPrefixLength {
		keyHash = keyHash[:relaySnapshotTokenPrefixLength]
	}
	return "sha256:" + keyHash
}

func relayHashSummaryForSnapshot(value string, prefixLength int) string {
	value = strings.TrimSpace(value)
	if value == "" || prefixLength <= 0 {
		return ""
	}
	hash := common.SHA256Hex(value)
	if len(hash) > prefixLength {
		hash = hash[:prefixLength]
	}
	return "sha256:" + hash
}

func relayUserAgentSummaryForSnapshot(userAgent string) string {
	userAgent = strings.Join(strings.Fields(strings.TrimSpace(userAgent)), " ")
	if userAgent == "" {
		return ""
	}
	runes := []rune(userAgent)
	if len(runes) > relaySnapshotUserAgentMaxRunes {
		userAgent = string(runes[:relaySnapshotUserAgentMaxRunes])
	}
	return userAgent
}

func relayAdapterDegradationsForSnapshot(ctx context.Context, apiType relay.APIType, channel *model.Channel) []relayAdapterDegradation {
	degradations := relayAdapterDegradationsFromContext(ctx)
	if len(degradations) == 0 {
		return nil
	}
	if channel == nil {
		return degradations
	}
	if supportsGeminiNativeRequest(ctx, apiType, channel.Type) {
		filtered := make([]relayAdapterDegradation, 0, len(degradations))
		for _, degradation := range degradations {
			if strings.EqualFold(degradation.Protocol, inputProtocolGemini) {
				continue
			}
			filtered = append(filtered, degradation)
		}
		return filtered
	}
	if supportsAnthropicNativeRequest(ctx, apiType, channel.Type) {
		filtered := make([]relayAdapterDegradation, 0, len(degradations))
		for _, degradation := range degradations {
			if strings.EqualFold(degradation.Protocol, inputProtocolAnthropic) {
				continue
			}
			filtered = append(filtered, degradation)
		}
		return filtered
	}
	return degradations
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
		addRelayPolicyActorStatusSnapshot(snapshot, token)
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
		addRelayPolicyActorStatusSnapshot(snapshot, token)
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

func buildRelayRateLimitUnavailableSnapshot(ctx context.Context, token *model.Token, dimension, reason string, scopeResult map[string]interface{}) string {
	raw := buildRelayPolicyDenySnapshot(ctx, token, "rate_limit_unavailable", "not_evaluated", scopeResult)
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return raw
	}
	dimension = strings.ToLower(strings.TrimSpace(dimension))
	if dimension == "" {
		dimension = "redis"
	}
	snapshot["rate_limit_snapshot"] = map[string]interface{}{
		"dimension":   dimension,
		"window":      "minute",
		"decision":    "deny",
		"unavailable": true,
		"dependency":  "redis",
		"reason":      strings.TrimSpace(reason),
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

func addRelayPolicyActorStatusSnapshot(snapshot map[string]interface{}, token *model.Token) {
	if snapshot == nil || token == nil {
		return
	}
	tokenStatus := map[string]interface{}{
		"id":         token.ID,
		"status":     tokenStatusSnapshot(token.Status),
		"expired_at": nil,
		"unlimited":  token.Unlimited || token.RemainQuota == common.QuotaUnlimited,
	}
	if token.ExpiredAt != nil {
		tokenStatus["expired_at"] = token.ExpiredAt.UTC().Format(time.RFC3339Nano)
	}
	snapshot["token_status"] = tokenStatus
	if token.User != nil {
		snapshot["user_status"] = map[string]interface{}{
			"id":     token.User.ID,
			"status": userStatusSnapshot(token.User.Status),
			"role":   userRoleSnapshot(token.User.Role),
		}
	}
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

func userRoleSnapshot(role int) string {
	switch role {
	case common.RoleUser:
		return "user"
	case common.RoleAdmin:
		return "admin"
	case common.RoleSuper:
		return "super_admin"
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
		"schema":           "routerx.snapshot.v1",
		"kind":             "route",
		"stage":            "p1",
		"source":           "relay",
		"redacted":         true,
		"requested_model":  requestedModel,
		"normalized_model": requestedModel,
		"candidate_count":  len(candidates),
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

func addRelayRouteUpstreamTargetSnapshot(raw string, target *ChannelUpstreamTarget) string {
	if target == nil || strings.TrimSpace(raw) == "" {
		return raw
	}
	source := strings.TrimSpace(target.BaseURLSource)
	if source != "base_urls" && source != "upstreams" {
		return raw
	}
	if target.BaseURLIndex < 0 {
		return raw
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return raw
	}
	snapshot["upstream_base_url_index"] = map[string]interface{}{
		"source": source,
		"index":  target.BaseURLIndex,
	}
	withTarget, err := json.Marshal(snapshot)
	if err != nil {
		return raw
	}
	return string(withTarget)
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

func forwardGeminiStream(r io.Reader, write func([]byte) error, flush func()) (*relay.Usage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var usage *relay.Usage
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if parsed := usageFromGeminiStreamLine(line); parsed != nil {
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

func forwardAnthropicStream(r io.Reader, write func([]byte) error, flush func()) (*relay.Usage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2<<20)
	var usage *relay.Usage
	accumulator := anthropicStreamUsageAccumulator{}
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if parsed := accumulator.apply(line); parsed != nil {
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
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	_ = json.Unmarshal(payload, &envelope)
	if usage := usageFromOpenAIUsageRaw(envelope.Usage); usage != nil {
		return usage
	}
	// Responses API streams carry final usage inside the response.completed event.
	return usageFromOpenAIUsageRaw(envelope.Response.Usage)
}

func usageFromGeminiStreamLine(line []byte) *relay.Usage {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return nil
	}
	var envelope struct {
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	_ = json.Unmarshal(payload, &envelope)
	usage := relay.Usage{
		PromptTokens:     envelope.UsageMetadata.PromptTokenCount,
		CompletionTokens: envelope.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      envelope.UsageMetadata.TotalTokenCount,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return &usage
}

type anthropicStreamUsageAccumulator struct {
	promptTokens     int
	completionTokens int
	totalTokens      int
}

func (a *anthropicStreamUsageAccumulator) apply(line []byte) *relay.Usage {
	parsed := usageFromAnthropicStreamLine(line)
	if parsed == nil {
		return nil
	}
	if parsed.PromptTokens > 0 {
		a.promptTokens = parsed.PromptTokens
	}
	if parsed.CompletionTokens > 0 {
		a.completionTokens = parsed.CompletionTokens
	}
	if parsed.TotalTokens > 0 {
		a.totalTokens = parsed.TotalTokens
	}
	totalTokens := a.promptTokens + a.completionTokens
	if totalTokens == 0 || a.totalTokens > totalTokens {
		totalTokens = a.totalTokens
	}
	if totalTokens == 0 {
		return nil
	}
	return &relay.Usage{
		PromptTokens:     a.promptTokens,
		CompletionTokens: a.completionTokens,
		TotalTokens:      totalTokens,
	}
}

func usageFromAnthropicStreamLine(line []byte) *relay.Usage {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return nil
	}
	var envelope struct {
		Usage   anthropicUsageFields `json:"usage"`
		Message struct {
			Usage anthropicUsageFields `json:"usage"`
		} `json:"message"`
	}
	_ = json.Unmarshal(payload, &envelope)
	usage := relay.Usage{
		PromptTokens:     envelope.Usage.InputTokens,
		CompletionTokens: envelope.Usage.OutputTokens,
	}
	if usage.PromptTokens == 0 {
		usage.PromptTokens = envelope.Message.Usage.InputTokens
	}
	if usage.CompletionTokens == 0 {
		usage.CompletionTokens = envelope.Message.Usage.OutputTokens
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return &usage
}

type anthropicUsageFields struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func usageFromOpenAIUsageRaw(raw json.RawMessage) *relay.Usage {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var usage relay.Usage
	_ = json.Unmarshal(raw, &usage)
	var responsesUsage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	}
	_ = json.Unmarshal(raw, &responsesUsage)
	if usage.PromptTokens == 0 {
		usage.PromptTokens = responsesUsage.InputTokens
	}
	if usage.CompletionTokens == 0 {
		usage.CompletionTokens = responsesUsage.OutputTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = responsesUsage.TotalTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return &usage
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

func supportsGeminiNativeStream(ctx context.Context, apiType relay.APIType, channelType int) bool {
	return supportsGeminiNativeGenerateRequest(ctx, apiType, channelType)
}

func supportsGeminiNativeRequest(ctx context.Context, apiType relay.APIType, channelType int) bool {
	return supportsGeminiNativeGenerateRequest(ctx, apiType, channelType) ||
		supportsGeminiNativeEmbedContentRequest(ctx, apiType, channelType) ||
		supportsGeminiNativeBatchEmbedContentsRequest(ctx, apiType, channelType)
}

func supportsGeminiNativeGenerateRequest(ctx context.Context, apiType relay.APIType, channelType int) bool {
	return apiType == relay.APIChatCompletions &&
		channelType == common.ChannelTypeGemini &&
		strings.EqualFold(relayIngressProtocolFromContext(ctx), inputProtocolGemini)
}

func supportsGeminiNativeEmbedContentRequest(ctx context.Context, apiType relay.APIType, channelType int) bool {
	return apiType == relay.APIEmbeddings &&
		channelType == common.ChannelTypeGemini &&
		strings.EqualFold(relayIngressProtocolFromContext(ctx), inputProtocolGemini) &&
		strings.EqualFold(relayGeminiNativeEmbeddingFromContext(ctx), relayGeminiNativeEmbedContentKind)
}

func supportsGeminiNativeBatchEmbedContentsRequest(ctx context.Context, apiType relay.APIType, channelType int) bool {
	return apiType == relay.APIEmbeddings &&
		channelType == common.ChannelTypeGemini &&
		strings.EqualFold(relayIngressProtocolFromContext(ctx), inputProtocolGemini) &&
		strings.EqualFold(relayGeminiNativeEmbeddingFromContext(ctx), relayGeminiNativeBatchEmbedContentsKind)
}

func supportsAnthropicNativeStream(ctx context.Context, apiType relay.APIType, channelType int) bool {
	return supportsAnthropicNativeRequest(ctx, apiType, channelType)
}

func supportsAnthropicNativeRequest(ctx context.Context, apiType relay.APIType, channelType int) bool {
	return apiType == relay.APIChatCompletions &&
		channelType == common.ChannelTypeClaude &&
		strings.EqualFold(relayIngressProtocolFromContext(ctx), inputProtocolAnthropic)
}

func relayRequestConvertAPIType(ctx context.Context, apiType relay.APIType, channelType int) relay.APIType {
	if supportsGeminiNativeBatchEmbedContentsRequest(ctx, apiType, channelType) {
		return relay.APIGeminiBatchEmbedContents
	}
	if supportsAnthropicNativeRequest(ctx, apiType, channelType) {
		return relay.APIAnthropicMessages
	}
	return apiType
}

func relayNonStreamUpstreamAPIType(ctx context.Context, apiType relay.APIType, channelType int) relay.APIType {
	if supportsGeminiNativeBatchEmbedContentsRequest(ctx, apiType, channelType) {
		return relay.APIGeminiBatchEmbedContents
	}
	return apiType
}

func relayNonStreamResponseAPIType(ctx context.Context, apiType relay.APIType, channelType int) relay.APIType {
	if supportsGeminiNativeBatchEmbedContentsRequest(ctx, apiType, channelType) {
		return relay.APIGeminiBatchEmbedContents
	}
	return apiType
}

func relayStreamUpstreamAPIType(ctx context.Context, apiType relay.APIType, channelType int) relay.APIType {
	if supportsGeminiNativeStream(ctx, apiType, channelType) {
		return relay.APIGeminiStreamGenerateContent
	}
	return apiType
}

func relayStreamOutputProtocol(ctx context.Context, apiType relay.APIType, channelType int) string {
	if supportsGeminiNativeStream(ctx, apiType, channelType) {
		return inputProtocolGemini
	}
	if supportsAnthropicNativeStream(ctx, apiType, channelType) {
		return inputProtocolAnthropic
	}
	return relayIngressProtocolFromAPIType(apiType)
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
		Content:         relayLogRequestBodyFromContext(ctx),
		Response:        relayLogResponseBodyFromContext(ctx),
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
