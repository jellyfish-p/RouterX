package middleware

import (
	"sort"
	"strings"
	"sync"
)

type apiKeyAuthMetricKey struct {
	Result string
	Reason string
}

// APIKeyAuthMetricSample is a low-cardinality snapshot of API key authentication outcomes.
type APIKeyAuthMetricSample struct {
	Result string
	Reason string
	Count  int64
}

var apiKeyAuthMetrics = struct {
	sync.Mutex
	counters map[apiKeyAuthMetricKey]int64
}{
	counters: map[apiKeyAuthMetricKey]int64{},
}

// ResetAPIKeyAuthMetrics clears in-memory API key auth counters for process starts and tests.
func ResetAPIKeyAuthMetrics() {
	apiKeyAuthMetrics.Lock()
	defer apiKeyAuthMetrics.Unlock()
	apiKeyAuthMetrics.counters = map[apiKeyAuthMetricKey]int64{}
}

// APIKeyAuthMetricsSnapshot returns stable, sorted copies for Prometheus rendering.
func APIKeyAuthMetricsSnapshot() []APIKeyAuthMetricSample {
	apiKeyAuthMetrics.Lock()
	defer apiKeyAuthMetrics.Unlock()

	samples := make([]APIKeyAuthMetricSample, 0, len(apiKeyAuthMetrics.counters))
	for key, count := range apiKeyAuthMetrics.counters {
		samples = append(samples, APIKeyAuthMetricSample{
			Result: key.Result,
			Reason: key.Reason,
			Count:  count,
		})
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Result != samples[j].Result {
			return samples[i].Result < samples[j].Result
		}
		return samples[i].Reason < samples[j].Reason
	})
	return samples
}

func recordAPIKeyAuthMetric(result, reason string) {
	result = normalizeAPIKeyAuthMetricDimension(result, "unknown")
	reason = normalizeAPIKeyAuthMetricDimension(reason, "unknown")

	apiKeyAuthMetrics.Lock()
	defer apiKeyAuthMetrics.Unlock()
	apiKeyAuthMetrics.counters[apiKeyAuthMetricKey{Result: result, Reason: reason}]++
}

func normalizeAPIKeyAuthMetricDimension(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}
