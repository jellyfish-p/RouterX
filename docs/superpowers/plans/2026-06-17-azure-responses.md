# Azure Responses Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Azure OpenAI non-streaming Responses forwarding for `POST /v1/responses`, including usage extraction, quota deduction, and Apifox documentation.

**Architecture:** Reuse the existing JSON Relay path and OpenAI-compatible usage extractor. Azure Responses is a JSON `/openai/v1` API, so `ConvertRequest` must preserve `model` as the Azure deployment name while stripping RouterX private fields; the adapter endpoint points at `/openai/v1/responses?api-version=preview` and response conversion keeps OpenAI-compatible JSON.

**Tech Stack:** Go, Gin router integration tests, RouterX Azure adapter, OpenAPI YAML for Apifox import.

---

### Task 1: Azure Responses JSON Forwarding

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/relay/azure.go`
- Modify: `docs/API.md`
- Modify: `docs/PROTOCOLS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [ ] **Step 1: Write the failing test**

Add `TestAzureResponsesUsesV1EndpointAndUsage` near the other Azure adapter tests. The test should create an Azure channel, call `POST /v1/responses`, and assert the upstream receives:

```text
POST /openai/v1/responses?api-version=preview
api-key: azure-secret
```

The JSON body must keep `model` and `input`, remove `routerx`, return a Responses JSON payload with `usage.input_tokens`, `usage.output_tokens`, and `usage.total_tokens`, deduct quota by `total_tokens`, and write a success log with upstream usage.

- [ ] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestAzureResponsesUsesV1EndpointAndUsage -count=1
```

Expected: FAIL before any upstream call because the Azure adapter does not yet allow `APIResponses` through JSON request conversion.

- [ ] **Step 3: Write minimal implementation**

In `internal/relay/azure.go`:

```go
case APIResponses:
	return "/openai/v1/responses?api-version=" + azureV1PreviewAPIVersion
```

Allow `APIResponses` in `ConvertResponse`, and add `APIResponses` to `azureUsesV1Endpoint` so JSON request conversion preserves `model`.

- [ ] **Step 4: Update docs and Apifox**

Update the Responses rows in `docs/API.md`, `docs/PROTOCOLS.md`, `docs/RELAY.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` to state that Azure Responses is supported with `/openai/v1/responses?api-version=preview`, `api-key`, JSON body stripping of `routerx`, and usage-based billing. Keep full Responses field matrix and streaming Responses marked as remaining work.

- [ ] **Step 5: Run verification**

Run:

```powershell
go test ./internal/router -run "TestAzureResponses|TestResponsesPassthrough" -count=1
go test ./... -count=1
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
git diff --check
```

- [ ] **Step 6: Commit**

Run:

```powershell
git add internal/router/router_test.go internal/relay/azure.go docs/API.md docs/PROTOCOLS.md docs/RELAY.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-azure-responses.md
git commit -m "feat: support azure responses"
```
