# Breaker Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a stable `breaker_snapshot` summary to no-available-channel rejection logs when all usable relay candidates are blocked by the `error_count` circuit breaker.

**Architecture:** Keep the existing `error_count` based candidate filtering and cooldown behavior unchanged. Extend channel selection facts so Relay can see which channels were health-blocked, then add a top-level `breaker_snapshot` object beside `scope_result` in the existing `policy_snapshot` JSON for no-candidate denials. The snapshot must be diagnostic only: redacted, bounded, and not used for routing decisions.

**Tech Stack:** Go, Gin router integration tests, GORM channel selection, RelayService policy snapshots, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Red Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add breaker snapshot rejection coverage**

Add `TestNoAvailableChannelWritesBreakerSnapshot` near the existing breaker tests. It should:

1. Set `relay.error_auto_ban=true`, `relay.error_ban_threshold=1`, and `relay.error_ban_cooldown_seconds=300`.
2. Create a user token with enough quota.
3. Create one enabled OpenAI-compatible channel for `gpt-breaker-snapshot`.
4. Set that channel `error_count=3` and `updated_at=now` so it is still inside the cooldown window.
5. Call `/v1/chat/completions`.
6. Assert the response is `502` with `no_available_channel`.
7. Assert the upstream server was not called.
8. Assert the failed log `policy_snapshot` contains:

```json
{
  "scope_result": {
    "route_candidate": "deny"
  },
  "breaker_snapshot": {
    "decision": "deny",
    "reason": "health_blocked",
    "auto_ban": true,
    "threshold": 1,
    "cooldown_seconds": 300,
    "blocked_channel_count": 1,
    "blocked_channels": [
      {
        "channel_id": "<created channel id>",
        "provider": "openai-compatible",
        "channel_group": "default",
        "error_count": 3
      }
    ]
  }
}
```

Also assert `cooldown_remaining_seconds` is positive and no larger than 300.

- [x] **Step 2: Verify red**

```powershell
go test ./internal/router -run "TestNoAvailableChannelWritesBreakerSnapshot" -count=1
```

Expected: FAIL because no-candidate policy snapshots do not yet include `breaker_snapshot`.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/channel_service.go`
- Modify: `internal/service/relay_service.go`

- [x] **Step 1: Return detailed channel selection facts**

Add a small service-level facts type:

```go
type RouteSelectionFacts struct {
	FilteredReasons map[string]int
	BreakerSnapshot map[string]interface{}
}
```

Add `SelectChannelCandidatesWithRouteDetailedFacts(modelName string, route RoutePreference) ([]model.Channel, RouteSelectionFacts, error)`. Keep `SelectChannelCandidatesWithRouteFacts` as a compatibility wrapper returning `facts.FilteredReasons`.

- [x] **Step 2: Capture health-blocked channel summaries**

While iterating channels, when `channelHealthBlocked(channel, breaker, now)` is true:

```go
facts.addHealthBlockedChannel(channel, breaker, now)
```

The snapshot should include `decision=deny`, `reason=health_blocked`, `auto_ban`, `threshold`, `cooldown_seconds`, `blocked_channel_count`, and a bounded `blocked_channels` list with `channel_id`, `provider`, `channel_group`, `error_count`, `updated_at`, and `cooldown_remaining_seconds`.

- [x] **Step 3: Add breaker snapshot to no-candidate policy denials**

Change `buildRelayNoAvailableChannelPolicySnapshot` to accept a breaker snapshot map:

```go
func buildRelayNoAvailableChannelPolicySnapshot(ctx context.Context, token *model.Token, breakerSnapshot map[string]interface{}) string
```

It should preserve the existing `scope_result` and add top-level `breaker_snapshot` only when the map is non-empty.

- [x] **Step 4: Use detailed facts from Relay**

In both non-stream and stream candidate selection paths, call `SelectChannelCandidatesWithRouteDetailedFacts`, merge `facts.FilteredReasons`, and pass `facts.BreakerSnapshot` into the no-available-channel policy snapshot.

- [x] **Step 5: Verify green**

```powershell
go test ./internal/router -run "TestNoAvailableChannelWritesBreakerSnapshot" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/POLICIES.md`
- Modify: `docs/SNAPSHOTS.md`
- Modify: `docs/OBSERVABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Update current snapshot coverage**

Document that health-blocked no-candidate denials now write `breaker_snapshot` with circuit breaker config and blocked channel summaries.

- [x] **Step 2: Update reliability evidence**

Add `TestNoAvailableChannelWritesBreakerSnapshot` to `docs/TESTING.md` and update P1-C7/WP1-5 wording so only background probing remains future work, not complete breaker snapshots.

- [x] **Step 3: Update Apifox**

Update the `policy_snapshot` schema description to mention `breaker_snapshot` for circuit-breaker no-candidate denials.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run targeted tests**

```powershell
go test ./internal/router -run "TestNoAvailableChannelWritesBreakerSnapshot|TestChatCompletionSkipsTrippedChannelAtConfiguredThreshold|TestChatCompletionHonorsDisabledAutoBanSetting|TestChannelBreakerCooldownAllowsProbeAfterWindow|TestRouterXRequestFieldIsIgnoredForRoutingAndStripped" -count=1
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
git add internal/router/router_test.go internal/service/channel_service.go internal/service/relay_service.go docs/POLICIES.md docs/SNAPSHOTS.md docs/OBSERVABILITY.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/RELAY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-breaker-snapshot.md
git commit -m "feat: snapshot breaker denials"
```
