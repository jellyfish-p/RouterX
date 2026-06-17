# Channel Cache Preload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `routing.channel_cache.preload` actively warm the in-process channel candidate cache when enabled.

**Architecture:** Extend the existing `ChannelService` candidate cache config with a `preload` flag, expose a small `PreloadCandidateCache` method, and call it after settings defaults are available in server startup. Channel CRUD cache-version bumps should also opportunistically warm the cache when preload is enabled; failures stay non-fatal because request-time cache reload remains the source of truth.

**Tech Stack:** Go service tests, GORM in-memory SQLite, existing settings table and channel candidate cache.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/service/channel_service_test.go`

- [ ] **Step 1: Add preload-positive test**

Create `TestChannelCandidateCachePreloadWarmsCache`: seed settings with cache enabled, `routing.channel_cache.preload=true`, TTL 60, version 1, create one channel, call `svc.PreloadCandidateCache()`, create a second higher-priority channel, then select candidates and assert only the originally preloaded channel is returned until the version changes.

- [ ] **Step 2: Add preload-disabled test**

Create `TestChannelCandidateCachePreloadSkipsWhenDisabled`: seed settings with cache enabled, `routing.channel_cache.preload=false`, TTL 60, version 1, create one channel, call `svc.PreloadCandidateCache()`, create a second higher-priority channel, then select candidates and assert both channels are visible because preload did not warm the cache.

- [ ] **Step 3: Verify red**

Run:

```powershell
go test ./internal/service -run "TestChannelCandidateCachePreload" -count=1
```

Expected: compile failure because `PreloadCandidateCache` does not exist yet.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/channel_service.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Extend cache config**

Add `preload bool` to `channelCandidateCacheConfig` and read `routing.channel_cache.preload` with default `true`.

- [ ] **Step 2: Add preloader**

Implement `PreloadCandidateCache() error` on `ChannelService`. It should no-op when the service is nil, cache is disabled, or preload is disabled; otherwise load ordered channels and write the existing in-memory cache with the current version and TTL.

- [ ] **Step 3: Warm after version bumps**

After `touchCandidateCacheVersion` increments `routing.channel_cache.version`, call `PreloadCandidateCache()` and intentionally ignore the error so CRUD succeeds even if a warmup load fails.

- [ ] **Step 4: Warm on server startup**

After `settingSvc.EnsureDefaults()` in `cmd/server/main.go`, call `channelSvc.PreloadCandidateCache()` and log a warning on failure.

- [ ] **Step 5: Verify green**

Run:

```powershell
go test ./internal/service -run "TestChannelCandidateCachePreload|TestChannelCandidateCacheUsesVersionInvalidation" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/SETTINGS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [ ] **Step 1: Mark preload as current**

Update `routing.channel_cache.preload` from “validated only” to “implemented”, describing startup and version-bump warmup.

- [ ] **Step 2: Update test and traceability references**

Add the new service tests to docs that list channel cache evidence.

- [ ] **Step 3: Add Apifox example**

Include `routing.channel_cache.preload` in the batch settings example.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [ ] **Step 1: Run full tests**

```powershell
go test ./... -count=1
```

- [ ] **Step 2: Validate Apifox YAML**

```powershell
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
```

- [ ] **Step 3: Check whitespace**

```powershell
git diff --check
```

- [ ] **Step 4: Commit**

```powershell
git add cmd/server/main.go internal/service/channel_service.go internal/service/channel_service_test.go docs/SETTINGS.md docs/RELAY.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-channel-cache-preload.md
git commit -m "feat: preload channel candidate cache"
```
