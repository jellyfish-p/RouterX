# RouterX 模型转发设计

## 目标

Relay 模块是 RouterX 的核心。它接收 OpenAI、Anthropic、Gemini 等不同入口协议的完整请求格式，按路由和通道配置转换为具体上游厂商请求，处理响应、日志和计费。

目标能力：

- 支持 OpenAI 全格式输入，包括 Responses、Chat Completions、Completions、Embeddings、Images、Audio、Models、Moderations 等。
- 支持 Anthropic 全格式输入，包括 Messages、Messages Stream、Count Tokens、Models 等。
- 支持 Gemini 全格式输入，包括 generateContent、streamGenerateContent、countTokens、embedContent、batchEmbedContents、Models 等。
- 支持 OpenAI、Anthropic、Gemini、xAI、Azure OpenAI、Qwen、DeepSeek、通用 OpenAI-Compatible、RouterX-Compatible 等上游对接。
- 支持 provider-specific 额外参数按约定格式透传。
- 支持多层 RouterX 串联调用，避免额外参数丢失、重复转换和路由循环。
- 支持非流式和 SSE 流式响应。
- 支持按模型、优先级、权重、健康状态和余额选择通道。
- 支持下游错误重试、通道熔断和自动恢复。
- 支持 usage 提取、额度扣减和调用日志写入。

## 当前代码基础

已有基础：

- `internal/relay/adapter.go` 定义 Adapter 接口和注册表。
- 各厂商适配器文件已存在并在 `init()` 中注册。
- `internal/router/user_router.go` 已注册部分 `/v1` 路由。
- `internal/model/channel.go` 已定义通道模型。
- `internal/model/log.go` 已定义调用日志模型。
- `internal/model/token.go` 已定义 API Key 和额度字段。

当前缺口：

- API Key 中间件仍是占位实现。
- Relay Handler 返回 `not implemented`。
- Relay Service 未实现通道选择、转发、转换、计费和日志。
- 适配器多数方法为 TODO。

## 核心流程

```text
Client(OpenAI / Anthropic / Gemini format)
    |
    | POST /v1/chat/completions 或 /v1/messages 或 /v1/models/{model}:generateContent
    v
ApiKeyAuthRequired
    |
    | current_user + current_token
    v
RateLimit
    |
    v
RelayHandler
    |
    | detect ingress protocol, read body, parse model, detect stream
    v
RelayService.Handle(apiType, body, context)
    |
    |-- precheck quota
    |-- select channel
    |-- get source translator and upstream adapter
    |-- convert request without dropping supported fields
    |-- call downstream
    |-- convert response or stream chunks
    |-- extract/estimate usage
    |-- deduct quota
    |-- write log
    v
Protocol-compatible Response
```

## 协议范围

RouterX 需要同时区分入口协议和上游厂商。

| 概念 | 示例 | 说明 |
|------|------|------|
| 入口协议 | `openai`、`anthropic`、`gemini` | 客户端发送给 RouterX 的请求格式和期望响应格式 |
| 上游厂商 | `openai`、`anthropic`、`gemini`、`xai`、`azure_openai`、`qwen`、`deepseek`、`routerx` | RouterX 最终调用的下游通道类型 |
| 内部信封 | `RelayEnvelope` | 保存入口协议、API 类型、原始 body、模型、stream、额外参数和上下文 |

设计要求：

- 入口协议决定响应格式，OpenAI 请求返回 OpenAI 兼容结构，Anthropic 请求返回 Anthropic 兼容结构，Gemini 请求返回 Gemini 兼容结构。
- 上游厂商只决定如何调用下游，不应该改变客户端看到的入口协议。
- 转换必须尽量无损，保留 tools/function calling、vision/multimodal、system/developer 指令、response format、reasoning/thinking、cache、metadata、safety settings 等字段。
- 无法语义等价转换的字段必须明确降级、忽略或返回 400，不允许静默误转造成行为不可预期。
- 报文转换可以参考 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 translator/executor 分层思路，但 RouterX 需要保留自身的鉴权、计费、日志和通道路由边界。

## 额外参数透传

额外参数使用保留命名空间 `routerx` 传递，避免污染 OpenAI、Anthropic、Gemini 原生字段。

JSON 请求示例：

