package service

import (
	"context"
	"errors"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/relay"
	"sort"
	"strings"
	"time"
)

type ChannelService struct{}

func NewChannelService() *ChannelService {
	return &ChannelService{}
}

// SelectChannel 根据模型名 + 优先级 + 权重 + 健康状态选择最优下游通道。
// 排除: status!=1, error_count>=threshold, balance=0
// 排序: 优先级降序 + 权重随机 + 响应时间加权
// 详见 DESIGN.md 5.5 节。
func (s *ChannelService) SelectChannel(modelName string) (*model.Channel, error) {
	modelName = strings.TrimSpace(modelName)
	var channels []model.Channel
	if err := internal.DB.Where("status = ? AND error_count < ?", common.ChannelStatusEnabled, 10).
		Order("priority DESC, error_count ASC, response_ms ASC, id ASC").
		Find(&channels).Error; err != nil {
		return nil, err
	}
	for _, channel := range channels {
		if channelSupportsModel(channel.Models, modelName) {
			return &channel, nil
		}
	}
	return nil, errors.New("no available channel")
}

// List 通道分页列表。
func (s *ChannelService) List(page, pageSize int, channelType, status *int) ([]model.Channel, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	query := internal.DB.Model(&model.Channel{})
	if channelType != nil {
		query = query.Where("type = ?", *channelType)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var channels []model.Channel
	err := query.Order("priority DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&channels).Error
	return channels, total, err
}

// GetByID 按 ID 获取通道。
func (s *ChannelService) GetByID(id uint) (*model.Channel, error) {
	var channel model.Channel
	if err := internal.DB.First(&channel, id).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

// Create 创建通道。
func (s *ChannelService) Create(channel *model.Channel) error {
	if channel == nil {
		return errors.New("channel is required")
	}
	channel.Name = strings.TrimSpace(channel.Name)
	channel.Models = normalizeModels(channel.Models)
	channel.BaseURL = normalizeBaseURL(channel.BaseURL, channel.Type)
	if channel.Name == "" || channel.Models == "" || strings.TrimSpace(channel.APIKey) == "" {
		return errors.New("name, models and api_key are required")
	}
	if channel.Weight <= 0 {
		channel.Weight = 1
	}
	if channel.Status == 0 {
		channel.Status = common.ChannelStatusEnabled
	}
	encrypted, err := common.EncryptSecret(strings.TrimSpace(channel.APIKey))
	if err != nil {
		return err
	}
	channel.APIKey = encrypted
	return internal.DB.Create(channel).Error
}

// Update 编辑通道。
func (s *ChannelService) Update(id uint, updates map[string]interface{}) error {
	allowed := filterUpdates(updates, "name", "models", "base_url", "api_key", "priority", "weight", "status")
	if len(allowed) == 0 {
		return nil
	}
	if v, ok := allowed["models"].(string); ok {
		allowed["models"] = normalizeModels(v)
	}
	if v, ok := allowed["base_url"].(string); ok {
		allowed["base_url"] = strings.TrimRight(strings.TrimSpace(v), "/")
	}
	if v, ok := allowed["api_key"].(string); ok {
		v = strings.TrimSpace(v)
		if v == "" {
			delete(allowed, "api_key")
		} else {
			encrypted, err := common.EncryptSecret(v)
			if err != nil {
				return err
			}
			allowed["api_key"] = encrypted
		}
	}
	if v, ok := allowed["weight"].(int); ok && v <= 0 {
		allowed["weight"] = 1
	}
	return internal.DB.Model(&model.Channel{}).Where("id = ?", id).Updates(allowed).Error
}

// Delete 软删除通道。
func (s *ChannelService) Delete(id uint) error {
	return internal.DB.Delete(&model.Channel{}, id).Error
}

// Test 测试通道连通性：向厂商 API 发探测请求, 记录 response_ms + model_count。
func (s *ChannelService) Test(channelID uint) (bool, int64, int, error) {
	channel, err := s.GetByID(channelID)
	if err != nil {
		return false, 0, 0, err
	}
	adapter, ok := relay.GetAdapter(channel.Type)
	if !ok {
		return false, 0, 0, errors.New("unsupported channel type")
	}
	apiKey, err := common.DecryptSecret(channel.APIKey)
	if err != nil {
		return false, 0, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	models, err := adapter.GetModelList(ctx, channel.BaseURL, apiKey)
	responseMs := time.Since(start).Milliseconds()
	if err != nil {
		_ = internal.DB.Model(channel).Updates(map[string]interface{}{
			"response_ms": responseMs,
			"error_count": channel.ErrorCount + 1,
		}).Error
		return false, responseMs, 0, err
	}
	_ = internal.DB.Model(channel).Updates(map[string]interface{}{
		"response_ms": responseMs,
		"error_count": 0,
	}).Error
	return true, responseMs, len(models), nil
}

func (s *ChannelService) ListModels() ([]string, error) {
	var channels []model.Channel
	if err := internal.DB.Where("status = ?", common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, channel := range channels {
		for _, modelName := range splitModels(channel.Models) {
			if modelName != "*" && modelName != "" {
				seen[modelName] = struct{}{}
			}
		}
	}
	models := make([]string, 0, len(seen))
	for modelName := range seen {
		models = append(models, modelName)
	}
	sort.Strings(models)
	return models, nil
}

func channelSupportsModel(models, modelName string) bool {
	if modelName == "" {
		return false
	}
	for _, candidate := range splitModels(models) {
		if candidate == "*" || candidate == modelName {
			return true
		}
	}
	return false
}

func normalizeModels(models string) string {
	return strings.Join(splitModels(models), ",")
}

func splitModels(models string) []string {
	parts := strings.Split(models, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		modelName := strings.TrimSpace(part)
		if modelName == "" {
			continue
		}
		if _, ok := seen[modelName]; ok {
			continue
		}
		seen[modelName] = struct{}{}
		result = append(result, modelName)
	}
	return result
}

func normalizeBaseURL(baseURL string, channelType int) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" {
		return baseURL
	}
	switch channelType {
	case common.ChannelTypeOpenAI, common.ChannelTypeOpenAICompat:
		return "https://api.openai.com"
	case common.ChannelTypeDeepSeek:
		return "https://api.deepseek.com"
	case common.ChannelTypeQwen:
		return "https://dashscope.aliyuncs.com/compatible-mode"
	default:
		return baseURL
	}
}
