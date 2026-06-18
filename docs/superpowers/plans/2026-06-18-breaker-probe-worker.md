# Channel Breaker Probe Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the missing background probe loop that periodically tests cooled-down, tripped channels and updates docs plus Apifox settings documentation.

**Architecture:** Reuse `ChannelService.Test` as the single probe mechanism so manual tests and background recovery update `response_ms`, `error_count`, and candidate cache invalidation consistently. Add a testable one-shot method for cooled tripped channels, then wrap it in a server-started worker controlled by hot relay settings.

**Tech Stack:** Go, GORM, in-memory SQLite service tests, OpenAI-compatible `httptest` upstream, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Add Probe Recovery Test

**Files:**
- Modify: `internal/service/channel_service_test.go`

- [ ] **Step 1: Write the failing service test**

Add imports:

```go
import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)
```

Add this test after `TestChannelBreakerCooldownAllowsProbeAfterWindow`:

```go
func TestChannelBreakerProbeRecoversCooledTrippedChannel(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		if req.URL.Path != "/v1/models" {
			t.Fatalf("probe should call model list endpoint, got %s", req.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-probe"}]}`))
	}))
	defer upstream.Close()

	db, err := gorm.Open(sqlite.Open("file:channel_service_breaker_probe_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Channel{}, &model.Setting{}); err != nil {
		t.Fatal(err)
	}
	oldDB, oldRDB := internal.DB, internal.RDB
	internal.DB = db
	internal.RDB = nil
	t.Cleanup(func() {
		internal.DB = oldDB
		internal.RDB = oldRDB
	})

	if err := db.Create([]model.Setting{
		{Key: "routing.channel_cache.enabled", Value: "false", Category: "routing"},
		{Key: "relay.error_auto_ban", Value: "true", Category: "relay"},
		{Key: "relay.error_ban_threshold", Value: "2", Category: "relay"},
		{Key: "relay.error_ban_cooldown_seconds", Value: "60", Category: "relay"},
		{Key: "relay.error_probe_enabled", Value: "true", Category: "relay"},
	}).Error; err != nil {
		t.Fatal(err)
	}

	cooledFailure := time.Now().Add(-2 * time.Minute)
	freshFailure := time.Now().Add(-30 * time.Second)
	if err := db.Create([]model.Channel{
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "cooled-tripped",
			Models:     "gpt-probe",
			BaseURL:    upstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  cooledFailure,
		},
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "fresh-tripped",
			Models:     "gpt-probe",
			BaseURL:    upstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  freshFailure,
		},
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "healthy",
			Models:     "gpt-probe",
			BaseURL:    upstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 0,
		},
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewChannelService()
	summary, err := svc.ProbeTrippedChannelsOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("breaker probe should not fail: %v", err)
	}
	if summary.Checked != 1 || summary.Succeeded != 1 || summary.Failed != 0 {
		t.Fatalf("probe should only test cooled tripped channel, got %+v", summary)
	}
	if upstreamCalls != 1 {
		t.Fatalf("probe should call upstream once, got %d", upstreamCalls)
	}

	var cooled model.Channel
	if err := db.Where("name = ?", "cooled-tripped").First(&cooled).Error; err != nil {
		t.Fatal(err)
	}
	if cooled.ErrorCount != 0 || cooled.ResponseMs <= 0 {
		t.Fatalf("successful probe should reset cooled channel error_count and response_ms, got %+v", cooled)
	}
	var fresh model.Channel
	if err := db.Where("name = ?", "fresh-tripped").First(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.ErrorCount != 2 {
		t.Fatalf("fresh tripped channel should stay blocked until cooldown elapses, got %+v", fresh)
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```powershell
go test ./internal/service -run TestChannelBreakerProbeRecoversCooledTrippedChannel -count=1
```

Expected: FAIL because `ProbeTrippedChannelsOnce` and its summary type do not exist yet.

### Task 2: Implement Probe Service and Worker

**Files:**
- Modify: `internal/service/channel_service.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`

- [ ] **Step 1: Add probe config and summary types**

In `internal/service/channel_service.go`, add near the existing breaker config types:

```go
type breakerProbeConfig struct {
	enabled  bool
	interval time.Duration
	batchSize int
}

type ChannelProbeSummary struct {
	Checked   int
	Succeeded int
	Failed    int
}
```

- [ ] **Step 2: Add config helpers and one-shot probe**

Add after `channelHealthBlocked`:

```go
func (s *ChannelService) breakerProbeConfig() breakerProbeConfig {
	cfg := breakerProbeConfig{
		enabled:  true,
		interval: time.Minute,
		batchSize: 20,
	}
	if internal.DB == nil {
		return cfg
	}
	settingSvc := NewSettingService()
	if enabled, err := settingSvc.GetBool("relay.error_probe_enabled"); err == nil {
		cfg.enabled = enabled
	}
	if intervalSeconds, err := settingSvc.GetInt("relay.error_probe_interval_seconds"); err == nil {
		cfg.interval = time.Duration(intervalSeconds) * time.Second
	}
	if batchSize, err := settingSvc.GetInt("relay.error_probe_batch_size"); err == nil && batchSize > 0 {
		cfg.batchSize = batchSize
	}
	return cfg
}

func (s *ChannelService) ProbeTrippedChannelsOnce(ctx context.Context, limit int) (ChannelProbeSummary, error) {
	summary := ChannelProbeSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	if internal.DB == nil {
		return summary, nil
	}
	breaker := s.circuitBreakerConfig()
	probeCfg := s.breakerProbeConfig()
	if !probeCfg.enabled || !breaker.autoBan || breaker.threshold <= 0 || breaker.cooldown <= 0 {
		return summary, nil
	}
	if limit <= 0 {
		limit = probeCfg.batchSize
	}
	cutoff := time.Now().Add(-breaker.cooldown)
	var channels []model.Channel
	if err := internal.DB.
		Where("status = ? AND error_count >= ? AND updated_at <= ?", common.ChannelStatusEnabled, breaker.threshold, cutoff).
		Order("updated_at ASC, error_count DESC, id ASC").
		Limit(limit).
		Find(&channels).Error; err != nil {
		return summary, err
	}
	for _, channel := range channels {
		select {
		case <-ctx.Done():
			return summary, ctx.Err()
		default:
		}
		summary.Checked++
		ok, _, _, err := s.Test(channel.ID)
		if ok && err == nil {
			summary.Succeeded++
			continue
		}
		summary.Failed++
	}
	return summary, nil
}
```

- [ ] **Step 3: Add background worker**

Add after `ProbeTrippedChannelsOnce`:

```go
func (s *ChannelService) StartBreakerProbeWorker(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		for {
			cfg := s.breakerProbeConfig()
			if !cfg.enabled || cfg.interval <= 0 {
				cfg.interval = time.Minute
			}
			timer := time.NewTimer(cfg.interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			cfg = s.breakerProbeConfig()
			if !cfg.enabled || cfg.interval <= 0 {
				continue
			}
			_, _ = s.ProbeTrippedChannelsOnce(ctx, cfg.batchSize)
		}
	}()
}
```

This worker waits one interval before the first probe to avoid an immediate startup storm.

- [ ] **Step 4: Start worker from server main**

In `cmd/server/main.go`, after log replication startup:

```go
logSvc.StartLogReplicationWorker(context.Background(), time.Minute, 100)
channelSvc.StartBreakerProbeWorker(context.Background())
```

- [ ] **Step 5: Add settings defaults and validation**

In `internal/service/setup_service.go`, add relay defaults:

```go
"relay.error_probe_enabled":          "true",
"relay.error_probe_interval_seconds": "60",
"relay.error_probe_batch_size":       "20",
```

In `internal/service/setting_service.go`, add `relay.error_probe_enabled` to the boolean setting group, add `relay.error_probe_batch_size` to positive integer validation, and add `relay.error_probe_interval_seconds` to non-negative integer validation.

- [ ] **Step 6: Run service tests and verify GREEN**

Run:

```powershell
go test ./internal/service -run "TestChannelBreakerProbeRecoversCooledTrippedChannel|TestChannelBreakerCooldownAllowsProbeAfterWindow" -count=1
```

Expected: PASS.

### Task 3: Update Docs and Apifox

**Files:**
- Modify: `docs/SETTINGS.md`
- Modify: `docs/OPERATIONS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/POLICIES.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/IMPLEMENTATION.md`
- Modify: `docs/DECISIONS.md`
- Modify: `docs/apifox/openapi.yaml`

- [ ] **Step 1: Document new relay settings**

Record:

```text
relay.error_probe_enabled=true
relay.error_probe_interval_seconds=60
relay.error_probe_batch_size=20
```

Explain that the worker probes only enabled channels whose `error_count` reached `relay.error_ban_threshold` and whose `channels.updated_at` is older than `relay.error_ban_cooldown_seconds`. `relay.error_probe_interval_seconds=0` disables the worker loop. `relay.error_ban_cooldown_seconds=0` keeps manual-only recovery.

- [ ] **Step 2: Replace future-gap wording**

Change “后台探测任务仍需补齐/后续增强” to “后台探测恢复已落地” in the P1 reliability sections, while leaving future items for explicit channel health state and deeper metrics.

- [ ] **Step 3: Add test reference**

Add `TestChannelBreakerProbeRecoversCooledTrippedChannel` to the reliability test matrix.

- [ ] **Step 4: Update Apifox settings schema text and example**

Add the three new setting keys to `docs/apifox/openapi.yaml` setting map description and example so Apifox import exposes them.

### Task 4: Verify and Commit

**Files:**
- All files changed above.

- [ ] **Step 1: Run targeted tests**

```powershell
go test ./internal/service -run "TestChannelBreakerProbeRecoversCooledTrippedChannel|TestChannelBreakerCooldownAllowsProbeAfterWindow" -count=1
go test ./internal/router -run "TestSettingsValidationAndReadiness|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestNoAvailableChannelWritesBreakerSnapshot" -count=1
```

- [ ] **Step 2: Run full test suite**

```powershell
go test ./... -count=1
```

- [ ] **Step 3: Verify OpenAPI YAML parses**

```powershell
bun --version
bun run -e "const fs=require('fs'); const yaml=require('yaml'); yaml.parse(fs.readFileSync('docs/apifox/openapi.yaml','utf8')); console.log('openapi yaml ok')"
```

- [ ] **Step 4: Check diff hygiene**

```powershell
git diff --check
git status --short
```

- [ ] **Step 5: Stage and commit**

```powershell
git add cmd/server/main.go internal/service/channel_service.go internal/service/channel_service_test.go internal/service/setup_service.go internal/service/setting_service.go docs/SETTINGS.md docs/OPERATIONS.md docs/RELAY.md docs/POLICIES.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/IMPLEMENTATION.md docs/DECISIONS.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-breaker-probe-worker.md
git diff --cached --check
git commit -m "feat: add breaker probe worker"
```
