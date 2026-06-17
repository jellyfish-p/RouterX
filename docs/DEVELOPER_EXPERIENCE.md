# RouterX 开发者体验契约

本文档面向调用方开发者、技术用户和企业集成者，回答“我怎样把现有模型调用接到 RouterX、哪些行为可以依赖、哪些能力需要按阶段开启”。它不替代 `docs/API.md` 和 `docs/RELAY.md`，也不复刻任何上游厂商 SDK 文档；它定义 RouterX 对调用方必须提供的接入体验、兼容边界和排障证据。

目标：

- 小白开发者能把一个 OpenAI-compatible HTTP 调用指向 RouterX，并完成第一次非流式 Chat 调用。
- 有经验的开发者能使用 API Key、模型名、路由偏好、错误 code、日志和账单事实构建稳定集成。
- 企业集成者能规划多环境、多 Key、灰度路由、可观测、重试和安全边界。

非目标：

- 不设计网页页面。
- 不规定具体 SDK 版本。
- 不把上游厂商专有能力伪装成 P0 已支持。
- 不允许调用方用请求参数覆盖 RouterX 的鉴权、额度、通道和访问控制决策。

相关文档：

- 接口和错误格式：`docs/API.md`
- 协议兼容、能力等级和 SDK 行为矩阵：`docs/PROTOCOLS.md`
- Relay、协议转换和 `routerx` 扩展：`docs/RELAY.md`
- 控制台能力：`docs/CONSOLE.md`
- 错误语义：`docs/ERRORS.md`
- 日志、指标和审计：`docs/OBSERVABILITY.md`
- 调用事实快照：`docs/SNAPSHOTS.md`
- API Key 生命周期和管理：`docs/API_KEYS.md`
- 策略、访问控制和路由偏好：`docs/POLICIES.md`
- 账号和权限：`docs/ACCOUNTS.md`
- 计费和额度：`docs/BILLING.md`
- 故障处理：`docs/RUNBOOKS.md`

## 1. 开发者分层

| 层级 | 典型用户 | 期待体验 | 不应要求 |
|------|----------|----------|----------|
| D0 首次调用者 | 个人开发者、小团队 | 改 base URL 和 API Key 后跑通 Chat | 先理解多通道、支付、审计和完整协议矩阵 |
| D1 应用集成者 | 把业务应用接入 RouterX 的开发者 | 稳定错误、可控超时、可查日志、可解释扣费 | 猜测内部通道选择 |
| D2 平台集成者 | 多团队、多环境、多模型接入 | API Key 轮换、路由偏好、模型映射、灰度和观测 | 通过用户请求绕过后台策略 |
| D3 企业集成者 | 私有部署和企业网关 | 多协议、多上游、审计、指标、故障处理和合规 | 牺牲 P0 默认路径稳定性 |

## 2. 最小接入合同

P0 最小接入只依赖四件事：

1. RouterX 实例地址。
2. 用户 API Key。
3. 至少一个可用通道。
4. OpenAI-compatible Chat 非流式请求。

HTTP 合同：

```http
POST /v1/chat/completions
Authorization: Bearer sk-...
Content-Type: application/json
```

最小请求：

```json
{
  "model": "gpt-test",
  "messages": [
    { "role": "user", "content": "hello" }
  ],
  "stream": false
}
```

P0 成功保证：

- 返回 OpenAI-compatible Chat Completions 外形。
- 写入模型调用日志。
- 按 usage 或 P0 最小规则计算 `quota_used`。
- 扣减用户额度；有限 API Key 同时消耗 Key 预算。
- 响应、日志和错误不泄露用户 API Key、上游密钥、数据库 DSN 或支付密钥。

P0 明确不承诺：

- Anthropic/Gemini 流式、客户端断开取消和完整流式 usage fallback。
- 全量 OpenAI Responses、Images、Audio、Embeddings、Moderations。
- Anthropic/Gemini 全量字段无损转换。
- 自动重试和跨通道流式切换。
- 调用方指定任意通道并绕过后台策略。

## 3. 从现有 OpenAI-compatible 调用迁移

迁移原则：

- 保持业务代码的模型调用形态不变。
- 把 base URL 指向 RouterX `/v1`。
- 把 API Key 换成 RouterX 用户 API Key。
- 模型名先保持调用方模型名，由 RouterX 通道和模型重写决定真实上游模型。

HTTP 示例：

