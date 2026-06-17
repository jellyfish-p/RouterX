# Auth Register Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the existing `/v0/user/register` username/password path respect registration settings, default to closed self-registration, and apply default quota/group when explicitly opened.

**Architecture:** Keep the current basic registration API shape. `SetupService` seeds auth registration settings, `SettingService` validates them, and `AuthService.Register` enforces `auth.register.enabled` plus `auth.register.username.enabled` before creating the user. Default quota and optional default group are resolved inside the same registration transaction so the returned user and persisted state match.

**Tech Stack:** Go, Gin router integration tests, GORM settings/users/groups, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add default settings coverage**

Add these keys to `TestSetupBootstrapAdminQuotaAndSettingsDefaults`:

```go
"auth.register.enabled",
"auth.register.username.enabled",
"auth.register.email.enabled",
"auth.register.phone.enabled",
"auth.register.captcha.required",
"auth.register.default_quota",
"auth.register.default_group_id",
```

- [x] **Step 2: Add validation coverage**

In `TestSettingsValidationAndReadiness`, assert:

```go
auth.register.enabled = "maybe" -> 400
auth.register.default_quota = "-1" -> 400
auth.register.default_group_id = "" -> 400
```

- [x] **Step 3: Add behavior coverage**

Create `TestUserRegisterRespectsRegistrationSettings`:

1. Initialize the app.
2. Call `/v0/user/register` while `auth.register.enabled=false`; expect HTTP 403 and no user.
3. Set `auth.register.enabled=true`, `auth.register.username.enabled=false`; call register again; expect HTTP 403.
4. Create a `groups` row named `trial`, set `auth.register.username.enabled=true`, `auth.register.default_quota=1234`, `auth.register.default_group_id=trial`.
5. Register successfully and assert returned/persisted user has quota `1234` and `group_id` equal to the trial group.

- [x] **Step 4: Verify red**

Run:

```powershell
go test ./internal/router -run "TestUserRegisterRespectsRegistrationSettings|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
```

Expected: failure because auth registration settings are not seeded/validated and registration is still open by default.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`
- Modify: `internal/service/auth_service.go`

- [x] **Step 1: Seed auth registration settings**

Add an `auth` default category with:

```go
"auth.register.enabled": "false",
"auth.register.username.enabled": "true",
"auth.register.email.enabled": "false",
"auth.register.phone.enabled": "false",
"auth.register.captcha.required": "true",
"auth.register.default_quota": "0",
"auth.register.default_group_id": "default",
```

- [x] **Step 2: Validate settings**

Treat the boolean flags as booleans, `auth.register.default_quota` as a non-negative integer, and `auth.register.default_group_id` as a non-empty string.

- [x] **Step 3: Enforce registration policy**

At the start of `AuthService.Register`, check:

```go
auth.register.enabled == true
auth.register.username.enabled == true
```

Return errors that the handler maps to 403 when self-registration is closed or username registration is disabled.

- [x] **Step 4: Apply default quota and group**

Read `auth.register.default_quota` and assign it to `users.quota`. Resolve `auth.register.default_group_id` by group name or numeric ID; if it is `default` and no group row exists, leave `group_id` nil so existing policy normalization still treats the user as default.

- [x] **Step 5: Verify green**

Run the targeted router command from Task 1 and ensure PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/ACCOUNTS.md`
- Modify: `docs/SETTINGS.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Mark basic switch as current**

Document that the current implementation supports closed-by-default username/password self-registration, username method gating, default quota, and optional default group. Keep captcha/email/phone as later slices.

- [x] **Step 2: Update Apifox**

Add 403 response semantics to `/v0/user/register` and include auth register keys in batch settings examples.

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

- [ ] **Step 4: Commit**

```powershell
git add internal/router/router_test.go internal/service/setup_service.go internal/service/setting_service.go internal/service/auth_service.go docs/API.md docs/ACCOUNTS.md docs/SETTINGS.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-auth-register-settings.md
git commit -m "feat: gate self registration"
```
