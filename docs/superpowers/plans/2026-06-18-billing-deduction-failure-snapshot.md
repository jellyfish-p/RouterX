# Billing Deduction Failure Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist an explanatory `billing_snapshot` when upstream succeeds but the final user/API Key quota deduction fails.

**Architecture:** Keep quota deduction transactional and keep failed calls at `logs.quota_used=0`. Add a failure billing snapshot builder in `RelayService` that records the calculated charge as `attempted_quota_used`, sets `billing_status=failed`, keeps `final_quota_used=0`, and records a stable deduction failure code. Reuse it in non-stream raw, non-stream JSON, and stream settlement failure paths.

**Tech Stack:** Go, Gin router integration tests, GORM, existing Relay billing snapshots, Apifox OpenAPI YAML.

---

### Task 1: Red Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add deduction failure snapshot coverage**

Add `TestChatCompletionDeductionFailureWritesBillingSnapshot` near the billing snapshot tests. The test creates a user with positive but insufficient quota, sends a successful upstream Chat response with `total_tokens=5`, expects RouterX to return `429 insufficient_quota`, and asserts:

```go
if upstreamCalls != 1 {
	t.Fatalf("deduction failure should happen after one upstream call, got %d", upstreamCalls)
}
if failedLog.QuotaUsed != 0 || failedLog.TotalTokens != 5 || failedLog.ErrorCode != "insufficient_quota" {
	t.Fatalf("deduction failure should write zero-quota failed log with usage and stable code, got %+v", failedLog)
}
if billingSnapshot["billing_status"] != "failed" ||
	billingSnapshot["deduction_result"] != "failed" ||
	billingSnapshot["deduction_error_code"] != "insufficient_user_quota" ||
	billingSnapshot["attempted_quota_used"] != float64(5) ||
	billingSnapshot["final_quota_used"] != float64(0) {
	t.Fatalf("deduction failure snapshot should explain failed charge: %+v", billingSnapshot)
}
```

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestChatCompletionDeductionFailureWritesBillingSnapshot" -count=1
```

Expected: failure because deduction failure logs do not yet include `billing_snapshot` and may not normalize to the stable `insufficient_quota` code.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/relay_service.go`
- Modify: `internal/service/token_service.go`

- [x] **Step 1: Preserve failed deduction snapshot inputs**

Change `DeductQuotaWithSnapshot` so it returns the populated `QuotaDeductionResult` alongside the error instead of discarding it:

```go
if err != nil {
	return result, err
}
```

- [x] **Step 2: Add failed billing snapshot builder**

Add `buildRelayBillingFailureSnapshot(usage *relay.Usage, billing relayBillingResult, deduction QuotaDeductionResult, err error) string` next to `buildRelayBillingSnapshot`. It should mirror the success snapshot but use:

```go
"billing_status":       "failed",
"final_quota_used":     int64(0),
"attempted_quota_used": quotaUsed,
"deduction_result":     "failed",
"deduction_error_code": quotaDeductionErrorCode(err),
"key_budget_after":     deduction.TokenQuotaBefore,
"user_balance_after":   deduction.UserQuotaBefore,
```

Add `quotaDeductionErrorCode` with stable values `insufficient_user_quota`, `insufficient_token_quota`, and `deduction_failed`.

- [x] **Step 3: Use snapshot when deduction fails**

In raw, JSON, and stream deduction error branches, wrap the failed log context:

```go
logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingFailureSnapshot(usage, billing, deduction, err))
_ = s.recordLog(logCtx, token, channel, reqInfo.Model, usage, common.LogStatusFailed, 0, "insufficient quota", clientIP)
```

Use `nil` usage for raw responses.

- [x] **Step 4: Verify green**

Run:

```powershell
go test ./internal/router -run "TestChatCompletionDeductionFailureWritesBillingSnapshot" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/BILLING.md`
- Modify: `docs/SNAPSHOTS.md`
- Modify: `docs/OBSERVABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Update docs**

Document that deduction failures write a failed log with `quota_used=0`, `billing_snapshot.billing_status=failed`, `attempted_quota_used`, `deduction_result=failed`, and a stable deduction error code.

- [x] **Step 2: Update Apifox schema**

Update the `LogInfo.billing_snapshot` description to mention failed deduction snapshots.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run full tests**

```powershell
go test ./... -count=1
```

- [x] **Step 2: Validate Apifox YAML**

```powershell
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
```

- [x] **Step 3: Check whitespace**

```powershell
git diff --check
```

- [x] **Step 4: Commit**

```powershell
git add internal/router/router_test.go internal/service/relay_service.go internal/service/token_service.go docs/API.md docs/BILLING.md docs/SNAPSHOTS.md docs/OBSERVABILITY.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-billing-deduction-failure-snapshot.md
git commit -m "feat: snapshot billing deduction failures"
```
