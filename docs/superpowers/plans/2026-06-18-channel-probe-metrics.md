# Channel Probe Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose background channel breaker probe success/failure counters through `/metrics` and keep docs plus Apifox aligned.

**Architecture:** Keep probe observability in the existing in-memory metrics path used by Relay duration metrics. `ChannelService.ProbeTrippedChannelsOnce` records one low-cardinality result counter per tested channel, and `/metrics` renders those counters as Prometheus text.

**Tech Stack:** Go, Gin router integration tests, existing RouterX service metrics helpers, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Add Failing Metrics Test

**Files:**
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Write the failing test**

Add this test near the existing metrics endpoint tests:

```go
func TestMetricsEndpointIncludesChannelProbeCounters(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")

	successUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" {
			t.Fatalf("successful probe should call /v1/models, got %s", req.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-probe-metrics"}]}`))
	}))
	defer successUpstream.Close()

	failedUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "no models", http.StatusInternalServerError)
	}))
	defer failedUpstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	settingSvc := service.NewSettingService()
	for key, value := range map[string]string{
		"observability.metrics_enabled":       "true",
		"routing.channel_cache.enabled":       "false",
		"relay.error_auto_ban":                "true",
		"relay.error_ban_threshold":           "2",
		"relay.error_ban_cooldown_seconds":    "60",
		"relay.error_probe_enabled":           "true",
		"relay.error_probe_interval_seconds":  "60",
		"relay.error_probe_batch_size":        "10",
	} {
		if err := settingSvc.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}

	cooledFailure := time.Now().Add(-2 * time.Minute)
	channels := []model.Channel{
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "probe-metrics-success",
			Models:     "gpt-probe-metrics",
			BaseURL:    successUpstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  cooledFailure,
		},
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "probe-metrics-failed",
			Models:     "gpt-probe-metrics",
			BaseURL:    failedUpstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  cooledFailure.Add(time.Second),
		},
	}
	if err := internal.DB.Create(&channels).Error; err != nil {
		t.Fatal(err)
	}

	summary, err := service.NewChannelService().ProbeTrippedChannelsOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("probe should complete: %v", err)
	}
	if summary.Checked != 2 || summary.Succeeded != 1 || summary.Failed != 1 {
		t.Fatalf("expected one successful and one failed probe, got %+v", summary)
	}

	resp := performJSON(r, http.MethodGet, "/metrics", "", nil)
	body := resp.Body.String()
	if resp.Code != http.StatusOK ||
		!strings.Contains(body, `routerx_channel_probe_total{result="success"} 1`) ||
		!strings.Contains(body, `routerx_channel_probe_total{result="failed"} 1`) {
		t.Fatalf("metrics should include channel probe counters, got %d %s", resp.Code, body)
	}
}
```

- [ ] **Step 2: Verify RED**

Run:

```powershell
go test ./internal/router -run TestMetricsEndpointIncludesChannelProbeCounters -count=1
```

Expected: FAIL because `/metrics` does not yet render `routerx_channel_probe_total`.

### Task 2: Implement Probe Counters

**Files:**
- Modify: `internal/service/relay_metrics.go`
- Modify: `internal/service/channel_service.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: Add in-memory probe metrics**

In `internal/service/relay_metrics.go`:

```go
type channelProbeMetricKey struct {
	Result string
}

type ChannelProbeMetricSample struct {
	Result string
	Count  int64
}
```

Extend `relayMetrics`:

```go
channelProbes map[channelProbeMetricKey]int64
```

Initialize and reset it with the other maps:

```go
channelProbes: map[channelProbeMetricKey]int64{},
```

Add:

```go
func ChannelProbeMetricsSnapshot() []ChannelProbeMetricSample {
	relayMetrics.Lock()
	defer relayMetrics.Unlock()
	keys := make([]channelProbeMetricKey, 0, len(relayMetrics.channelProbes))
	for key := range relayMetrics.channelProbes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].Result < keys[j].Result
	})
	samples := make([]ChannelProbeMetricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, ChannelProbeMetricSample{
			Result: key.Result,
			Count:  relayMetrics.channelProbes[key],
		})
	}
	return samples
}

func recordChannelProbeResult(success bool) {
	result := "failed"
	if success {
		result = "success"
	}
	relayMetrics.Lock()
	defer relayMetrics.Unlock()
	relayMetrics.channelProbes[channelProbeMetricKey{Result: result}]++
}
```

- [ ] **Step 2: Record probe outcomes**

In `internal/service/channel_service.go`, update `ProbeTrippedChannelsOnce`:

```go
if ok && err == nil {
	summary.Succeeded++
	recordChannelProbeResult(true)
	continue
}
summary.Failed++
recordChannelProbeResult(false)
```

- [ ] **Step 3: Render `/metrics` counter**

In `internal/router/router.go`, add to `extendedMetrics`:

```go
ChannelProbes []metricSample
```

Render:

```go
writeLabeledCounter(&b, "routerx_channel_probe_total", "Background channel breaker probes by result.", extended.ChannelProbes)
```

Collect:

```go
metrics.ChannelProbes = collectChannelProbeMetrics()
```

Add:

```go
func collectChannelProbeMetrics() []metricSample {
	rows := service.ChannelProbeMetricsSnapshot()
	samples := make([]metricSample, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, metricSample{
			Labels: []metricLabel{{Name: "result", Value: row.Result}},
			Value:  row.Count,
		})
	}
	return samples
}
```

- [ ] **Step 4: Verify GREEN**

Run:

```powershell
go test ./internal/router -run TestMetricsEndpointIncludesChannelProbeCounters -count=1
```

Expected: PASS.

### Task 3: Update Docs and Apifox

**Files:**
- Modify: `docs/OBSERVABILITY.md`
- Modify: `docs/OPERATIONS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/apifox/openapi.yaml`

- [ ] **Step 1: Document the metric**

Add `routerx_channel_probe_total` to the Prometheus metric lists with labels:

```text
result=success|failed
```

Explain that it counts background breaker probe outcomes and intentionally avoids channel ID labels.

- [ ] **Step 2: Update test and traceability matrices**

Add `TestMetricsEndpointIncludesChannelProbeCounters` to the testing docs and update P1 reliability/observability wording to say background probe result counters are covered.

- [ ] **Step 3: Update Apifox `/metrics` description and sample**

Mention that `/metrics` includes channel probe result counters, and add one sample line if a sample block is present.

### Task 4: Verify and Commit

**Files:**
- All files changed above.

- [ ] **Step 1: Run targeted tests**

```powershell
go test ./internal/router -run "TestMetricsEndpointIncludesChannelProbeCounters|TestMetricsEndpointIncludesRelayPaymentAndInfrastructureSignals" -count=1
```

- [ ] **Step 2: Run full test suite**

```powershell
go test ./... -count=1
```

- [ ] **Step 3: Parse Apifox OpenAPI YAML**

```powershell
bun -e "const fs=require('fs'); const yaml=require('yaml'); yaml.parse(fs.readFileSync('docs/apifox/openapi.yaml','utf8')); console.log('openapi yaml ok')"
```

- [ ] **Step 4: Check diff hygiene**

```powershell
git diff --check
git status --short
```

- [ ] **Step 5: Stage and commit**

```powershell
git add internal/router/router.go internal/router/router_test.go internal/service/channel_service.go internal/service/relay_metrics.go docs/OBSERVABILITY.md docs/OPERATIONS.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-channel-probe-metrics.md
git diff --cached --check
git commit -m "feat: expose breaker probe metrics"
```
