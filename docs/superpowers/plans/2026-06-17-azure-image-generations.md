# Azure Image Generations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Azure OpenAI Image Generations relay support while keeping RouterX billing, logging, and Apifox documentation aligned.

**Architecture:** Extend the existing Azure adapter only for JSON image generation requests. Azure chat/completions/embeddings keep deployment-path behavior, while Image Generations uses the Azure `/openai/v1/images/generations?api-version=preview` endpoint and keeps `model` in the upstream body because Azure uses it as the deployment selector for this API shape.

**Tech Stack:** Go, Gin router tests, RouterX relay adapters, OpenAPI YAML for Apifox.

---

### Task 1: Azure Image Generation Relay

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/relay/azure.go`
- Modify: `docs/API.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/PROTOCOLS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Write the failing test**

Add `TestAzureImageGenerationsUsesV1EndpointAndMinimumCharge` near the other Azure relay tests. The test creates an Azure channel, sends `POST /v1/images/generations`, and asserts that the upstream receives:

```text
POST /openai/v1/images/generations?api-version=preview
api-key: azure-secret
```

The upstream JSON body must preserve `model` and `prompt` but omit `routerx`. The response has no usage, so RouterX must deduct exactly `1` from the token budget and user balance and write a success log with `usage_source=minimum`.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestAzureImageGenerationsUsesV1EndpointAndMinimumCharge -count=1
```

Expected: FAIL because `AzureAdapter.ConvertRequest` rejects `APIImagesGenerations` before implementation.

- [x] **Step 3: Write minimal implementation**

Update `internal/relay/azure.go` so:

```go
APIImagesGenerations
```

is accepted by `ConvertRequest` and `ConvertResponse`; `routerx` is stripped, `model` is preserved for this API type, and `GetAPIEndpoint` returns:

```text
/openai/v1/images/generations?api-version=preview
```

- [x] **Step 4: Update docs and Apifox**

Update the Azure rows in `docs/API.md`, `docs/RELAY.md`, `docs/PROTOCOLS.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` to state that Azure Image Generations is supported with the `/openai/v1/images/generations` path, `api-key`, JSON passthrough, and minimum billing when usage is absent.

- [x] **Step 5: Verify and commit**

Run:

```powershell
go test ./internal/router -run "TestAzure(ChatCompletionUsesDeploymentPathAndAPIKey|CompletionsUsesDeploymentPathAndAPIKey|ChannelFetchModelsUsesDeploymentsEndpoint|EmbeddingsUsesDeploymentPathAndAPIKey|ImageGenerationsUsesV1EndpointAndMinimumCharge)" -count=1
go test ./... -count=1
git diff --check
```

Then stage and commit:

```powershell
git add internal/router/router_test.go internal/relay/azure.go docs/API.md docs/RELAY.md docs/PROTOCOLS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-azure-image-generations.md
git commit -m "feat: support azure image generations"
```
