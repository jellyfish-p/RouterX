# RouterX 测试设计

## 目标

测试体系的目标是证明 RouterX 的商业级默认体验真实可靠，而不是只证明接口能返回 200。

用户路径以 `docs/FLOWS.md` 为准，控制台能力以 `docs/CONSOLE.md` 为准，开发者体验以 `docs/DEVELOPER_EXPERIENCE.md` 为准，API Key 生命周期以 `docs/API_KEYS.md` 为准，策略与访问控制以 `docs/POLICIES.md` 为准，协议兼容和能力矩阵以 `docs/PROTOCOLS.md` 为准，调用事实快照以 `docs/SNAPSHOTS.md` 为准，支付和充值以 `docs/PAYMENTS.md` 为准，术语边界以 `docs/GLOSSARY.md` 为准，能力覆盖以 `docs/TRACEABILITY.md` 为准，阶段验收门禁以 `docs/ACCEPTANCE.md` 为准，安全威胁以 `docs/SECURITY.md` 为准，错误语义以 `docs/ERRORS.md` 为准，观测审计以 `docs/OBSERVABILITY.md` 为准，故障处理以 `docs/RUNBOOKS.md` 为准。新增用户路径、管理路径或进阶路径时，应先补路径合同、控制台能力契约、开发者体验契约、API Key 契约、策略契约、协议矩阵、调用事实快照契约和验收门禁，再补接口、数据模型、Runbook 和测试断言。

测试必须覆盖：

- 开箱路径：初始化、登录、创建 API Key、创建通道、第一次模型调用、日志和额度变化。
- 安全默认：API Key 和下游密钥不泄露，余额不足和权限不足不调用下游。
- 协议兼容：`/v1` 成功和失败都保持入口协议兼容格式。
- 计费一致：usage、`quota_used`、用户余额、Key 预算和账单聚合互相一致。
- 路由可解释：通道选择、模型重写、错误来源和重试行为可还原。

## 当前覆盖

当前已有测试集中在 `internal/router/router_test.go`。

