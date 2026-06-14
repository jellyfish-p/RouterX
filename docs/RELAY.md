# RouterX 模型转发设计

## 目标

Relay 模块是 RouterX 的核心。它接收 OpenAI、Anthropic、Gemini 等不同入口协议的完整请求格式，按路由和通道配置转换为具体上游厂商请求，处理响应、日志和计费。调用方接入体验、SDK 行为和迁移边界以 `docs/DEVELOPER_EXPERIENCE.md` 为准；入口协议、APIType、上游厂商和能力等级以 `docs/PROTOCOLS.md` 为准。

Relay 的产品取舍是：默认路径先保证一个 OpenAI-compatible 调用稳定闭环，进阶路径再展开完整多协议和多上游矩阵。多协议和多上游是同等核心目标，但不能让矩阵复杂度阻塞第一次可用。

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
- `internal/router/user_router.go` 已注册 OpenAI、Anthropic、Gemini 相关 `/v1` 路由。
- `internal/model/channel.go` 已定义通道模型。
- `internal/model/log.go` 已定义调用日志模型。
- `internal/model/token.go` 已定义 API Key 和额度字段。
- `ApiKeyAuthRequired` 已接入真实 API Key 校验，并写入 `current_user` 和 `current_token`。
- `RelayHandler` 已接入 OpenAI-compatible、Anthropic Messages、Gemini generateContent/countTokens 和模型列表等入口。
- `RelayService` 已有通道选择、上游解析、非流式转发、OpenAI-compatible Chat/Legacy Completions 基础 SSE 转发、Responses/Embeddings/Moderations/Image Generations 基础 JSON 透传、模型重写、usage 提取、扣费和日志基础链路。
- `ChannelService` 已支持多 base URL、多 key、key 选择模式、模型重写、通道分组和扩展配置。
- Anthropic/Gemini 非流式入口已覆盖基础成功响应、usage、扣费和非文本 content/parts 的可解释降级。

当前缺口：

- OpenAI-compatible SSE 流式已覆盖客户端写入失败后的上游取消断言；Anthropic Messages Stream 和 Gemini streamGenerateContent 已支持转 OpenAI-compatible Chat SSE 上游并输出入口协议 SSE 事件；Anthropic/Gemini 原生上游流式和更完整的 usage fallback 策略仍需补齐。
- 多协议入口与多上游之间的完整语义转换实现仍需要按 `docs/PROTOCOLS.md` 的能力矩阵补齐，尤其是流式、原生字段保真和更完整 SDK 错误矩阵。
- Adapter 接口需要扩展以支持 headers、query、provider-specific 参数、流式 chunk 和更细粒度错误。
- 非流式安全重试已经按 `relay.retry_count` 支持基础换候选；Redis 限流已覆盖全局、IP 和 Token 的分钟窗口；熔断、更多限流维度、计费快照和可观测指标仍需要生产级实现。
- Image Edits/Variations 已支持 OpenAI-compatible multipart 表单基础透传、`routerx` 表单字段路由偏好和最低计费，仍需补文件大小限制、安全扫描、字段保真和价格规则；Audio Transcriptions/Translations 已支持 OpenAI-compatible multipart 表单基础透传、`routerx` 表单字段路由偏好和最低计费，仍需补完整音频格式策略、时长限制、文件大小限制和价格规则；Audio Speech 已支持 JSON 请求和二进制响应基础透传，但仍需补完整音频格式策略、时长限制和价格规则；Responses 仍需继续补完整原生字段矩阵和多上游转换，Embeddings 仍需补输入 schema、批量边界和模型能力策略，Image Generations/Moderations 仍需补完整安全策略和价格规则。

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

- `routerx.route` 只表达 RouterX 路由偏好，如目标通道、通道分组、上游 provider、禁用某些 provider 等。
- `routerx.route` 不能绕过管理员策略、安全策略和系统健康判断；它只能在已允许的候选通道集合内进一步收窄范围。
- `routerx.upstream.headers`、`routerx.upstream.query`、`routerx.upstream.body` 用于传递上游调用所需的额外 header、query 和 body 字段。
- `routerx.provider.<provider>` 用于传递只对某个上游 provider 生效的额外参数。
- Adapter 调用真实厂商前必须移除 `routerx` 字段，除非上游通道类型是 `routerx`。
- 禁止透传敏感 header，如 `Authorization`、`Cookie`、`Set-Cookie`、`X-Api-Key`，这些必须来自通道配置。
- multipart 或非 JSON 请求当前可使用 `routerx` 表单字段传递 JSON 字符串；`X-RouterX-Options` header 是后续扩展目标。

