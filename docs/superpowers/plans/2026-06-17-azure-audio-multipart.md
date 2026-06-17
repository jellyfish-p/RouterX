# Azure Audio Multipart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Azure OpenAI Audio Transcriptions and Audio Translations multipart relay support.

**Architecture:** Reuse RouterX's existing multipart parsing, `routerx` form-field stripping, model rewrite, logging, and minimum-billing path. Extend the Azure adapter with content-type aware requests using `api-key`, and map the two multipart audio API types to Azure `/openai/v1/audio/{operation}?api-version=preview` endpoints.

**Tech Stack:** Go, Gin router integration tests, RouterX relay adapters, OpenAPI YAML for Apifox.

---

### Task 1: Azure Audio Transcriptions/Translations Multipart Relay

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

Add `TestAzureAudioMultipartUsesV1EndpointAndMinimumCharge` near the Azure adapter tests. The test should table-drive:

```text
POST /v1/audio/transcriptions -> /openai/v1/audio/transcriptions?api-version=preview
POST /v1/audio/translations -> /openai/v1/audio/translations?api-version=preview
```

For each case, create a multipart request containing `model`, `prompt`, `routerx`, and `file`. Assert the Azure upstream receives multipart/form-data, `api-key: azure-secret`, no `Authorization`, no leaked `routerx`, preserved `model`, `prompt`, and file bytes. Assert RouterX returns upstream JSON, deducts `1`, and writes a success log with `usage_source=minimum`.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestAzureAudioMultipartUsesV1EndpointAndMinimumCharge -count=1
```

Expected: FAIL because `AzureAdapter` does not yet implement `DoRequestWithContentType`, so multipart Azure relay returns `unsupported_multipart_channel`.

- [x] **Step 3: Write minimal implementation**

Update `internal/relay/azure.go`:

```go
func (a *AzureAdapter) DoRequestWithContentType(ctx context.Context, baseURL, endpoint, apiKey string, body []byte, contentType string) (*http.Response, error)
```

Use the same `api-key` auth as JSON Azure requests and preserve the provided multipart content type. Add endpoint cases:

```text
APIAudioTranscriptions -> /openai/v1/audio/transcriptions?api-version=preview
APIAudioTranslations   -> /openai/v1/audio/translations?api-version=preview
```

Allow both API types in `ConvertResponse` for JSON transcription/translation responses.

- [x] **Step 4: Update docs and Apifox**

Update the OpenAI audio rows and Azure provider rows in `docs/API.md`, `docs/RELAY.md`, `docs/PROTOCOLS.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` to show Azure multipart transcription/translation support. Keep advanced audio limits, format policy, and safety scanning marked as remaining work.

- [x] **Step 5: Verify and commit**

Run:

```powershell
go test ./internal/router -run "TestAzure(ChatCompletionUsesDeploymentPathAndAPIKey|CompletionsUsesDeploymentPathAndAPIKey|ChannelFetchModelsUsesDeploymentsEndpoint|EmbeddingsUsesDeploymentPathAndAPIKey|ImageGenerationsUsesV1EndpointAndMinimumCharge|AudioSpeechUsesV1EndpointAndMinimumCharge|AudioMultipartUsesV1EndpointAndMinimumCharge)" -count=1
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
go test ./... -count=1
git diff --check
```

Then stage and commit:

```powershell
git add internal/router/router_test.go internal/relay/azure.go docs/API.md docs/RELAY.md docs/PROTOCOLS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-azure-audio-multipart.md
git commit -m "feat: support azure audio multipart"
```
