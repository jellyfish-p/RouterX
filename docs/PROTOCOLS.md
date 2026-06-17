# RouterX 协议兼容与能力矩阵

## 目标

本文定义 RouterX `/v1` 模型转发的协议兼容边界、能力等级和验收矩阵。

RouterX 的长期目标是同时支持 OpenAI、Anthropic、Gemini 等入口协议，并能转发到 OpenAI、Anthropic、Gemini、Azure OpenAI、xAI、Qwen、DeepSeek、OpenAI-Compatible、RouterX-Compatible 等上游。这个目标必须分阶段落地，不能把“路由已注册”误写成“协议完整兼容”。

本文是多协议、多上游、APIType 和 SDK 兼容状态的权威入口：

- 产品定位和阶段边界见 `docs/DESIGN.md`。
- 策略、分组、作用域、限流和路由偏好冲突规则见 `docs/POLICIES.md`。
- Relay 链路、通道选择、Adapter 扩展和 usage/计费衔接见 `docs/RELAY.md`。
- 错误 code、HTTP 状态、协议错误外形和扣费语义见 `docs/ERRORS.md`。
- 测试断言和矩阵用例见 `docs/TESTING.md`。

## 设计原则

- 入口协议决定客户端看到的请求和响应格式；上游厂商只决定 RouterX 如何调用下游。
- `/v1` 不返回 RouterX 管理端 `{success,data,message}` 包装。
- 路由已注册只表示请求能进入对应 handler，不表示字段、流式、错误和 usage 已完整兼容。
- P0 优先保证 OpenAI-compatible Chat/Models 非流式可用闭环；当前已推进 OpenAI Chat 和 Legacy Completions 基础 SSE 流式。
- P1 扩展主流 SDK 可用的 OpenAI、Anthropic、Gemini 基础非流式和流式能力。
- P2 才扩展高级 API、多模态、企业路由、深度观测和更严格审计。
- 对无法等价转换的字段，必须明确返回错误、记录降级原因或进入能力矩阵，不允许静默丢弃关键语义。
- 用户请求中的 `routerx` 扩展只能收窄候选通道，不能绕过 API Key 作用域、管理员策略、额度、限流或通道健康状态。

## 术语

| 术语 | 含义 |
|------|------|
| 入口协议 | 客户端发送给 RouterX 的协议格式，例如 OpenAI-compatible、Anthropic Messages、Gemini generateContent。 |
| 上游厂商 | RouterX 实际调用的下游 provider，例如 OpenAI、Claude、Gemini、Azure OpenAI、xAI、Qwen、DeepSeek。 |
| APIType | RouterX 内部对接口能力的枚举，例如 `APIChatCompletions`、`APIAnthropicMessages`、`APIGeminiGenerateContent`。 |
| 能力矩阵 | 入口协议、APIType、上游厂商、字段转换、流式、usage、错误格式和测试状态的组合表。 |
| 原生透传 | 入口协议和上游协议相同或高度兼容时，尽量保留原请求语义。 |
| 协议转换 | 入口协议和上游协议不同时，在 RouterX 内完成请求和响应结构互转。 |

## 能力等级

| 等级 | 含义 | 对外承诺 |
|------|------|----------|
| 已注册 | 路由或 APIType 已存在，请求能进入 RouterX，但未承诺完整行为。 | 只能作为后续实现入口，不能写进用户承诺。 |
| 基础实现 | 已有最小闭环，适合受控场景验证，但错误、流式、字段保真或测试仍需补齐。 | 可在文档中标明边界后使用。 |
| P0 稳定 | 满足开箱路径验收，错误、日志、扣费和安全边界清楚。 | 可作为默认推荐路径。 |
| P1 兼容 | 主流 SDK 的基础非流式和流式行为可用，降级原因可解释。 | 可作为进阶生产能力。 |
| P2 高级 | 支持高级 API、多模态、企业路由、观测和审计。 | 可作为长期运营能力。 |
| 目标扩展 | 产品设计需要，但当前没有完整实现或测试闭环。 | 只能出现在路线图、设计和任务卡中。 |

