package service

import (
	"sort"
	"strconv"
	"sync"
	"time"

	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/relay"
)

var relayDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type relayDurationMetricKey struct {
	Protocol string
	APIType  string
	Provider string
}

type upstreamDurationMetricKey struct {
	Provider  string
	ChannelID string
	Status    string
}

type channelProbeMetricKey struct {
	Result string
}

type relayHistogramValue struct {
	Buckets    []int64
	Count      int64
	SumSeconds float64
}

// RelayDurationMetricSample is a Prometheus histogram snapshot for RouterX relay work.
type RelayDurationMetricSample struct {
	Protocol   string
	APIType    string
	Provider   string
	Buckets    []HistogramBucket
	Count      int64
	SumSeconds float64
}

// UpstreamDurationMetricSample is a Prometheus histogram snapshot for upstream calls.
type UpstreamDurationMetricSample struct {
	Provider   string
	ChannelID  string
	Status     string
	Buckets    []HistogramBucket
	Count      int64
	SumSeconds float64
}

// ChannelProbeMetricSample is a low-cardinality breaker probe counter sample.
type ChannelProbeMetricSample struct {
	Result string
	Count  int64
}

// HistogramBucket stores one cumulative histogram bucket.
type HistogramBucket struct {
	Le    string
	Count int64
}

var relayMetrics = struct {
	sync.Mutex
	relayDurations    map[relayDurationMetricKey]relayHistogramValue
	upstreamDurations map[upstreamDurationMetricKey]relayHistogramValue
	channelProbes     map[channelProbeMetricKey]int64
}{
	relayDurations:    map[relayDurationMetricKey]relayHistogramValue{},
	upstreamDurations: map[upstreamDurationMetricKey]relayHistogramValue{},
	channelProbes:     map[channelProbeMetricKey]int64{},
}

// ResetRelayMetrics clears in-memory relay metrics between router instances.
func ResetRelayMetrics() {
	relayMetrics.Lock()
	defer relayMetrics.Unlock()
	relayMetrics.relayDurations = map[relayDurationMetricKey]relayHistogramValue{}
	relayMetrics.upstreamDurations = map[upstreamDurationMetricKey]relayHistogramValue{}
	relayMetrics.channelProbes = map[channelProbeMetricKey]int64{}
}

// RelayMetricsSnapshot returns stable, sorted histogram copies for /metrics rendering.
func RelayMetricsSnapshot() ([]RelayDurationMetricSample, []UpstreamDurationMetricSample) {
	relayMetrics.Lock()
	defer relayMetrics.Unlock()

	relayKeys := make([]relayDurationMetricKey, 0, len(relayMetrics.relayDurations))
	for key := range relayMetrics.relayDurations {
		relayKeys = append(relayKeys, key)
	}
	sort.Slice(relayKeys, func(i, j int) bool {
		if relayKeys[i].Protocol != relayKeys[j].Protocol {
			return relayKeys[i].Protocol < relayKeys[j].Protocol
		}
		if relayKeys[i].APIType != relayKeys[j].APIType {
			return relayKeys[i].APIType < relayKeys[j].APIType
		}
		return relayKeys[i].Provider < relayKeys[j].Provider
	})
	relaySamples := make([]RelayDurationMetricSample, 0, len(relayKeys))
	for _, key := range relayKeys {
		value := relayMetrics.relayDurations[key]
		relaySamples = append(relaySamples, RelayDurationMetricSample{
			Protocol:   key.Protocol,
			APIType:    key.APIType,
			Provider:   key.Provider,
			Buckets:    relayHistogramBuckets(value),
			Count:      value.Count,
			SumSeconds: value.SumSeconds,
		})
	}

	upstreamKeys := make([]upstreamDurationMetricKey, 0, len(relayMetrics.upstreamDurations))
	for key := range relayMetrics.upstreamDurations {
		upstreamKeys = append(upstreamKeys, key)
	}
	sort.Slice(upstreamKeys, func(i, j int) bool {
		if upstreamKeys[i].Provider != upstreamKeys[j].Provider {
			return upstreamKeys[i].Provider < upstreamKeys[j].Provider
		}
		if upstreamKeys[i].ChannelID != upstreamKeys[j].ChannelID {
			return upstreamKeys[i].ChannelID < upstreamKeys[j].ChannelID
		}
		return upstreamKeys[i].Status < upstreamKeys[j].Status
	})
	upstreamSamples := make([]UpstreamDurationMetricSample, 0, len(upstreamKeys))
	for _, key := range upstreamKeys {
		value := relayMetrics.upstreamDurations[key]
		upstreamSamples = append(upstreamSamples, UpstreamDurationMetricSample{
			Provider:   key.Provider,
			ChannelID:  key.ChannelID,
			Status:     key.Status,
			Buckets:    relayHistogramBuckets(value),
			Count:      value.Count,
			SumSeconds: value.SumSeconds,
		})
	}
	return relaySamples, upstreamSamples
}

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