```json
{
  "model": "gpt-4o-mini",
  "messages": [{ "role": "user", "content": "hi" }],
  "routerx": {
    "route": {
      "channel_group": "premium",
      "upstream_provider": "anthropic"
    },
    "upstream": {
      "headers": {
        "anthropic-beta": "tools-2025-01-24"
      },
      "query": {
        "api-version": "2024-10-21"
      },
      "body": {
        "provider_extra_param": true
      }
    },
    "provider": {
      "openai": { "reasoning_effort": "medium" },
      "anthropic": { "thinking": { "type": "enabled", "budget_tokens": 1024 } },
      "gemini": { "safetySettings": [] },
      "xai": { "search_parameters": { "mode": "auto" } }
    }
  }
}
```

规则：

- `routerx.route` 只表达 RouterX 路由偏好，如目标渠道、渠道分组、上游 provider、禁用某些 provider 等。
- `routerx.upstream.headers`、`routerx.upstream.query`、`routerx.upstream.body` 用于传递上游调用所需的额外 header、query 和 body 字段。
- `routerx.provider.<provider>` 用于传递只对某个上游 provider 生效的额外参数。
- Adapter 调用真实厂商前必须移除 `routerx` 字段，除非上游通道类型是 `routerx`。
- 禁止透传敏感 header，如 `Authorization`、`Cookie`、`Set-Cookie`、`X-Api-Key`，这些必须来自通道配置。
- multipart 或非 JSON 请求可使用 `routerx` 表单字段传递 JSON 字符串，或使用 `X-RouterX-Options` header 传递 base64url JSON。

## 多层 RouterX 兼容

RouterX 可以把另一个 RouterX 作为上游通道。

兼容规则：

- 上游类型为 `routerx` 时，允许保留 `routerx` 扩展对象继续向下一层传递。
- 每层必须增加或更新 `X-RouterX-Hop`，默认最大跳数建议为 `3`，超过后返回 508 或 400，避免路由循环。
- 每层必须透传或生成 `X-Request-Id`，并可追加 `X-RouterX-Chain` 记录链路摘要。
- 每层只能消费属于自己的 `routerx.route` 指令；未知或下一层需要的 provider 参数必须保留。
- 向真实厂商发起请求前必须剥离 RouterX 私有字段和 `X-RouterX-*` 内部 header。
- 多层转发时 usage 以最后真实厂商返回或最后一层 RouterX 返回为准，上层 RouterX 不能重复估算导致重复计费。

## Adapter 接口

现有接口：

```go
type Adapter interface {
    GetChannelType() int
    ConvertRequest(apiType APIType, body []byte) ([]byte, error)
    GetAPIEndpoint(apiType APIType, model string) string
    DoRequest(ctx context.Context, baseURL, endpoint, apiKey string, body []byte) (*http.Response, error)
    ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error)
    GetModelList(ctx context.Context, baseURL string, apiKey string) ([]string, error)
}
```

建议扩展：

```go
type RequestOptions struct {
    Stream bool
    Headers map[string]string
    Query map[string]string
    ExtraBody map[string]any
    IngressProtocol string
    UpstreamProvider string
}

type Adapter interface {
    GetChannelType() int
    ConvertRequest(apiType APIType, body []byte) ([]byte, *RequestOptions, error)
    BuildRequest(ctx context.Context, channel *model.Channel, endpoint string, body []byte, opts *RequestOptions) (*http.Request, error)
    GetAPIEndpoint(apiType APIType, model string) (string, error)
    ConvertResponse(apiType APIType, body []byte) ([]byte, *Usage, error)
    ConvertStreamChunk(apiType APIType, chunk []byte) ([]byte, *UsageDelta, error)
    GetModelList(ctx context.Context, channel *model.Channel) ([]string, error)
}
```

扩展原因：

- Azure 使用 `api-key` header 和 deployment 路径。
- Gemini 使用 query key 和不同的 body 结构。
- Anthropic 需要 `x-api-key` 和 `anthropic-version`。
- xAI/Grok 基本使用 OpenAI-Compatible API，但有 provider-specific 扩展参数。
- 流式响应需要逐 chunk 转换。

## APIType 映射

