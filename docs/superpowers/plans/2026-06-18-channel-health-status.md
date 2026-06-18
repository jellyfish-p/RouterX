# Channel Health Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose explicit channel health status in admin channel APIs so operators can see whether a channel is healthy, manually disabled, breaker-tripped, or ready for probe.

**Architecture:** Keep health as a computed response field backed by the existing channel status, `error_count`, and relay breaker settings. `ChannelService` owns the health decision so route filtering, breaker snapshots, probe worker behavior, and admin responses stay aligned. Admin DTOs include the computed summary without adding a database migration.

**Tech Stack:** Go, Gin router integration tests, existing RouterX channel service/DTO handlers, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Health Status API Behavior

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Write the failing test**

Add a router integration test near the channel management and breaker tests:

```go
func TestAdminChannelListIncludesHealthStatus(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	r := newTestRouter(t)

	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := service.NewSettingService().Set("relay.error_auto_ban", "true"); err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("relay.error_ban_threshold", "2"); err != nil {
		t.Fatal(err)
	}
	if err := service.NewSettingService().Set("relay.error_ban_cooldown_seconds", "60"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	channels := []model.Channel{
		{Type: common.ChannelTypeOpenAICompat, Name: "health-ok", Models: "gpt-health", BaseURL: "http://127.0.0.1:9101", ChannelGroup: "default", Status: common.ChannelStatusEnabled, ErrorCount: 0},
		{Type: common.ChannelTypeOpenAICompat, Name: "health-tripped", Models: "gpt-health", BaseURL: "http://127.0.0.1:9102", ChannelGroup: "default", Status: common.ChannelStatusEnabled, ErrorCount: 2, UpdatedAt: now.Add(-10 * time.Second)},
		{Type: common.ChannelTypeOpenAICompat, Name: "health-probing", Models: "gpt-health", BaseURL: "http://127.0.0.1:9103", ChannelGroup: "default", Status: common.ChannelStatusEnabled, ErrorCount: 2, UpdatedAt: now.Add(-2 * time.Minute)},
		{Type: common.ChannelTypeOpenAICompat, Name: "health-disabled", Models: "gpt-health", BaseURL: "http://127.0.0.1:9104", ChannelGroup: "default", Status: common.ChannelStatusManualOff, ErrorCount: 0},
	}
	if err := internal.DB.Create(&channels).Error; err != nil {
		t.Fatal(err)
	}

	resp := performJSON(r, http.MethodGet, "/v0/admin/channel?page_size=20", rootJWT, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("list channels failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data struct {
			Data []struct {
				Name                     string `json:"name"`
				HealthStatus             string `json:"health_status"`
				HealthReason             string `json:"health_reason"`
				CooldownRemainingSeconds int64  `json:"cooldown_remaining_seconds"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	byName := map[string]struct {
		HealthStatus             string
		HealthReason             string
		CooldownRemainingSeconds int64
	}{}
	for _, item := range payload.Data.Data {
		byName[item.Name] = struct {
			HealthStatus             string
			HealthReason             string
			CooldownRemainingSeconds int64
		}{item.HealthStatus, item.HealthReason, item.CooldownRemainingSeconds}
	}
	if got := byName["health-ok"]; got.HealthStatus != "healthy" || got.HealthReason != "ok" {
		t.Fatalf("healthy channel should expose healthy/ok, got %+v", got)
	}
	if got := byName["health-tripped"]; got.HealthStatus != "tripped" || got.HealthReason != "error_count_threshold" || got.CooldownRemainingSeconds <= 0 {
		t.Fatalf("fresh tripped channel should expose tripped with remaining cooldown, got %+v", got)
	}
	if got := byName["health-probing"]; got.HealthStatus != "probing" || got.HealthReason != "cooldown_elapsed" || got.CooldownRemainingSeconds != 0 {
		t.Fatalf("cooled tripped channel should expose probing, got %+v", got)
	}
	if got := byName["health-disabled"]; got.HealthStatus != "disabled" || got.HealthReason != "manual_status" {
		t.Fatalf("manual-off channel should expose disabled/manual_status, got %+v", got)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/router -run TestAdminChannelListIncludesHealthStatus -count=1`

Expected: FAIL because channel list responses do not yet contain `health_status`, `health_reason`, or `cooldown_remaining_seconds`.

### Task 2: Computed Health Summary

**Files:**
- Modify: `internal/service/channel_service.go`
- Modify: `internal/dto/channel.go`
- Modify: `internal/handler/channel_handler.go`

- [x] **Step 1: Add response fields**

Extend `dto.ChannelInfo`:

```go
HealthStatus             string `json:"health_status"`
HealthReason             string `json:"health_reason"`
CooldownRemainingSeconds int64  `json:"cooldown_remaining_seconds,omitempty"`
```

- [x] **Step 2: Add service summary type and method**

Add a public computed summary in `internal/service/channel_service.go`:

```go
type ChannelHealthSummary struct {
	Status                   string
	Reason                   string
	CooldownRemainingSeconds int64
}

func (s *ChannelService) ChannelHealthSummary(channel model.Channel) ChannelHealthSummary {
	breaker := s.circuitBreakerConfig()
	now := time.Now()
	if channel.Status != common.ChannelStatusEnabled {
		return ChannelHealthSummary{Status: "disabled", Reason: "manual_status"}
	}
	if !breaker.autoBan || channel.ErrorCount < breaker.threshold {
		return ChannelHealthSummary{Status: "healthy", Reason: "ok"}
	}
	if channelHealthBlocked(channel, breaker, now) {
		remaining := int64(0)
		if breaker.cooldown > 0 && !channel.UpdatedAt.IsZero() {
			left := breaker.cooldown - now.Sub(channel.UpdatedAt)
			if left > 0 {
				remaining = int64(left.Seconds())
				if remaining == 0 {
					remaining = 1
				}
			}
		}
		return ChannelHealthSummary{Status: "tripped", Reason: "error_count_threshold", CooldownRemainingSeconds: remaining}
	}
	return ChannelHealthSummary{Status: "probing", Reason: "cooldown_elapsed"}
}
```

- [x] **Step 3: Attach the summary in handler responses**

Add handler helpers:

```go
func (h *ChannelHandler) channelInfo(channel *model.Channel) dto.ChannelInfo {
	info := dto.ChannelInfoFromModel(channel)
	if channel == nil || h == nil || h.svc == nil {
		return info
	}
	health := h.svc.ChannelHealthSummary(*channel)
	info.HealthStatus = health.Status
	info.HealthReason = health.Reason
	info.CooldownRemainingSeconds = health.CooldownRemainingSeconds
	return info
}

func (h *ChannelHandler) channelInfos(channels []model.Channel) []dto.ChannelInfo {
	items := make([]dto.ChannelInfo, 0, len(channels))
	for i := range channels {
		items = append(items, h.channelInfo(&channels[i]))
	}
	return items
}
```

Use `h.channelInfos(channels)` in `List`, `h.channelInfo(channel)` in `Create`, and `h.channelInfo(after)` wherever channel detail responses are returned.

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/router -run TestAdminChannelListIncludesHealthStatus -count=1`

Expected: PASS.

### Task 3: Documentation and Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/OPERATIONS.md`
- Modify: `docs/OBSERVABILITY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document response fields**

Update admin channel API docs to state that channel list/create responses include:

```text
health_status: healthy | disabled | tripped | probing
health_reason: ok | manual_status | error_count_threshold | cooldown_elapsed
cooldown_remaining_seconds: positive only when tripped in a cooldown window
```

- [x] **Step 2: Update reliability docs**

Replace the remaining “显式健康状态仍需补齐” wording for WP1-5/P1-C7 with wording that the admin API now exposes computed health status, while deeper persisted health transitions remain future work if needed.

- [x] **Step 3: Update Apifox schema**

Add the three fields to the `ChannelInfo` schema in `docs/apifox/openapi.yaml` with examples and enum values for the string fields.

- [x] **Step 4: Validate docs format**

Run: `bun -e "const fs=require('fs'); const yaml=require('yaml'); yaml.parse(fs.readFileSync('docs/apifox/openapi.yaml','utf8')); console.log('openapi yaml ok')"`

Expected: prints `openapi yaml ok`.

### Task 4: Verification and Commit

**Files:**
- Verify all modified files

- [x] **Step 1: Run focused tests**

Run: `go test ./internal/router -run "TestAdminChannelListIncludesHealthStatus|TestChannelBreakerCooldownAllowsProbeAfterWindow|TestChannelBreakerProbeRecoversCooledTrippedChannel|TestMetricsEndpointIncludesChannelProbeCounters" -count=1`

Expected: PASS.

- [x] **Step 2: Run full backend tests**

Run: `go test ./... -count=1`

Expected: PASS.

- [x] **Step 3: Check diffs**

Run: `git diff --check`

Expected: no output.

- [x] **Step 4: Stage and commit**

Run:

```bash
git add internal/router/router_test.go internal/service/channel_service.go internal/dto/channel.go internal/handler/channel_handler.go docs/API.md docs/RELAY.md docs/OPERATIONS.md docs/OBSERVABILITY.md docs/ROADMAP.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-channel-health-status.md
git commit -m "feat: expose channel health status"
```

Expected: commit succeeds on branch `codex/backend-p0-closure`.
