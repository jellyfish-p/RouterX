package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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

func NewRelayService(ch *ChannelService, tokenSvc *TokenService, logSvc *LogService, settingSvc *SettingService) *RelayService {
	return &RelayService{channelService: ch, tokenService: tokenSvc, logService: logSvc, settingService: settingSvc}
}

type HTTPError struct {
	Status  int
	Message string
	Type    string
	Code    string
}

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
	if token == nil {
		return nil, nil, &HTTPError{Status: 401, Message: "invalid api key", Type: "authentication_error", Code: "invalid_api_key"}
	}
	reqInfo, err := parseRelayRequest(apiType, body)
	if err != nil {
		return nil, nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
	}
	if reqInfo.Stream {
		return nil, nil, &HTTPError{Status: 400, Message: "stream is not supported in P0 relay", Type: "invalid_request_error", Code: "unsupported_stream"}
	}
	if !s.tokenService.HasAvailableQuota(token) {
		_ = s.recordLog(token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, "insufficient quota", clientIP)
		return nil, nil, &HTTPError{Status: 429, Message: "insufficient quota", Type: "insufficient_quota", Code: "insufficient_quota"}
	}

	channel, err := s.channelService.SelectChannel(reqInfo.Model)
	if err != nil {
		_ = s.recordLog(token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, "no available channel", clientIP)
		return nil, nil, &HTTPError{Status: 502, Message: "no available upstream channel", Type: "upstream_error", Code: "no_available_channel"}
	}
	adapter, err := s.GetAdapter(channel.Type)
	if err != nil {
		_ = s.recordLog(token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, nil, &HTTPError{Status: 502, Message: "unsupported upstream channel", Type: "upstream_error", Code: "unsupported_channel"}
	}
	apiKey, err := common.DecryptSecret(channel.APIKey)
	if err != nil {
		_ = s.recordLog(token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream secret decrypt failed", clientIP)
		return nil, nil, &HTTPError{Status: 502, Message: "upstream channel secret is not available", Type: "upstream_error", Code: "upstream_secret_error"}
	}

	outBody, err := adapter.ConvertRequest(apiType, body)
	if err != nil {
		return nil, nil, &HTTPError{Status: 400, Message: "invalid request body", Type: "invalid_request_error", Code: "invalid_json"}
	}
	endpoint := adapter.GetAPIEndpoint(apiType, reqInfo.Model)
	timeout := s.relayTimeout()
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	resp, err := adapter.DoRequest(reqCtx, channel.BaseURL, endpoint, apiKey, outBody)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		_ = s.markChannelFailure(channel, latencyMs)
		_ = s.recordLog(token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream request failed", clientIP)
		return nil, nil, &HTTPError{Status: 502, Message: "upstream request failed", Type: "upstream_error", Code: "upstream_request_failed"}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		_ = s.markChannelFailure(channel, latencyMs)
		_ = s.recordLog(token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream response read failed", clientIP)
		return nil, nil, &HTTPError{Status: 502, Message: "upstream response read failed", Type: "upstream_error", Code: "upstream_response_failed"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = s.markChannelFailure(channel, latencyMs)
		message := fmt.Sprintf("upstream returned status %d", resp.StatusCode)
		_ = s.recordLog(token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, message, clientIP)
		status := 502
		if resp.StatusCode == 429 {
			status = 429
		}
		return nil, nil, &HTTPError{Status: status, Message: message, Type: "upstream_error", Code: fmt.Sprintf("upstream_%d", resp.StatusCode)}
	}

	converted, usage, err := adapter.ConvertResponse(apiType, respBody)
	if err != nil {
		_ = s.markChannelFailure(channel, latencyMs)
		_ = s.recordLog(token, channel, reqInfo.Model, nil, common.LogStatusFailed, 0, "upstream response conversion failed", clientIP)
		return nil, nil, &HTTPError{Status: 502, Message: "upstream response conversion failed", Type: "upstream_error", Code: "upstream_conversion_failed"}
	}
	quotaUsed := quotaFromUsage(usage)
	if err := s.tokenService.DeductQuota(token.ID, quotaUsed); err != nil {
		_ = s.recordLog(token, channel, reqInfo.Model, usage, common.LogStatusFailed, 0, err.Error(), clientIP)
		return nil, usage, &HTTPError{Status: 429, Message: "insufficient quota", Type: "insufficient_quota", Code: "insufficient_quota"}
	}
	_ = s.markChannelSuccess(channel, latencyMs)
	_ = s.recordLog(token, channel, reqInfo.Model, usage, common.LogStatusSuccess, quotaUsed, "", clientIP)
	return converted, usage, nil
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

type relayRequestInfo struct {
	Model  string
	Stream bool
}

func parseRelayRequest(apiType relay.APIType, body []byte) (relayRequestInfo, error) {
	if apiType == relay.APIModels {
		return relayRequestInfo{}, nil
	}
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return relayRequestInfo{}, errors.New("invalid json body")
	}
	payload.Model = strings.TrimSpace(payload.Model)
	if payload.Model == "" {
		return relayRequestInfo{}, errors.New("model is required")
	}
	return relayRequestInfo{Model: payload.Model, Stream: payload.Stream}, nil
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

func quotaFromUsage(usage *relay.Usage) int64 {
	if usage == nil || usage.TotalTokens <= 0 {
		return 1
	}
	return int64(usage.TotalTokens)
}

func (s *RelayService) recordLog(token *model.Token, channel *model.Channel, modelName string, usage *relay.Usage, status int, quotaUsed int64, errMsg, ip string) error {
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
		UserID:    token.UserID,
		TokenID:   tokenID,
		ChannelID: channelID,
		Model:     modelName,
		Status:    status,
		QuotaUsed: quotaUsed,
		ErrorMsg:  errMsg,
		IP:        ip,
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
	return internal.DB.Model(channel).Updates(map[string]interface{}{
		"response_ms": responseMs,
		"error_count": channel.ErrorCount + 1,
	}).Error
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
