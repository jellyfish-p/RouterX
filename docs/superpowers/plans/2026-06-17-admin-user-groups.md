# Admin User Groups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add admin APIs for managing user groups so `users.group_id`, billing/user-group policy settings, and default registration group choices are operable through the backend.

**Architecture:** Reuse `UserService` and `UserHandler` because user group management is adjacent to admin user management and audit helpers already live there. The API exposes list/create/update/delete under `/v0/admin/groups`; delete protects the `default` group and any group currently assigned to users.

**Tech Stack:** Go, Gin router integration tests, GORM `groups`/`users`, existing admin audit logs, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add route behavior coverage**

Create `TestAdminUserGroupManagement`:

1. Initialize the app.
2. Log in as root.
3. `POST /v0/admin/groups` with `{ "name": "vip", "ratio": 0.8 }`; expect 200 and returned `name=vip`, `ratio=0.8`.
4. `GET /v0/admin/groups?keyword=vip`; expect the created group in paginated data.
5. `PUT /v0/admin/groups/:id` with `{ "name": "vip-renamed", "ratio": 0.9 }`; expect 200.
6. Create a user assigned to that group, then `DELETE /v0/admin/groups/:id`; expect 400 because the group is in use.
7. Create an unused group and delete it; expect 200 and soft-deleted/missing from list.
8. Create or find `default` group and assert deleting it returns 400.
9. Query `/v0/admin/audit?resource_type=user_group`; expect `user_group.create`, `user_group.update`, and `user_group.delete`.

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestAdminUserGroupManagement" -count=1
```

Expected: failure because `/v0/admin/groups` routes do not exist.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/dto/user.go`
- Modify: `internal/dto/mapper.go`
- Modify: `internal/service/user_service.go`
- Modify: `internal/handler/user_handler.go`
- Modify: `internal/router/admin_router.go`

- [x] **Step 1: Add DTOs and mapper**

Add request/response DTOs:

```go
type UserGroupListRequest struct {
    Page     int    `form:"page" binding:"omitempty,min=1"`
    PageSize int    `form:"page_size" binding:"omitempty,min=1,max=100"`
    Keyword  string `form:"keyword"`
}

type CreateUserGroupRequest struct {
    Name  string  `json:"name" binding:"required,min=1,max=64"`
    Ratio float64 `json:"ratio"`
}

type UpdateUserGroupRequest struct {
    Name  string   `json:"name"`
    Ratio *float64 `json:"ratio"`
}

type UserGroupInfo struct {
    ID        uint      `json:"id"`
    Name      string    `json:"name"`
    Ratio     float64   `json:"ratio"`
    CreatedAt time.Time `json:"created_at"`
}
```

Map `model.Group` to `UserGroupInfo`.

- [x] **Step 2: Add service methods**

Implement:

```go
ListGroups(operatorRole int, page, pageSize int, keyword string) ([]model.Group, int64, error)
CreateGroup(operatorRole int, name string, ratio float64) (*model.Group, error)
GetGroupByID(id uint) (*model.Group, error)
UpdateGroup(operatorRole int, id uint, name string, ratio *float64) error
DeleteGroup(operatorRole int, id uint) error
```

Rules:

- Admin+ may list/create/update/delete.
- `name` trims spaces and must be non-empty.
- `ratio <= 0` defaults to `1` on create, but update rejects explicit `ratio <= 0`.
- Names must be unique.
- `default` group cannot be deleted.
- Groups assigned to any non-deleted user cannot be deleted.

- [x] **Step 3: Add handler methods**

Implement admin endpoints:

```go
GET    /v0/admin/groups
POST   /v0/admin/groups
PUT    /v0/admin/groups/:id
DELETE /v0/admin/groups/:id
```

Write admin audits:

```text
user_group.create
user_group.update
user_group.delete
```

Use `resource_type=user_group` and `resource_id=<id>`.

- [x] **Step 4: Register routes**

Add the four routes to `internal/router/admin_router.go` near user management routes.

- [x] **Step 5: Verify green**

Run the targeted router command from Task 1 and ensure PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/POLICIES.md`
- Modify: `docs/DATA_MODEL.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document admin group APIs**

Add `/v0/admin/groups` APIs to `docs/API.md` and explain `default`/in-use delete protection.

- [x] **Step 2: Sync policy/data model docs**

Mark user group CRUD as current backend capability. Keep billing runtime ratios in settings as the authoritative pricing source; `groups.ratio` is retained as group metadata/legacy display ratio.

- [x] **Step 3: Update Apifox**

Add paths and schemas for group list/create/update/delete so Apifox import includes every new API.

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
git add internal/router/router_test.go internal/dto/user.go internal/dto/mapper.go internal/service/user_service.go internal/handler/user_handler.go internal/router/admin_router.go docs/API.md docs/POLICIES.md docs/DATA_MODEL.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-admin-user-groups.md
git commit -m "feat: manage user groups"
```
