# OpenAI Text Request Body Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace generic Apifox JSON placeholders for OpenAI Responses and Legacy Completions with typed, human-readable request body documentation.

**Architecture:** Keep this as a documentation-contract slice. Add a router test that rejects generic request bodies for the two text-generation OpenAI-compatible endpoints, then define named OpenAPI requestBody components with field descriptions and examples.

**Tech Stack:** Go router tests, Apifox OpenAPI YAML, Markdown testing docs.

---

### Task 1: Typed Request Body Documentation

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `docs/apifox/openapi.yaml`
- Modify: `docs/TESTING.md`
- Create: `docs/superpowers/plans/2026-06-19-openai-text-requestbody-docs.md`

- [x] **Step 1: Write the failing Apifox contract test**

Assert that `POST /v1/responses` and `POST /v1/completions` do not use `#/components/requestBodies/GenericJSON` or an equivalent open object placeholder.

- [x] **Step 2: Run the focused test and verify it fails**

Run: `go test ./internal/router -run TestApifoxOpenAITextEndpointsUseTypedRequestBodies -count=1`

Expected: FAIL listing both endpoints.

- [x] **Step 3: Add typed requestBody components**

Create `OpenAIResponsesJSON` and `OpenAICompletionsJSON` in `docs/apifox/openapi.yaml`, then point the operations at them.

- [x] **Step 4: Update testing docs**

Document the new Apifox contract test in `docs/TESTING.md`.

- [x] **Step 5: Verify and commit**

Run targeted tests, full tests, `git diff --check`, and the protected pricing diff guard, then commit.
