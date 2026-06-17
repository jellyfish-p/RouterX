package service

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
)

func TestLogServiceWritesMainFactAndExternalLogDB(t *testing.T) {
	mainDB := newLogServiceTestDB(t, "main")
	logDB := newLogServiceTestDB(t, "external")
	withMainDB(t, mainDB)

	user, token := createLogServiceUserAndToken(t, mainDB)
	tokenID := token.ID
	loggedAt := time.Now().Add(-time.Minute).UTC()

	svc := NewLogServiceWithLogDB(logDB)
	err := svc.Record(&model.Log{
		UserID:        user.ID,
		TokenID:       &tokenID,
		Model:         "gpt-log",
		TotalTokens:   17,
		QuotaUsed:     42,
		UsageSource:   common.LogUsageSourceUpstream,
		Status:        common.LogStatusSuccess,
		IP:            "203.0.113.7",
		UserAgent:     "routerx-test",
		RequestID:     "req-log-db-success",
		CreatedAt:     loggedAt,
		RouteSnapshot: `{"provider":"test"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	var mainCount int64
	if err := mainDB.Model(&model.Log{}).Where("request_id = ?", "req-log-db-success").Count(&mainCount).Error; err != nil {
		t.Fatal(err)
	}
	if mainCount != 1 {
		t.Fatalf("main DB must preserve one billing fact, got %d", mainCount)
	}

	var externalCount int64
	if err := logDB.Model(&model.Log{}).Where("request_id = ?", "req-log-db-success").Count(&externalCount).Error; err != nil {
		t.Fatal(err)
	}
	if externalCount != 1 {
		t.Fatalf("external log DB should receive one diagnostic log, got %d", externalCount)
	}

	var storedToken model.Token
	if err := mainDB.First(&storedToken, token.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.LastUsedAt == nil || storedToken.LastModel != "gpt-log" || storedToken.LastUsedIPHash == "" || storedToken.LastUserAgentHash == "" {
		t.Fatalf("main DB token usage summary should be updated, got %+v", storedToken)
	}
}

func TestLogServiceFallsBackWhenExternalLogDBWriteFails(t *testing.T) {
	mainDB := newLogServiceTestDB(t, "main-fallback")
	logDB := newLogServiceTestDB(t, "external-fallback")
	withMainDB(t, mainDB)

	user, token := createLogServiceUserAndToken(t, mainDB)
	tokenID := token.ID
	sqlDB, err := logDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	svc := NewLogServiceWithLogDB(logDB)
	err = svc.Record(&model.Log{
		UserID:      user.ID,
		TokenID:     &tokenID,
		Model:       "gpt-log-fallback",
		TotalTokens: 9,
		QuotaUsed:   11,
		UsageSource: common.LogUsageSourceUpstream,
		Status:      common.LogStatusSuccess,
		IP:          "203.0.113.9",
		RequestID:   "req-log-db-fallback",
	})
	if err != nil {
		t.Fatalf("external log DB failure should not discard main DB fact: %v", err)
	}

	var mainLog model.Log
	if err := mainDB.Where("request_id = ?", "req-log-db-fallback").First(&mainLog).Error; err != nil {
		t.Fatal(err)
	}
	if mainLog.QuotaUsed != 11 || mainLog.BillingSnapshot == "" {
		t.Fatalf("main DB should keep recoverable billing fact, got %+v", mainLog)
	}
}

func TestLogServiceListsFromExternalLogDBWhenConfigured(t *testing.T) {
	mainDB := newLogServiceTestDB(t, "main-list")
	logDB := newLogServiceTestDB(t, "external-list")
	withMainDB(t, mainDB)

	if err := logDB.Create(&model.Log{
		UserID:    99,
		Model:     "gpt-log-list",
		Status:    common.LogStatusSuccess,
		RequestID: "req-log-db-list",
		CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	logs, total, err := NewLogServiceWithLogDB(logDB).List(nil, nil, nil, "gpt-log-list", nil, "", "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(logs) != 1 || logs[0].RequestID != "req-log-db-list" {
		t.Fatalf("log list should read from external log DB when configured, total=%d logs=%+v", total, logs)
	}
}

func TestLogServiceListFallsBackToMainDBWhenExternalLogDBFails(t *testing.T) {
	mainDB := newLogServiceTestDB(t, "main-list-fallback")
	logDB := newLogServiceTestDB(t, "external-list-fallback")
	withMainDB(t, mainDB)

	if err := mainDB.Create(&model.Log{
		UserID:    100,
		Model:     "gpt-log-list-fallback",
		Status:    common.LogStatusSuccess,
		RequestID: "req-log-db-list-fallback",
		CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := logDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	logs, total, err := NewLogServiceWithLogDB(logDB).List(nil, nil, nil, "gpt-log-list-fallback", nil, "", "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(logs) != 1 || logs[0].RequestID != "req-log-db-list-fallback" {
		t.Fatalf("log list should fall back to main DB facts when external log DB fails, total=%d logs=%+v", total, logs)
	}
}

func newLogServiceTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:log_service_"+name+"_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func withMainDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	oldDB, oldRDB := internal.DB, internal.RDB
	internal.DB = db
	internal.RDB = nil
	t.Cleanup(func() {
		internal.DB = oldDB
		internal.RDB = oldRDB
	})
}

func createLogServiceUserAndToken(t *testing.T, db *gorm.DB) (model.User, model.Token) {
	t.Helper()
	username := "log-user-" + time.Now().Format("150405.000000000")
	user := model.User{
		Username: &username,
		Role:     common.RoleUser,
		Status:   common.UserStatusEnabled,
		Quota:    1000,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{
		UserID:      user.ID,
		Name:        "log-token",
		Key:         "log-token-" + username,
		Status:      common.TokenStatusEnabled,
		RemainQuota: 1000,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	return user, token
}