## 当前代码事实

当前代码已经具备以下基础：

- `/v1` 路由已覆盖 OpenAI-compatible、Anthropic Messages 和 Gemini generateContent/countTokens/Models 的主要入口。
- OpenAI-compatible Chat Completions 非流式、Chat/Legacy Completions 基础 SSE 流式、Responses/Embeddings/Moderations/Image Generations 基础 JSON 透传和 Models 已形成基础闭环。
- Anthropic Messages 入口已有基础转换链路：Anthropic Messages 转内部 OpenAI Chat 形态，再返回 Anthropic 兼容响应；`stream=true` 可转 OpenAI-compatible SSE 上游并输出 Anthropic SSE 事件；非文本 content blocks 会降级为 compact JSON 文本。
- Gemini generateContent 入口已有基础转换链路：Gemini contents 转内部 OpenAI Chat 形态，再返回 Gemini 兼容响应；非文本 parts 会降级为 compact JSON 文本。
- Anthropic count_tokens 和 Gemini countTokens 当前是本地近似计数能力，不等同于真实厂商 tokenizer 完全一致。
- OpenAI、OpenAI-Compatible、xAI、RouterX-Compatible 使用 OpenAI adapter 形态；Qwen、DeepSeek 复用 OpenAI-compatible 形态。
- Claude adapter 支持 Chat/Messages 基础能力。
- Gemini adapter 支持 Chat/generateContent 基础能力。
- Azure OpenAI adapter 已支持 Chat Completions 基础转发，使用 deployment 路径、`api-version` query 和 `api-key` header；其他 Azure API 仍属于目标扩展，不能承诺生产兼容。
- OpenAI Chat 和 Legacy Completions 的 `stream=true` 仅对 OpenAI SSE 形态通道开放；非 OpenAI SSE 通道返回 `unsupported_stream_channel` 且不会调用下游；客户端写入失败时会取消上游请求并写失败日志。

## 入口协议矩阵

