package service

import (
	"routerx/internal"
	"routerx/internal/model"
	"routerx/internal/relay"
)

type RelayService struct {
	channelService *ChannelService
}

func NewRelayService(ch *ChannelService) *RelayService {
	return &RelayService{channelService: ch}
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
func (s *RelayService) RelayChatCompletion(token *model.Token, body []byte) ([]byte, *relay.Usage, error) {
	// TODO: Phase 3 实现
	_ = internal.DB
	return nil, nil, nil
}

// RelayCompletions 转发 Text Completions (Legacy)。
func (s *RelayService) RelayCompletions(token *model.Token, body []byte) ([]byte, *relay.Usage, error) {
	// TODO: Phase 6 实现
	return nil, nil, nil
}

// GetAdapter 根据通道类型返回对应的适配器实例。
func (s *RelayService) GetAdapter(channelType int) (relay.Adapter, error) {
	// TODO: Phase 3 实现 (OpenAI), Phase 7 实现 (Azure/Claude/...)
	return nil, nil
}

// Relay 通用转发入口。
func (s *RelayService) Relay(token *model.Token, apiType relay.APIType, body []byte) ([]byte, *relay.Usage, error) {
	// TODO: Phase 6 实现
	return nil, nil, nil
}
