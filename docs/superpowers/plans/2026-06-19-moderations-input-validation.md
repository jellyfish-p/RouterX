# Moderations Input Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject invalid OpenAI-compatible Moderations `input` values locally before upstream calls or billing.

**Architecture:** Reuse the existing OpenAI-compatible JSON relay parsing path in `internal/service/relay_service.go`. Add a Moderations-specific input validator beside the existing Embeddings, Images, and Audio validators, then document the stable error code in the human docs and Apifox OpenAPI file.

**Tech Stack:** Go, Gin router integration tests, RouterX relay service, OpenAPI YAML for Apifox.

---

### Task 1: Moderations Input Validation

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/service/relay_service.go`
- Modify: `docs/API.md`
- Modify: `docs/PROTOCOLS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Write the failing test**

Add `TestModerationsRejectsInvalidInputBeforeUpstream` near the existing Moderations tests. The test creates an OpenAI-compatible channel, sends invalid `input` values to `POST /v1/moderations`, and asserts RouterX returns `400 invalid_moderation_input`, does not call upstream, and does not deduct the user quota or API Key budget.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestModerationsRejectsInvalidInputBeforeUpstream -count=1
```

Expected: FAIL because the current Moderations relay path accepts invalid `input` and calls upstream.

- [x] **Step 3: Write minimal implementation**

Add `errInvalidModerationInput`, `validateModerationInput`, and a `relayRequestErrorCode` mapping to `invalid_moderation_input`. The validator accepts a non-empty string or non-empty array of non-empty strings, and rejects missing, null, blank, empty-array, mixed-type, or blank-item values.

- [x] **Step 4: Update docs and Apifox**

Update the Moderations rows and OpenAI error description in `docs/API.md`, `docs/PROTOCOLS.md`, `docs/RELAY.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` to describe the new local validation and error code.

- [x] **Step 5: Verify and commit**

Run:

```powershell
gofmt -w internal/service/relay_service.go internal/router/router_test.go
go test ./internal/router -run "TestModerationsRejectsInvalidInputBeforeUpstream|TestModerationsPassthroughUsesMinimumChargeWithoutUsage|TestAzureModerationsUnsupportedAPITypeDoesNotCallUpstream|TestApifoxOpenAPI" -count=1
go test ./...
git diff --check
git diff -G "billing\.|倍率|user_group_ratios|channel_group_ratios|effective_ratio|default_ratio|multiplier" --
```

Then stage and commit:

```powershell
git add internal/router/router_test.go internal/service/relay_service.go docs/API.md docs/PROTOCOLS.md docs/RELAY.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-19-moderations-input-validation.md
git commit -m "feat: validate moderations input"
```