冲突规则：

- 跨模块访问控制、分组、限流、预算和策略快照以 `docs/POLICIES.md` 为准；本节只描述 Relay 如何应用这些策略。
- 管理员配置、用户/API Key 状态、用户余额、Key 预算、访问控制、通道状态、熔断状态和密钥安全策略优先级最高。
- `routerx.route` 指定的 provider、通道分组或通道偏好不存在、不可用或不可访问时，不自动降级到越权通道。
- 请求携带非法 `routerx` 结构时返回当前入口协议兼容的 400 错误，例如 `invalid_routerx_options` 或 `invalid_routerx_route`。
- 路由偏好合法但筛选后无通道时返回当前入口协议兼容的无可用通道错误；如果原因是访问策略禁止，应返回 403。
- 被接受、忽略或拒绝的路由偏好必须进入日志摘要，P1/P2 进一步进入结构化路由决策快照。

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

APIType、入口协议、路由状态和能力等级的权威矩阵见 `docs/PROTOCOLS.md`。本节只保留 Relay 视角的路径映射，避免把“路由已注册”误解为“协议完整兼容”。

| 入口协议 | APIType | 路径 | P 阶段 |
|----------|---------|------|--------|
| OpenAI | `APIResponses` | `/v1/responses` | P1 |
| OpenAI | `APIChatCompletions` | `/v1/chat/completions` | P0 |
| OpenAI | `APICompletions` | `/v1/completions` | P1 |
| OpenAI | `APIEmbeddings` | `/v1/embeddings` | P1 |
| OpenAI | `APIImagesGenerations` | `/v1/images/generations` | P2 |
| OpenAI | `APIImagesEdits` | `/v1/images/edits` | P2 |
| OpenAI | `APIImagesVariations` | `/v1/images/variations` | P2 |
| OpenAI | `APIAudioTranscriptions` | `/v1/audio/transcriptions` | P2 |
| OpenAI | `APIAudioTranslations` | `/v1/audio/translations` | P2 |
| OpenAI | `APIAudioSpeech` | `/v1/audio/speech` | P2 |
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

路径冲突处理：`/v1/models` 同时服务 OpenAI、Anthropic、Gemini 模型列表。当前实现默认返回 OpenAI `models` 结构，可用 `?format=gemini` 或 `?format=anthropic` 请求特定格式，并可通过 `anthropic-version` header 识别 Anthropic 格式。目标设计可继续扩展 `?routerx_protocol=` 或 `X-RouterX-Protocol`，但必须保持向后兼容。

## 阶段矩阵

多协议和多上游是 RouterX 的同等核心目标，但实现必须分阶段交付。阶段等级、字段降级、路径冲突和新增 provider 准入清单以 `docs/PROTOCOLS.md` 为准。

| 阶段 | 入口协议 | 上游范围 | 关键验收 |
|------|----------|----------|----------|
| P0 | OpenAI-compatible Chat/Models 为主，Anthropic/Gemini 基础路由存在 | OpenAI/OpenAI-Compatible 优先，兼容 xAI/Qwen/DeepSeek 的 OpenAI 形态 | 非流式闭环、OpenAI Chat/Completions 基础 SSE、API Key、通道选择、日志、扣费 |
| P1 | OpenAI、Anthropic、Gemini 基础非流式和多协议流式 | OpenAI-Compatible、Anthropic、Gemini、Azure、xAI、Qwen、DeepSeek、RouterX-Compatible | 主流 SDK 可用，字段降级清晰，流式可计费 |
| P2 | 高级 OpenAI API、多模态、企业路由 | 多区域、多层 RouterX、企业上游和高级 provider 参数 | 高级 API 可观测、可审计、可限流、可回滚 |

阶段约束：

- P0 不要求每个协议和上游组合都可用，但要求失败边界明确，不能静默误转。
- P1 每新增一个入口协议或上游 provider，都必须同时补齐错误映射、usage 提取、密钥过滤、日志字段和对应 Runbook。
- P2 才扩展高级 API 和企业路由，避免高级能力挤占基础闭环可靠性。

## 错误语义

Relay 错误需要同时服务三类读者：客户端 SDK、普通用户和管理员。客户端看到入口协议兼容错误；用户看到是否需要充值、换模型或检查 API Key；管理员能从日志判断是通道、密钥、上游还是系统配置问题。

