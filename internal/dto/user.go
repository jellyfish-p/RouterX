package dto

// UserListRequest 用户列表查询参数
type UserListRequest struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	Keyword  string `form:"keyword"` // 模糊搜索用户名/显示名
	Role     *int   `form:"role"`
	Status   *int   `form:"status"`
	GroupID  *uint  `form:"group_id"`
}

// CreateUserRequest 创建用户请求 (Admin)
type CreateUserRequest struct {
	Username    string `json:"username" binding:"required,min=3,max=64"`
	Password    string `json:"password" binding:"required,min=6,max=128"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        int    `json:"role"`
	Quota       int64  `json:"quota"`
	GroupID     *uint  `json:"group_id"`
}

// UpdateUserRequest 编辑用户请求 (Admin)
type UpdateUserRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        *int   `json:"role"`
	Status      *int   `json:"status"`
	GroupID     *uint  `json:"group_id"`
}

// UpdateQuotaRequest 调整用户余额
type UpdateQuotaRequest struct {
	Quota int64 `json:"quota" binding:"required"`
}

// UpdateSelfRequest 用户自助修改个人信息
type UpdateSelfRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}
