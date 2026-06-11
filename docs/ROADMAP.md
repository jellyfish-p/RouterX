# RouterX 实施路线图

## 当前阶段判断

当前项目已经具备后端骨架、路由分组、模型、迁移、适配器接口和基础文档，但尚未形成可用的模型中转闭环。

当前已完成：

- Gin 入口和路由结构。
- DB 初始化和嵌入式 SQL 迁移。
- PostgreSQL/MySQL/SQLite 方言支持。
- Redis 初始化。
- 用户、身份、Token、通道、日志、设置等模型。
- 模型转发 `/v1` 部分路由注册。
- Adapter 接口和厂商适配器注册文件。

当前未完成：

- 首次初始化创建管理员。
- User JWT/API Key 真实鉴权。
- API Key 管理接口。
- 通道 CRUD 和连通性测试。
- RelayService 转发闭环。
- Adapter 请求/响应转换。
- 计费扣费和日志落库。
- Settings 缓存和动态读取。
- 限流、熔断、监控和安全加固。

## 阶段总览

| 阶段 | 目标 | 优先级 |
|------|------|--------|
| P0-1 | 初始化和基础账号可用 | 必须 |
| P0-2 | API Key 和通道管理可用 | 必须 |
| P0-3 | OpenAI 入口 Chat 非流式转发闭环和 RouterX 扩展参数 | 必须 |
| P0-4 | 计费、日志和基础看板 | 必须 |
| P1-1 | 流式响应和重试熔断 | 重要 |
| P1-2 | 多厂商适配扩展 | 重要 |
| P1-3 | 用户控制台、充值码和在线支付 | 重要 |
| P2-1 | OAuth/OIDC 和企业账号 | 增强 |
| P2-2 | 观测性、审计和生产安全 | 增强 |
| P3 | 高级 OpenAI API 和多区域能力 | 长期 |

## P0-1 初始化和基础账号

目标：系统可以从空数据库完成首次初始化，管理员可以通过统一登录进入管理端。

实现内容：

- `SetupHandler.Init`。
- `SetupService.Init` 事务。
- 创建超级管理员 `users`。
- 创建本地 `user_identities(username, local)`。
- 写入默认 settings。
- 优先使用 `JWT_SECRET` 写入 `jwt.secret`；未配置时初始化生成一次并写入数据库。
- `AuthService.UserLogin`。
- User JWT 签发和校验。
- `AdminAuthRequired` 基于 User JWT 校验管理员角色。

验收标准：

- 空库启动后 `GET /v0/setup/status` 返回 `initialized=false`。
- `POST /v0/setup/init` 成功创建超级管理员。
- 初始化后再次调用 init 返回冲突错误。
- 管理员可以通过 `/v0/user/login` 获取 JWT。
- 未登录访问 `/v0/admin/user` 返回 401。
- 已登录管理员访问 `/v0/admin/user` 不被鉴权层拦截。

风险：

- 并发初始化创建多个超级管理员。
- JWT secret 为空或重复生成导致会话失效。

## P0-2 API Key 和通道管理

目标：管理员可以维护下游通道，用户可以创建 API Key。

实现内容：

- `ChannelService` CRUD。
- `ChannelHandler` CRUD。
- 通道连通性测试。
- `TokenService` 创建、列表、更新、删除、校验。
- 用户端 Token 路由注册。
- `ApiKeyAuthRequired`。
- Redis Token 缓存。
- 用户禁用和 Token 禁用联动失效。

验收标准：

- 管理员可以创建 OpenAI-Compatible 通道。
- 通道列表不会返回完整 API Key。
- 用户可以创建 `sk-` API Key。
- API Key 明文只在创建响应中出现。
- 无效、禁用、过期 API Key 访问 `/v1/models` 返回 401。
- 有效 API Key 通过鉴权并写入 `current_user`、`current_token`。

风险：

- API Key 明文存储和日志泄露。
- Redis 缓存未失效导致禁用后仍可用。

## P0-3 Chat 非流式转发闭环

目标：可以用 OpenAI SDK 通过 RouterX 调用一个 OpenAI-Compatible 下游通道，并保留 `routerx` 扩展参数格式，为后续多入口协议和多层 RouterX 转发打基础。

实现内容：

