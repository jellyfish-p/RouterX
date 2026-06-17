package internal

import (
	"path/filepath"
	"testing"

	"routerx/internal/common"
	"routerx/internal/model"
)

func TestInitLogDBUsesConfiguredDSN(t *testing.T) {
	oldLogDB := LogDB
	LogDB = nil
	path := filepath.Join(t.TempDir(), "routerx-logs.db")
	t.Cleanup(func() {
		if LogDB != nil && LogDB != oldLogDB {
			if sqlDB, err := LogDB.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		LogDB = oldLogDB
	})

	t.Setenv("LOG_SQL_DSN", "sqlite://"+path)

	if err := InitLogDB(); err != nil {
		t.Fatal(err)
	}
	if LogDB == nil {
		t.Fatal("LOG_SQL_DSN should initialize an independent log database")
	}

	if err := LogDB.Create(&model.Log{
		UserID:    1,
		Model:     "gpt-log-init",
		Status:    common.LogStatusSuccess,
		RequestID: "req-log-init",
	}).Error; err != nil {
		t.Fatalf("initialized log DB should contain the logs schema: %v", err)
	}
}
