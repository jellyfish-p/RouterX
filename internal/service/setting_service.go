package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

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
	if internal.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		v, err := internal.RDB.HGet(ctx, "settings", key).Result()
		if err == nil {
			return v, nil
		}
		if err != redis.Nil {
			// Redis 是可降级依赖，继续回落 DB。
		}
	}

	var setting model.Setting
	if err := internal.DB.Where("key = ?", key).First(&setting).Error; err != nil {
		return "", err
	}
	if internal.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = internal.RDB.HSet(ctx, "settings", key, setting.Value).Err()
	}
	return setting.Value, nil
}

// GetInt 读取整数配置。
func (s *SettingService) GetInt(key string) (int, error) {
	v, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n, nil
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
	var settings []model.Setting
	query := internal.DB.Model(&model.Setting{}).Order("category ASC, key ASC")
	if category != "" {
		query = query.Where("category = ?", category)
	}
	if err := query.Find(&settings).Error; err != nil {
		return nil, err
	}
	return settings, nil
}

// Set 写入单个配置项, 写 DB 后刷新 Redis 缓存。
func (s *SettingService) Set(key, value string) error {
	if key == "" {
		return errors.New("setting key is required")
	}
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var setting model.Setting
		err := tx.Where("key = ?", key).First(&setting).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(&model.Setting{Key: key, Value: value, Category: categoryFromKey(key)}).Error
		case err != nil:
			return err
		default:
			return tx.Model(&setting).Updates(map[string]interface{}{
				"value":    value,
				"category": categoryFromKey(key),
			}).Error
		}
	})
	if err != nil {
		return err
	}
	if internal.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = internal.RDB.HSet(ctx, "settings", key, value).Err()
	}
	return nil
}

// BatchSet 批量更新配置项。
func (s *SettingService) BatchSet(settings map[string]string) error {
	return internal.DB.Transaction(func(tx *gorm.DB) error {
		for key, value := range settings {
			if key == "" {
				continue
			}
			var setting model.Setting
			err := tx.Where("key = ?", key).First(&setting).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(&model.Setting{Key: key, Value: value, Category: categoryFromKey(key)}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if err := tx.Model(&setting).Updates(map[string]interface{}{
				"value":    value,
				"category": categoryFromKey(key),
			}).Error; err != nil {
				return err
			}
		}
		if internal.RDB != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for key, value := range settings {
				_ = internal.RDB.HSet(ctx, "settings", key, value).Err()
			}
		}
		return nil
	})
}

// LoadCache 启动时将全量 settings 加载到 Redis Hash。
func (s *SettingService) LoadCache() error {
	if internal.RDB == nil {
		return nil
	}
	var settings []model.Setting
	if err := internal.DB.Find(&settings).Error; err != nil {
		return err
	}
	values := make(map[string]interface{}, len(settings))
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	if len(values) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return internal.RDB.HSet(ctx, "settings", values).Err()
}

func categoryFromKey(key string) string {
	for i, ch := range key {
		if ch == '.' && i > 0 {
			return key[:i]
		}
	}
	if key == "" {
		return "general"
	}
	return "general"
}