| 入口协议 | 路径 | 当前等级 | P0/P1/P2 定位 | 说明 |
|----------|------|----------|---------------|------|
| OpenAI-compatible Models | `GET /v1/models`、`GET /v1/models/:model` | 基础实现 | P0 | 默认返回 OpenAI `models` 结构。 |
| OpenAI-compatible Chat | `POST /v1/chat/completions` | 基础实现，目标 P0/P1 稳定 | P0/P1 | 非流式为 P0 主链路；OpenAI SSE 基础流式和客户端断开取消已有测试；多协议 chunk 转换仍属 P1。 |
| OpenAI Responses | `POST /v1/responses` | 基础实现 | P1 | OpenAI-compatible 通道可 JSON 透传，`input_tokens/output_tokens/total_tokens` usage 已映射到日志和扣费；完整 Responses 原生字段矩阵仍需补齐。 |
| OpenAI Completions | `POST /v1/completions` | 基础实现 | P1 | 支持 OpenAI-compatible JSON 和基础 SSE 透传、usage 提取与扣费；完整旧 SDK 边界仍需补齐。 |
| OpenAI Embeddings | `POST /v1/embeddings` | 基础实现 | P1 | OpenAI-compatible 通道可 JSON 透传到 `/v1/embeddings`，`routerx` 私有字段会在上游前剥离，`prompt_tokens/total_tokens` usage 已映射到日志和扣费；输入 schema、批量边界和模型能力策略仍需补齐。 |
| OpenAI Image Generations | `POST /v1/images/generations` | 基础实现 | P2 | OpenAI-compatible 通道可 JSON 透传到 `/v1/images/generations`，`routerx` 私有字段会在上游前剥离；上游无 usage 时按 P0 最低计费 `1` 写日志和扣费，完整安全扫描、文件/尺寸策略和价格规则仍需补齐。 |
| OpenAI Image Edits/Variations | `POST /v1/images/edits`、`POST /v1/images/variations` | 基础实现 | P2 | OpenAI-compatible 通道可 multipart 表单透传，`routerx` 表单字段会在上游前剥离并参与路由偏好；上游无 usage 时按 P0 最低计费 `1` 写日志和扣费。完整文件大小限制、安全扫描、字段保真和价格规则仍需补齐。 |
| OpenAI Audio Speech | `POST /v1/audio/speech` | 基础实现 | P2 | OpenAI-compatible 通道可 JSON 透传到 `/v1/audio/speech`，`routerx` 私有字段会在上游前剥离；上游二进制音频响应会保留 Content-Type 透传；无 usage 时按 P0 最低计费 `1` 写日志和扣费。完整音频格式策略、时长限制和价格规则仍需补齐。 |
| OpenAI Audio Transcriptions/Translations | `POST /v1/audio/transcriptions`、`POST /v1/audio/translations` | 基础实现 | P2 | OpenAI-compatible 通道可 multipart 表单透传，`routerx` 表单字段会在上游前剥离并参与路由偏好；上游无 usage 时按 P0 最低计费 `1` 写日志和扣费。完整音频格式策略、时长限制、文件大小限制、转写字段保真和价格规则仍需补齐。 |
| OpenAI Moderations | `POST /v1/moderations` | 基础实现 | P2 | OpenAI-compatible 通道可 JSON 透传到 `/v1/moderations`，`routerx` 私有字段会在上游前剥离；上游无 usage 时按 P0 最低计费 `1` 写日志和扣费，完整安全策略、分类字段保真和价格规则仍需补齐。 |
| Anthropic Messages | `POST /v1/messages` | 基础实现 | P1 | 已有基础非流式转换、基础 SSE 转换、字段降级和下游错误外形测试；仍需补 Anthropic 原生流式细节、字段保真和完整 SDK 矩阵。 |
| Anthropic Count Tokens | `POST /v1/messages/count_tokens` | 基础实现 | P1 | 当前为近似计数；未来可按上游或 tokenizer 精确化。 |
| Anthropic Models | `GET /v1/models` | 基础实现 | P1 | 通过 `?format=anthropic` 或 Anthropic header 请求特定外形。 |
| Gemini generateContent | `POST /v1/models/{model}:generateContent` | 基础实现 | P1 | 已有基础转换、字段降级和下游错误外形测试，仍需补非流式原生字段保真和完整 SDK 矩阵。 |
| Gemini streamGenerateContent | `POST /v1/models/{model}:streamGenerateContent` | 基础实现 | P1 | Gemini 入口可转 OpenAI-compatible Chat SSE 上游，并输出 Gemini SSE 事件；usage 从 OpenAI SSE chunk 提取后扣费。Gemini 原生上游流式、完整 chunk 字段保真和 usage fallback 仍需补齐。 |
| Gemini countTokens | `POST /v1/models/{model}:countTokens` | 基础实现 | P1 | 当前为近似计数；未来可按上游或 tokenizer 精确化。 |
| Gemini embedContent | `POST /v1/models/{model}:embedContent` | 基础实现 | P1/P2 | 当前转 OpenAI-compatible Embeddings 上游，Gemini `content.parts[].text` 映射为 OpenAI `input`，OpenAI embedding 响应转换为 Gemini `embedding.values`，usage 写入日志并扣费；完整字段保真、taskType/title 和原生 Gemini 上游仍需补齐。 |
| Gemini batchEmbedContents | `POST /v1/models/{model}:batchEmbedContents` | 基础实现 | P1/P2 | 当前转 OpenAI-compatible Embeddings 上游，Gemini `requests[].content.parts[].text` 映射为 OpenAI `input` 数组，OpenAI embedding list 转换为 Gemini `embeddings[].values`，usage 写入日志并扣费；完整批量边界、字段保真、taskType/title 和原生 Gemini 上游仍需补齐。 |