- `RelayHandler.ChatCompletions`。
- `RelayService` 非流式处理。
- `OpenAIAdapter` 或 `OpenAICompatAdapter`。
- `routerx` 扩展参数解析和真实上游剥离。
- `X-RouterX-Hop`、`X-Request-Id` 基础传递。
- `ChannelService.SelectChannel`。
- 模型匹配 `models`。
- 下游 HTTP 客户端。
- 当前格式兼容的错误返回。
- 请求超时配置。

验收标准：

- `POST /v1/chat/completions` 可以成功转发到 OpenAI-Compatible 下游。
- 请求中的未知兼容格式字段不被无故丢弃。
- `routerx.route` 和 `routerx.provider.openai` 可以被解析，不会直接发送给真实 OpenAI 上游。
- RouterX-Compatible 上游可以保留 `routerx` 扩展字段并递增 `X-RouterX-Hop`。
- 下游 401/403 不重试并返回明确错误。
- 下游 5xx 可按配置重试其他通道。
- 无可用通道返回 502。
- 可用 OpenAI SDK 调通基础 chat completion。

风险：

- 请求体读取后无法再次转发。
- 下游响应过大导致内存问题。
- 错误响应不兼容 OpenAI SDK。
- 扩展参数未剥离导致真实厂商 400。
- 多层 RouterX 未限制 hop 导致循环转发。

## P0-4 计费、日志和基础看板

目标：每次成功调用都能统计 usage、扣减额度并写入日志。

实现内容：

- `LogService.Record`。
- `TokenService.DeductQuota`。
- 用户额度和 Token 额度扣减事务。
- usage 解析。
- 计费倍率和分组倍率。
- `/v0/admin/log` 查询。
- `/v0/user/log` 查询。
- `/v0/admin/dashboard` 基础统计。
- `/v0/user/billing` 基础统计。

验收标准：

- 成功调用后 `logs` 出现记录。
- `quota_used` 与 usage 和倍率一致。
- Token 额度有限时优先扣 Token。
- Token 无限时扣用户额度。
- 余额不足时不调用下游并返回 429。
- 用户只能查看自己的日志。
- 管理员可以按用户、Token、通道、时间筛选日志。

风险：

- 并发扣费透支。
- usage 为空导致免费调用。
- 日志保存敏感请求体。

## P1-1 流式响应和可靠性

目标：支持 `stream=true`，并具备基础重试、熔断、限流。

实现内容：

- SSE 透明转发。
- chunk 转换接口。
- 客户端断开取消下游请求。
- 流式 usage 汇总或估算。
- `RateLimit` 中间件。
- 通道错误计数。
- 自动熔断和恢复策略。

验收标准：

- OpenAI SDK stream 模式可用。
- 客户端断开后下游请求被取消。
- 已输出 chunk 后不跨通道重试。
- 限流超过阈值返回 429。
- 连续失败通道不再被选择。

风险：

- 流式响应不能准确计费。
- 客户端断开导致 goroutine 泄露。
- 错误重试导致重复扣费。

## P1-2 多厂商适配

目标：接入 OpenAI、Anthropic、Gemini 全格式入口协议，并支持 OpenAI、Anthropic、Gemini、xAI 等主要上游 provider。

实现内容：

- OpenAI Responses、Chat、Completions、Embeddings、Images、Audio、Models、Moderations 入口格式。
- Anthropic Messages、Messages Stream、Count Tokens、Models 入口格式。
- Gemini generateContent、streamGenerateContent、countTokens、embedContent、batchEmbedContents、Models 入口格式。
- OpenAI、Azure OpenAI、Anthropic、Gemini、xAI、Qwen、DeepSeek、OpenAI-Compatible、RouterX-Compatible 上游适配。
- OpenAI / Anthropic / Gemini 之间的请求和响应双向转换。
- provider-specific 额外参数透传和安全过滤。
- 厂商模型列表。
- 厂商特定错误映射。

验收标准：

- OpenAI、Anthropic、Gemini 三类 SDK 的基础非流式和流式调用可用。
- OpenAI、Anthropic、Gemini 任一入口协议转主要上游时语义保持一致或明确返回不支持错误。
- xAI 上游可以通过 OpenAI 入口协议调用，并支持 `routerx.provider.xai` 额外参数。
- 多层 RouterX 转发不会丢失 `routerx` 扩展参数，且不会形成路由循环。
- 厂商错误不会泄露密钥。
- 模型列表可以聚合展示。

