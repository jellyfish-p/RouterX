package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	UserIdentityMethodUsername = "username"
	UserIdentityMethodEmail    = "email"
	UserIdentityMethodPhone    = "phone"
	UserIdentityMethodOAuth    = "oauth"
	UserIdentityMethodOIDC     = "oidc"

	UserIdentityProviderLocal = "local"
)

// User 用户表。
// 仅保存核心用户资料和主展示信息，登录方式由 UserIdentity 独立扩展。
type User struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	Username    *string        `gorm:"type:varchar(64);index" json:"username,omitempty"`
	DisplayName string         `gorm:"type:varchar(128);not null;default:''" json:"display_name"`
	Email       *string        `gorm:"type:varchar(128);index" json:"email,omitempty"`
	Phone       *string        `gorm:"type:varchar(32);index" json:"phone,omitempty"`
	Role        int            `gorm:"not null;default:0" json:"role"`   // 0=User, 1=Admin, 2=SuperAdmin
	Quota       int64          `gorm:"not null;default:0" json:"quota"`  // 剩余额度 (1/100000000用户额度)
	Status      int            `gorm:"not null;default:1" json:"status"` // 0=禁用, 1=启用
	GroupID     *uint          `json:"group_id"`
	Group       *Group         `gorm:"foreignKey:GroupID" json:"group,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
	Identities  []UserIdentity `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"identities,omitempty"`
	Tokens      []Token        `gorm:"foreignKey:UserID" json:"-"`
	Logs        []Log          `gorm:"foreignKey:UserID" json:"-"`
}

// UserIdentity 用户登录身份表。
// method 支持 username/email/phone/oauth/oidc，provider 区分 local 或第三方提供方。
type UserIdentity struct {
	ID           uint           `gorm:"primaryKey" json:"id"`
	UserID       uint           `gorm:"not null;index;index:idx_user_identities_user_method,priority:1" json:"user_id"`
	User         *User          `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"user,omitempty"`
	Method       string         `gorm:"type:varchar(32);not null;uniqueIndex:idx_user_identities_identity,priority:1;index:idx_user_identities_user_method,priority:2" json:"method"`
	Provider     string         `gorm:"type:varchar(64);not null;default:'local';uniqueIndex:idx_user_identities_identity,priority:2" json:"provider"`
	Identifier   string         `gorm:"type:varchar(256);not null;uniqueIndex:idx_user_identities_identity,priority:3" json:"identifier"`
	PasswordHash string         `gorm:"type:varchar(256)" json:"-"`
	VerifiedAt   *time.Time     `json:"verified_at,omitempty"`
	LastUsedAt   *time.Time     `json:"last_used_at,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}