## 上游厂商矩阵

| 上游厂商 | 当前等级 | 推荐阶段 | 关键要求 |
|----------|----------|----------|----------|
| OpenAI | 基础实现 | P0 | Bearer 鉴权、标准 `/v1` endpoint、Chat/Models 优先稳定。 |
| OpenAI-Compatible | 基础实现 | P0 | 默认按 OpenAI 形态透传；差异通过通道配置和 provider-specific 参数处理。 |
| RouterX-Compatible | 基础实现 | P1 | 保留 `routerx` 扩展、处理 hop 限制、避免重复计费和路由循环。 |
| xAI | 基础实现 | P1 | OpenAI-compatible 为主，搜索、reasoning 等扩展走 `routerx.provider.xai`。 |
| Qwen | 基础实现 | P1 | 兼容模式复用 OpenAI adapter，后续补 DashScope 差异。 |
| DeepSeek | 基础实现 | P1 | OpenAI-compatible 为主，后续补模型特性和错误映射。 |
| Claude / Anthropic | 基础实现 | P1 | Messages endpoint、`x-api-key`、`anthropic-version`、tool/thinking/vision 字段映射。 |
| Gemini | 基础实现 | P1 | `contents`、`parts`、`generationConfig`、`safetySettings`、query key 和错误外形。 |
| Azure OpenAI | 基础实现（Chat） | P1/P2 | Chat Completions 已支持 deployment 路径、`api-version`、`api-key` header 和模型到 deployment 映射；其他 API 仍需补齐。 |

## 入口协议 x 上游转换矩阵

| 入口协议 | 上游类型 | 当前等级 | 商业级要求 |
|----------|----------|----------|------------|
| OpenAI-compatible | OpenAI/OpenAI-Compatible/xAI/Qwen/DeepSeek | 基础实现 | 标准字段尽量透传，`routerx` 私有字段发出前剥离，usage 可提取。 |
| OpenAI-compatible | Claude/Anthropic | 基础实现 | Chat/Responses 转 Messages，system、tools、vision、thinking/reasoning 显式映射。 |
| OpenAI-compatible | Gemini | 基础实现 | messages/content parts 转 contents/parts，generationConfig 和 safetySettings 显式映射。 |
| OpenAI-compatible | Azure OpenAI | 基础实现（Chat） | Chat Completions 已支持 model 到 deployment、query `api-version`、header `api-key` 和 OpenAI usage 扣费；Embeddings/Images/Audio 等仍需补齐。 |
| Anthropic | Anthropic | 目标 P1 | 原生 Messages 尽量透传，保留 content blocks、tool_use、tool_result 和 thinking。 |
| Anthropic | OpenAI-Compatible | 基础实现 | Messages 转 Chat，响应再转 Anthropic 外形，字段降级可解释。 |
| Anthropic | Gemini | 目标扩展 | Messages/content blocks 转 contents/parts，tool 语义和 system 指令可解释。 |
| Gemini | Gemini | 目标 P1 | 原生 generateContent 尽量透传，保留 tools、safetySettings 和 generationConfig。 |
| Gemini | OpenAI-Compatible | 基础实现 | contents/parts 转 Chat，响应再转 Gemini 外形，字段降级可解释。 |
| Gemini | Anthropic | 目标扩展 | contents/parts 转 Messages，system_instruction 和工具调用语义可解释。 |

## 能力维度矩阵

每个入口协议、APIType 和上游组合都必须按以下维度标注状态。

