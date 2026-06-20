# Image Generation Count Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject OpenAI-compatible Image Generations requests with an invalid `n` value before upstream routing.

**Architecture:** Reuse the JSON relay parsing path in `internal/service/relay_service.go`. Add an optional `n` validator beside the existing Image Generations prompt and size validators, map it to a stable OpenAI-compatible error code, then update human docs and Apifox descriptions.

**Tech Stack:** Go, Gin router integration tests, RouterX relay service, OpenAPI YAML for Apifox.

---

### Task 1: Image Generation Count Validation

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

Add `TestImageGenerationsRejectsInvalidCountBeforeUpstream` near the existing Image Generations tests. The test should initialize root quota and API Key budget, create an OpenAI-compatible image channel, then send `n=null`, `n=0`, `n=-1`, `n=1.5`, and `n="2"` to `POST /v1/images/generations` with a valid model and prompt. Each request must return HTTP 400 with code `invalid_image_count`, the upstream counter must stay `0`, and user/API Key quota must remain unchanged.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestImageGenerationsRejectsInvalidCountBeforeUpstream -count=1
```

Expected before implementation: at least one invalid count request reaches upstream or returns a non-400 response.

- [x] **Step 3: Write minimal implementation**

Add `N json.RawMessage` to the relay request payload, add `errInvalidImageCount`, validate `n` only for `relay.APIImagesGenerations`, accept missing `n`, and accept only integer values `>= 1` when present.

- [x] **Step 4: Update docs and Apifox**

Update `docs/API.md`, `docs/PROTOCOLS.md`, `docs/RELAY.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` so Image Generations documents `invalid_image_count` for invalid `n`; keep protected pricing configuration text untouched.

- [x] **Step 5: Verify and commit**

Run:

```powershell
go test ./internal/router -run "TestImageGenerationsRejectsInvalidCountBeforeUpstream|TestImageGenerationsRejectsInvalidPromptBeforeUpstream|TestImageGenerationsRejectsInvalidSizeBeforeUpstream|TestImageGenerationsPassthroughUsesMinimumChargeWithoutUsage|TestApifoxOpenAPI" -count=1
go test ./...
git diff --check
<run the protected pricing diff guard from the active thread notes>
git status --short
```

Expected: tests pass, diff check has no errors, the protected pricing guard has no actual diff output, and only this slice's files are changed before commit.
