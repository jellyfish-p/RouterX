# Rate Limit Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a stable `rate_limit_snapshot` summary to local rate-limit rejection logs so global/IP/Token/User/Model/Channel denials explain the limit that fired.

**Architecture:** Keep existing Redis fixed-minute counters and rejection behavior. Return the counter value from rate-limit helpers, then add a top-level `rate_limit_snapshot` object beside `scope_result` in the existing `policy_snapshot` JSON. Middleware dimensions use the authenticated token already placed in Gin context; Relay model/channel dimensions reuse the same snapshot builder inside `RelayService`.

**Tech Stack:** Go, Gin middleware/router tests, RelayService, TokenService, Redis fake server, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Red Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add global/IP snapshot assertions**

Add `TestRateLimitGlobalAndIPWriteSnapshotDetails` near the existing rate limit tests. It should attach fake Redis, set `rate_limit.global_per_min=1`, disable other dimensions, issue two valid `/v1/chat/completions` requests, and assert the failed log has:

```json
{
  "scope_result": {
    "rate_limit": "deny",
    "rate_limit_dimension": "global"
  },
  "rate_limit_snapshot": {
    "dimension": "global",
    "window": "minute",
    "threshold": 1,
    "current": 2,
    "remaining": 0,
    "decision": "deny"
  }
}
```

Then clear Redis and logs, set `rate_limit.global_per_min=0`, `rate_limit.per_ip_per_min=1`, issue two valid requests, and assert the same fields with `dimension=ip`.

- [x] **Step 2: Verify red**

```powershell
go test ./internal/router -run "TestRateLimitGlobalAndIPWriteSnapshotDetails" -count=1
```

Expected: FAIL because rate-limit denials do not yet include `rate_limit_snapshot`.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/middleware/ratelimit.go`
- Modify: `internal/service/token_service.go`
- Modify: `internal/service/relay_service.go`

- [x] **Step 1: Return Redis counter values**

Change middleware `exceeded` and Relay `relayRateLimitExceeded` helpers to return `(exceeded bool, current int64)`. Keep fail-open behavior on Redis errors by returning `false, 0`.

- [x] **Step 2: Build rate limit snapshot**

Add a service-package helper that augments deny policy snapshots:

```go
func buildRelayRateLimitDenySnapshot(ctx context.Context, token *model.Token, dimension string, limit, current int64, scopeResult map[string]interface{}) string
```

It should call the same base fields as `buildRelayPolicyDenySnapshot` and add:

```json
"rate_limit_snapshot": {
  "dimension": "...",
  "window": "minute",
  "threshold": limit,
  "current": current,
  "remaining": max(0, limit-current),
  "decision": "deny"
}
```

- [x] **Step 3: Use it from middleware and Relay**

Add `TokenService.RecordRateLimitDeniedPolicyLog(...)` and call it from `middleware.writeRateLimitError` with dimension, limit and current. Update `enforceModelRateLimit` and `enforceChannelRateLimit` to use `buildRelayRateLimitDenySnapshot`.

- [x] **Step 4: Verify green**

```powershell
go test ./internal/router -run "TestRateLimitGlobalAndIPWriteSnapshotDetails" -count=1
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
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Update current snapshot coverage**

Document that Redis global/IP/Token/User/Model/Channel rate-limit denials now write both `scope_result` and `rate_limit_snapshot`.

- [x] **Step 2: Update test evidence**

Add `TestRateLimitGlobalAndIPWriteSnapshotDetails` to the testing evidence and remove stale wording that global/IP log summaries are future work.

- [x] **Step 3: Update Apifox**

Update `policy_snapshot` schema description to mention `rate_limit_snapshot` for rate-limit denials.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run targeted tests**

```powershell
go test ./internal/router -run "TestRateLimitGlobalAndIPWriteSnapshotDetails|TestRateLimitPerChannelRejectsBeforeUpstream|TestRateLimitPerModelRejectsBeforeUpstream|TestRateLimitPerUserAppliesAcrossAPIKeys|TestRateLimitUsesSettingsAndEntryProtocolErrorShape|TestMetricsEndpointIncludesRelayPaymentAndInfrastructureSignals" -count=1
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
git add internal/router/router_test.go internal/middleware/ratelimit.go internal/service/token_service.go internal/service/relay_service.go docs/POLICIES.md docs/SNAPSHOTS.md docs/OBSERVABILITY.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-rate-limit-snapshot.md
git commit -m "feat: snapshot rate limit denials"
```