| 维度 | P0 最小要求 | P1 兼容要求 | P2 高级要求 |
|------|-------------|-------------|-------------|
| 请求解析 | 解析 model、stream、body JSON，错误明确 | 支持各协议原生路径、header、query 和 multipart | 支持文件、音频、图像和实时类接口 |
| 请求转换 | OpenAI Chat 非流式不丢关键字段 | 多协议字段映射和降级原因可解释 | 多模态、tool calling、reasoning、cache 等高级语义保真 |
| 响应转换 | 返回 OpenAI-compatible 成功和错误结构 | Anthropic/Gemini 成功与错误结构分别兼容 SDK | 高级 API 响应、分页、二进制或事件流兼容 |
| 流式 | OpenAI Chat/Legacy Completions 基础 SSE 可用，未支持协议明确拒绝，OpenAI SSE 客户端断开取消可测 | SSE/chunk 转换、更多流式 usage 策略 | 高级流式事件、部分失败和审计摘要 |
| usage | 优先使用下游 usage，缺失时最低规则 | provider usage 映射、本地估算和来源记录 | tokenizer 精确化、价格快照和利润分析 |
| 错误 | 不泄露密钥，错误 code 稳定 | 每个入口协议使用各自错误外形 | 下游错误归因、重试、熔断和告警联动 |
| 安全 | API Key、额度、通道状态先于下游调用 | 作用域、分组、敏感 header 过滤和路由偏好审计 | KMS、租户隔离、合规审计和风险策略 |
| 测试 | P0 Chat/Models 有集成测试 | 矩阵组合有 fixture 和 SDK 行为断言 | 故障注入、性能、长期运行和回归基线 |

## 路径冲突和协议识别

`/v1/models` 同时承载 OpenAI、Anthropic 和 Gemini 模型列表，必须保持向后兼容。

当前规则：

- 默认返回 OpenAI-compatible `models` 结构。
- `?format=gemini` 返回 Gemini 模型列表外形。
- `?format=anthropic` 返回 Anthropic 模型列表外形。
- 存在 Anthropic 相关 header 时，可以识别为 Anthropic 格式。

目标扩展：

- 可增加 `?routerx_protocol=openai|anthropic|gemini`。
- 可增加 `X-RouterX-Protocol`，但不得破坏现有 `format` 规则。
- 路径、header、query 识别冲突时返回明确错误或采用文档化优先级，不做不可解释猜测。

## `routerx` 扩展兼容规则

`routerx` 是 RouterX 的保留扩展命名空间，用于路由偏好和 provider-specific 参数。

规则：

- OpenAI-compatible、Anthropic 和 Gemini 原生字段仍按各自协议解析。
- `routerx.route` 只表达路由偏好，不能绕过 `docs/POLICIES.md` 中的策略决策顺序。
- `routerx.upstream.headers`、`routerx.upstream.query`、`routerx.upstream.body` 只补充非敏感上游参数。
- `routerx.provider.<provider>` 只在选中对应上游时生效；当前基础实现用于补充匹配 provider 的 JSON body 缺省字段，完整字段映射和降级摘要继续按 provider 矩阵补齐。
- 真实厂商上游发出前必须剥离 `routerx` 字段。
- RouterX-Compatible 上游当前可以继续接收 `routerx` 字段；服务端会递增 `X-RouterX-Hop`，达到默认上限 `3` 时本地拒绝。链路追踪摘要仍可继续增强。
- 被接受、忽略或拒绝的扩展参数必须进入日志摘要，P1/P2 进入结构化路由决策快照。

## 字段保真和降级

协议转换必须把字段分为四类处理。

| 类型 | 示例 | 处理规则 |
|------|------|----------|
| 标准可映射字段 | model、messages、temperature、top_p、max_tokens | 显式映射，并在测试中断言。 |
| provider-specific 字段 | Anthropic thinking、Gemini safetySettings、xAI search_parameters | 通过 `routerx.provider` 或原生入口保留。 |
| 可安全忽略字段 | 不影响语义的 metadata 或客户端 trace 字段 | 可以忽略，但 P1/P2 应记录降级摘要。 |
| 不可安全忽略字段 | tools、tool_result、image/audio parts、response_format、reasoning | 不支持时返回明确错误或标注降级，不能静默丢弃。 |

## 流式兼容

当前代码事实：

