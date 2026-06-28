package main

import (
	"errors"
	"testing"
)

func TestRedisInitFailureIsFatalOnlyForExternalDatabaseMode(t *testing.T) {
	err := errors.New("redis unavailable")

	t.Setenv("SQL_DSN", "sqlite://data/routerx.db")
	if redisInitFailureIsFatal(err) {
		t.Fatal("sqlite mode should not treat Redis init failure as fatal")
	}

	t.Setenv("SQL_DSN", "")
	if redisInitFailureIsFatal(err) {
		t.Fatal("default sqlite mode should not treat Redis init failure as fatal")
	}

	t.Setenv("SQL_DSN", "postgres://routerx:secret@db.example/routerx?sslmode=disable")
	if !redisInitFailureIsFatal(err) {
		t.Fatal("external database mode must treat Redis init failure as fatal")
	}

	if redisInitFailureIsFatal(nil) {
		t.Fatal("nil Redis init error must not be fatal")
	}
}
