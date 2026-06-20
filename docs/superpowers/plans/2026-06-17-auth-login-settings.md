# Auth Login Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the existing `/v0/user/login` password path respect documented login-method settings without weakening username/password login.

**Architecture:** Seed login settings in `SetupService`, validate them in `SettingService`, and let `AuthService.UserLogin` filter local identity candidates by login method. Username/password login remains a hard-on safety baseline; email/phone password identities are ignored unless their settings are explicitly enabled.

**Tech Stack:** Go, Gin router integration tests, GORM settings/user identities, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add default settings coverage**

Add these keys to `TestSetupBootstrapAdminQuotaAndSettingsDefaults`:

```go
"auth.login.username_password.enabled",
"auth.login.email_password.enabled",
"auth.login.phone_password.enabled",
"auth.login.email_code.enabled",
"auth.login.phone_code.enabled",
"auth.login.oauth.enabled",
"auth.login.oidc.enabled",
```

- [x] **Step 2: Add validation coverage**

In `TestSettingsValidationAndReadiness`, assert:

```go
auth.login.email_password.enabled = "maybe" -> 400
auth.login.username_password.enabled = "false" -> 400
```

- [x] **Step 3: Add behavior coverage**

Create `TestUserLoginRespectsLoginMethodSettings`:

1. Initialize the app.
2. Create a normal user through the admin API with username `login-method-user`.
3. Manually add local email and phone identities for that user using the same password hash.
4. Assert username/password login succeeds with defaults.
5. Assert email/password and phone/password login fail with defaults.
6. Enable `auth.login.email_password.enabled=true` and `auth.login.phone_password.enabled=true`.
7. Assert email/password and phone/password login succeed.

- [x] **Step 4: Verify red**

Run:

```powershell
go test ./internal/router -run "TestUserLoginRespectsLoginMethodSettings|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
```

Expected: failure because login settings are not seeded/validated and email/phone login candidates are not filtered by settings.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`
- Modify: `internal/service/auth_service.go`

- [x] **Step 1: Seed auth login settings**

Add these defaults under the existing `auth` category:

```go
"auth.login.username_password.enabled": "true",
"auth.login.email_password.enabled":    "false",
"auth.login.phone_password.enabled":    "false",
"auth.login.email_code.enabled":        "false",
"auth.login.phone_code.enabled":        "false",
"auth.login.oauth.enabled":             "false",
"auth.login.oidc.enabled":              "false",
```

- [x] **Step 2: Validate settings**

Treat all `auth.login.*.enabled` keys as booleans. Reject `auth.login.username_password.enabled=false` because the documented baseline says username/password login cannot be disabled.

- [x] **Step 3: Filter login candidates**

In the local identity lookup, skip disabled methods:

```go
username -> auth.login.username_password.enabled
email    -> auth.login.email_password.enabled
phone    -> auth.login.phone_password.enabled
```

Missing or invalid settings should fall back to the safe defaults: username allowed, email/phone denied.

- [x] **Step 4: Verify green**

Run the targeted router command from Task 1 and ensure PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/ACCOUNTS.md`
- Modify: `docs/SETTINGS.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Mark password login settings as current**

Document that username/password login remains always enabled, while email/phone password login is currently supported only for existing local identities and is gated by settings.

- [x] **Step 2: Update Apifox settings examples**

Add representative `auth.login.*` keys to the batch settings request description/example.

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
git add internal/router/router_test.go internal/service/setup_service.go internal/service/setting_service.go internal/service/auth_service.go docs/ACCOUNTS.md docs/SETTINGS.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-auth-login-settings.md
git commit -m "feat: gate password login methods"
```
