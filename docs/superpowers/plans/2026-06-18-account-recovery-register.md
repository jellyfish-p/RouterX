# Account Recovery On Register Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `POST /v0/user/register` recover a self-cancelled username account instead of creating a second user or permanently rejecting the preserved identity.

**Architecture:** Keep the existing registration API shape and reuse `users.status=disabled` as the cancelled state. `AuthService.Register` will detect a preserved `username/local` identity whose user is disabled and role=user, update the existing user and username identity in one transaction, leave old API Keys disabled, and return the recovered user. `AuthHandler.Register` will write a `user.recover` audit record only when the service reports a recovery.

**Tech Stack:** Go, Gin router integration tests, GORM `users`/`user_identities`/`tokens`, existing admin audit log, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Change the self-cancel test to assert recovery**

In `TestUserSelfCancelDisablesAccountButPreservesIdentity`, replace the final duplicate-registration rejection block with assertions that same-username registration recovers the original account:

```go
	recoverResp := performJSON(r, http.MethodPost, "/v0/user/register", "", map[string]interface{}{
		"username":     "cancel-user",
		"password":     "newpassword123",
		"display_name": "Recovered User",
	})
	if recoverResp.Code != http.StatusOK {
		t.Fatalf("preserved identity should recover cancelled account, got %d %s", recoverResp.Code, recoverResp.Body.String())
	}
	var recoveredPayload struct {
		Data struct {
			ID          uint   `json:"id"`
			DisplayName string `json:"display_name"`
			Status      int    `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recoverResp.Body.Bytes(), &recoveredPayload); err != nil {
		t.Fatal(err)
	}
	if recoveredPayload.Data.ID != user.ID || recoveredPayload.Data.DisplayName != "Recovered User" || recoveredPayload.Data.Status != common.UserStatusEnabled {
		t.Fatalf("recovery should return original enabled user, got %+v want id=%d", recoveredPayload.Data, user.ID)
	}
	if err := internal.DB.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.Status != common.UserStatusEnabled || user.DisplayName != "Recovered User" {
		t.Fatalf("recovered user should be enabled with updated profile, got %+v", user)
	}
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.Status != common.TokenStatusDisabled {
		t.Fatalf("recovery must not re-enable old API keys, got status=%d", storedToken.Status)
	}
	loginOldPasswordResp := performJSON(r, http.MethodPost, "/v0/user/login", "", map[string]interface{}{
		"account":  "cancel-user",
		"password": "password123",
	})
	if loginOldPasswordResp.Code != http.StatusUnauthorized {
		t.Fatalf("old password should not work after recovery, got %d %s", loginOldPasswordResp.Code, loginOldPasswordResp.Body.String())
	}
	loginRecoveredJWT := loginBearer(t, r, "cancel-user", "newpassword123")
	if loginRecoveredJWT == "" {
		t.Fatal("recovered account should log in with new password")
	}
	var recoverAuditCount int64
	if err := internal.DB.Model(&model.AdminAuditLog{}).
		Where("action = ? AND resource_type = ? AND resource_id = ?", "user.recover", "user", fmt.Sprint(user.ID)).
		Count(&recoverAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if recoverAuditCount != 1 {
		t.Fatalf("recovery should write one audit record, got count=%d", recoverAuditCount)
	}
	var userCount int64
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "cancel-user").Count(&userCount).Error; err != nil {
		t.Fatal(err)
	}
	if userCount != 1 {
		t.Fatalf("recovery must not create a second account, got count=%d", userCount)
	}
```

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestUserSelfCancelDisablesAccountButPreservesIdentity" -count=1
```

Expected: failure because current registration returns HTTP 400 when the preserved username identity exists.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/auth_service.go`
- Modify: `internal/handler/auth_handler.go`

- [x] **Step 1: Return registration metadata**

Add a service result type:

```go
type RegisterResult struct {
	User      *model.User
	Recovered bool
}
```

Change `AuthService.Register` to return `(*RegisterResult, error)` and set `Recovered=false` for new accounts.

- [x] **Step 2: Add disabled-account recovery inside the registration transaction**

When `username/local` identity exists, preload its user. If the user is a normal disabled user, update the existing user instead of returning duplicate:

```go
existingIdentity, err := findIdentityForRecovery(tx, model.UserIdentityMethodUsername, username)
if err != nil {
	return err
}
if existingIdentity != nil {
	if existingIdentity.User == nil || existingIdentity.User.Role != common.RoleUser {
		return errors.New("username already exists")
	}
	if existingIdentity.User.Status != common.UserStatusDisabled {
		return errors.New("username already exists")
	}
	recovered, err := recoverRegisteredUser(tx, existingIdentity, password, displayName, email)
	if err != nil {
		return err
	}
	result = &RegisterResult{User: recovered, Recovered: true}
	return nil
}
```

Implement helpers:

```go
func findIdentityForRecovery(tx *gorm.DB, method, identifier string) (*model.UserIdentity, error)
func recoverRegisteredUser(tx *gorm.DB, identity *model.UserIdentity, password, displayName, email string) (*model.User, error)
```

`recoverRegisteredUser` must:

- Hash the new password.
- Update `users.status` to enabled.
- Update display name when provided, otherwise keep the existing display name.
- Update email only when provided.
- Update the matching username identity `password_hash` and `verified_at`.
- Not change existing disabled API Keys.
- Return the refreshed user.

- [x] **Step 3: Record recovery audit in the handler**

Update `AuthHandler.Register`:

```go
result, err := h.svc.Register(req.Username, req.Password, req.DisplayName, req.Email)
...
if result.Recovered {
	auditSvc := service.NewUserService()
	_ = auditSvc.RecordAdminAuditLog(service.AdminAuditRecordInput{
		RequestID: c.GetString("request_id"),
		ActorUserID: result.User.ID,
		ActorRole: result.User.Role,
		Action: "user.recover",
		ResourceType: "user",
		ResourceID: strconv.FormatUint(uint64(result.User.ID), 10),
		AfterSummary: auditSummary(dto.UserBriefFromModel(result.User)),
		Result: "success",
		IP: c.ClientIP(),
		UserAgent: c.GetHeader("User-Agent"),
	})
}
common.Success(c, dto.UserBriefFromModel(result.User))
```

Use the existing `auditSummary` helper from the handler package and import `strconv`.

- [x] **Step 4: Verify green**

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

- [x] **Step 1: Document registration recovery**

Update user registration docs to state that same-username registration for a disabled preserved account recovers the original user, updates the password, keeps quota/history, and does not re-enable old API Keys.

- [x] **Step 2: Update account boundary text**

Change current boundary text from “recovery is future work” to “basic username recovery is implemented; password secondary confirmation, privacy scrubbing, email/phone/OAuth/OIDC recovery remain future work.”

- [x] **Step 3: Update testing and traceability**

Update references for `TestUserSelfCancelDisablesAccountButPreservesIdentity` so it includes recovery and `user.recover` audit evidence.

- [x] **Step 4: Update Apifox**

Update `/v0/user/register` description and `/v0/user/self` delete description so Apifox import matches the current behavior.

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
git add internal/router/router_test.go internal/service/auth_service.go internal/handler/auth_handler.go docs/API.md docs/ACCOUNTS.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-account-recovery-register.md
git commit -m "feat: recover cancelled accounts"
```
