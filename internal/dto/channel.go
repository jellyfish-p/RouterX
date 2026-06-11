package dto

// ChannelListRequest 通道列表查询
type ChannelListRequest struct {
	Page     int   `form:"page" binding:"omitempty,min=1"`
	PageSize int   `form:"page_size" binding:"omitempty,min=1,max=100"`
	Type     *int  `form:"type"`   // 厂商类型筛选
	Status   *int  `form:"status"` // 状态筛选
}

// CreateChannelRequest 创建通道
type CreateChannelRequest struct {
	Type     int    `json:"type" binding:"required,min=1"`
	Name     string `json:"name" binding:"required,max=64"`
	Models   string `json:"models" binding:"required"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key" binding:"required"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}

// UpdateChannelRequest 编辑通道
type UpdateChannelRequest struct {
	Name     *string `json:"name"`
	Models   *string `json:"models"`
	BaseURL  *string `json:"base_url"`
	APIKey   *string `json:"api_key"`
	Priority *int    `json:"priority"`
	Weight   *int    `json:"weight"`
	Status   *int    `json:"status"`
}

// TestChannelResult 通道连通性测试结果
type TestChannelResult struct {
	Success     bool   `json:"success"`
	ResponseMs  int64  `json:"response_ms"`
	Error       string `json:"error,omitempty"`
	ModelCount  int    `json:"model_count,omitempty"`
}
