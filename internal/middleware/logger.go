package middleware

import (
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
)

var httpDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type httpRequestMetricKey struct {
	Method    string
	PathGroup string
	Status    string
}

type httpDurationMetricKey struct {
	Method    string
	PathGroup string
}

type httpDurationMetricValue struct {
	Buckets    []int64
	Count      int64
	SumSeconds float64
}

// HTTPRequestMetricSample is a low-cardinality snapshot of requests handled by Gin.
type HTTPRequestMetricSample struct {
	Method    string
	PathGroup string
	Status    string
	Count     int64
}

// HTTPDurationBucket is one cumulative Prometheus histogram bucket.
type HTTPDurationBucket struct {
	Le    string
	Count int64
}

// HTTPDurationMetricSample is a duration histogram grouped by method and route template.
type HTTPDurationMetricSample struct {
	Method     string
	PathGroup  string
	Buckets    []HTTPDurationBucket
	Count      int64
	SumSeconds float64
}

var httpMetrics = struct {
	sync.Mutex
	requests  map[httpRequestMetricKey]int64
	durations map[httpDurationMetricKey]httpDurationMetricValue
}{
	requests:  map[httpRequestMetricKey]int64{},
	durations: map[httpDurationMetricKey]httpDurationMetricValue{},
}

// Logger Gin 中间件：结构化请求日志。
// 记录每个请求的 method / path / status / latency / client_ip。
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := c.GetHeader("X-Request-Id")
		if requestID == "" {
			if generated, err := common.GenerateRandomString(8); err == nil {
				requestID = generated
			}
		}
		if requestID != "" {
			c.Set("request_id", requestID)
			c.Header("X-Request-Id", requestID)
		}

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		recordHTTPMetrics(c, latency)

		log.Printf("[HTTP] %s %s | %d | %v | %s | request_id=%s",
			c.Request.Method,
			c.Request.URL.Path,
			status,
			latency,
			c.ClientIP(),
			requestID,
		)
	}
}

// ResetHTTPMetrics clears in-memory HTTP metrics. SetupRouter calls it once so tests
// and fresh process starts do not inherit counters from an older router instance.
func ResetHTTPMetrics() {
	httpMetrics.Lock()
	defer httpMetrics.Unlock()
	httpMetrics.requests = map[httpRequestMetricKey]int64{}
	httpMetrics.durations = map[httpDurationMetricKey]httpDurationMetricValue{}
}

// HTTPMetricsSnapshot returns stable, sorted copies for Prometheus rendering.
func HTTPMetricsSnapshot() ([]HTTPRequestMetricSample, []HTTPDurationMetricSample) {
	httpMetrics.Lock()
	defer httpMetrics.Unlock()

	requests := make([]HTTPRequestMetricSample, 0, len(httpMetrics.requests))
	for key, count := range httpMetrics.requests {
		requests = append(requests, HTTPRequestMetricSample{
			Method:    key.Method,
			PathGroup: key.PathGroup,
			Status:    key.Status,
			Count:     count,
		})
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].Method != requests[j].Method {
			return requests[i].Method < requests[j].Method
		}
		if requests[i].PathGroup != requests[j].PathGroup {
			return requests[i].PathGroup < requests[j].PathGroup
		}
		return requests[i].Status < requests[j].Status
	})

	durations := make([]HTTPDurationMetricSample, 0, len(httpMetrics.durations))
	for key, value := range httpMetrics.durations {
		buckets := make([]HTTPDurationBucket, 0, len(value.Buckets)+1)
		for i, count := range value.Buckets {
			buckets = append(buckets, HTTPDurationBucket{
				Le:    strconv.FormatFloat(httpDurationBuckets[i], 'f', -1, 64),
				Count: count,
			})
		}
		buckets = append(buckets, HTTPDurationBucket{Le: "+Inf", Count: value.Count})
		durations = append(durations, HTTPDurationMetricSample{
			Method:     key.Method,
			PathGroup:  key.PathGroup,
			Buckets:    buckets,
			Count:      value.Count,
			SumSeconds: value.SumSeconds,
		})
	}
	sort.Slice(durations, func(i, j int) bool {
		if durations[i].Method != durations[j].Method {
			return durations[i].Method < durations[j].Method
		}
		return durations[i].PathGroup < durations[j].PathGroup
	})
	return requests, durations
}

func recordHTTPMetrics(c *gin.Context, latency time.Duration) {
	pathGroup := c.FullPath()
	if pathGroup == "" {
		pathGroup = "unmatched"
	}
	method := c.Request.Method
	status := strconv.Itoa(c.Writer.Status())
	seconds := latency.Seconds()
	if seconds < 0 {
		seconds = 0
	}

	httpMetrics.Lock()
	defer httpMetrics.Unlock()
	httpMetrics.requests[httpRequestMetricKey{Method: method, PathGroup: pathGroup, Status: status}]++
	key := httpDurationMetricKey{Method: method, PathGroup: pathGroup}
	value := httpMetrics.durations[key]
	if len(value.Buckets) != len(httpDurationBuckets) {
		value.Buckets = make([]int64, len(httpDurationBuckets))
	}
	for i, bucket := range httpDurationBuckets {
		if seconds <= bucket {
			value.Buckets[i]++
		}
	}
	value.Count++
	value.SumSeconds += seconds
	httpMetrics.durations[key] = value
}
