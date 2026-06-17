# Billing Usage Missing Strategy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the documented `billing.usage_missing_strategy` setting so operators can keep the current minimum-charge fallback or reject upstream responses that do not provide billable usage.

**Architecture:** Keep the decision in `RelayService`, immediately after a successful upstream response has been converted and before any quota deduction. Add the setting to setup defaults and validation, keep `minimum` as the backward-compatible default, and make `reject` write a failed log with zero quota and a stable `usage_missing` error code.

**Tech Stack:** Go, Gin router integration tests, RouterX settings service, Relay billing path, OpenAPI/Apifox docs.

---

### Task 1: Configurable Missing Usage Strategy

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/service/relay_service.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`
- Modify: `internal/service/log_service.go`
- Modify: `docs/API.md`
- Modify: `docs/BILLING.md`
- Modify: `docs/ERRORS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/SETTINGS.md`
- Modify: `docs/SNAPSHOTS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Write the failing tests**

Add `billing.usage_missing_strategy` to `TestSetupBootstrapAdminQuotaAndSettingsDefaults`.

Extend `TestSettingsValidationAndReadiness` with:

```go
badUsageMissingStrategy := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
    "billing.usage_missing_strategy": "free",
})
if badUsageMissingStrategy.Code != http.StatusBadRequest {
    t.Fatalf("usage missing strategy should reject unknown values, got %d %s", badUsageMissingStrategy.Code, badUsageMissingStrategy.Body.String())
}
```

Add `TestUsageMissingStrategyRejectsWithoutDeductingQuota` near the existing no-usage minimum-charge tests. The test should:

1. Initialize setup and set `billing.usage_missing_strategy=reject`.
2. Create an API key and OpenAI-compatible Moderations channel.
3. Return a successful upstream moderations JSON response without `usage`.
4. Assert the client receives 502 with code `usage_missing`.
5. Assert upstream was called once, token and user quota remain unchanged, and the failed log has `quota_used=0`, `error_code=usage_missing`, and `error_source=billing`.

- [x] **Step 2: Run tests to verify they fail**

Run:

```powershell
go test ./internal/router -run "TestUsageMissingStrategyRejectsWithoutDeductingQuota|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
```

Expected: FAIL because the setting is not initialized or validated yet, and no-usage responses still use minimum charge.

- [x] **Step 3: Implement minimal backend changes**

Add default `billing.usage_missing_strategy=minimum` in `SetupService.buildDefaultSettings`.

Validate `billing.usage_missing_strategy` as an enum accepting `minimum` and `reject`.

Add a RelayService accessor:

```go
func (s *RelayService) usageMissingStrategy() string {
    // Missing or invalid values fall back to "minimum".
}
```

Add a helper used by non-stream and stream settlement paths:

```go
func (s *RelayService) rejectMissingUsage(ctx context.Context, token *model.Token, channel *model.Channel, modelName string, usage *relay.Usage, clientIP string) *HTTPError {
    // Return nil unless strategy is reject and usage is missing.
    // When rejecting, write failed log with zero quota and return usage_missing.
}
```

Call the helper before `calculateRelayBilling` in raw response, converted response, and stream completion settlement paths.

- [x] **Step 4: Update docs and Apifox**

Update settings, billing, relay, snapshots, errors, API, testing, traceability, and Apifox docs to state that `billing.usage_missing_strategy` defaults to `minimum`; `reject` returns `usage_missing`, does not deduct quota, and logs a billing-sourced failure. Mark estimate/tokenizer as future work rather than current behavior.

- [x] **Step 5: Run verification**

Run:

```powershell
go test ./internal/router -run "TestUsageMissingStrategyRejectsWithoutDeductingQuota|TestModerationsPassthroughUsesMinimumChargeWithoutUsage|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
go test ./... -count=1
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
git diff --check
```

- [x] **Step 6: Commit**

Run:

```powershell
git add internal/router/router_test.go internal/service/relay_service.go internal/service/setup_service.go internal/service/setting_service.go internal/service/log_service.go docs/API.md docs/BILLING.md docs/ERRORS.md docs/RELAY.md docs/SETTINGS.md docs/SNAPSHOTS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-billing-usage-missing-strategy.md
git commit -m "feat: configure missing usage billing"
```