| 入口协议 | APIType | 路径 | P 阶段 |
|----------|---------|------|--------|
| OpenAI | `APIResponses` | `/v1/responses` | P1 |
| OpenAI | `APIChatCompletions` | `/v1/chat/completions` | P0 |
| OpenAI | `APICompletions` | `/v1/completions` | P1 |
| OpenAI | `APIEmbeddings` | `/v1/embeddings` | P1 |
| OpenAI | `APIImagesGenerations` | `/v1/images/generations` | P1 |
| OpenAI | `APIAudioTranscriptions` | `/v1/audio/transcriptions` | P1 |
| OpenAI | `APIAudioSpeech` | `/v1/audio/speech` | P1 |
| OpenAI | `APIModels` | `/v1/models` | P0 |
| OpenAI | `APIModerations` | `/v1/moderations` | P2 |
| Anthropic | `APIAnthropicMessages` | `/v1/messages` | P1 |
| Anthropic | `APIAnthropicMessagesCountTokens` | `/v1/messages/count_tokens` | P1 |
| Anthropic | `APIAnthropicModels` | `/v1/models` | P1 |
| Gemini | `APIGeminiGenerateContent` | `/v1/models/{model}:generateContent` | P1 |
| Gemini | `APIGeminiStreamGenerateContent` | `/v1/models/{model}:streamGenerateContent` | P1 |
| Gemini | `APIGeminiCountTokens` | `/v1/models/{model}:countTokens` | P1 |
| Gemini | `APIGeminiEmbedContent` | `/v1/models/{model}:embedContent` | P1 |
| Gemini | `APIGeminiBatchEmbedContents` | `/v1/models/{model}:batchEmbedContents` | P1 |
| Gemini | `APIGeminiModels` | `/v1/models` | P1 |

路径冲突处理：`/v1/models` 同时服务 OpenAI、Anthropic、Gemini 模型列表。默认返回 OpenAI `models` 结构；如请求带有 `x-goog-api-client`、Gemini query 风格、`?routerx_protocol=gemini` 或 `X-RouterX-Protocol: gemini`，返回 Gemini 结构；如请求带有 `anthropic-version`、`?routerx_protocol=anthropic` 或 `X-RouterX-Protocol: anthropic`，返回 Anthropic 结构。

## 通道选择

通道候选条件：

- `status = enabled`。
- 未软删除。
- `models` 包含请求模型，或 `models = "*"`。
- `error_count` 低于熔断阈值。
- 如启用余额检查，`balance > 0`。
- 通道类型有可用 Adapter。

选择策略：

```text
1. 查询所有候选通道
2. 按 priority 分组，选择最高 priority 组
3. 在同组内按 weight 加权随机
4. 如果开启 latency 优化，可将 response_ms 作为降权因子
5. 返回选中的通道
```

加权随机示例：

```text
channels = [A(weight=5), B(weight=3), C(weight=2)]
total = 10
random n in [1,10]
n <= 5       -> A
5 < n <= 8   -> B
8 < n <= 10  -> C
```

失败重试：

- 只在幂等或可安全重试的错误上重试。
- 网络错误、连接超时、502/503/504 可以重试。
- 401/403 不重试，应标记通道配置错误。
- 400 不重试，通常是请求不兼容或参数错误。
- 流式响应一旦向客户端写出 chunk，就不能切换通道重试。

## 厂商适配

适配层需要支持“入口协议 x 上游厂商”的组合转换，而不是只支持 OpenAI 请求转非 OpenAI 上游。

| 入口协议 | 目标上游 | 要求 |
|----------|----------|------|
| OpenAI | OpenAI / Azure / xAI / Qwen / DeepSeek / OpenAI-Compatible | 尽量透传，保留未知兼容字段和 provider-specific 扩展 |
| OpenAI | Anthropic | Chat/Responses 转 Messages，tools、vision、system、thinking/reasoning 需要显式映射 |
| OpenAI | Gemini | Chat/Responses 转 contents/generationConfig/safetySettings/system_instruction |
| Anthropic | Anthropic | 尽量透传 Messages 原生格式 |
| Anthropic | OpenAI / xAI / OpenAI-Compatible | Messages 转 Chat/Responses，保留 tool use/tool result 语义 |
| Anthropic | Gemini | Messages 转 contents/parts，system 转 system_instruction |
| Gemini | Gemini | 尽量透传 generateContent 原生格式 |
| Gemini | OpenAI / xAI / OpenAI-Compatible | contents/parts 转 messages/content parts |
| Gemini | Anthropic | contents/parts 转 Messages content blocks |

全格式输入要求：

- OpenAI：支持 text、image、audio、file content parts，tools/function calling，response_format，parallel_tool_calls，reasoning，metadata，stream_options，modalities 等字段。
- Anthropic：支持 system、messages、content blocks、tool_use、tool_result、thinking、metadata、stop_sequences、stream 等字段。
- Gemini：支持 contents、parts、system_instruction、tools/functionDeclarations、toolConfig、safetySettings、generationConfig、cachedContent、streamGenerateContent 等字段。
- 对暂不支持的字段，Adapter 必须返回明确错误或记录降级原因，不能无提示丢弃影响语义的字段。

### OpenAI 和通用 OpenAI-Compatible

