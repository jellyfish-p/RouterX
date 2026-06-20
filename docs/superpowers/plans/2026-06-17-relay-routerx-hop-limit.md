# Relay RouterX Hop Limit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the documented `relay.routerx_max_hops` setting so RouterX-Compatible loop protection is configurable while preserving the default limit of 3.

**Architecture:** Keep the hop limit enforcement in `RelayService`, where the selected upstream channel type is known. Add the setting to setup defaults and validation, expose a typed accessor, convert the package-level `nextRouterXHop` helper into a service-aware helper, and keep both non-stream and stream relay paths using the same limit logic.

**Tech Stack:** Go, Gin router integration tests, RouterX settings service, OpenAPI/Apifox docs.

---

### Task 1: Configurable RouterX Hop Limit

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/service/relay_service.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`
- Modify: `docs/API.md`
- Modify: `docs/DEVELOPER_EXPERIENCE.md`
- Modify: `docs/PROTOCOLS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/SETTINGS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Write the failing tests**

Add `relay.routerx_max_hops` to `TestSetupBootstrapAdminQuotaAndSettingsDefaults`.

Extend `TestSettingsValidationAndReadiness` with:

```go
badRouterXHopLimit := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
    "relay.routerx_max_hops": "0",
})
if badRouterXHopLimit.Code != http.StatusBadRequest {
    t.Fatalf("routerx max hops should reject zero values, got %d %s", badRouterXHopLimit.Code, badRouterXHopLimit.Body.String())
}
```

Add `TestRouterXCompatibleUpstreamUsesConfiguredHopLimit` near the existing RouterX-Compatible hop tests. The test should:

1. Initialize setup and create an API key.
2. Set `relay.routerx_max_hops` to `1`.
3. Create an enabled RouterX-Compatible channel.
4. Send a `/v1/chat/completions` request with `X-RouterX-Hop: 1`.
5. Assert HTTP 400, code `routerx_hop_exceeded`, zero upstream calls, no user/API Key deduction, and a failed log with zero quota.

- [x] **Step 2: Run tests to verify they fail**

Run:

```powershell
go test ./internal/router -run "TestRouterXCompatibleUpstreamUsesConfiguredHopLimit|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
```

Expected: FAIL because the setting is not initialized or validated yet, and the relay still uses the hardcoded limit `3`.

- [x] **Step 3: Implement minimal backend changes**

Add default `relay.routerx_max_hops=3` in `SetupService.buildDefaultSettings`.

Validate `relay.routerx_max_hops` as a positive integer in `SettingService`.

Add a RelayService accessor:

```go
func (s *RelayService) RouterXMaxHops() int {
    // Invalid or missing values fall back to 3.
}
```

Change:

```go
routerXHop, forwardRouterXHop, err := nextRouterXHop(ctx, channel)
```

to:

```go
routerXHop, forwardRouterXHop, err := s.nextRouterXHop(ctx, channel)
```

for both non-stream and stream relay paths. The helper should reject when the inbound hop is greater than or equal to `s.RouterXMaxHops()`.

- [x] **Step 4: Update docs and Apifox**

Update settings, relay, API, developer experience, protocols, testing, traceability, and Apifox docs to describe that the default hop limit is 3 and is configurable via `relay.routerx_max_hops`.

- [x] **Step 5: Run verification**

Run:

```powershell
go test ./internal/router -run "TestRouterXCompatibleUpstreamUsesConfiguredHopLimit|TestRouterXCompatibleUpstreamRejectsHopLimit|TestRouterXCompatibleUpstreamPreservesRouterXAndIncrementsHop|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
go test ./... -count=1
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
git diff --check
```

- [x] **Step 6: Commit**

Run:

```powershell
git add internal/router/router_test.go internal/service/relay_service.go internal/service/setup_service.go internal/service/setting_service.go internal/service/log_service.go docs/API.md docs/DEVELOPER_EXPERIENCE.md docs/PROTOCOLS.md docs/RELAY.md docs/SETTINGS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-relay-routerx-hop-limit.md
git commit -m "feat: configure routerx hop limit"
```