风险：

- 不同厂商 tool calling、vision、system message 语义差异。
- token usage 字段差异导致计费偏差。

## P1-3 用户控制台、充值码和在线支付

目标：普通用户可自助管理 API Key、查看用量、兑换充值码，并通过 Stripe 或易支付在线购买额度。

实现内容：

- User JWT 鉴权。
- 用户注册和登录。
- 用户个人信息修改。
- API Key 管理页面。
- 充值码生成和兑换。
- 充值商品和支付订单。
- Stripe Checkout Session 和 Webhook 入账。
- 易支付跳转支付、异步通知和同步返回页。
- 用户账单统计。
- 模型价格展示。

验收标准：

- 用户可以注册登录。
- 用户只能管理自己的 API Key。
- 充值码只能使用一次。
- 兑换和增加额度在事务中完成。
- 支付成功通知幂等处理，同一个 provider 事件只入账一次。
- Stripe 和易支付通知必须完成签名、金额、货币、订单状态校验后才能入账。
- 用户账单与日志聚合一致。

风险：

- 充值码并发重复兑换。
- 支付回调重复通知导致重复入账。
- 易支付同步返回页被伪造导致前端误判支付成功。
- 普通用户越权查看他人日志。

## P2-1 OAuth/OIDC

目标：支持企业和第三方账号登录。

实现内容：

- OAuth Provider 配置。
- OAuth state Redis 存储。
- OAuth callback 登录和绑定。
- OIDC Discovery。
- OIDC ID Token 校验。
- 第三方身份绑定和解绑。
- 登录审计。

验收标准：

- 每个 provider 可独立启用关闭。
- state 和 nonce 校验有效。
- 已绑定身份可登录。
- 未绑定身份按配置自动注册或要求绑定。
- 禁用 provider 后不能继续登录。

风险：

- 错误使用 email 自动绑定导致账号接管。
- OIDC token 校验不完整导致伪造登录。

## P2-2 生产运维增强

目标：满足生产部署的可观测和安全要求。

实现内容：

- 结构化日志。
- Request ID。
- Prometheus metrics。
- `/ready` 就绪检查。
- 管理审计日志。
- 通道 API Key 加密存储。
- 加密主密钥来自 `ENCRYPTION_KEY` 或 KMS，不能依赖实例本地随机值。
- API Key 哈希存储迁移。
- 日志脱敏和保留策略。
- 数据库备份和迁移发布流程。

验收标准：

- 可通过 metrics 查看请求量、错误率、耗时、通道状态。
- 管理员关键操作有审计记录。
- 数据库中不再保存 API Key 明文。
- 下游 API Key 不出现在任何日志和响应中。
- readiness 能反映 DB/Redis/配置状态。

风险：

- 加密主密钥丢失导致下游密钥不可恢复。
- 迁移 API Key 哈希后无法兼容旧明文校验。

## P3 高级能力

候选能力：

- Files API。
- Fine-tuning API。
- Moderations API。
- Assistants API。
- Realtime API。
- 多区域通道路由。
- 模型别名和灰度路由。
- 成本分析和利润报表。
- 企业多组织、多项目、多 API Key 分组。
- Webhook 和告警通知。

## 推荐开发顺序

1. 完成 P0-1 初始化和 Admin 登录。
2. 完成 P0-2 API Key 校验和通道管理。
3. 先实现 OpenAI 入口非流式 Chat 和 `routerx` 扩展参数，跑通最小闭环。
4. 接入日志和扣费，避免形成免费网关。
5. 增加流式和重试，再扩展厂商。
6. 最后做 OAuth/OIDC、观测性、安全加固和高级 API。

## 总体验收标准

达到 MVP 可用状态需要满足：

- 空数据库可以初始化。
- 管理员可以登录。
- 管理员可以创建通道。
- 用户可以创建 API Key。
- OpenAI SDK 可以通过 `/v1/chat/completions` 非流式调用成功。
- 调用后写日志并扣额度。
- 余额不足时拒绝调用。
- 禁用用户、Token、通道后立即生效。
- 下游密钥不在响应和日志中泄露。
