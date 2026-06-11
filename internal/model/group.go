package model

import (
	"time"
)

// Group 用户分组表。
// 控制计费倍率，不同分组享受不同折扣/溢价。
type Group struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	Name      string         `gorm:"type:varchar(64);not null" json:"name"`
	Ratio     float64        `gorm:"not null;default:1.0" json:"ratio"` // 计费倍率, 1.0=原价
	CreatedAt time.Time      `json:"created_at"`
	Users     []User         `gorm:"foreignKey:GroupID" json:"-"`
}

// Group.Default 返回默认分组 (ratio=1.0)。
func (Group) Default() *Group {
	return &Group{ID: 0, Name: "default", Ratio: 1.0}
}
