# API Key Leak Window Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add user and admin API Key leak-window analysis APIs that summarize recent usage without exposing plaintext keys or raw IP addresses.

**Architecture:** Reuse existing `logs` and `tokens` facts. `TokenService` will aggregate a bounded time window for one token, returning call totals plus top models, error codes and source IP hashes. User routes are scoped to the current user; admin routes can inspect any token.

**Tech Stack:** Go, Gin, GORM, existing router integration tests, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Router Regression Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add leak window test**

Add `TestAPIKeyLeakWindowSummarizesRecentUse` near the existing API Key risk tests. The test should:

1. Initialize RouterX and login as root.
2. Create one API Key through `TokenService`.
3. Insert two recent logs and one old log for that token.
4. Insert one recent log for another token to prove isolation.
5. Call `GET /v0/user/token/:id/leak-window?window_hours=24`.
6. Assert the response includes only the two recent logs, has model/error/IP-hash summaries, and does not contain plaintext API Key or raw IP.
7. Call `GET /v0/admin/token/:id/leak-window?window_hours=24` and assert the same token is visible to an admin.

- [x] **Step 2: Run red test**

Run:

```bash
go test ./internal/router -run TestAPIKeyLeakWindowSummarizesRecentUse -count=1
```

Expected: FAIL because the leak-window routes do not exist.

### Task 2: Service, DTO, Handler and Routes

**Files:**
- Modify: `internal/service/token_service.go`
- Modify: `internal/dto/token.go`
- Modify: `internal/handler/token_handler.go`
- Modify: `internal/router/user_router.go`
- Modify: `internal/router/admin_router.go`

- [x] **Step 1: Add service result types**

Add `TokenLeakWindowStats` and `TokenLeakWindowCounter` to `token_service.go`. Include token, window start/end/hours, totals, first/last use, model counters, error-code counters, source-IP-hash counters, and existing last-used hashes from the token row.

- [x] **Step 2: Add service query methods**

Add:

```go
func (s *TokenService) GetLeakWindowForUser(id, userID uint, windowHours int) (TokenLeakWindowStats, error)
func (s *TokenService) GetLeakWindow(id uint, windowHours int) (TokenLeakWindowStats, error)
```

Normalize `window_hours` to default `24`, minimum `1`, maximum `720`. Query only logs in `[now-window, now]`; aggregate in memory using existing safe hash helper for IP values.

- [x] **Step 3: Add DTO responses**

Add `TokenLeakWindowResponse` and `TokenLeakWindowCounterResponse` to `dto/token.go`.

- [x] **Step 4: Add handlers and routes**

Add `TokenHandler.LeakWindow` and `TokenHandler.AdminLeakWindow`. Register:

```text
GET /v0/user/token/:id/leak-window
GET /v0/admin/token/:id/leak-window
```

- [x] **Step 5: Run focused test**

Run:

```bash
go test ./internal/router -run TestAPIKeyLeakWindowSummarizesRecentUse -count=1
```

Expected: PASS.

### Task 3: Documentation and Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/API_KEYS.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document the APIs**

Add the two leak-window endpoints to API and API Key docs. State that the response uses existing logs, hashes source IPs, never returns plaintext API Key, and defaults to 24 hours with a 720-hour cap.

- [x] **Step 2: Update status matrices**

Mark basic leak-window analysis as covered while keeping automatic rotation suggestions and alerting as future work.

- [x] **Step 3: Validate OpenAPI YAML**

Run:

```bash
bun -e "const fs=require('fs'); const yaml=require('yaml'); yaml.parse(fs.readFileSync('docs/apifox/openapi.yaml','utf8')); console.log('openapi yaml ok')"
```

Expected: `openapi yaml ok`.

### Task 4: Verification and Commit

**Files:**
- Verify all modified files.

- [x] **Step 1: Run focused tests**

Run:

```bash
go test ./internal/router -run TestAPIKeyLeakWindowSummarizesRecentUse -count=1
```

- [x] **Step 2: Run full backend tests**

Run:

```bash
go test ./... -count=1
```

- [x] **Step 3: Check diffs**

Run:

```bash
git diff --check
git diff --cached --check
```

- [ ] **Step 4: Commit**

Run:

```bash
git add internal/router/router_test.go internal/service/token_service.go internal/dto/token.go internal/handler/token_handler.go internal/router/user_router.go internal/router/admin_router.go docs/API.md docs/API_KEYS.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-api-key-leak-window.md
git commit -m "feat: expose api key leak window"
```

Expected: commit succeeds on `codex/backend-p0-closure`.