```bash
curl https://routerx.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-routerx-user-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-test",
    "messages": [
      { "role": "user", "content": "hello" }
    ],
    "stream": false
  }'
```

SDK 配置合同：

| 配置项 | RouterX 要求 |
|--------|--------------|
| base URL | 指向 RouterX 的 `/v1` 前缀 |
| API Key | 使用 RouterX 用户 API Key，不使用上游厂商 Key |
| model | 使用 RouterX 对外模型名，可由通道做模型重写 |
| stream | OpenAI-compatible Chat 在 OpenAI SSE 形态通道上可设为 `true`；其他协议和通道按 `docs/PROTOCOLS.md` 能力矩阵处理 |
| timeout | 调用方应设置业务可接受超时，RouterX 也有 `relay.timeout` |
| retry | P0 调用方可以对本地网络错误和明确可重试错误做保守重试；不要对 400/401/403 盲目重试 |

迁移检查清单：

- API Key 是否以 `sk-` 开头且来自 RouterX。
- 请求是否命中 RouterX 实例，而不是直接命中上游厂商。
- `/v1/models` 是否能返回兼容模型列表。
- `POST /v1/chat/completions` 是否非流式成功。
- 用户日志中是否出现本次调用。
- 用户余额或 Key 预算是否按 `quota_used` 变化。

## 4. API Key 使用体验

API Key 是调用 `/v1/*` 的唯一模型调用凭据。User JWT 只用于控制台和 `/v0/user/*`、`/v0/admin/*`。

创建体验：

- API Key 明文只在创建响应展示一次。
- 后续只能看到脱敏摘要、状态、过期时间、额度和使用信息。
- 忘记明文时只能创建新 Key 或轮换，不能恢复旧 Key。

运行体验：

| 场景 | 期望行为 |
|------|----------|
| Key 不存在、禁用、删除或过期 | 401 `invalid_api_key` 或等价认证错误 |
| 所属用户禁用 | 403 `user_disabled` |
| API Key 预算不足 | 429 `insufficient_quota` |
| 用户额度不足 | 429 `insufficient_quota` |
| Key 有限额度 | 成功调用扣用户余额，并消耗 Key 剩余预算 |
| Key 无限额度 | 成功调用扣用户额度 |

应用侧建议：

- 每个业务环境使用独立 API Key，例如 dev、staging、prod。
- 每个重要应用或租户使用独立 API Key，方便限额、禁用和账单追踪。
- 不把 API Key 写入前端、移动端、日志、错误上报或仓库。
- 使用环境变量、密钥管理服务或部署平台 secret 注入。
- 轮换时先创建新 Key、灰度切流、确认日志，再禁用旧 Key。

目标高级能力：

- API Key 最近使用时间。
- API Key 最后错误摘要。
- 批量禁用和批量轮换。
- API Key 标签、环境、所有者和备注。
- 可选 scope：模型范围、通道分组、请求类型、时间窗口。

## 5. 模型名和路由体验

调用方只应该依赖 RouterX 对外模型名。真实上游模型名由通道配置决定。

模型名处理：

| 阶段 | 行为 |
|------|------|
| 请求入口 | 调用方提交 `model` |
| 候选过滤 | RouterX 用请求模型匹配通道支持模型 |
| 模型重写 | 命中通道后将调用方模型名映射为上游模型名 |
| 日志记录 | 应记录调用方模型名；目标补充上游模型名和重写规则摘要 |

开发者不应：

- 在业务代码中硬编码上游厂商真实模型名，除非它也是 RouterX 对外模型名。
- 通过 `routerx` 私有字段强制使用无权限通道。
- 假设相同模型名在所有通道上价格、上下文长度和能力完全一致。

目标可见性：

- `/v0/user/models` 目标接口返回当前用户可用模型、价格和支持能力。
- 日志显示请求模型、上游模型目标字段和通道摘要。
- 管理员可以解释模型为何无可用通道。

## 6. `routerx` 扩展参数

`routerx` 是 RouterX 私有扩展命名空间，用于表达路由偏好和 provider-specific 参数。它不是模型厂商原生字段。

示例：

```json
{
  "model": "gpt-4o-mini",
  "messages": [{ "role": "user", "content": "hi" }],
  "routerx": {
    "route": {
      "channel_group": "premium",
      "upstream_provider": "openai"
    },
    "upstream": {
      "headers": {},
      "query": {},
      "body": {}
    },
    "provider": {
      "openai": {},
      "anthropic": {},
      "gemini": {}
    }
  }
}
```