完整错误 code 目录、当前代码事实和目标收敛规则以 `docs/ERRORS.md` 为准。本文保留 Relay 视角的来源、重试、扣费和排障原则。

错误来源分类：

| 来源 | 示例 code | HTTP | 是否重试 | 是否扣费 | 处理人 |
|------|-----------|------|----------|----------|--------|
| 请求格式 | `invalid_json`、`model_required`、`unsupported_api` | 400/404 | 否 | 否 | 调用方 |
| 鉴权状态 | `invalid_api_key`、`expired_api_key`、`user_disabled` | 401/403 | 否 | 否 | 用户或管理员 |
| 额度和限流 | `insufficient_quota`、`rate_limit_exceeded` | 429 | 否 | 否 | 用户或管理员 |
| 路由策略 | `route_forbidden`、`no_available_channel`、`unsupported_channel` | 403/502 | 否或换候选通道 | 否 | 管理员 |
| 通道配置 | `upstream_secret_error`、`unsupported_channel` | 502 | 否 | 否 | 管理员 |
| 下游临时故障 | `upstream_request_failed`、`upstream_502`、`upstream_503`、`upstream_timeout` | 502/504 | 非流式可重试 | 否，除非已有有效 usage | 管理员或自动恢复 |
| 下游请求不兼容 | `upstream_400`、`upstream_conversion_failed` | 400/502 | 否 | 否 | 调用方或适配器实现者 |
| 计费结算 | `billing_failed`、`insufficient_quota_after_usage` | 429/500 | 否 | 按事务结果 | 管理员 |

排障动作：

| code 类型 | 客户端动作 | 管理员动作 | 日志必须包含 |
|-----------|------------|------------|--------------|
| `invalid_*`、`model_required` | 修正请求体、模型名或 `routerx` 扩展 | 不需要介入，除非大量出现 | request_id、入口协议、解析失败字段 |
| `invalid_api_key`、`expired_api_key` | 更换或重新创建 API Key | 检查用户和 API Key 状态 | user_id、token_id、失败原因摘要 |
| `insufficient_quota`、`rate_limit_exceeded` | 充值、降低并发或等待窗口 | 调整用户额度、Key 预算或限流配置 | user quota、key budget、限流 key |
| `route_forbidden` | 移除越权路由偏好或联系管理员 | 检查通道分组、用户分组和访问策略 | `routerx.route` 摘要、拒绝原因 |
| `no_available_channel` | 换模型或联系管理员 | 检查通道启用、模型匹配、熔断和 provider adapter | 候选过滤摘要 |
| `upstream_secret_error` | 联系管理员 | 检查 `ENCRYPTION_KEY`、通道密钥和密钥格式 | channel_id、provider、解密错误摘要 |
| `upstream_timeout`、`upstream_5xx` | 稍后重试 | 检查下游可用性、重试和熔断状态 | 下游状态、耗时、重试次数 |

错误映射规则：

- 入口协议决定错误外形，不能返回 RouterX `{success,data,message}` 包装。
- OpenAI-compatible 入口使用 `{ "error": { "message", "type", "code" } }`。
- Anthropic 入口使用 `{ "type": "error", "error": { "type", "message" } }`。
- Gemini 入口使用 `{ "error": { "code", "message", "status" } }`。
- 下游 401/403 通常表示通道密钥或账号权限问题，不对其他通道无限重试，避免扩大错误和触发风控。
- 下游 429/5xx/网络超时可在非流式且未向客户端写出时重试其他候选通道。
- 流式响应一旦写出 chunk，不再切换通道；错误只能结束流并写失败摘要。
- Adapter 转换失败必须区分“请求字段不支持”和“实现缺陷”，不能用模糊 500 掩盖。

## 通道选择

### 路由决策优先级

路由决策必须先做不可绕过的系统过滤，再应用用户偏好，最后才进入优先级和权重选择。推荐顺序如下：
完整策略语义见 `docs/POLICIES.md`。

