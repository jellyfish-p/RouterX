# Payment Order Cancel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a user API for cancelling an unpaid `pending` payment order and moving it to the documented `closed` status without granting quota.

**Architecture:** Keep cancellation in `UserService`, scoped by current `user_id` and `order_no`. The handler returns the updated payment order DTO and writes a `payment_order.cancel` audit only when a pending order transitions to `closed`; already closed orders are idempotent and do not duplicate audits.

**Tech Stack:** Go, Gin router integration tests, GORM `payment_orders`, existing payment DTOs, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add route behavior coverage**

Create `TestUserCancelsPendingPaymentOrder` near `TestUserCreatesAndListsPaymentOrders`:

1. Initialize the app and log in as root.
2. Create an enabled `payment_products` row.
3. Enable `payment.stripe.enabled`.
4. Create a pending order with `POST /v0/user/payment/orders`.
5. Call `POST /v0/user/payment/orders/:order_no/cancel`; expect 200 with `status=closed`.
6. Assert the DB order is `closed`, `users.quota` is unchanged, and `payment_order.cancel` audit exists.
7. Call cancel again; expect 200 with `status=closed` and no duplicate cancel audit.
8. Create another pending order, mark it `paid` in DB, call cancel, and expect 400 while status remains `paid`.

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestUserCancelsPendingPaymentOrder" -count=1
```

Expected: failure because `/v0/user/payment/orders/:order_no/cancel` is not registered.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/user_service.go`
- Modify: `internal/handler/user_handler.go`
- Modify: `internal/router/user_router.go`

- [x] **Step 1: Add service method**

Implement:

```go
func (s *UserService) CancelPaymentOrder(userID uint, orderNo string) (*model.PaymentOrder, bool, error)
```

Rules:

- `userID` and `orderNo` are required.
- Query by both `user_id` and `order_no`; users cannot cancel other users' orders.
- `pending` becomes `closed` with `updated_at=now`.
- `closed` returns successfully with `changed=false`.
- Any other status returns `payment order is not pending`.
- Return the updated order and whether the status changed.

- [x] **Step 2: Add handler and route**

Add:

```go
POST /v0/user/payment/orders/:order_no/cancel
```

Handler behavior:

- Require current user.
- Call `CancelPaymentOrder`.
- On not found return 404; on non-pending return 400.
- If `changed=true`, write `payment_order.cancel` audit using `paymentOrderAuditSummary`.
- Return `dto.PaymentOrderInfoFromModel(order)`.

- [x] **Step 3: Verify green**

Run:

```powershell
go test ./internal/router -run "TestUserCancelsPendingPaymentOrder" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/PAYMENTS.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document cancellation API**

Add `POST /v0/user/payment/orders/:order_no/cancel` to user payment API docs. State that it only closes pending orders, is idempotent for already closed orders, and never grants quota.

- [x] **Step 2: Sync payment contract and testing docs**

Update payment state-machine text and test coverage references with `TestUserCancelsPendingPaymentOrder`.

- [x] **Step 3: Update Apifox**

Add the cancel path returning `PaymentOrderInfo` inside the standard response envelope.

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
git add internal/router/router_test.go internal/service/user_service.go internal/handler/user_handler.go internal/router/user_router.go docs/API.md docs/PAYMENTS.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-payment-order-cancel.md
git commit -m "feat: cancel payment orders"
```
