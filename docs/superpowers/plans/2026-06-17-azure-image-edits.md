# Azure Image Edits Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Azure OpenAI Image Edits multipart forwarding for `POST /v1/images/edits` while keeping `POST /v1/images/variations` limited to providers that actually expose that endpoint.

**Architecture:** Reuse the existing Relay multipart path, which rewrites the form model field, removes the `routerx` form field, preserves file parts, and calls adapters through `DoRequestWithContentType`. Extend only the Azure adapter endpoint/response allow-list for `APIImagesEdits`, using Azure's `/openai/v1/images/edits?api-version=preview` shape and `api-key` authentication. Document that Azure image variations are not advertised until an upstream Azure endpoint is verified.

**Tech Stack:** Go, Gin router tests, Azure adapter, OpenAPI YAML for Apifox import.

---

### Task 1: Azure Image Edits Adapter Support

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/relay/azure.go`
- Modify: `docs/PROTOCOLS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [ ] **Step 1: Write the failing test**

Add `TestAzureImageEditsMultipartUsesV1EndpointAndMinimumCharge` near the other Azure advanced API tests. The test should create an Azure channel, send multipart data to `/v1/images/edits`, assert the upstream path is `/openai/v1/images/edits`, query `api-version=preview`, `api-key` is used instead of Bearer auth, `routerx` is stripped, `model` and prompt fields are preserved, image and mask files arrive intact, and minimum charge deducts one quota.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/router -run TestAzureImageEditsMultipartUsesV1EndpointAndMinimumCharge -count=1`

Expected: FAIL because the Azure adapter currently returns `unsupported api type` for `APIImagesEdits`.

- [ ] **Step 3: Write minimal implementation**

In `internal/relay/azure.go`, add:

```go
case APIImagesEdits:
	return "/openai/v1/images/edits?api-version=" + azureV1PreviewAPIVersion
```

Allow `APIImagesEdits` in `ConvertResponse`, and update the adapter comment so Azure `/openai/v1` image/audio coverage is accurate.

- [ ] **Step 4: Update docs and Apifox**

Update protocol and relay docs to say Azure Image Edits is supported through `/openai/v1/images/edits?api-version=preview`. Azure Image Variations was verified later through `/openai/v1/images/variations?api-version=preview` and is tracked by `TestAzureImageVariationsMultipartUsesV1EndpointAndMinimumCharge`. Add the Azure Image Edits test to `docs/TESTING.md` and adjust Apifox descriptions for `/v1/images/edits`.

- [ ] **Step 5: Run verification**

Run:

```powershell
go test ./internal/router -run TestAzureImageEditsMultipartUsesV1EndpointAndMinimumCharge -count=1
go test ./internal/router -run "TestAzureImage(Generations|Edits)|TestImageMultipartPassthrough" -count=1
go test ./... -count=1
git diff --check
```

- [ ] **Step 6: Commit**

Run:

```powershell
git add internal/router/router_test.go internal/relay/azure.go docs/PROTOCOLS.md docs/RELAY.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-azure-image-edits.md
git commit -m "feat: support azure image edits"
```
