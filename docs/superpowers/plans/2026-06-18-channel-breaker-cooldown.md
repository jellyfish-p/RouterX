# Channel Breaker Cooldown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow automatically health-blocked channels to re-enter candidate selection after a configured cooldown so a later successful call can reset `error_count`.

**Architecture:** Extend the existing `error_count` based circuit breaker without adding a new status column. `relay.error_ban_cooldown_seconds=0` preserves current behavior; values greater than zero keep recently failed channels blocked, but allow channels whose `updated_at` is older than the cooldown to be selected as a half-open probe candidate.

**Tech Stack:** Go, GORM, existing `ChannelService` candidate selection, settings defaults and validation, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Red Test

**Files:**
- Modify: `internal/service/channel_service_test.go`

- [x] **Step 1: Add cooldown probe selection coverage**

Add `TestChannelBreakerCooldownAllowsProbeAfterWindow` near the existing candidate cache tests. The test should:

```go
db, err := gorm.Open(sqlite.Open("file:channel_service_breaker_cooldown_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
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
}).Error; err != nil {
	t.Fatal(err)
}
freshFailure := time.Now().Add(-30 * time.Second)
cooledFailure := time.Now().Add(-2 * time.Minute)
if err := db.Create([]model.Channel{
	{
		Type: common.ChannelTypeOpenAICompat, Name: "freshly-tripped", Models: "gpt-cooldown",
		BaseURL: "http://fresh.example", APIKey: "fresh-key", Priority: 30, Weight: 1,
		Status: common.ChannelStatusEnabled, ErrorCount: 2, UpdatedAt: freshFailure,
	},
	{
		Type: common.ChannelTypeOpenAICompat, Name: "cooled-probe", Models: "gpt-cooldown",
		BaseURL: "http://cooled.example", APIKey: "cooled-key", Priority: 20, Weight: 1,
		Status: common.ChannelStatusEnabled, ErrorCount: 2, UpdatedAt: cooledFailure,
	},
	{
		Type: common.ChannelTypeOpenAICompat, Name: "healthy-backup", Models: "gpt-cooldown",
		BaseURL: "http://healthy.example", APIKey: "healthy-key", Priority: 10, Weight: 1,
		Status: common.ChannelStatusEnabled, ErrorCount: 0,
	},
}).Error; err != nil {
	t.Fatal(err)
}

svc := NewChannelService()
candidates, reasons, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-cooldown", RoutePreference{})
if err != nil {
	t.Fatal(err)
}
if len(candidates) != 2 || candidates[0].Name != "cooled-probe" || candidates[1].Name != "healthy-backup" {
	t.Fatalf("cooled tripped channel should be allowed as a probe while fresh trip remains blocked, candidates=%+v reasons=%+v", candidates, reasons)
}
if reasons[routeFilterReasonHealthBlocked] != 1 {
	t.Fatalf("fresh tripped channel should still be counted as health_blocked, got %+v", reasons)
}
```

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/service -run "TestChannelBreakerCooldownAllowsProbeAfterWindow" -count=1
```

Expected: FAIL because all channels at or above the threshold are currently filtered regardless of age.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/channel_service.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`

- [x] **Step 1: Extend circuit breaker config**

Add a `cooldown time.Duration` field to `circuitBreakerConfig`. In `circuitBreakerConfig()`, read `relay.error_ban_cooldown_seconds` with `GetInt`. Keep the default at `0` so existing behavior is unchanged unless the setting is present and positive.

- [x] **Step 2: Add the half-open helper**

Add a helper near the candidate selection code:

```go
func channelHealthBlocked(channel model.Channel, breaker circuitBreakerConfig, now time.Time) bool {
	if !breaker.autoBan || channel.ErrorCount < breaker.threshold {
		return false
	}
	if breaker.cooldown <= 0 || channel.UpdatedAt.IsZero() {
		return true
	}
	return now.Sub(channel.UpdatedAt) < breaker.cooldown
}
```

Use it in `SelectChannelCandidatesWithRouteFacts` instead of the direct `ErrorCount >= threshold` check. Capture `now := time.Now()` once before the loop.

- [x] **Step 3: Register the new setting**

Add default setting:

```go
"relay.error_ban_cooldown_seconds": "300",
```

Validate it as a non-negative int. `0` means no automatic half-open probing.

- [x] **Step 4: Verify green**

Run:

```powershell
go test ./internal/service -run "TestChannelBreakerCooldownAllowsProbeAfterWindow" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/SETTINGS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/OPERATIONS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/POLICIES.md`
- Modify: `docs/IMPLEMENTATION.md`
- Modify: `docs/DECISIONS.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document cooldown behavior**

Document `relay.error_ban_cooldown_seconds`, including that `0` disables automatic half-open probing and positive values allow cooled-down tripped channels to re-enter candidate selection.

- [x] **Step 2: Update Apifox settings description**

Update the settings schema/description text that lists relay settings so Apifox imports the new key meaning.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run targeted tests**

```powershell
go test ./internal/service -run "TestChannelBreakerCooldownAllowsProbeAfterWindow" -count=1
go test ./internal/router -run "TestSettingsValidationAndReadiness|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestChatCompletionSkipsTrippedChannelAtConfiguredThreshold|TestChatCompletionHonorsDisabledAutoBanSetting" -count=1
```

- [x] **Step 2: Run full tests**

```powershell
go test ./... -count=1
```

- [x] **Step 3: Validate Apifox YAML**

```powershell
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
```

- [x] **Step 4: Check whitespace**

```powershell
git diff --check
```

- [x] **Step 5: Commit**

```powershell
git add internal/service/channel_service.go internal/service/channel_service_test.go internal/service/setup_service.go internal/service/setting_service.go docs/SETTINGS.md docs/RELAY.md docs/OPERATIONS.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/POLICIES.md docs/IMPLEMENTATION.md docs/DECISIONS.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-channel-breaker-cooldown.md
git commit -m "feat: add channel breaker cooldown"
```
