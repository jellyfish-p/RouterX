# Azure Audio Speech Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Azure OpenAI Audio Speech relay support with RouterX billing, logging, and Apifox documentation aligned.

**Architecture:** Reuse the existing OpenAI-compatible `/v1/audio/speech` handler and raw-response billing path. Extend only the Azure adapter so JSON speech requests use Azure's `/openai/v1/audio/speech?api-version=preview` endpoint, keep `model` as the deployment selector, strip RouterX private fields, and preserve the upstream audio response Content-Type.

**Tech Stack:** Go, Gin router integration tests, RouterX relay adapters, OpenAPI YAML for Apifox.

---

### Task 1: Azure Audio Speech Relay

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

Add `TestAzureAudioSpeechUsesV1EndpointAndMinimumCharge` near the Azure adapter tests. The test creates an Azure channel, sends `POST /v1/audio/speech`, and asserts the upstream receives:

```text
POST /openai/v1/audio/speech?api-version=preview
api-key: azure-secret
```

The upstream JSON body must preserve `model`, `input`, and `voice`, omit `routerx`, and return binary `audio/mpeg`. RouterX must return those bytes with the upstream Content-Type, deduct exactly `1` from token and user quota, and write a success log with `usage_source=minimum`.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run TestAzureAudioSpeechUsesV1EndpointAndMinimumCharge -count=1
```

Expected: FAIL because `AzureAdapter.ConvertRequest` rejects `APIAudioSpeech`.

- [x] **Step 3: Write minimal implementation**

Update `internal/relay/azure.go` so `APIAudioSpeech` is accepted by `ConvertRequest`; the Azure `/openai/v1` API types preserve `model`, while deployment-path API types still strip it. Add `APIAudioSpeech` endpoint:

```text
/openai/v1/audio/speech?api-version=preview
```

- [x] **Step 4: Update docs and Apifox**

Update `docs/API.md`, `docs/RELAY.md`, `docs/PROTOCOLS.md`, `docs/TESTING.md`, `docs/TRACEABILITY.md`, and `docs/apifox/openapi.yaml` to describe Azure Audio Speech support and keep Audio Transcriptions/Translations marked as remaining work.

- [x] **Step 5: Verify and commit**

Run:

```powershell
go test ./internal/router -run "TestAzure(ChatCompletionUsesDeploymentPathAndAPIKey|CompletionsUsesDeploymentPathAndAPIKey|ChannelFetchModelsUsesDeploymentsEndpoint|EmbeddingsUsesDeploymentPathAndAPIKey|ImageGenerationsUsesV1EndpointAndMinimumCharge|AudioSpeechUsesV1EndpointAndMinimumCharge)" -count=1
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
go test ./... -count=1
git diff --check
```

Then stage and commit:

```powershell
git add internal/router/router_test.go internal/relay/azure.go docs/API.md docs/RELAY.md docs/PROTOCOLS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-azure-audio-speech.md
git commit -m "feat: support azure audio speech"
```