使用规则：

- 完整策略语义以 `docs/POLICIES.md` 为准。
- `routerx.route` 只能表达偏好，不能扩大权限。
- 后台策略、安全过滤、用户状态、API Key 状态、额度、通道启用状态和访问控制永远优先。
- 真实厂商上游调用前必须剥离 `routerx` 字段。
- RouterX-Compatible 上游可以继续接收 `routerx`，但必须有 hop 限制，避免循环。
- `routerx.upstream.headers` 不能覆盖 `Authorization`、`Cookie`、`Set-Cookie`、`X-Api-Key`、`api-key` 等敏感鉴权字段。
- 非 JSON 请求当前可通过 `routerx` 表单字段或 `X-RouterX-Options` header 传递路由偏好；body/form 中的 `routerx` 优先于 header，两者都必须校验和脱敏。

调用方应预期：

| 场景 | 行为 |
|------|------|
| 偏好合法且有候选 | 正常路由，日志记录偏好被接受 |
| 未知但安全的偏好字段 | 可忽略，并记录摘要 |
| `routerx` 结构非法 | 400 `invalid_routerx_options` |
| route 格式非法 | 400 `invalid_routerx_route` |
| route 越权 | 403 `route_forbidden` |
| route 合法但无候选 | 502 `no_available_channel` 或入口协议等价错误 |

## 7. 错误处理体验

调用方必须使用 HTTP 状态和稳定 code 共同判断错误。

常见处理：

| HTTP | code 示例 | 调用方动作 |
|------|-----------|------------|
| 400 | `invalid_json`、`model_required`、`unsupported_stream` | 修正请求或改用已支持的流式入口，不重试 |
| 401 | `invalid_api_key` | 更换或重新创建 API Key，不重试 |
| 403 | `user_disabled`、`route_forbidden` | 联系管理员或调整路由偏好 |
| 429 | `insufficient_quota`、`rate_limit_exceeded` | 降低并发、等待窗口、充值或调整额度 |
| 502 | `no_available_channel`、`upstream_secret_error`、`unsupported_stream_channel` | 管理员检查通道、模型、密钥、上游和流式能力 |
| 502/504 | `upstream_5xx`、`upstream_timeout` | 可按业务幂等性和阶段策略保守重试 |

重试建议：

- 400、401、403 默认不重试。
- 429 需要区分本地限流、余额不足和上游限流。
- `relay.retry_count=0` 时服务端不自动重试；大于 0 时仅非流式对 429、5xx、网络错误、超时和响应读取失败换候选通道。
- 调用方如自行重试，应避免让非幂等业务产生重复消费。
- 服务端发生重试时，会留下失败尝试日志和最终成功/失败日志，便于追踪最终通道。

错误响应不保证所有 provider message 原样透出。RouterX 可以返回脱敏摘要，以保护密钥、内部路径和上游敏感响应。

## 8. 用量、额度和账单体验

开发者接入时必须能回答三件事：

1. 这次调用有没有成功。
2. 这次调用消耗了多少 usage 和额度。
3. 消耗从哪个用户或 API Key 余额扣除。

P0 计费事实：

- 成功调用写 `logs`。
- `quota_used` 是用户账单聚合的核心事实。
- 上游返回 `usage.total_tokens` 时优先使用。
- usage 缺失时 P0 可以使用最小扣费规则，但必须可解释。
- 失败且未调用上游时不扣模型消费额度。
- 已调用上游但扣费失败的场景必须有事务语义和失败日志。

开发者体验要求：

- 用户能按时间范围查看调用次数、tokens 和额度消耗。
- 用户能按 API Key 筛选自己的日志。
- 管理员能按 user、token、channel、model、status 和时间筛选全局日志。
- 成功调用的响应 usage、日志 usage 和账单聚合不应互相矛盾。

## 9. 观测和排障体验

调用方遇到错误时，应该能把一条失败请求交给管理员排查，而不暴露 API Key 明文。

最低排障材料：

- 时间。
- 路径和入口协议。
- HTTP 状态。
- 错误 code。
- model。
- API Key 脱敏摘要或 token id。
- request_id 目标字段。

管理员目标排障材料：

- user_id、token_id、channel_id。
- route preference 是否被接受、忽略或拒绝。
- 候选通道过滤摘要。
- 上游状态码和脱敏错误摘要。
- usage、quota_used 和扣费事务结果。
- retry_count、latency_ms、route_snapshot 和 billing_snapshot 目标字段，字段语义以 `docs/SNAPSHOTS.md` 为准。