- OpenAI-compatible Chat 和 Legacy Completions 的 `stream=true` 已支持 OpenAI SSE 形态通道的基础转发。
- 当前从 OpenAI SSE `data:` JSON chunk 中提取 `usage`，结束后扣费并写成功日志。
- Anthropic Messages Stream 和 Gemini `streamGenerateContent` 已支持入口协议到 OpenAI-compatible Chat SSE 上游的基础 chunk 转换；Anthropic/Gemini 原生上游流式和非 OpenAI SSE 通道 chunk 转换仍未开放。

P1 目标：

- OpenAI SSE、Anthropic Messages Stream 和 Gemini streamGenerateContent 已有基础入口协议兼容输出。
- 下游 chunk 转换由 Adapter 或 translator 完成。
- 客户端断开时取消下游请求。
- 已向客户端写出 chunk 后，不再切换通道重试。
- 流式 usage 优先使用 provider 事件；缺失时使用估算策略并记录 usage 来源。

## 错误兼容

错误外形由入口协议决定：

- OpenAI-compatible：`{ "error": { "message", "type", "code" } }`。
- Anthropic：`{ "type": "error", "error": { "type", "message" } }`。
- Gemini：`{ "error": { "code", "message", "status" } }`。

当前缺口：

- API Key 鉴权、用户禁用、配额预检查和基础下游错误已经按入口协议返回 OpenAI-compatible、Anthropic 或 Gemini 错误外形。
- Anthropic/Gemini 非流式成功、Anthropic/Gemini 基础流式、入口鉴权错误、基础字段降级和基础下游错误外形已有测试；原生字段保真、原生上游流式和完整 SDK 行为仍需要在 P1 补齐。
- 下游错误摘要必须脱敏，不能泄露下游密钥、数据库 DSN、支付密钥或内部堆栈。

## 新增协议或上游的准入清单

新增一个入口协议、APIType 或上游 provider 时，必须同步完成：

1. 在 `docs/PROTOCOLS.md` 补矩阵等级、阶段和字段降级规则。
2. 在 `docs/API.md` 补路径、鉴权、错误格式和当前状态。
3. 在 `docs/RELAY.md` 补 Adapter/translator 链路和通道选择影响。
4. 在 `docs/ERRORS.md` 补对应错误 code、协议外形和扣费语义。
5. 在 `docs/POLICIES.md` 补 API Key scope、模型访问、分组或路由偏好影响。
6. 在 `docs/TESTING.md` 补 fixture、成功断言、错误断言和降级断言。
7. 在 `docs/RUNBOOKS.md` 补排障路径。
8. 在 `docs/TRACEABILITY.md` 补能力到验收追踪。

## 验收标准

P0 验收：

- OpenAI-compatible `/v1/models` 和 `/v1/chat/completions` 非流式可用。
- OpenAI-compatible Chat/Legacy Completions 基础 SSE 流式可用；未支持的流式协议或通道明确失败。
- 无效 API Key、禁用用户、禁用 API Key、余额不足、无可用通道都在下游调用前失败。
- 成功调用写日志并扣费；失败调用不误扣。
- 下游密钥、用户 API Key 和内部配置不出现在响应或日志中。

P1 验收：

- OpenAI、Anthropic、Gemini 主流 SDK 能完成基础非流式和流式调用。
- 多入口协议错误外形分别兼容。
- 主要上游 provider 的请求/响应转换、usage、错误和安全过滤都有测试。
- 字段不支持时有明确错误或降级摘要。
- `routerx` 扩展参数的接受、忽略和拒绝可审计。

P2 验收：

- 高级 API、多模态和企业路由能力有完整测试矩阵。
- 观测指标能按入口协议、APIType、上游 provider、模型和错误 code 聚合。
- 管理审计可以还原协议能力变更、通道配置变更和策略变更。
- 长期运行中可以通过 Runbook 定位协议转换、上游故障、计费偏差和 SDK 不兼容问题。
