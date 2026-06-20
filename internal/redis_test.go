package internal

import "testing"

func TestInitRedisSkipsEmptyConfig(t *testing.T) {
	oldRDB := RDB
	RDB = nil
	t.Cleanup(func() {
		if RDB != nil && RDB != oldRDB {
			_ = RDB.Close()
		}
		RDB = oldRDB
	})
	t.Setenv("REDIS_CONN", "")

	if err := InitRedis(); err != nil {
		t.Fatalf("empty REDIS_CONN should be allowed for SQLite single-node mode: %v", err)
	}
	if RDB != nil {
		t.Fatal("empty REDIS_CONN should leave Redis disabled instead of connecting to localhost")
	}
}
