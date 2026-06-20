package service

import (
	"os"
	"sort"
	"strings"
	"sync"
)

type infrastructureErrorMetricKey struct {
	Operation string
}

// InfrastructureErrorMetricSample is a low-cardinality DB/Redis error counter sample.
type InfrastructureErrorMetricSample struct {
	Operation string
	Count     int64
}

var infrastructureErrorMetrics = struct {
	sync.Mutex
	dbErrors    map[infrastructureErrorMetricKey]int64
	redisErrors map[infrastructureErrorMetricKey]int64
}{
	dbErrors:    map[infrastructureErrorMetricKey]int64{},
	redisErrors: map[infrastructureErrorMetricKey]int64{},
}

// ResetInfrastructureErrorMetrics clears process-local infrastructure counters.
func ResetInfrastructureErrorMetrics() {
	infrastructureErrorMetrics.Lock()
	defer infrastructureErrorMetrics.Unlock()
	infrastructureErrorMetrics.dbErrors = map[infrastructureErrorMetricKey]int64{}
	infrastructureErrorMetrics.redisErrors = map[infrastructureErrorMetricKey]int64{}
}

// RecordDBError increments the DB error counter for the given operation.
func RecordDBError(operation string) {
	recordInfrastructureError("db", operation)
}

// RecordRedisError increments the Redis error counter for the given operation.
func RecordRedisError(operation string) {
	recordInfrastructureError("redis", operation)
}

// RedisRequiredForCurrentMode captures the production safety boundary shared by
// readiness checks and request-time controls: external SQL backends need Redis
// for cross-instance caches and critical fixed-window limits.
func RedisRequiredForCurrentMode() bool {
	dsn := strings.TrimSpace(os.Getenv("SQL_DSN"))
	if dsn == "" || strings.HasPrefix(dsn, "sqlite://") || strings.HasPrefix(dsn, "file:") {
		return false
	}
	return true
}

// InfrastructureErrorMetricsSnapshot returns sorted copies for Prometheus rendering.
func InfrastructureErrorMetricsSnapshot() ([]InfrastructureErrorMetricSample, []InfrastructureErrorMetricSample) {
	infrastructureErrorMetrics.Lock()
	defer infrastructureErrorMetrics.Unlock()
	return infrastructureErrorSamples(infrastructureErrorMetrics.dbErrors), infrastructureErrorSamples(infrastructureErrorMetrics.redisErrors)
}

func recordInfrastructureError(kind, operation string) {
	operation = normalizeInfrastructureMetricOperation(operation)
	infrastructureErrorMetrics.Lock()
	defer infrastructureErrorMetrics.Unlock()
	key := infrastructureErrorMetricKey{Operation: operation}
	switch kind {
	case "db":
		infrastructureErrorMetrics.dbErrors[key]++
	case "redis":
		infrastructureErrorMetrics.redisErrors[key]++
	}
}

func infrastructureErrorSamples(counters map[infrastructureErrorMetricKey]int64) []InfrastructureErrorMetricSample {
	keys := make([]infrastructureErrorMetricKey, 0, len(counters))
	for key := range counters {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].Operation < keys[j].Operation
	})
	samples := make([]InfrastructureErrorMetricSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, InfrastructureErrorMetricSample{
			Operation: key.Operation,
			Count:     counters[key],
		})
	}
	return samples
}

func normalizeInfrastructureMetricOperation(operation string) string {
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation == "" {
		return "unknown"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range operation {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	normalized := strings.Trim(b.String(), "_")
	if normalized == "" {
		return "unknown"
	}
	return normalized
}
