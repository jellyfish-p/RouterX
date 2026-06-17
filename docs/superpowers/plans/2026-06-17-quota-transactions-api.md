# Quota Transactions API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add user and admin APIs for querying `quota_transactions` so充值、充值码、退款、人工补账和扣回形成可查询的额度流水事实链。

**Architecture:** Reuse `UserService` for quota transaction queries because the service already owns payment, redeem code, refund, and manual adjustment writes. Expose `/v0/user/quota-transactions` for the current user's own ledger and `/v0/admin/quota-transactions` for admin global queries with filters. Return sanitized DTOs and keep model-call consumption in `logs`, not in this ledger.

**Tech Stack:** Go, Gin router integration tests, GORM `quota_transactions`, existing pagination DTO, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add route behavior coverage**

Create `TestQuotaTransactionListAPIs`:

1. Initialize the app and log in as root.
2. Create a normal user `ledger-user`.
3. Admin adjusts that user's quota with `PATCH /v0/admin/user/:id/quota`, reason `seed credit`, and `request_id` from middleware.
4. Log in as `ledger-user`.
5. Call `GET /v0/user/quota-transactions?type=admin_adjust`; expect 200, one row for the user, `amount=25`, `balance_before=0`, `balance_after=25`, and no rows for other users.
6. As root, call `GET /v0/admin/quota-transactions?user_id=<id>&type=admin_adjust`; expect the same row.
7. As root, call `GET /v0/admin/quota-transactions?source_type=admin_action`; expect the row can be filtered by source type.

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestQuotaTransactionListAPIs" -count=1
```

Expected: failure because `/v0/user/quota-transactions` and `/v0/admin/quota-transactions` are not registered.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/dto/user.go`
- Modify: `internal/dto/mapper.go`
- Modify: `internal/service/user_service.go`
- Modify: `internal/handler/user_handler.go`
- Modify: `internal/router/user_router.go`
- Modify: `internal/router/admin_router.go`

- [x] **Step 1: Add DTOs and mapper**

Add query and response DTOs:

```go
type QuotaTransactionListRequest struct {
    Page       int    `form:"page" binding:"omitempty,min=1"`
    PageSize   int    `form:"page_size" binding:"omitempty,min=1,max=100"`
    UserID     *uint  `form:"user_id"`
    Type       string `form:"type"`
    SourceType string `form:"source_type"`
    SourceID   string `form:"source_id"`
    StartTime  string `form:"start_time"`
    EndTime    string `form:"end_time"`
}

type QuotaTransactionInfo struct {
    ID            uint      `json:"id"`
    UserID        uint      `json:"user_id"`
    Type          string    `json:"type"`
    Amount        int64     `json:"amount"`
    BalanceBefore int64     `json:"balance_before"`
    BalanceAfter  int64     `json:"balance_after"`
    SourceType    string    `json:"source_type"`
    SourceID      string    `json:"source_id"`
    Reason        string    `json:"reason,omitempty"`
    ActorUserID   *uint     `json:"actor_user_id,omitempty"`
    RequestID     *string   `json:"request_id,omitempty"`
    CreatedAt     time.Time `json:"created_at"`
}
```

Map `model.QuotaTransaction` to `QuotaTransactionInfo`.

- [x] **Step 2: Add service query methods**

Implement:

```go
func (s *UserService) ListQuotaTransactions(operatorRole int, page, pageSize int, filter QuotaTransactionFilter) ([]model.QuotaTransaction, int64, error)
func (s *UserService) ListUserQuotaTransactions(userID uint, page, pageSize int, filter QuotaTransactionFilter) ([]model.QuotaTransaction, int64, error)
```

Rules:

- Admin list requires `operatorRole >= common.RoleAdmin`.
- User list forces `user_id=<current user>` regardless of query input.
- Filters support `type`, `source_type`, `source_id`, `user_id`, `start_time`, and `end_time`.
- Time parsing should reuse the existing common layouts: RFC3339, `2006-01-02 15:04:05`, and `2006-01-02`.
- Sort by `id DESC` and use existing `normalizePage`.

- [x] **Step 3: Add handler methods and routes**

Add:

```go
GET /v0/user/quota-transactions
GET /v0/admin/quota-transactions
```

Responses use `dto.PaginatedResult`.

- [x] **Step 4: Verify green**

Run:

```powershell
go test ./internal/router -run "TestQuotaTransactionListAPIs" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/PAYMENTS.md`
- Modify: `docs/BILLING.md`
- Modify: `docs/DATA_MODEL.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document ledger APIs**

Add user and admin quota transaction list APIs to `docs/API.md`, including filters and the distinction between quota ledger and model consumption logs.

- [x] **Step 2: Sync payment/billing/data docs**

Mark quota transaction query APIs as current backend capability and keep the existing rule that model calls remain in `logs.quota_used`.

- [x] **Step 3: Update Apifox**

Add paths and schemas for the two new APIs so Apifox import includes the ledger endpoints.

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
git add internal/router/router_test.go internal/dto/user.go internal/dto/mapper.go internal/service/user_service.go internal/handler/user_handler.go internal/router/user_router.go internal/router/admin_router.go docs/API.md docs/PAYMENTS.md docs/BILLING.md docs/DATA_MODEL.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-quota-transactions-api.md
git commit -m "feat: expose quota transactions"
```