| 测试 | 已覆盖 |
|------|--------|
| `TestP0BackendFlow` | 初始化、登录、API Key 创建、用户禁止编辑 Key 额度、通道创建、模型列表、密钥脱敏、无效 Key、空额度 Key、禁用用户 |
| `TestAdminPrivilegeBoundaries` | 管理员和超级管理员权限边界、设置脱敏、管理员不能越权管理同级或自己 |
| `TestUserRedeemsRedemCodeOnce` | 用户兑换未使用充值码、额度增加、充值码标记 used/used_by/used_at，写入幂等额度流水，重复兑换不再入账 |
| `TestAdminQuotaAdjustmentWritesTransaction` | 管理员调整用户额度时写入额度流水，记录 actor、reason、变更前后余额和幂等键 |
| `TestAdminManagesRedemCodes` | 管理员生成随机充值码、导入指定充值码、列表查询、作废未使用码，作废码不可兑换 |
| `TestUserListsAvailableModels` | 用户查看当前启用通道的去重模型列表，禁用通道模型不可见，价格状态明确标记未就绪 |
| `TestChannelExtendedManagement` | 多 key、多 base URL、模型重写、通道分组、扩展配置、密钥加密 |
| `TestSetupBootstrapAdminQuotaAndSettingsDefaults` | 初始化管理员启动额度和 settings 默认值 |
| `TestSettingsValidationAndReadiness` | settings 类型校验、`server.port`/`server.mode` 边界、限流阈值 `0` 禁用语义、JWT/生产 readiness 和关键配置缺失 |
| `TestSettingDefaultsBackfillPreservesExistingValues` | 启动默认配置回填不会覆盖已有值 |
| `TestSettingCacheRefreshesStaleRedisValues` | settings 读取缓存、单项更新和批量更新后的 Redis 刷新边界 |
| `TestAPIKeyAuthErrorsUseEntryProtocolShape` | Anthropic/Gemini 入口 API Key 鉴权错误外形 |
| `TestAnthropicAndGeminiEntrypointsConvertSuccessAndDegradeFields` | Anthropic/Gemini 非流式成功响应、usage、扣费和非文本 content/parts 降级 |
| `TestGeminiEmbedContentConvertsOpenAIEmbeddingsAndDeductsUsage` | Gemini embedContent 转 OpenAI-compatible Embeddings 上游，返回 Gemini `embedding.values` 外形，usage 写日志和扣费 |
| `TestGeminiBatchEmbedContentsConvertsOpenAIEmbeddingsAndDeductsUsage` | Gemini batchEmbedContents 转 OpenAI-compatible Embeddings 批量 input，上游 embedding list 返回 Gemini `embeddings[].values` 外形，usage 写日志和扣费 |
| `TestRateLimitUsesSettingsAndEntryProtocolErrorShape` | Redis Token 限流读取 `rate_limit.*`，本地 429 不调用上游，并返回入口协议兼容错误 |
| `TestChatCompletionInvalidRequestDoesNotCallUpstream` | 非法 JSON、缺少 model 在本地失败且不污染通道和账单 |
| `TestChannelRoutingConfigResolution` | `upstreams` 优先、密钥选择归一化、模型重写和真实 Relay 请求不泄密 |
| `TestUserBillingMatchesLogs` | 多次成功/失败混合后，用户账单、日志、余额和 Key 预算一致 |
| `TestChatCompletionSuccessLogsAndDeductsQuota` | Chat 非流式成功调用、日志、用户额度、Key 预算和账单聚合 |
| `TestAzureChatCompletionUsesDeploymentPathAndAPIKey` | Azure OpenAI Chat 基础转发，deployment 路径、`api-version` query、`api-key` header、`model/routerx` 剥离、usage 日志和扣费 |
| `TestResponsesPassthroughExtractsUsageAndDeductsQuota` | Responses 基础 JSON 透传、`routerx` 剥离、`input_tokens/output_tokens` usage 映射、日志和扣费 |
| `TestEmbeddingsPassthroughExtractsUsageAndDeductsQuota` | Embeddings 基础 JSON 透传、`routerx` 剥离、`prompt_tokens/total_tokens` usage 映射、日志和扣费 |
| `TestModerationsPassthroughUsesMinimumChargeWithoutUsage` | Moderations 基础 JSON 透传、`routerx` 剥离、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestImageGenerationsPassthroughUsesMinimumChargeWithoutUsage` | Image Generations 基础 JSON 透传、`routerx` 剥离、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestImageMultipartPassthroughUsesRouteAndMinimumCharge` | Image Edits/Variations multipart 表单透传、`routerx` 表单字段剥离与路由偏好、图像/遮罩文件字段保留、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestAudioSpeechPassthroughReturnsBinaryAndUsesMinimumCharge` | Audio Speech 基础 JSON 透传、`routerx` 剥离、二进制音频响应和 Content-Type 透传、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestAudioTranscriptionsMultipartPassthroughUsesRouteAndMinimumCharge` | Audio Transcriptions multipart 表单透传、`routerx` 表单字段剥离与路由偏好、文件字段保留、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestChatCompletionStreamForwardsSSEAndDeductsUsage` | OpenAI-compatible Chat SSE chunk 转发、usage 提取、日志和扣费 |
| `TestCompletionsStreamForwardsSSEAndDeductsUsage` | Legacy Completions SSE chunk 转发、`routerx` 剥离、usage 提取、日志和扣费 |
| `TestChatCompletionStreamCancelsUpstreamWhenClientWriteFails` | 客户端写入失败时取消上游 SSE 请求，失败日志不扣费 |
| `TestChatCompletionStreamRejectsNonOpenAISSEUpstream` | 非 OpenAI SSE 通道在流式请求中被上游前拒绝 |
| `TestGeminiStreamGenerateContentConvertsOpenAISSEAndDeductsUsage` | Gemini streamGenerateContent 转 OpenAI-compatible Chat SSE，上游 OpenAI chunk 转 Gemini SSE 事件，usage 扣费和日志 |
| `TestAnthropicMessagesStreamConvertsOpenAISSEAndDeductsUsage` | Anthropic Messages stream 转 OpenAI-compatible Chat SSE，上游 OpenAI chunk 转 Anthropic SSE 事件，usage 扣费和日志 |
| `TestChatCompletionUpstreamBadRequestMapping` | 下游 400 错误映射、失败日志和密钥不泄露 |
| `TestChatCompletionUpstreamErrorStatusMapping` | 下游 401/403/429/5xx 错误映射、失败日志、通道错误计数和不扣费 |
| `TestChatCompletionUpstreamTimeoutMapping` | 下游超时错误映射、失败日志、通道错误计数和不扣费 |
| `TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce` | 非流式 5xx 按 `relay.retry_count` 换候选通道，最终只按成功 usage 扣费一次 |
| `TestChatCompletionDoesNotRetryNonRetryableUpstreamStatus` | 下游 400 不触发候选通道重试 |
| `TestChatCompletionSkipsTrippedChannelAtConfiguredThreshold` | `relay.error_ban_threshold` 生效后跳过达到阈值的故障通道 |
| `TestChatCompletionHonorsDisabledAutoBanSetting` | `relay.error_auto_ban=false` 时高 `error_count` 通道仍可参与候选并在成功后恢复计数 |
| `TestAnthropicAndGeminiEntrypointsMapUpstreamErrorsToEntryProtocol` | Anthropic/Gemini 入口下游错误按各自协议外形返回且不泄密、不扣费 |
| `TestRelayPrecheckRejectsBeforeUpstream` | 无效 Key、禁用 Key、额度不足、禁用通道不调用下游 |
| `TestRouterXRoutePreferenceFiltersChannels` | `routerx.route` 被接受、未知字段忽略、非法结构拒绝和筛选后无候选 |

仍需优先补齐：

- Anthropic/Gemini 原生上游流式和更完整的流式 usage fallback/估算策略。
- Anthropic/Gemini 更完整 SDK 行为细节、原生字段保真和流式错误路径。

## 测试原则

- 测试使用本地 `httptest` 下游服务，不依赖真实 OpenAI、Anthropic、Gemini 或其他厂商。
- 每个模型转发测试至少断言三类结果：HTTP 响应、数据库状态、敏感信息不泄露。
- `/v0` 接口断言 RouterX 统一响应，`/v1` 接口断言入口协议兼容响应。
- 失败测试必须证明是否调用了下游；预检失败路径的下游请求计数必须为 0。
- 计费测试必须同时检查 `logs.quota_used`、用户余额、Key 预算和用户账单统计。
- 不使用真实外部支付、OAuth/OIDC 或短信/邮件服务；这些能力用 provider stub 或签名 fixture 测试。

## 测试夹具合同

### 本地下游桩

模型转发测试使用 `httptest.Server` 模拟真实上游。测试通道的 `base_url` 指向该 server。

下游桩至少需要支持：

| 能力 | 要求 |
|------|------|
| 请求计数 | 记录总请求次数，用于证明预检失败没有调用下游。 |
| 请求快照 | 记录 method、path、query、headers 和 body。 |
| 响应脚本 | 可配置状态码、响应体、延迟和连接错误。 |
| usage fixture | 成功响应可返回固定 `prompt_tokens`、`completion_tokens`、`total_tokens`。 |
| 敏感字段检查 | 能断言下游收到的是通道 API Key，而不是用户 API Key。 |

P0 OpenAI-compatible Chat 成功响应示例：

```json
{
  "id": "chatcmpl-test",
  "object": "chat.completion",
  "model": "gpt-test",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "ok" },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 3,
    "completion_tokens": 2,
    "total_tokens": 5
  }
}
```

下游请求断言：

- path 应匹配 Adapter 生成的 endpoint，例如 OpenAI-compatible Chat 为 `/v1/chat/completions`。
- `Authorization` 应为通道下游密钥，不应为用户的 RouterX API Key。
- 请求体不应包含 `routerx` 私有字段，除非上游通道类型是 RouterX-Compatible。
- 模型重写启用时，下游 body 中的 `model` 应为上游模型名。
- 敏感 header 如 `Cookie`、`Set-Cookie`、用户提交的 `X-Api-Key` 不应被透传。

### 数据库断言

模型转发测试不能只断言 HTTP 响应。成功和失败都需要检查数据库事实。

成功调用至少检查：

| 表 | 字段 | 断言 |
|----|------|------|
| `logs` | `user_id`、`token_id`、`channel_id` | 均指向本次调用实体 |
| `logs` | `model` | 保存客户端请求模型；如有模型重写，后续目标字段保存上游模型 |
| `logs` | `prompt_tokens`、`completion_tokens`、`total_tokens` | 与 usage fixture 一致 |
| `logs` | `quota_used` | P0 等于 `total_tokens`，缺失 usage 时为最低值 `1` |
| `logs` | `status` | 成功为 `1`，失败为 `2` |
| `tokens` | `remain_quota` / `quota_limit` / `quota_used` | 有限 API Key 按 `quota_used` 消耗预算 |
| `users` | `quota` | 有限和无限 API Key 调用成功时均扣减用户额度 |
| `channels` | `error_count` | 成功后清零或保持 0 |

失败调用至少检查：

- 预检失败时下游请求计数为 0。
- 默认不增加 `quota_used`。
- `logs.status=failed`，`error_msg` 是脱敏摘要。
- 若尚未选中通道，`logs.channel_id` 可以为空。
- 下游密钥、用户 API Key、数据库 DSN 不出现在 `content`、`response`、`error_msg` 或接口响应中。

### 错误格式断言

`/v1` 错误格式由入口协议决定。

OpenAI-compatible 最小断言：

```json
{
  "error": {
    "message": "string",
    "type": "string",
    "code": "string"
  }
}
```

Anthropic-compatible 最小断言：

```json
{
  "type": "error",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

Gemini-compatible 最小断言：

```json
{
  "error": {
    "code": 400,
    "message": "string",
    "status": "INVALID_ARGUMENT"
  }
}
```

通用错误断言：

- 不出现 RouterX 管理端 `{ "success": false, "data": null }` 包装。
- HTTP status 与错误分类一致。
- `message` 可读但不泄露内部密钥、堆栈或 DSN。
- `code` 或 `status` 可用于测试稳定断言，不依赖完整自然语言文案。

## P0 详细用例

### `TestSetupBootstrapAdminQuota`

目的：避免第一次验证调用被 0 额度卡死。

前置：

- 空数据库。
- 设置 `JWT_SECRET` 和 `ENCRYPTION_KEY`。

步骤：

1. `POST /v0/setup/init` 创建超级管理员。
2. 查询超级管理员用户记录。
3. 查询 `settings` 中的启动额度配置。

断言：

- 初始化成功。
- 超级管理员存在且启用。
- 如果配置 `billing.bootstrap_admin_quota > 0`，用户额度等于或大于该值。
- 如果配置为 0，接口或文档必须给出明确额度调整路径。

### Settings registry/readiness/cache tests

目的：证明 settings 不是散落字符串，而是有默认值、类型校验、缓存刷新和生产就绪边界的配置系统。

前置：

- 空数据库初始化。
- 使用 `docs/SETTINGS.md` 中 current 阶段 key 作为断言清单。

断言：

- 初始化后 current 阶段 key 均存在，默认值与注册表一致。
- 重复初始化或补缺失流程不覆盖已修改配置。
- typed accessor 读取 int、float、bool、string 时类型正确。
- 非法值不会被静默解析为 0 值。
- 修改配置后缓存刷新，后续读取使用新值。
- 敏感值如 `jwt.secret` 在管理端响应和审计摘要中脱敏。
- 生产模式下关键配置缺失或非法时 `/ready` 不就绪。

### `TestChatCompletionSuccessLogsAndDeductsQuota`

目的：证明 P0 不是只支持模型列表，而是真正完成一次模型调用闭环。

前置：

- 初始化管理员并登录。
- 管理员或用户拥有可用额度。
- 创建有限额度 API Key。
- 创建 OpenAI-compatible 通道，`base_url` 指向本地 `httptest` 下游。
- 下游返回 OpenAI-compatible Chat 响应，并包含 usage。

请求：

```json
{
  "model": "gpt-test",
  "messages": [{ "role": "user", "content": "hello" }],
  "stream": false
}
```

断言：

- `POST /v1/chat/completions` 返回 200。
- 响应包含 OpenAI-compatible `choices` 和 `usage`。
- 下游收到一次请求，且不包含 `routerx` 私有字段。
- `logs` 新增一条成功记录，包含 user、token、channel、model、tokens、`quota_used`。
- 有限额度 API Key 的剩余预算减少 `quota_used`，或累计已用增加 `quota_used`。
- 用户 `quota` 同时减少 `quota_used`。
- 通道 `error_count` 清零或保持 0。
- 响应、日志和通道列表不包含下游 API Key 明文。

### `TestChatCompletionInvalidRequestDoesNotCallUpstream`

目的：证明请求错误在本地失败，不污染通道和账单。

场景：

| 请求 | 期望 |
|------|------|
| 非法 JSON | 400 `invalid_json` |
| 缺少 `model` | 400 `model_required` 或 `invalid_request` |

断言：

- 下游请求计数为 0。
- 不扣用户余额或 Key 预算。
- 可写失败日志，但 `channel_id` 可以为空。
- `/v1` 错误为 OpenAI-compatible 错误结构，不返回 `{success,data,message}`。

### `TestChatCompletionUpstreamErrorMapping`

目的：证明下游错误被正确分类，不泄露内部细节。

场景：

| 下游行为 | 期望 |
|----------|------|
| 400 | 返回兼容 400，不重试 |
| 401/403 | 返回兼容 502 或配置错误摘要，不重试，增加通道错误计数 |
| 429 | `relay.retry_count > 0` 时非流式可换候选通道，否则返回兼容上游错误 |
| 500/502/503/504 | `relay.retry_count > 0` 时非流式未写出前可重试候选通道 |
| 超时 | 返回 504 或上游超时 code |
| 非法响应 JSON | 返回 `upstream_conversion_failed` |

断言：

- 错误响应不包含下游密钥、DSN、堆栈或完整下游响应体。
- 可重试错误才触发重试。
- 失败日志包含错误摘要和下游状态码。
- 未产生有效 usage 时不扣费。

### `TestRelayPrecheckRejectsBeforeUpstream`

目的：证明安全和计费预检发生在下游调用前。

场景：

| 场景 | 期望 |
|------|------|
| 无效 API Key | 401，下游计数 0 |
| 禁用 API Key | 401，下游计数 0 |
| 禁用用户 | 403，下游计数 0 |
| 余额不足 | 429，下游计数 0 |
| 禁用通道 | 502，下游计数 0 |
| 模型不匹配 | 502，下游计数 0 |

断言：

- 不扣费。
- 失败日志可排障。
- 错误结构符合入口协议。

### `TestChannelRoutingConfigResolution`

目的：证明通道高级配置行为稳定、可解释。

前置：

- 创建包含 `api_key`、`api_keys`、`base_url`、`base_urls`、`upstreams`、`model_rewrites` 的通道。

断言：

- `upstreams` 非空时优先使用，不与外层 `api_keys` 或 `base_urls` 交叉组合。
- `key_selection_mode=random` 时 key 来自候选集合。
- 空值或未知 `key_selection_mode` 归一为 `round_robin`。
- `model_rewrites` 将客户端模型改写为上游模型。
- 明文密钥不会出现在响应、日志或错误中。

### `TestUserBillingMatchesLogs`

目的：证明用户账单统计来自调用事实。

前置：

- 同一用户执行多次成功调用和失败调用。

断言：

- `GET /v0/user/log` 只返回当前用户日志。
- `GET /v0/user/billing` 的调用数、token 数和消耗额度与 `logs` 聚合一致。
- 失败调用默认不增加 `quota_used`。
- 有限额度 API Key 调用同时扣 `users.quota` 和 Key 预算。
- 无限 Token 调用扣 `users.quota`，`tokens.remain_quota` 保持 `-1`。
- 创建有限额度 API Key 只设置预算上限，不扣用户余额，不计入模型消费日志。
- 管理员日志筛选与用户日志视角一致。

## P1/P2 测试矩阵

| 阶段 | 能力 | 测试重点 |
|------|------|----------|
| P0 | 开发者最小接入 | base URL + RouterX API Key、`/v1/models`、非流式 Chat、OpenAI Chat/Completions 基础 SSE、日志和扣费 |
| P1 | SSE 流式 | 已覆盖 OpenAI-compatible Chat 和 Legacy Completions 基础 chunk 转发、Anthropic Messages Stream/Gemini streamGenerateContent 到 OpenAI-compatible SSE、usage 扣费和客户端断开取消；继续补 Anthropic/Gemini 原生上游流式、usage fallback 和已输出后不切换通道的更多故障注入 |
| P1 | 路由偏好 | `routerx.route` 被接受、忽略、拒绝和筛选后无候选 |
| P1 | 多协议入口 | 已覆盖 Anthropic/Gemini 基础非流式成功、Anthropic/Gemini 基础流式、鉴权错误和基础下游错误外形；继续按 `docs/PROTOCOLS.md` 断言完整 SDK 行为、原生字段保真和 Anthropic/Gemini 原生流式路径 |
| P1 | 多上游转换 | 按 `docs/PROTOCOLS.md` 断言 OpenAI-compatible、Anthropic、Gemini、Azure、xAI、Qwen、DeepSeek 的请求/响应转换和降级原因 |
| P1 | 调用事实快照 | request、policy、route、usage、billing、error 快照脱敏且能解释历史调用 |
| P1 | 计费规则 | 价格表达式、倍率、访问控制、规则快照和历史账单解释 |
| P1 | 可靠性 | 已覆盖非流式安全重试、Redis 全局/IP/Token 基础限流和 `error_count` 自动熔断候选过滤；继续补半开恢复、探测任务、更多限流维度和生产 fail-open/fail-closed 策略 |
| P1 | 运行模式 | SQLite 单镜像无 Redis 可运行；外部数据库无 Redis 不就绪或启动失败 |
| P1 | 通道候选缓存 | 预加载、缓存命中、管理员修改后版本失效、集群实例回源一致 |
| P1 | 独立日志数据库 | `LOG_SQL_DSN` 写入、日志库故障降级、主库结算最小事实可恢复 |
| P2 | 企业账号 | OAuth/OIDC state、nonce、subject 绑定、禁止 email 自动接管 |
| P2 | 高级 API Key 管理 | 轮换、泄露上报、最近使用、作用域拒绝、批量禁用、审计和缓存失效 |
| P2 | 支付充值 | Stripe/易支付签名、金额校验、订单状态、重复回调幂等、额度流水和人工修正审计 |
| P2 | 观测审计 | Request ID、结构化日志、Prometheus 指标、管理审计日志、生产 `/ready` |

## 测试数据约定

- 测试 API Key 使用 `sk-` 前缀，但断言数据库只保存 SHA256 哈希。
- 下游测试密钥使用明显字符串，例如 `upstream-secret`，并断言响应和日志不包含该字符串。
- 模型名使用 `gpt-test`、`client-model`、`upstream-model` 等固定值。
- 额度使用小整数，便于断言扣减结果。
- 下游测试服务记录请求次数、路径、header 和 body，用于证明是否调用下游以及私有字段是否剥离。

## 验证命令

文档和代码变更后至少运行：

```text
go test ./...
git diff --check
```

文档一致性变更后额外搜索：

```text
rg -n "<旧术语或待办标记的搜索模式>" README.md docs
```
