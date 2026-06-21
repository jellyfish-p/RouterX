package service

import (
	"context"
	"encoding/json"
	"net/http"
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

func TestLogServiceCompletesExistingErrorSnapshotDiagnostics(t *testing.T) {
	mainDB := newLogServiceTestDB(t, "existing-error-snapshot")
	withMainDB(t, mainDB)

	user, token := createLogServiceUserAndToken(t, mainDB)
	tokenID := token.ID
	loggedAt := time.Date(2026, 6, 20, 4, 10, 0, 0, time.UTC)

	err := NewLogServiceWithLogDB(mainDB).Record(&model.Log{
		UserID:        user.ID,
		TokenID:       &tokenID,
		Model:         "gpt-existing-error-snapshot",
		Status:        common.LogStatusFailed,
		ErrorCode:     "route_forbidden",
		ErrorSource:   common.LogErrorSourceRoute,
		ErrorMsg:      "channel group is not allowed by api key scope",
		RequestID:     "req-existing-error-snapshot",
		CreatedAt:     loggedAt,
		ErrorSnapshot: `{"custom_note":"kept"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	var stored model.Log
	if err := mainDB.Where("request_id = ?", "req-existing-error-snapshot").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(stored.ErrorSnapshot), &snapshot); err != nil {
		t.Fatalf("existing error snapshot should remain valid JSON, got %q: %v", stored.ErrorSnapshot, err)
	}
	if snapshot["custom_note"] != "kept" ||
		snapshot["schema"] != "routerx.snapshot.v1" ||
		snapshot["kind"] != "error" ||
		snapshot["source"] != "relay" ||
		snapshot["redacted"] != true ||
		snapshot["request_id"] != "req-existing-error-snapshot" ||
		snapshot["created_at"] != "2026-06-20T04:10:00Z" ||
		snapshot["error_code"] != "route_forbidden" ||
		snapshot["error_source"] != common.LogErrorSourceRoute ||
		snapshot["called_upstream"] != false ||
		snapshot["http_status"] != float64(http.StatusForbidden) ||
		snapshot["retryable"] != false ||
		snapshot["charged"] != false ||
		snapshot["safe_message"] != "channel group is not allowed by api key scope" {
		t.Fatalf("existing error snapshot should be completed without losing custom fields: %+v", snapshot)
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

	var pending model.LogReplicationOutbox
	if err := mainDB.Where("log_id = ?", mainLog.ID).First(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != model.LogReplicationStatusPending || pending.Attempts != 1 || pending.LastError == "" {
		t.Fatalf("external log DB failure should leave a retryable outbox item, got %+v", pending)
	}
}

func TestLogServiceReplaysPendingExternalLogOutbox(t *testing.T) {
	mainDB := newLogServiceTestDB(t, "main-replay")
	failedLogDB := newLogServiceTestDB(t, "external-replay-failed")
	withMainDB(t, mainDB)

	user, token := createLogServiceUserAndToken(t, mainDB)
	tokenID := token.ID
	sqlDB, err := failedLogDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	svc := NewLogServiceWithLogDB(failedLogDB)
	if err := svc.Record(&model.Log{
		UserID:      user.ID,
		TokenID:     &tokenID,
		Model:       "gpt-log-replay",
		TotalTokens: 5,
		QuotaUsed:   13,
		UsageSource: common.LogUsageSourceUpstream,
		Status:      common.LogStatusSuccess,
		RequestID:   "req-log-db-replay",
	}); err != nil {
		t.Fatalf("external log DB failure should not fail main record: %v", err)
	}

	recoveredLogDB := newLogServiceTestDB(t, "external-replay-recovered")
	replaySvc := NewLogServiceWithLogDB(recoveredLogDB)
	replayed, err := replaySvc.ReplayLogReplicationOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != 1 {
		t.Fatalf("expected one pending log to replay, got %d", replayed)
	}

	var externalLog model.Log
	if err := recoveredLogDB.Where("request_id = ?", "req-log-db-replay").First(&externalLog).Error; err != nil {
		t.Fatal(err)
	}
	if externalLog.QuotaUsed != 13 || externalLog.Model != "gpt-log-replay" {
		t.Fatalf("external log DB should receive replayed log fact, got %+v", externalLog)
	}

	var outbox model.LogReplicationOutbox
	if err := mainDB.Where("log_id = ?", externalLog.ID).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if outbox.Status != model.LogReplicationStatusCompleted || outbox.CompletedAt == nil {
		t.Fatalf("successful replay should mark outbox completed, got %+v", outbox)
	}
}

func TestLogServiceWorkerReplaysPendingExternalLogOutbox(t *testing.T) {
	mainDB := newLogServiceTestDB(t, "main-worker")
	logDB := newLogServiceTestDB(t, "external-worker")
	withMainDB(t, mainDB)

	user, token := createLogServiceUserAndToken(t, mainDB)
	tokenID := token.ID
	if err := mainDB.Create(&model.Log{
		UserID:      user.ID,
		TokenID:     &tokenID,
		Model:       "gpt-log-worker",
		TotalTokens: 3,
		QuotaUsed:   8,
		UsageSource: common.LogUsageSourceUpstream,
		Status:      common.LogStatusSuccess,
		RequestID:   "req-log-db-worker",
		CreatedAt:   time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	var entry model.Log
	if err := mainDB.Where("request_id = ?", "req-log-db-worker").First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := mainDB.Create(&model.LogReplicationOutbox{
		LogID:         entry.ID,
		Status:        model.LogReplicationStatusPending,
		NextAttemptAt: time.Now().Add(-time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	NewLogServiceWithLogDB(logDB).StartLogReplicationWorker(ctx, 10*time.Millisecond, 10)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var externalCount int64
		if err := logDB.Model(&model.Log{}).Where("request_id = ?", "req-log-db-worker").Count(&externalCount).Error; err != nil {
			t.Fatal(err)
		}
		if externalCount == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("log replication worker should replay pending outbox item")
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

	logs, total, err := NewLogServiceWithLogDB(logDB).List(nil, nil, nil, "gpt-log-list", nil, "", "", nil, "", "", 1, 20)
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

	logs, total, err := NewLogServiceWithLogDB(logDB).List(nil, nil, nil, "gpt-log-list-fallback", nil, "", "", nil, "", "", 1, 20)
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
	if err := db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.LogReplicationOutbox{}); err != nil {
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
		UserID:     user.ID,
		Name:       "log-token",
		Key:        "log-token-" + username,
		Status:     common.TokenStatusEnabled,
		QuotaLimit: 1000,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	return user, token
}