func (s *RelayService) recordRelayDuration(apiType relay.APIType, channel *model.Channel, duration time.Duration) {
	if channel == nil {
		return
	}
	key := relayDurationMetricKey{
		Protocol: relayMetricProtocol(apiType),
		APIType:  relayMetricAPIType(apiType),
		Provider: relayMetricProvider(channel.Type),
	}
	recordRelayHistogramValue(relayMetrics.relayDurations, key, duration)
}

func (s *RelayService) recordUpstreamDuration(channel *model.Channel, status string, duration time.Duration) {
	if channel == nil {
		return
	}
	key := upstreamDurationMetricKey{
		Provider:  relayMetricProvider(channel.Type),
		ChannelID: strconv.FormatUint(uint64(channel.ID), 10),
		Status:    status,
	}
	recordRelayHistogramValue(relayMetrics.upstreamDurations, key, duration)
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

func recordRelayHistogramValue[K comparable](target map[K]relayHistogramValue, key K, duration time.Duration) {
	seconds := duration.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	relayMetrics.Lock()
	defer relayMetrics.Unlock()
	value := target[key]
	if len(value.Buckets) != len(relayDurationBuckets) {
		value.Buckets = make([]int64, len(relayDurationBuckets))
	}
	for i, bucket := range relayDurationBuckets {
		if seconds <= bucket {
			value.Buckets[i]++
		}
	}
	value.Count++
	value.SumSeconds += seconds
	target[key] = value
}

func relayHistogramBuckets(value relayHistogramValue) []HistogramBucket {
	buckets := make([]HistogramBucket, 0, len(value.Buckets)+1)
	for i, count := range value.Buckets {
		buckets = append(buckets, HistogramBucket{
			Le:    strconv.FormatFloat(relayDurationBuckets[i], 'f', -1, 64),
			Count: count,
		})
	}
	buckets = append(buckets, HistogramBucket{Le: "+Inf", Count: value.Count})
	return buckets
}

func relayMetricProtocol(apiType relay.APIType) string {
	switch apiType {
	case relay.APIGeminiGenerateContent, relay.APIGeminiStreamGenerateContent, relay.APIGeminiCountTokens, relay.APIGeminiEmbedContent, relay.APIGeminiBatchEmbedContents:
		return "gemini"
	case relay.APIAnthropicMessages, relay.APIAnthropicCountTokens:
		return "anthropic"
	default:
		return "openai"
	}
}

func relayMetricAPIType(apiType relay.APIType) string {
	switch apiType {
	case relay.APIChatCompletions:
		return "chat"
	case relay.APICompletions:
		return "completions"
	case relay.APIResponses:
		return "responses"
	case relay.APIEmbeddings:
		return "embeddings"
	case relay.APIImagesGenerations:
		return "images.generations"
	case relay.APIImagesEdits:
		return "images.edits"
	case relay.APIImagesVariations:
		return "images.variations"
	case relay.APIAudioTranscriptions:
		return "audio.transcriptions"
	case relay.APIAudioTranslations:
		return "audio.translations"
	case relay.APIAudioSpeech:
		return "audio.speech"
	case relay.APIModerations:
		return "moderations"
	case relay.APIGeminiGenerateContent:
		return "generate_content"
	case relay.APIGeminiStreamGenerateContent:
		return "stream_generate_content"
	case relay.APIGeminiCountTokens:
		return "count_tokens"
	case relay.APIGeminiEmbedContent:
		return "embed_content"
	case relay.APIGeminiBatchEmbedContents:
		return "batch_embed_contents"
	case relay.APIAnthropicMessages:
		return "messages"
	case relay.APIAnthropicCountTokens:
		return "count_tokens"
	default:
		return "unknown"
	}
}

func relayMetricProvider(channelType int) string {
	switch channelType {
	case common.ChannelTypeOpenAI:
		return "openai"
	case common.ChannelTypeAzure:
		return "azure"
	case common.ChannelTypeClaude:
		return "anthropic"
	case common.ChannelTypeGemini:
		return "gemini"
	case common.ChannelTypeQwen:
		return "qwen"
	case common.ChannelTypeDeepSeek:
		return "deepseek"
	case common.ChannelTypeXAI:
		return "xai"
	case common.ChannelTypeRouterX:
		return "routerx"
	case common.ChannelTypeOpenAICompat:
		return "openai-compatible"
	default:
		return "unknown"
	}
}