开发者不应在工单或错误上报中提供：

- 完整 API Key。
- 上游密钥。
- Cookie。
- 完整 prompt 或响应正文，除非已脱敏且确有必要。

## 10. 多环境和企业接入

推荐环境模型：

| 环境 | API Key | 通道 | 用途 |
|------|---------|------|------|
| dev | 独立 Key，低额度 | 可用测试通道 | 本地开发 |
| staging | 独立 Key，有限额度 | 近似生产通道 | 发布前验证 |
| prod | 独立 Key，审计和告警 | 高可用通道 | 真实流量 |

企业接入目标：

- 每个团队或服务使用独立 API Key。
- 通过用户分组、通道分组和模型分组控制访问。
- 用 `routerx.route` 表达灰度或偏好，而不是硬编码具体通道 ID。
- 日志和指标按团队、Key、模型、通道聚合。
- 高风险 Key 轮换、禁用和泄露处理有 Runbook。

## 11. 阶段边界

| 阶段 | 开发者体验承诺 |
|------|----------------|
| P0 | OpenAI-compatible Models 和 Chat 基础闭环；OpenAI Chat 基础 SSE；API Key 鉴权；错误、日志和扣费可解释 |
| P1 | 多协议流式、更多入口协议、主流上游转换、`routerx` 扩展、访问控制、重试熔断和更完整日志，能力等级以 `docs/PROTOCOLS.md` 为准 |
| P2 | 高级 API、企业身份、支付运营、高级 API Key 管理、指标告警、审计和 KMS |

阶段原则：

- P0 不把未支持能力伪装成可用。
- P1 每新增一个入口协议或上游 provider，都要先补 `docs/PROTOCOLS.md` 的能力等级和降级规则，再补 SDK 行为、错误格式、usage、日志和测试。
- P2 的企业和运营能力不能破坏 P0 的 base URL + API Key 最小迁移路径。

## 12. 验收要求

P0 验收：

- 用 RouterX API Key 可以调用 `/v1/models`。
- 用 RouterX API Key 可以完成非流式 `/v1/chat/completions`。
- OpenAI-compatible Chat `stream=true` 命中 OpenAI SSE 形态通道时可完成基础流式；未支持的流式协议或通道返回明确错误。
- 无效 API Key、余额不足、无通道和上游密钥错误都有稳定 code。
- 成功调用写日志并扣正确余额。
- 响应、日志和错误不泄露密钥。

P1 验收：

- 主流 SDK 可通过 RouterX 完成基础非流式和流式调用。
- `routerx.route` 合法、非法、越权和无候选路径都有稳定行为。
- 多协议入口成功和错误外形分别兼容 OpenAI、Anthropic 和 Gemini。
- 重试和熔断行为能从日志解释。

P2 验收：

- API Key 高级管理、企业身份、支付、审计和指标能支持长期生产接入。
- 企业集成者能按团队、环境、Key 和模型拆分权限、账单和排障。

## 13. 文档同步

新增或改变开发者体验时，必须同步检查：

- `docs/API.md`：接口、请求、响应、错误和鉴权。
- `docs/PROTOCOLS.md`：入口协议、APIType、上游厂商、能力等级、字段降级和 SDK 兼容矩阵。
- `docs/API_KEYS.md`：API Key 使用、轮换、泄露处理、作用域、最近使用和高级管理。
- `docs/POLICIES.md`：策略决策、访问控制、限流、预算、分组和路由偏好。
- `docs/RELAY.md`：协议转换、`routerx`、路由、重试和 usage。
- `docs/ERRORS.md`：错误 code、HTTP、重试和扣费语义。
- `docs/OBSERVABILITY.md`：request_id、日志字段、指标和告警。
- `docs/SNAPSHOTS.md`：调用事实快照、脱敏、历史解释和诊断字段边界。
- `docs/BILLING.md`：额度、扣费和账单事实。
- `docs/CONSOLE.md`：开发者可见状态、动作和排障入口。
- `docs/RUNBOOKS.md`：接入失败、Key 泄露和账单异常处理步骤。
- `docs/TESTING.md`：SDK 兼容、错误格式、日志和扣费测试。
- `docs/TRACEABILITY.md`：新增能力 ID 或验收证据。

开发者体验的核心不是把所有上游能力一次性做完，而是让每个已开放能力都能被稳定调用、准确计费、清楚排障、安全扩展。
