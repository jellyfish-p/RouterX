package relay

import (
	"context"
	"net/http"
)

// APIType 标识 OpenAI API 类型。
type APIType int

const (
	APIChatCompletions     APIType = iota // /v1/chat/completions
	APICompletions                        // /v1/completions
	APIImagesGenerations                  // /v1/images/generations
	APIImagesEdits                        // /v1/images/edits
	APIImagesVariations                   // /v1/images/variations
	APIAudioTranscriptions                // /v1/audio/transcriptions
	APIAudioTranslations                  // /v1/audio/translations
	APIAudioSpeech                        // /v1/audio/speech
	APIEmbeddings                         // /v1/embeddings
	APIModels                             // /v1/models
	APIFiles                              // /v1/files
	APIFineTuning                         // /v1/fine_tuning/jobs
	APIModerations                        // /v1/moderations
)

// Usage 用量统计 (OpenAI 标准格式)。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Adapter 厂商适配器接口。
// 所有大模型厂商通过实现此接口接入 RouterX。
//
// 新增厂商步骤:
//  1. 创建 adapter_xxx.go，实现 Adapter 接口
//  2. 在 relay_service.go GetAdapter() 中注册 channel.Type -> Adapter 的映射
type Adapter interface {
	// GetChannelType 返回适配器对应的厂商类型 (对应 common.ChannelType*)
	GetChannelType() int

	// ConvertRequest 将 OpenAI 格式请求体转换为厂商特定请求体。
	// body 是原始 JSON 字节，返回转换后的 JSON 字节。
	ConvertRequest(apiType APIType, body []byte) ([]byte, error)

	// GetAPIEndpoint 返回厂商特定 API 路径。
	// 例如 Azure 的 /openai/deployments/{model}/chat/completions?api-version=xxx
	GetAPIEndpoint(apiType APIType, model string) string

	// DoRequest 发送 HTTP 请求到下游厂商。
	// 返回原始 *http.Response 以便处理流式/非流式。
	DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error)

	// ConvertResponse 将厂商响应体解析为统一的 OpenAI 格式。
	// 返回转换后的 JSON 字节 + 提取的 Usage 信息。
	ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error)

	// GetModelList 从厂商获取可用模型列表。
	GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error)
}

// AdapterConstructor 是 Adapter 的工厂函数类型。
type AdapterConstructor func() Adapter

// registry 存储 channelType -> Adapter 工厂函数的映射。
var registry = make(map[int]AdapterConstructor)

// Register 注册一个适配器工厂。
// 通常在 init() 中调用，如: relay.Register(common.ChannelTypeOpenAI, func() Adapter { return &OpenAIAdapter{} })
func Register(channelType int, constructor AdapterConstructor) {
	registry[channelType] = constructor
}

// GetAdapter 根据 channelType 创建对应的适配器实例。
func GetAdapter(channelType int) (Adapter, bool) {
	constructor, ok := registry[channelType]
	if !ok {
		return nil, false
	}
	return constructor(), true
}

// SupportedTypes 返回所有已注册的 channelType 列表。
func SupportedTypes() []int {
	types := make([]int, 0, len(registry))
	for t := range registry {
		types = append(types, t)
	}
	return types
}
