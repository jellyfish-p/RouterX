# Developer Minimal Access Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the P0 developer minimal access evidence from a generic placeholder into concrete, test-backed documentation.

**Architecture:** Add a small documentation contract test in `internal/router/router_test.go` that parses the P0 rows in `docs/TRACEABILITY.md` and rejects vague evidence placeholders. Then update the traceability and testing documents so P0-C16 points at concrete tests that already exercise `/v1/models`, `/v1/chat/completions`, API Key auth, request IDs, logs, and usage facts.

**Tech Stack:** Go `testing`, existing router test helpers, Markdown documentation, Git verification.

---

### Task 1: P0 Evidence Guard

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`

- [ ] **Step 1: Write the failing test**

Add `TestTraceabilityP0RowsUseConcreteEvidence` near the existing Apifox/documentation tests. The test should read `docs/TRACEABILITY.md`, inspect only rows whose ID starts with `P0-C`, and fail when the evidence cell contains generic placeholder phrases such as `验收`, `待补`, `仍需`, `TODO`, or `未覆盖` without being tied to concrete test names.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/router -run TestTraceabilityP0RowsUseConcreteEvidence -count=1`

Expected: FAIL on P0-C16 because its evidence still contains generic SDK/HTTP wording.

- [ ] **Step 3: Update the docs**

Change P0-C16 evidence to concrete tests already covering the minimal access path, and add the new traceability guard to `docs/TESTING.md`.

- [ ] **Step 4: Verify green**

Run: `go test ./internal/router -run "TestTraceabilityP0RowsUseConcreteEvidence|TestApifoxOpenAPI" -count=1`

Expected: PASS.

- [ ] **Step 5: Final checks and archive**

Run `go test ./...`, `git diff --check`, and the protected pricing diff guard. If all pass and the protected guard has no actual diff output, stage and commit.
