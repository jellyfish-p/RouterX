package service

import (
	"routerx/internal"
	"routerx/internal/model"
)

type ChannelService struct{}

func NewChannelService() *ChannelService {
	return &ChannelService{}
}

// SelectChannel 根据模型名 + 优先级 + 权重 + 健康状态选择最优下游通道。
// 排除: status!=1, error_count>=threshold, balance=0
// 排序: 优先级降序 + 权重随机 + 响应时间加权
// 详见 DESIGN.md 5.5 节。
func (s *ChannelService) SelectChannel(model string) (*model.Channel, error) {
	// TODO: Phase 3 实现
	_ = internal.DB
	return nil, nil
}

// List 通道分页列表。
func (s *ChannelService) List(page, pageSize int, channelType, status *int) ([]model.Channel, int64, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, 0, nil
}

// GetByID 按 ID 获取通道。
func (s *ChannelService) GetByID(id uint) (*model.Channel, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, nil
}

// Create 创建通道。
func (s *ChannelService) Create(channel *model.Channel) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// Update 编辑通道。
func (s *ChannelService) Update(id uint, updates map[string]interface{}) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// Delete 软删除通道。
func (s *ChannelService) Delete(id uint) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// Test 测试通道连通性：向厂商 API 发探测请求, 记录 response_ms + model_count。
func (s *ChannelService) Test(channelID uint) (bool, int64, int, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return false, 0, 0, nil
}
