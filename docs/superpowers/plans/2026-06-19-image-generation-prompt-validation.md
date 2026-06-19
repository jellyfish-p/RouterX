# Image Generation Prompt Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject OpenAI-compatible Image Generations requests with a missing, null, non-string, or blank `prompt` before upstream routing.

**Architecture:** Reuse the existing JSON relay parsing path in `internal/service/relay_service.go`. Add a prompt validator beside the current Image Generations size validator, map it to a stable OpenAI-compatible error code, then update the human docs and Apifox schema descriptions.

**Tech Stack:** Go, Gin router integration tests, RouterX relay service, OpenAPI YAML for Apifox.

---

### Task 1: Image Generation Prompt Validation

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

Add `TestImageGenerationsRejectsInvalidPromptBeforeUpstream` near the existing Image Generations tests. The test should create a local upstream counter, initialize root quota and API Key budget, create an OpenAI-compatible image channel, then send missing, `null`, numeric, and blank `prompt` bodies to `POST /v1/images/generations`. Each request must return HTTP 400 with code `invalid_image_prompt`, the upstream counter must stay `0`, and user/API Key quota must remain unchanged.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestImageGenerationsRejectsInvalidPromptBeforeUpstream -count=1
```

Expected before implementation: at least one invalid prompt request reaches upstream or returns a non-400 response.

- [x] **Step 3: Write minimal implementation**

Add `Prompt json.RawMessage` to the relay request payload, add `errInvalidImagePrompt`, validate `prompt` only for `relay.APIImagesGenerations`, and map it to `invalid_image_prompt`.

- [x] **Step 4: Update docs and Apifox**

Update `docs/API.md`, `docs/PROTOCOLS.md`, `docs/RELAY.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` so Image Generations documents `invalid_image_prompt` for missing, non-string, or blank `prompt`; keep protected pricing configuration text untouched.

- [x] **Step 5: Verify and commit**

Run:

```powershell
go test ./internal/router -run "TestImageGenerationsRejectsInvalidPromptBeforeUpstream|TestImageGenerationsRejectsInvalidSizeBeforeUpstream|TestImageGenerationsPassthroughUsesMinimumChargeWithoutUsage|TestApifoxOpenAPI" -count=1
go test ./...
git diff --check
<run the protected pricing diff guard from the active thread notes>
git status --short
```

Expected: tests pass, diff check has no errors, the protected pricing guard has no actual diff output, and only this slice's files are changed before commit.
