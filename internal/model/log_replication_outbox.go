package model

import "time"

const (
	LogReplicationStatusPending   = "pending"
	LogReplicationStatusCompleted = "completed"
	LogReplicationStatusFailed    = "failed"
)

// LogReplicationOutbox records main-DB log facts that still need to be mirrored to LOG_SQL_DSN.
type LogReplicationOutbox struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	LogID         uint       `gorm:"not null;uniqueIndex" json:"log_id"`
	Status        string     `gorm:"type:varchar(32);not null;default:'pending';index" json:"status"`
	Attempts      int        `gorm:"not null;default:0" json:"attempts"`
	LastError     string     `gorm:"type:text" json:"last_error,omitempty"`
	NextAttemptAt time.Time  `gorm:"not null;index" json:"next_attempt_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
