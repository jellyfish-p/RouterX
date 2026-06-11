package dto

// InitStatusResponse 系统初始化状态
type InitStatusResponse struct {
	Initialized bool `json:"initialized"`
}

// SetupInitRequest 首次初始化请求
type SetupInitRequest struct {
	Username    string `json:"username" binding:"required,min=3,max=64"`
	Password    string `json:"password" binding:"required,min=6,max=128"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

// DashboardStats 仪表盘统计数据
type DashboardStats struct {
	UserCount          int64 `json:"user_count"`
	ChannelCount       int64 `json:"channel_count"`
	TokenCount         int64 `json:"token_count"`
	TodayCallCount     int64 `json:"today_call_count"`
	TodayQuotaUsed     int64 `json:"today_quota_used"`
	ActiveChannelCount int64 `json:"active_channel_count"`
}

// PaginatedResult 分页结果
type PaginatedResult struct {
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Data     interface{} `json:"data"`
}
