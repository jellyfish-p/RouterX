# Self Account Cancel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a user self-cancellation API that preserves account and identity records while disabling future login and API Key use.

**Architecture:** Reuse `users.status=disabled` as the current cancelled-account representation, matching `docs/ACCOUNTS.md`. Implement the mutation in `UserService` so the user status change and API Key disabling happen in one transaction; the handler writes a `user.self_cancel` audit summary and returns a simple success response.

**Tech Stack:** Go, Gin router integration tests, GORM `users`/`tokens`/`user_identities`, existing admin audit log, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add route behavior coverage**

Create `TestUserSelfCancelDisablesAccountButPreservesIdentity`:

1. Enable username registration without captcha.
2. Register and log in as `cancel-user`.
3. Create one API Key.
4. Call `DELETE /v0/user/self`; expect 200.
5. Assert `users.status=disabled`, the API Key is disabled, `user_identities` still has the username identity, and `admin_audit_logs` has one `user.self_cancel` record.
6. Attempt username/password login again; expect 400.
7. Attempt `POST /v0/user/register` with the same username again; expect 400 and no second user row.

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestUserSelfCancelDisablesAccountButPreservesIdentity" -count=1
```

Expected: failure because `DELETE /v0/user/self` is not registered.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/user_service.go`
- Modify: `internal/handler/user_handler.go`
- Modify: `internal/router/user_router.go`

- [x] **Step 1: Add service method**

Implement:

```go
func (s *UserService) CancelSelf(userID uint) (*model.User, error)
```

Rules:

- `userID` is required.
- Only normal users (`role=RoleUser`) can self-cancel.
- The transaction updates `users.status` to `UserStatusDisabled` and disables all of the user's tokens.
- It does not soft-delete `users`, `user_identities`, `tokens`, or `logs`.
- If the user is already disabled, return the current user successfully so the route stays idempotent.

- [x] **Step 2: Add handler and route**

Add:

```go
DELETE /v0/user/self
```

Handler behavior:

- Require current user.
- Call `CancelSelf`.
- Write one `user.self_cancel` audit record using a safe summary.
- Return success message.

- [x] **Step 3: Verify green**

Run:

```powershell
go test ./internal/router -run "TestUserSelfCancelDisablesAccountButPreservesIdentity" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/ACCOUNTS.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document self-cancel API**

Add `DELETE /v0/user/self` to user account API docs. State that cancellation disables login and API Keys but preserves identity, logs, quota, and history.

- [x] **Step 2: Sync account and testing docs**

Update account current-boundary text and test coverage references with `TestUserSelfCancelDisablesAccountButPreservesIdentity`.

- [x] **Step 3: Update Apifox**

Add the delete path returning the standard success response.

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
git add internal/router/router_test.go internal/service/user_service.go internal/handler/user_handler.go internal/router/user_router.go docs/API.md docs/ACCOUNTS.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-self-account-cancel.md
git commit -m "feat: cancel user accounts"
```