特点：

- 请求体和响应体基本透传。
- 鉴权使用 `Authorization: Bearer <api_key>`。
- Endpoint 使用标准 `/v1/...`。

默认路径：

| API | Endpoint |
|-----|----------|
| Chat | `/v1/chat/completions` |
| Completions | `/v1/completions` |
| Embeddings | `/v1/embeddings` |
| Models | `/v1/models` |

### Azure OpenAI

特点：

- model 通常映射为 deployment name。
- 鉴权使用 `api-key` header。
- 需要 `api-version`。

Endpoint 示例：

```text
/openai/deployments/{deployment}/chat/completions?api-version=2024-10-21
```

设计要求：

- 通道配置需要保存 `api_version`，可放在扩展配置字段或 settings。
- `model` 到 deployment 的映射需要支持别名。

### Anthropic / Claude

特点：

- 使用 Messages API。
- 鉴权使用 `x-api-key`。
- 需要 `anthropic-version`。
- OpenAI `messages` 或 Gemini `contents` 需要转换为 Anthropic `messages` 和 `system` 字段。

转换规则：

- OpenAI `system` 消息合并为 Claude `system`。
- OpenAI `user` 和 `assistant` 保留为 Claude messages。
- `max_tokens` 必填时需补默认值。
- 响应 content 转回 OpenAI `choices[0].message.content`。
- Anthropic 原生入口请求转 Anthropic 上游时应保持原始 Messages 语义。

### Gemini

特点：

- 使用 `contents[].parts[]`。
- API Key 通常在 query 中。
- System message 映射到 `system_instruction`。

Endpoint 示例：

```text
/v1beta/models/{model}:generateContent?key={api_key}
```

转换规则：

- OpenAI `user` 映射 Gemini `role=user`。
- OpenAI `assistant` 映射 Gemini `role=model`。
- `temperature`、`top_p`、`max_tokens` 映射到 `generationConfig`。
- Gemini 原生入口请求转 Gemini 上游时应保持 `contents`、`parts`、`tools`、`safetySettings` 和 `generationConfig`。

### xAI / Grok

特点：

- 多数接口使用 OpenAI-Compatible 请求和响应结构。
- 鉴权使用 `Authorization: Bearer <api_key>`。
- Base URL 通常类似 `https://api.x.ai/v1`。
- 可能存在搜索、实时信息、reasoning 等 provider-specific 参数。

要求：

- OpenAI 入口协议转 xAI 上游时优先透传标准 OpenAI 字段。
- xAI 特有参数通过 `routerx.provider.xai` 或 `routerx.upstream.body` 传递。
- xAI 响应应按入口协议转换；OpenAI 入口保持 OpenAI 响应，Anthropic/Gemini 入口需要转换回对应协议。

### RouterX-Compatible 上游

特点：

- 上游也是 RouterX 或兼容 RouterX 扩展格式的网关。
- 可以保留入口协议和 `routerx` 扩展对象，交给下一层继续处理。

要求：

- 传递 `X-RouterX-Hop` 和 `X-Request-Id`。
- 保留 `routerx.provider` 和未知 `routerx` 扩展字段。
- 不重复注入已存在的 provider-specific 参数。

### Qwen

特点：

- DashScope 兼容模式基本兼容 OpenAI。
- 鉴权使用 Bearer。
- 默认路径可为 `/compatible-mode/v1/chat/completions`。

### DeepSeek

特点：

- 基本 OpenAI-Compatible。
- 请求和响应可以优先透传。

## 非流式响应

非流式处理流程：

```text
read request body
    -> adapter.ConvertRequest
    -> http.Client.Do
    -> read downstream body with size limit
    -> adapter.ConvertResponse
    -> extract usage
    -> deduct quota
    -> write log
    -> return response
```

要求：

- 下游响应体读取必须有最大限制，避免异常大响应撑爆内存。
- 需要记录下游状态码。
- 需要对下游错误转换为当前路由对应的兼容错误。
- 日志保存响应应截断。

## 流式响应

流式处理流程：

```text
client stream=true
    -> downstream stream request
    -> set text/event-stream headers
    -> read downstream chunk
    -> adapter.ConvertStreamChunk
    -> write client chunk
    -> flush
    -> collect usage if available
    -> stream ends
    -> deduct quota
    -> write log
```

要求：

- 不缓存完整响应。
- 客户端断开时取消下游请求。
- 已向客户端写出后不做跨通道重试。
- 如果 provider 不返回 usage，使用估算策略。
- 日志中保存摘要，不保存完整流式内容。