```text
1. 解析入口协议、API 类型、请求模型、stream 和 routerx 扩展参数
2. 校验用户、API Key、用户余额、Key 预算、限流和基础请求大小
3. 读取或构建通道候选缓存：按模型、APIType、用户分组、通道分组和路由版本预加载
4. 过滤不可用通道：禁用、软删除、无 Adapter、模型不匹配、熔断、余额不足
5. 应用访问控制：用户分组、Token 策略、通道分组和模型权限
6. 应用 routerx.route 偏好，只在已允许候选集中继续收窄
7. 选择最高 priority 的候选组
8. 在同 priority 组内按 weight 加权随机
9. 应用模型重写，得到上游真实模型名
10. 解析通道上游：upstreams 优先，其次 api_keys/base_urls，再其次单 api_key/base_url
11. 构造上游请求，剥离 RouterX 私有字段并注入安全来源的鉴权信息
```

这个顺序是商业级默认体验的核心约束：小白用户不需要理解它也能稳定调用，技术用户可以通过日志解释每一次通道命中，管理员可以确信请求参数不能越过后台策略。

通道候选条件：

- `status = enabled`。
- 未软删除。
- `models` 包含请求模型，或 `models = "*"`。
- `relay.error_auto_ban=true` 时，`error_count` 低于 `relay.error_ban_threshold`。
- 如启用余额检查，`balance > 0`。
- 通道类型有可用 Adapter。

选择策略：

```text
1. 读取预加载的候选通道索引，缓存缺失或版本落后时回源加载
2. 按 priority 分组，选择最高 priority 组
3. 在同组内按 weight 加权随机
4. 如果开启 latency 优化，可将 response_ms 作为降权因子
5. 返回选中的通道
```

候选缓存规则：

- 单机 SQLite 模式可以使用进程内缓存和短 TTL。
- DB+Redis 或集群模式必须使用 Redis 保存 `routing.channel_cache.version`、失效标记或共享候选快照。
- 管理员修改通道、模型、通道分组、用户分组访问控制、价格规则或相关 settings 后，必须递增路由版本并广播失效。
- API Key scope 和 `routerx.route` 这类请求级收窄规则在缓存候选集之后应用，避免生成高基数缓存。
- 缓存不可绕过数据库最终事实；版本不一致时不得长期使用旧候选集。

当前代码事实：

- 当前通道查询已经按 `priority DESC, idx ASC, error_count ASC, response_ms ASC, id ASC` 排序。
- 当前通道候选过滤读取 `relay.error_auto_ban` 和 `relay.error_ban_threshold`；关闭自动熔断时，高 `error_count` 通道仍可参与候选。
- 当前候选集会保留最高 `priority` 的通道，再按 `weight` 加权随机；`weight <= 0` 按 `1` 处理。
- 当前多 key 选择支持 `round_robin` 和 `random`；未知值会归一为 `round_robin`。
- 当前多 `base_urls` 在解析上游时随机选择；后续如增加顺序、权重或健康优先策略，必须进入通道路由快照。
- 当前 `routerx.route` 支持按 `channel_group`/`group`、`channel_id`、`channel`/`channel_name`、`provider`/`upstream_provider` 和 `disabled_providers` 收窄候选；未知 route key 会被忽略，非法结构返回 `invalid_routerx_options` 或 `invalid_routerx_route`。

可解释性要求：

- 每次请求应能记录候选通道过滤原因、最终选中通道、模型重写结果和是否发生重试。
- 如果请求带有 `routerx.route` 偏好，日志应记录该偏好是否被接受、忽略或拒绝。
- 熔断排除、余额排除、模型不匹配、provider 不支持等原因必须能用于排障和审计。
- P0 可先记录摘要，P1/P2 再扩展为结构化路由决策快照。

### 通道内部上游解析

同一个通道内部可能同时配置单 key、多 key、多个 base URL 或完整上游数组。为避免不可解释的随机行为，解析优先级固定如下：

| 优先级 | 配置 | 说明 |
|--------|------|------|
| 1 | `upstreams` | 完整上游对象数组，base URL 与 API Key 作为一组绑定；适合多节点或多账号精确配对。 |
| 2 | `api_key`/`api_keys` + `base_url`/`base_urls` | 单字段和数组字段组合；API Key 按 key 选择策略选择，base URL 当前随机选择。 |
| 3 | provider 默认 Base URL + API Key | 只适合明确存在 provider 默认地址的适配器。 |

解析约束：

- `upstreams` 非空时优先使用，不再把其 key 与外层 `base_urls` 任意交叉组合。
- 单 `api_key` 与 `api_keys` 同时存在时，服务层可以把单 key 作为兼容存量并放入候选 key 集，但日志和管理端应提示配置来源。
- 上游 API Key 只能来自通道配置或安全密钥管理，不接受用户请求覆盖。
- 如果无法解析出有效 API Key，应返回兼容格式错误并标记为通道配置问题。

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

