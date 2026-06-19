# Multipart Required File Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject OpenAI-compatible Images and Audio multipart requests that omit their required file field before any upstream call or billing.

**Architecture:** Reuse the existing multipart parsing path in `internal/service/relay_service.go`. Track whether each API's required file form field is present while parsing, then return one stable OpenAI-compatible error code before channel selection and billing when the field is missing.

**Tech Stack:** Go, Gin router integration tests, RouterX relay multipart parser, OpenAPI YAML for Apifox.

---

### Task 1: Multipart Required File Validation

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

Add `TestMultipartRejectsMissingRequiredFileBeforeUpstream` near the multipart relay tests. It should create one OpenAI-compatible channel and send multipart requests without file parts to:

```text
POST /v1/images/edits
POST /v1/images/variations
POST /v1/audio/transcriptions
POST /v1/audio/translations
```

Each request should include `model`, and image edits should include `prompt`. The expected response is `400 multipart_file_required`; upstream calls must remain `0`, and user/API Key quota must remain unchanged.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestMultipartRejectsMissingRequiredFileBeforeUpstream -count=1
```

Expected: FAIL because the current parser accepts multipart requests that omit `image` or `file` and forwards them upstream.

- [x] **Step 3: Write minimal implementation**

Add `errMultipartFileRequired`, map it to `multipart_file_required`, and add helper logic:

```go
func requiredMultipartFileField(apiType relay.APIType) string {
    switch apiType {
    case relay.APIImagesEdits, relay.APIImagesVariations:
        return "image"
    case relay.APIAudioTranscriptions, relay.APIAudioTranslations:
        return "file"
    default:
        return ""
    }
}
```

In `parseMultipartRelayRequest`, record whether a file part with that field name appears. After parsing and routerx option handling, return `errMultipartFileRequired` when the required field is missing.

- [x] **Step 4: Update docs and Apifox**

Update `docs/API.md`, `docs/PROTOCOLS.md`, `docs/RELAY.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` so Image Edits/Variations and Audio Transcriptions/Translations document `multipart_file_required` for missing `image` or `file`.

- [x] **Step 5: Verify and commit**

Run:

```powershell
gofmt -w internal/service/relay_service.go internal/router/router_test.go
go test ./internal/router -run "TestMultipartRejectsMissingRequiredFileBeforeUpstream|TestImageMultipartPassthroughUsesRouteAndMinimumCharge|TestAudioMultipartRejectsInvalidResponseFormatBeforeUpstream|TestApifoxOpenAPI" -count=1
go test ./...
git diff --check
git diff -G "billing\.|倍率|user_group_ratios|channel_group_ratios|effective_ratio|default_ratio|multiplier" --
```

Then stage and commit:

```powershell
git add internal/router/router_test.go internal/service/relay_service.go docs/API.md docs/PROTOCOLS.md docs/RELAY.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-19-multipart-required-file-validation.md
git commit -m "feat: validate multipart required files"
```
