package service

import (
	"routerx/internal"
	"routerx/internal/model"
)

type LogService struct{}

func NewLogService() *LogService {
	return &LogService{}
}

// Record 写入请求日志 (异步/同步可选)。
func (s *LogService) Record(log *model.Log) error {
	// TODO: Phase 3 实现
	_ = internal.DB
	return nil
}

// List 日志分页查询, 支持多维筛选。
func (s *LogService) List(userID, tokenID, channelID *uint, model string, status *int, startTime, endTime string, page, pageSize int) ([]model.Log, int64, error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil, 0, nil
}

// Clear 清空日志 (软删除或 TRUNCATE)。
func (s *LogService) Clear() error {
	// TODO: Phase 4 实现
	_ = internal.DB
	return nil
}

// GetUserStats 用户用量统计 (指定时间段内的调用次数 + 总消耗)。
func (s *LogService) GetUserStats(userID uint, startTime, endTime string) (callCount int64, totalQuota int64, totalTokens int64, err error) {
	// TODO: Phase 5 实现
	_ = internal.DB
	return 0, 0, 0, nil
}

// GetDashboardStats 仪表盘全局统计。
func (s *LogService) GetDashboardStats() (userCount, channelCount, tokenCount, todayCalls, todayQuota, activeChannels int64, err error) {
	// TODO: Phase 4 实现
	_ = internal.DB
	return 0, 0, 0, 0, 0, 0, nil
}