当前实现：

- Chat Completions 已按 deployment 路径调用 Azure OpenAI。
- 当前默认 `api-version` 为 `2024-02-15-preview`。
- 发往 Azure 前会剥离 `model` 和 `routerx`，因为 deployment 已经由路径表达。
- Azure 返回的 OpenAI-compatible `usage` 会写入 RouterX 日志并扣费。

设计要求：

- 后续通道配置需要保存 `api_version`，可放在扩展配置字段或 settings。
- `model` 到 deployment 的映射需要支持别名。
- Embeddings、Images、Audio 等 Azure API 仍需逐项补齐。

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

### P0 Chat 合同

P0 的 Chat 处理要求 OpenAI-compatible 非流式主链路稳定，并支持 Chat/Legacy Completions OpenAI SSE 形态的基础流式；链路上的失败必须可解释。

成功路径：

```text
parse model/stream
    -> stream=false 或空
    -> HasAvailableQuota(user quota + key budget)
    -> SelectChannel(model)
    -> ResolveUpstream(channel)
    -> ApplyModelRewrite
    -> adapter.ConvertRequest
    -> adapter.DoRequest
    -> adapter.ConvertResponse + usage
    -> quotaFromUsage 或最低计费
    -> TokenService.DeductQuota(user quota + key budget)
    -> markChannelSuccess
    -> LogService.Record(success)
    -> return OpenAI-compatible response
```

失败路径要求：

- `stream=true` 走流式链路；如果选中通道不是 OpenAI SSE 形态，返回 `unsupported_stream_channel` 且不调用下游。
- 额度预检失败不选择通道、不调用下游，并写失败日志。
- 无可用通道返回 `no_available_channel`，失败日志中 `channel_id` 可为空。
- 上游密钥解析失败返回 `upstream_secret_error`，不泄露密钥明文。
- 下游 400/401/403 不作为可安全重试错误；401/403 应帮助管理员定位通道配置问题。
- 下游 5xx、网络错误和超时在非流式且未向客户端写出时可以按配置重试候选通道。
- `relay.retry_count=0` 时保持单次调用；大于 0 时最多额外尝试对应数量的候选通道。
- 扣费失败必须写失败日志；如果响应尚未返回客户端，应返回 429。

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

当前代码事实：

- `/v1/chat/completions` 和 `/v1/completions` 的 `stream=true` 已支持 OpenAI-compatible SSE 基础转发。
- 支持的上游通道范围是 OpenAI、OpenAI-Compatible、xAI、Qwen、DeepSeek 和 RouterX-Compatible 这类 OpenAI SSE 形态通道。
- Service 会先确认上游返回 2xx，再让 Handler 写 `text/event-stream` 响应头，避免把上游错误伪装成成功流。
- 当前从 OpenAI SSE `data:` JSON chunk 中提取 `usage`；缺失 usage 时沿用最低扣费规则。

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
    -> if token has budget cap then deduct user quota and key budget atomically
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
- 下游请求、响应读取、非 2xx 状态、响应转换或流式转发失败后，`error_count` 原子递增。
- `relay.error_auto_ban=true` 且 `error_count >= relay.error_ban_threshold` 后，通道会被候选查询排除。
- `relay.error_auto_ban=false` 时仍记录 `error_count`，但不因阈值排除候选。

恢复策略：

- 手动恢复：管理员启用通道。
- 定时探测：后台任务定期测试熔断通道。
- 半开状态：允许少量请求探测，成功后恢复。

当前 `channels.status` 只有 `disabled/enabled/manual_off`，自动熔断通过 `error_count` 排除通道实现，不会自动改写 status；后续再增加状态字段、半开探测和恢复任务。

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

- P0 当前使用 Redis 固定分钟窗口计数器，读取 `rate_limit.enabled`、`rate_limit.global_per_min`、`rate_limit.per_ip_per_min` 和 `rate_limit.per_token_per_min`。
- 全局、IP、Token 三个维度已接入；任一维度阈值为 `0` 时跳过该维度。
- 命中本地限流时不调用上游，并按入口协议返回 OpenAI、Anthropic 或 Gemini 兼容的 429 错误。
- P1 可升级令牌桶或漏桶。
- Redis 不可用时可选择 fail-open 或 fail-closed，生产建议对高风险接口 fail-closed。

## 配置项

建议 settings：