## Usage 和计费

Usage 来源优先级：

| 优先级 | 来源 |
|--------|------|
| 1 | 下游响应中的标准 usage |
| 2 | 适配器从厂商响应转换出的 usage |
| 3 | 本地 tokenizer 估算 |
| 4 | 配置的最低计费规则 |

扣费步骤：

```text
usage -> price rule -> group ratio -> quota_used
    -> if token.remain_quota != -1 then deduct token quota first
    -> remaining cost deduct user quota
    -> if insufficient, return 429 before sending non-stream request
```

流式请求预检：

- 请求开始前检查 Token 或用户有可用额度。
- 可按模型设置最低预留额度。
- 请求结束后按实际 usage 结算。
- 如果实际 usage 超过预留额度，允许扣到 0 后禁用继续调用，或采用数据库条件更新阻止透支。

## 日志写入

每次调用写入 `logs`。

成功日志：

- `status=success`。
- 写入 user、token、channel、model。
- 写入 prompt/completion/total tokens。
- 写入 quota_used。
- 写入截断后的 request/response。

失败日志：

- `status=failed`。
- 如果尚未选中通道，`channel_id` 可为空。
- 写入错误类型、下游状态码和错误摘要。
- 不扣费或按配置扣最低失败成本。

## 熔断和恢复

错误计数：

- 下游成功后 `error_count=0`。
- 可重试失败后 `error_count += 1`。
- 达到 `relay.error_ban_threshold` 后通道进入自动禁用或临时熔断状态。

恢复策略：

- 手动恢复：管理员启用通道。
- 定时探测：后台任务定期测试熔断通道。
- 半开状态：允许少量请求探测，成功后恢复。

当前 `channels.status` 只有 `disabled/enabled/manual_off`，自动熔断可以先通过 `error_count` 排除通道实现，后续再增加状态字段。

## 限流

限流维度：

| 维度 | Key 示例 |
|------|----------|
| 全局 | `rl:global:{minute}` |
| IP | `rl:ip:{ip}:{minute}` |
| 用户 | `rl:user:{user_id}:{minute}` |
| Token | `rl:token:{token_id}:{minute}` |
| 模型 | `rl:model:{model}:{minute}` |
| 通道 | `rl:channel:{channel_id}:{minute}` |

限流算法：

- P0 使用固定窗口或滑动窗口计数器。
- P1 可升级令牌桶或漏桶。
- Redis 不可用时可选择 fail-open 或 fail-closed，生产建议对高风险接口 fail-closed。

## 配置项

建议 settings：

| key | 默认 | 说明 |
|-----|------|------|
| `relay.timeout` | `120` | 下游请求超时秒数 |
| `relay.retry_count` | `2` | 非流式请求重试次数 |
| `relay.error_auto_ban` | `true` | 是否自动熔断失败通道 |
| `relay.error_ban_threshold` | `10` | 连续失败阈值 |
| `relay.log_request_body` | `false` | 是否记录请求体 |
| `relay.log_response_body` | `false` | 是否记录响应体 |
| `relay.log_body_max_bytes` | `4096` | 日志体最大长度 |
| `relay.stream_usage_strategy` | `provider_or_estimate` | 流式 usage 策略 |
| `billing.default_ratio` | `1.0` | 默认计费倍率 |

## 安全要求

- 下游 API Key 不写日志。
- 下游 API Key 使用 `ENCRYPTION_KEY` 或 KMS 加密存储，不能依赖实例本地随机密钥。
- Authorization、Cookie、API Key 必须在日志中脱敏。
- Adapter 错误返回不能泄露完整下游密钥或内部 DSN。
- 请求体日志默认关闭。
- 对图片、音频、文件类接口设置上传大小限制。
- 对下游响应体设置最大读取大小。

## 实施顺序

1. 实现 API Key 中间件和 TokenService 校验。
2. 实现 OpenAI 入口 Chat Completions 和 OpenAI-Compatible 上游的非流式闭环。
3. 实现 `routerx` 扩展参数解析、真实上游剥离和 RouterX-Compatible 上游透传。
4. 实现 ChannelService 模型匹配和优先级选择。
5. 实现 RelayService 非流式闭环。
6. 接入 LogService 和 TokenService 扣费事务。
7. 实现 SSE 流式转发。
8. 扩展 Anthropic、Gemini、xAI、Azure、DeepSeek、Qwen 等上游适配。
9. 实现 OpenAI、Anthropic、Gemini 三类入口协议之间的请求/响应转换。
10. 增加熔断、限流、模型价格和统计看板。
