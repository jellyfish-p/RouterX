# Gemini Embedding Request Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Gemini `embedContent` and `batchEmbedContents` malformed request bodies a stable local error code before Relay routing.

**Architecture:** Keep validation at the existing Gemini-to-OpenAI conversion boundary, because that is the earliest point with Gemini request semantics. Add one sentinel error that preserves detailed messages while mapping all Gemini embedding shape failures to a stable code.

**Tech Stack:** Go service tests, RouterX relay service, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Stable Error Code

**Files:**
- Modify: `internal/service/relay_service_test.go`
- Modify: `internal/service/relay_service.go`
- Modify: `docs/API.md`
- Modify: `docs/PROTOCOLS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Write the failing test**

Add Gemini embedding malformed request cases to `TestProtocolWrapperRequestErrorsUseStableCodes`.

- [x] **Step 2: Run the focused test and verify it fails**

Run: `go test ./internal/service -run TestProtocolWrapperRequestErrorsUseStableCodes -count=1`

Expected: FAIL with current `invalid_request` code.

- [x] **Step 3: Implement the minimal mapping**

Add `errInvalidGeminiEmbeddingRequest`, wrap Gemini embedding shape errors with that sentinel, and map it to `invalid_gemini_embedding_request`.

- [x] **Step 4: Update docs and Apifox descriptions**

Document the stable local error code for Gemini embedding request shape failures.

- [x] **Step 5: Verify and commit**

Run targeted tests, full tests, `git diff --check`, and the protected pricing diff guard, then commit.