| key | 默认 | 说明 |
|-----|------|------|
| `relay.timeout` | `120` | 下游请求超时秒数 |
| `relay.retry_count` | `0` | 默认不自动重试；大于 0 时非流式只对 429、5xx、网络错误、超时和响应读取失败换候选 |
| `relay.error_auto_ban` | `true` | 是否按 `error_count` 自动排除故障通道 |
| `relay.error_ban_threshold` | `10` | 自动排除通道的连续错误阈值 |
| `relay.log_body_max_bytes` | `0` | Relay 日志体最大长度，`0` 表示默认不记录 body |
| `relay.stream_usage_strategy` | `provider_or_estimate` | 流式 usage 策略 |
| `billing.default_ratio` | `1.0` | 默认计费倍率 |

完整 settings 注册表、当前默认值和目标配置以 `docs/SETTINGS.md` 为准。

## 安全要求

- 下游 API Key 不写日志。
- 下游 API Key 使用 `ENCRYPTION_KEY` 或 KMS 加密存储，不能依赖实例本地随机密钥。
- Authorization、Cookie、API Key 必须在日志中脱敏。
- Adapter 错误返回不能泄露完整下游密钥或内部 DSN。
- 请求体日志默认关闭。
- 对图片、音频、文件类接口设置上传大小限制。
- 对下游响应体设置最大读取大小。

## Relay 验证矩阵

Relay 每新增一个入口协议、上游 provider 或 APIType，都必须先同步 `docs/PROTOCOLS.md` 的能力等级和降级规则，再补齐同等范围的验证，并同步 `docs/RUNBOOKS.md` 中的排查路径。

| 验证维度 | P0 最小要求 | P1/P2 扩展要求 |
|----------|-------------|----------------|
| 鉴权和额度 | 无效 Key、禁用用户、禁用 API Key、余额不足均在调用下游前失败 | Key 预算、用户限额、预留额度和流式超额策略可解释 |
| 通道选择 | 模型匹配、启用状态、优先级、权重和无可用通道可测 | `routerx.route`、访问控制、熔断排除、路由快照可测 |
| 上游解析 | 单 `base_url + api_key`、多 key、多 base URL、`upstreams` 优先级可测 | 区域、健康优先、加权 base URL 或 KMS 解密可测 |
| 请求转换 | OpenAI-compatible Chat 非流式字段不被无故丢弃；Anthropic/Gemini 非流式基础 content 降级可测 | OpenAI/Anthropic/Gemini 多入口语义转换矩阵可测 |
| 响应转换 | 成功响应保留入口协议格式，usage 能提取；OpenAI Chat/Completions 基础 SSE、Gemini streamGenerateContent 基础 SSE、Responses/Embeddings 基础 usage 映射和 Anthropic/Gemini 基础非流式已覆盖 | Anthropic/Gemini 原生流式 chunk、tool calling、vision、reasoning 和降级原因可测 |
| 错误映射 | OpenAI-compatible 错误格式、状态码和 code 正确 | Anthropic/Gemini 错误格式、下游错误脱敏和 SDK 行为可测 |
| 计费日志 | 成功调用写日志并扣额度，失败调用不误扣 | usage 来源、价格快照、重试结果和账单聚合一致 |
| 重试熔断 | 非流式 429/5xx/网络错误/超时可按 `relay.retry_count` 换候选，400/401/403 不重试 | 半开恢复、限流、更多故障注入和流式不可切换通道可测 |
| 安全过滤 | `routerx` 私有字段真实厂商前剥离，敏感 header 不透传 | 多层 RouterX hop、provider-specific 参数和审计摘要可测 |

## 实施顺序

1. 收口现有 OpenAI-compatible 非流式 Chat/Models 闭环，补齐错误格式、请求限制和测试。
2. 完善 `routerx` 扩展参数解析、真实上游剥离和 RouterX-Compatible 上游透传。
3. 完善 LogService、TokenService 扣费事务和计费规则快照。
4. 实现 SSE 流式转发、客户端断开取消和流式 usage 策略。
5. 增加重试、限流、熔断、自动恢复和可观测指标。
6. 扩展 Anthropic、Gemini、xAI、Azure、DeepSeek、Qwen 等上游适配。
7. 实现 OpenAI、Anthropic、Gemini 三类入口协议之间的请求/响应转换。
8. 扩展 Responses 完整字段矩阵、Images、Audio、Moderations 等高级 API。
9. 增加模型价格、访问控制、统计看板和管理审计。
