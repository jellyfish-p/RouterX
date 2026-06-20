# V1 Typed Request Body Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure every `/v1` Apifox request body uses a typed schema instead of the generic JSON placeholder.

**Architecture:** Add a broad Apifox contract test over all `/v1` operations with request bodies. Fix the remaining Gemini streaming request body by reusing the existing Gemini generateContent schema.

**Tech Stack:** Go router tests, Apifox OpenAPI YAML, Markdown testing docs.

---

### Task 1: Typed Request Body Guard

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `docs/apifox/openapi.yaml`
- Modify: `docs/TESTING.md`
- Create: `docs/superpowers/plans/2026-06-19-v1-typed-requestbody-guard.md`

- [x] **Step 1: Write the failing guard test**

Scan all `/v1` OpenAPI operations with request bodies and fail if any still use the generic JSON placeholder.

- [x] **Step 2: Run the focused test and verify it fails**

Run: `go test ./internal/router -run TestApifoxV1OperationsUseTypedRequestBodies -count=1`

Expected: FAIL listing `POST /v1/models/{model}:streamGenerateContent`.

- [x] **Step 3: Reuse the typed Gemini request body**

Point `streamGenerateContent` at `#/components/requestBodies/GeminiGenerateContentJSON`.

- [x] **Step 4: Update testing docs**

Document the new all-`/v1` requestBody guard in `docs/TESTING.md`.

- [x] **Step 5: Verify and commit**

Run targeted tests, full tests, `git diff --check`, and the protected pricing diff guard, then commit.
