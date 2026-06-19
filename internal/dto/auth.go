package dto

// LoginRequest Admin / User 登录请求
type LoginRequest struct {
	Username       string `json:"username"`
	Account        string `json:"account"`
	CredentialType string `json:"credential_type"`
	Password       string `json:"password"`
	CaptchaID      string `json:"captcha_id"`
	CaptchaCode    string `json:"captcha_code"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	Token string    `json:"token,omitempty"` // User端返回JWT; Admin端不返回 (Cookie)
	User  UserBrief `json:"user"`
}

// LoginCodeRequest 请求生成邮箱或手机号登录验证码。
type LoginCodeRequest struct {
	Account string `json:"account" binding:"required"`
}

// LoginCodeResponse 返回可被 /v0/user/login 消费的验证码挑战。
type LoginCodeResponse struct {
	CaptchaID      string `json:"captcha_id"`
	DeliveryMethod string `json:"delivery_method"`
	DebugCode      string `json:"debug_code,omitempty"`
	TTLSeconds     int    `json:"ttl_seconds"`
}

// RegisterRequest 用户注册请求
type RegisterRequest struct {
	Username       string `json:"username" binding:"required,min=3,max=64"`
	Password       string `json:"password" binding:"required,min=6,max=128"`
	DisplayName    string `json:"display_name"`
	Email          string `json:"email"`
	Phone          string `json:"phone"`
	RegisterMethod string `json:"register_method"`
	CaptchaID      string `json:"captcha_id"`
	CaptchaCode    string `json:"captcha_code"`
}

// RegisterCaptchaResponse 注册图片验证码响应。
type RegisterCaptchaResponse struct {
	CaptchaID       string `json:"captcha_id"`
	CaptchaImageSVG string `json:"captcha_image_svg"`
	TTLSeconds      int    `json:"ttl_seconds"`
}

type OAuthRegistrationRequiredResponse struct {
	RegistrationRequired bool   `json:"registration_required"`
	RegistrationTicket   string `json:"registration_ticket"`
	Provider             string `json:"provider"`
	SuggestedUsername    string `json:"suggested_username"`
	Email                string `json:"email,omitempty"`
}

type OAuthRegisterRequest struct {
	RegistrationTicket string `json:"registration_ticket" binding:"required"`
	Username           string `json:"username" binding:"required,min=3,max=64"`
	Password           string `json:"password" binding:"required,min=6,max=128"`
	DisplayName        string `json:"display_name"`
	Email              string `json:"email"`
	CaptchaID          string `json:"captcha_id"`
	CaptchaCode        string `json:"captcha_code"`
}

type OIDCRegistrationRequiredResponse struct {
	RegistrationRequired bool   `json:"registration_required"`
	RegistrationTicket   string `json:"registration_ticket"`
	Provider             string `json:"provider"`
	SuggestedUsername    string `json:"suggested_username"`
	Email                string `json:"email,omitempty"`
}

type OIDCRegisterRequest struct {
	RegistrationTicket string `json:"registration_ticket" binding:"required"`
	Username           string `json:"username" binding:"required,min=3,max=64"`
	Password           string `json:"password" binding:"required,min=6,max=128"`
	DisplayName        string `json:"display_name"`
	Email              string `json:"email"`
	CaptchaID          string `json:"captcha_id"`
	CaptchaCode        string `json:"captcha_code"`
}

// UserBrief 用户简要信息 (脱敏)
type UserBrief struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Role        int    `json:"role"`
	Quota       int64  `json:"quota"`
	Status      int    `json:"status"`
	GroupID     *uint  `json:"group_id,omitempty"`
}

// ChangePasswordRequest 修改密码请求
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6"`
}
