package service

import (
	"encoding/json"
	"routerx/internal"
	"routerx/internal/model"
)

type SettingService struct{}

func NewSettingService() *SettingService {
	return &SettingService{}
}

// Get 读取一个配置项。
// 优先从 Redis 缓存取，未命中则查 DB 并回填缓存。
func (s *SettingService) Get(key string) (string, error) {
	// TODO: Phase 1/2 实现
	_ = internal.DB
	_ = internal.RDB
	return "", nil
}

// GetInt 读取整数配置。
func (s *SettingService) GetInt(key string) (int, error) {
	v, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	var result int
	err = json.Unmarshal([]byte(v), &result)
	return result, err
}

// GetBool 读取布尔配置。
func (s *SettingService) GetBool(key string) (bool, error) {
	v, err := s.Get(key)
	if err != nil {
		return false, err
	}
	var result bool
	err = json.Unmarshal([]byte(v), &result)
	return result, err
}

// GetAll 获取全量配置 (可选按 category 过滤)。
func (s *SettingService) GetAll(category string) ([]model.Setting, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, nil
}

// Set 写入单个配置项, 写 DB 后刷新 Redis 缓存。
func (s *SettingService) Set(key, value string) error {
	// TODO: Phase 1/2 实现
	_ = internal.DB
	_ = internal.RDB
	return nil
}

// BatchSet 批量更新配置项。
func (s *SettingService) BatchSet(settings map[string]string) error {
	// TODO: Phase 4 实现
	_ = internal.DB
	_ = internal.RDB
	return nil
}

// LoadCache 启动时将全量 settings 加载到 Redis Hash。
func (s *SettingService) LoadCache() error {
	// TODO: Phase 1 实现
	_ = internal.DB
	_ = internal.RDB
	return nil
}
