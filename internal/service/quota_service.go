package service

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"routerx/internal/model"
)

type quotaChange struct {
	UserID         uint
	Amount         int64
	Type           string
	SourceType     string
	SourceID       string
	IdempotencyKey string
	Reason         string
	ActorUserID    *uint
	RequestID      string
}

func applyQuotaChange(tx *gorm.DB, change quotaChange) (int64, int64, error) {
	if change.UserID == 0 || change.Amount == 0 || change.Type == "" || change.SourceType == "" || change.SourceID == "" || change.IdempotencyKey == "" {
		return 0, 0, errors.New("invalid quota change")
	}

	var user model.User
	if err := tx.Select("id", "quota").First(&user, change.UserID).Error; err != nil {
		return 0, 0, err
	}
	balanceBefore := user.Quota
	if change.Amount < 0 && balanceBefore < -change.Amount {
		return 0, 0, errors.New("insufficient user quota")
	}
	balanceAfter := balanceBefore + change.Amount

	res := tx.Model(&model.User{}).
		Where("id = ? AND quota = ?", change.UserID, balanceBefore).
		Update("quota", balanceAfter)
	if res.Error != nil {
		return 0, 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, 0, errors.New("quota balance changed, please retry")
	}

	var requestID *string
	if trimmed := strings.TrimSpace(change.RequestID); trimmed != "" {
		requestID = &trimmed
	}
	record := model.QuotaTransaction{
		UserID:         change.UserID,
		Type:           change.Type,
		Amount:         change.Amount,
		BalanceBefore:  balanceBefore,
		BalanceAfter:   balanceAfter,
		SourceType:     change.SourceType,
		SourceID:       change.SourceID,
		IdempotencyKey: change.IdempotencyKey,
		Reason:         strings.TrimSpace(change.Reason),
		ActorUserID:    change.ActorUserID,
		RequestID:      requestID,
	}
	if err := tx.Create(&record).Error; err != nil {
		return 0, 0, err
	}
	return balanceBefore, balanceAfter, nil
}
