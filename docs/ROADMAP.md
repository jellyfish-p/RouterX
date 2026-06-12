# RouterX 实施路线图

## 阶段判断

RouterX 当前已经不再只是后端骨架。代码已经具备 P0 平台基础能力，但模型转发、计费和生产运维仍需要继续补齐，才能达到完整商业级自部署体验。

当前已具备：

- Gin 路由、分层 handler/service/model/relay 结构。
- DB 初始化、嵌入式 SQL 迁移、SQLite/PostgreSQL/MySQL 方言支持。
- Redis 初始化；SQLite 单机模式可无 Redis，外部数据库或集群模式必须 Redis 可用。
- 首次初始化超级管理员和默认 `settings`。
- 用户注册、统一登录、User JWT 鉴权、管理员角色边界。
- 用户、管理员、API Key、通道、日志、设置等基础接口。
- API Key 创建、列表、更新、删除、校验和 SHA256 哈希存储。
- 通道 CRUD、启用/禁用、连通性测试、模型拉取、密钥加密。
- 通道多 base URL、多 API Key、key 选择模式、模型重写、通道分组和扩展配置。
- `/v1` OpenAI/Gemini/Anthropic 相关入口路由注册。
- OpenAI-compatible 模型列表和非流式转发基础闭环。
- 调用日志、用户账单统计和管理看板基础接口。
- `/health` 和 `/ready`。

仍需要补齐：

- 流式响应、重试、熔断、限流的生产级闭环。
- 多协议入口和多上游转换矩阵的完整语义映射，能力等级以 `docs/PROTOCOLS.md` 为准。
- 模型价格表、通道价格覆盖、分组倍率、访问控制和计费快照，策略契约见 `docs/POLICIES.md`。
- 充值码、在线支付插件、支付回调幂等、额度流水和支付审计，契约以 `docs/PAYMENTS.md` 为准。
- 结构化日志、Prometheus 指标、管理审计日志和告警。
- OAuth/OIDC、验证码、账号注销/恢复、多会话等账号增强。

## 阶段总览

阶段推进遵循三层取舍：

- P0 先稳：不追求功能最多，追求从初始化到第一次调用的最短可靠闭环。
- P1 再强：打开多协议、多上游、流式、计费策略和可靠性能力，让技术用户可深度控制。
- P2 后运营：补企业账号、支付插件、审计、观测和长期生产能力，不阻塞基础自部署。

| 阶段 | 目标 | 状态 |
|------|------|------|
| P0-1 | 初始化、账号、权限和 API Key 基础可用 | 基本完成，继续补安全和体验细节 |
| P0-2 | 通道管理和 OpenAI-compatible 非流式最小闭环 | 基本完成，继续补错误和计费边界 |
| P0-3 | 日志、额度和基础账单一致 | 部分完成，需补价格规则和事务快照 |
| P1-1 | 流式响应、重试、熔断和限流 | 待完善 |
| P1-2 | 多协议入口和多上游转换矩阵 | 待完善，协议矩阵见 `docs/PROTOCOLS.md` |
| P1-3 | 计费策略、访问控制、充值码和可选支付插件 | 待完善，策略契约见 `docs/POLICIES.md`，支付契约见 `docs/PAYMENTS.md` |
| P2-1 | OAuth/OIDC 和企业账号体系 | 待规划实现 |
| P2-2 | 生产观测、安全和审计增强 | 待完善 |
| P3 | 高级 API、多区域和企业扩展能力 | 长期候选 |

## P0：自部署可用闭环

目标：从空数据库到第一次成功模型调用形成可验证闭环。

P0 的产品取舍是“少选择、强默认、可验证”。小白用户不需要理解全部路由、协议、计费和运维细节，也应该能完成一次安全调用；技术用户可以看到后续扩展入口，但这些入口不能阻塞首次可用。

当前 P0 已完成或已有基础：

- `POST /v0/setup/init` 创建超级管理员和默认 `settings`。
- `/v0/user/login` 统一签发 User JWT。
- `/v0/admin/*` 基于 User JWT 校验管理员权限。
- `/v0/user/token` 支持 API Key 生命周期。
- `/v0/admin/channel` 支持通道生命周期、密钥加密和扩展路由配置。
- `/v1/models` 可基于有效 API Key 返回模型列表。
- `/v1/chat/completions` 已有非流式转发骨架和基础计费日志链路。

P0 剩余重点：

- 完善 OpenAI-compatible Chat 非流式错误格式、请求限制和重试策略。
- 增加初始化启动额度或明确额度调整引导，避免第一次验证调用被 0 额度阻断。
- 统一日志、usage、扣费和基础账单统计，保证 P0 调用事实可追溯。
- 明确所有 P0 路由的权限、状态码、错误 code 和 SDK 兼容错误格式。
- 增加围绕余额不足、禁用用户、禁用 API Key、禁用通道、无可用通道的测试。

P0 验收标准：

- 空库启动后可以初始化管理员。
- 初始化后的管理员可以完成第一次验证调用，或系统明确提示先完成额度调整。
- 管理员可以创建通道，用户可以创建 API Key。
- 有效 API Key 可以调用 `/v1/models` 和基础非流式 Chat。
- 第一次调用后，用户能看到自己的日志和额度变化，管理员能看到全局调用记录。
- 禁用用户、Token 或通道后立即影响调用。
- 成功调用写入日志并扣减额度。
- 余额不足时不调用下游。
- API Key 和下游密钥不出现在响应或日志中。
- 支付、OAuth/OIDC、多协议完整矩阵和高级计费表达式未配置时，不影响 P0 开箱路径。

### P0 实现任务卡

P0 的实现目标是把已有基础能力收口成稳定闭环。下面任务卡按建议顺序执行。

| 任务 | 代码区域 | 输入/触发 | 必须落库或返回 | 验收用例建议 |
|------|----------|-----------|----------------|--------------|
| P0-T1 初始化启动额度 | `SetupService`、`settings`、`users.quota` | 首次 `POST /v0/setup/init` | 超级管理员可获得 `billing.bootstrap_admin_quota` 或明确为 0 并提示调整额度 | `TestSetupBootstrapAdminQuota` |
| P0-T1b settings 注册表与就绪 | `SettingService`、setup、ready、settings cache | 初始化、修改配置、生产 readiness | current 阶段 key 存在且默认值正确；类型非法不静默降级；关键配置缺失时生产 `/ready` 不就绪 | `TestSettingsRegistryAndReadiness` |
| P0-T2 Chat 非流式成功闭环 | `RelayHandler`、`RelayService`、OpenAI adapter、`TokenService`、`LogService` | 有效 API Key 调用 `/v1/chat/completions` | 返回 OpenAI-compatible 响应；写成功日志；扣用户余额并更新 Key 预算；通道 `error_count=0` | `TestChatCompletionSuccessLogsAndDeductsQuota` |
| P0-T3 Chat 请求错误 | `RelayService.parseRelayRequest`、错误映射 | 缺少 model、非法 JSON、`stream=true` | 返回 OpenAI-compatible 400 错误；不选择通道；不扣费 | `TestChatCompletionInvalidRequestDoesNotCallUpstream` |
| P0-T4 下游错误映射 | Adapter、Relay 错误映射、通道错误计数 | 下游返回 400/401/403/429/5xx 或超时 | 400 不重试；401/403 标记配置问题；5xx/超时可按配置重试；失败日志脱敏 | `TestChatCompletionUpstreamErrorMapping` |
| P0-T5 预检拒绝不调用下游 | API Key auth、用户余额、Key 预算、ChannelService | 禁用用户、禁用 Key、余额不足、Key 预算不足、禁用通道、无模型匹配 | 返回 401/403/429/502 兼容错误；下游请求计数为 0；失败日志可排障 | `TestRelayPrecheckRejectsBeforeUpstream` |
| P0-T6 通道高级字段事实校验 | `ChannelService.ResolveUpstream`、`ApplyModelRewrite` | 多 key、多 base URL、`upstreams`、模型重写 | `upstreams` 优先；key 选择策略符合配置；上游模型名被重写；密钥不泄露 | `TestChannelRoutingConfigResolution` |
| P0-T7 基础账单一致性 | `LogService`、`TokenService.DeductQuota`、用户账单接口 | 多次成功调用和失败调用 | `SUM(logs.quota_used)` 与用户账单聚合一致；失败调用不误扣或按配置最低失败成本记录 | `TestUserBillingMatchesLogs` |

P0 测试建议使用本地 `httptest` 下游服务模拟 OpenAI-compatible 响应，避免依赖真实厂商。测试必须同时断言接口响应、数据库日志、用户余额、Key 预算和敏感信息不泄露。

P0 代码落点清单：

| 落点 | 收口目标 | 完成证据 |
|------|----------|----------|
| `SetupService` | 初始化默认 settings、启动额度、重复初始化保护 | 空库初始化测试通过，管理员能获得首次验证所需额度或明确提示 |
| setup/ready 路由和中间件 | 未初始化拦截、协议兼容错误、生产就绪状态 | `/v0/setup/status`、`/ready` 和未初始化 `/v1` 请求都有断言 |
| `RelayHandler` / `RelayService` | 非流式 Chat 主链路、请求校验、错误映射、usage 提取 | 本地上游桩成功、400、401/403、429、5xx、超时路径都有测试 |
| Provider adapter | OpenAI-compatible 请求/响应转换和错误透传边界 | 下游收到正确模型名、Authorization 和 body；响应不泄露内部错误 |
| `ChannelService` | 优先级、权重、多 key、多 base URL、`upstreams` 和模型重写 | 通道选择与上游解析测试稳定，不依赖人工观察随机结果 |
| `TokenService` / `LogService` | 额度预检、条件扣费、日志账单一致性 | 成功调用扣费，失败调用不误扣，日志聚合与账单接口一致 |
| 测试夹具 | 真实路由 + 隔离 DB + 本地下游桩 | `docs/TESTING.md` 中 P0 用例能按同一夹具落地 |

## P1：核心商业能力

目标：从最小闭环升级为可运营的多协议、多上游中转系统。

P1 的产品取舍是“进阶可控”。系统开始暴露多协议、多上游、计费、限流、熔断和访问控制，但每个高级能力必须有默认值、解释路径和失败模式。协议兼容和能力等级以 `docs/PROTOCOLS.md` 为准；访问控制、限流、分组和路由偏好的统一语义以 `docs/POLICIES.md` 为准。

实现内容：

- SSE 流式转发、客户端断开取消、流式 usage 汇总或估算。
- 通道失败重试、错误计数、自动熔断、手动恢复和后续半开探测。
- Redis 限流，覆盖全局、IP、用户、Token、模型和通道维度。
- OpenAI、Anthropic、Gemini 三类入口协议的基础非流式和流式调用。
- OpenAI-compatible、Anthropic、Gemini、xAI、Qwen、DeepSeek、Azure OpenAI、RouterX-Compatible 等上游。
- `routerx` 扩展参数、provider-specific 参数、安全过滤和多层 RouterX hop 限制。
- 路由决策快照，记录候选过滤、`routerx.route` 偏好处理、最终通道、模型重写和重试结果。
- 多入口协议错误映射，保证 OpenAI、Anthropic、Gemini 客户端分别收到对应兼容错误。
- `model_prices`、`channel_model_prices`、通道分组、用户分组倍率、访问控制和计费规则版本。
- 计费事实链快照，记录 usage 来源、表达式、倍率、访问控制、扣费事务和账单聚合依据。
- 充值码兑换和可选支付插件接口，按 `docs/PAYMENTS.md` 保证订单、事件、额度流水和审计。

P1 验收标准：

- 主流 SDK 可以通过 RouterX 完成基础非流式和流式调用。
- 不支持的协议字段明确返回错误或记录降级原因，不静默误转。
- 通道选择、重试、熔断和限流行为可解释。
- 用户请求中的路由偏好不能绕过管理员策略，接受、忽略或拒绝原因可审计。
- 成功调用的日志、usage、扣费、计费快照和账单聚合一致。
- 计费规则变更不会改变历史账单解释。

## P2：企业与生产增强

目标：满足长期运行、企业接入、安全审计和生产观测。

P2 的产品取舍是“运营增强可选”。企业身份、支付插件、管理审计和高级观测应该能支撑商业运营，但不能反向污染 P0 的开箱路径。

实现内容：

- OAuth Provider 配置、OAuth state、身份绑定和账号恢复。
- OIDC Discovery、Authorization Code Flow、ID Token 校验和企业 SSO。
- 注册验证码、登录验证码、账号注销保留身份、恢复账号和登录审计。
- 结构化日志、Request ID、Prometheus `/metrics`、更严格 `/ready`。
- 管理审计日志，覆盖设置、通道、用户、价格、支付等关键操作。
- API Key 哈希迁移兜底、下游密钥轮换、KMS 扩展。
- 支付回调幂等、退款记录、人工修正和支付审计。

P2 验收标准：

- 企业身份接入不会因邮箱自动匹配造成账号接管。
- 管理员关键操作可审计、可追踪。
- 生产必要配置缺失时 readiness 准确反映不可用状态。
- 指标能展示请求量、错误率、耗时、通道状态、DB/Redis 错误。
- 支付重复通知不会重复入账。

## P3：高级候选能力

候选方向：

- OpenAI Files、Fine-tuning、Assistants、Realtime 等高级 API。
- 多区域通道路由和成本优先路由。
- 模型别名、灰度路由和 A/B 路由。
- 成本分析、利润报表和告警通知。
- 企业多组织、多项目、多 API Key 分组。
- Webhook、事件订阅和外部审计系统集成。

## 实现工作包与验收证据

实现时按工作包推进。每个工作包必须能用接口、日志、数据库状态或测试证明完成，不能只依赖人工观感。阶段能否宣称完成以 `docs/ACCEPTANCE.md` 的证据等级和验收门禁为准。

具体文件落点、P0 顺序、验收和禁止事项以 `docs/IMPLEMENTATION.md` 为准；本节只保留阶段和工作包摘要。

| 工作包 | 阶段 | 主要模块 | 必须证明的结果 | 当前测试线索 |
|--------|------|----------|----------------|--------------|
| WP0-1 初始化、settings 与就绪 | P0 | setup、settings、ready、JWT | 空库可初始化；重复初始化被拒绝；初始化后 JWT 可用；settings 默认值和类型可验证；`/ready` 能反映 DB、JWT 和关键配置状态 | `TestP0BackendFlow` 覆盖初始化主链路，需补 `TestSettingsRegistryAndReadiness` |
| WP0-2 账号与权限 | P0 | auth、user、admin middleware | 用户登录签发 JWT；管理员/超级管理员边界正确；普通用户不能越权 | `TestAdminPrivilegeBoundaries` 覆盖管理员边界 |
| WP0-3 API Key 生命周期 | P0 | token、API Key auth、Redis 缓存 | Key 明文只返回一次；DB 保存哈希；禁用、过期、删除、用户禁用立即影响 `/v1` | `TestP0BackendFlow` 覆盖创建、禁用和额度边界 |
| WP0-4 通道基础管理 | P0 | channel、adapter registry、secret encryption | 管理员可创建/测试/启停通道；下游密钥加密；响应和日志不泄露密钥 | `TestP0BackendFlow`、`TestChannelExtendedManagement` |
| WP0-5 OpenAI-compatible 非流式闭环 | P0 | relay handler/service、OpenAI adapter、logs、quota | `/v1/models` 和 Chat 非流式可用；无可用通道、余额不足、无效 Key 返回 SDK 兼容错误；成功调用写日志并扣费 | 已有 Chat 成功、下游 400、预检拒绝测试；仍需补本地请求错误和更多下游状态 |
| WP0-6 基础日志与账单 | P0 | logs、billing stats、dashboard | 用户只看自己的日志；管理员可筛选全局日志；账单聚合来自日志事实 | 已有单次成功账单聚合；仍需补多次成功/失败混合一致性 |
| WP1-1 流式和取消 | P1 | relay stream、adapter stream、context cancel | SSE 不缓存完整响应；客户端断开取消下游；流式 usage 可结算或估算 | 新增流式集成测试 |
| WP1-2 路由决策快照 | P1 | channel selection、route policy、logs、POLICIES | 记录候选过滤、`routerx.route` 处理、最终通道、模型重写和重试结果 | 新增路由策略单元/集成测试 |
| WP1-3 多协议与多上游 | P1 | translator、adapter、error mapper、PROTOCOLS | OpenAI/Anthropic/Gemini 入口与主要上游组合可用；不支持字段明确失败；能力等级与 `docs/PROTOCOLS.md` 一致 | 新增协议矩阵测试 |
| WP1-4 计费规则和访问控制 | P1 | model_prices、channel_model_prices、settings、logs、POLICIES | 价格表达式、倍率、访问控制、计费快照和账单聚合一致 | 新增计费事实链测试 |
| WP1-5 可靠性 | P1 | retry、circuit breaker、rate limit、Redis | 非流式可安全重试；流式不跨通道重试；熔断、恢复和限流可解释 | 新增故障注入测试 |
| WP1-6 通道候选缓存 | P1 | ChannelService、Redis、settings、POLICIES | 按模型、APIType、用户分组和通道分组预加载候选集；管理员修改后集群失效一致 | 新增缓存失效和版本回源测试 |
| WP1-7 独立日志数据库 | P1 | LogService、OBSERVABILITY、BILLING、DATA_MODEL | `LOG_SQL_DSN` 可写独立日志库，主库保留结算最小事实或 outbox | 新增日志库降级和账单恢复测试 |
| WP2-1 企业账号 | P2 | OAuth/OIDC、captcha、session、audit | OAuth/OIDC 不因 email 自动接管；注销恢复保留历史事实；登录审计可查 | 新增账号风险测试 |
| WP2-2 生产观测和审计 | P2 | structured logs、metrics、audit、ready | `/metrics`、Request ID、审计日志和生产 `/ready` 完整可用 | 新增 readiness/metrics 测试 |
| WP2-3 支付和充值 | P2 | payment products/orders/events、quota_transactions、webhook | 支付回调签名、金额、状态和幂等入账正确；重复通知不重复加额度；退款和人工修正有流水和审计 | 新增支付幂等、额度流水和人工修正测试 |

验收证据优先级：

1. 自动化测试和接口响应。
2. 数据库记录、日志记录和计费快照。
3. 运行时指标和就绪检查。
4. 手工验证记录。

测试缺口优先级：

详细测试场景、前置数据、请求和断言以 `docs/TESTING.md` 为准；本节只记录优先级。

| 优先级 | 测试主题 | 当前状态 | 覆盖原因 |
|--------|----------|----------|----------|
| 1 | Chat 非流式成功调用写日志并扣费 | 已覆盖：`TestChatCompletionSuccessLogsAndDeductsQuota` | 证明 P0 不只是 `/v1/models` 可用，而是真正完成一次模型调用闭环。 |
| 2 | Chat 非流式下游 400/401/403/5xx/超时错误映射 | 部分覆盖：已有下游 400；仍需补 401/403/429/5xx/超时 | 证明 SDK 兼容错误、重试边界和通道故障归因正确。 |
| 3 | 余额不足、禁用用户、禁用 API Key、禁用通道不调用下游 | 已覆盖：`TestRelayPrecheckRejectsBeforeUpstream` | 证明安全和计费预检发生在下游调用前。 |
| 4 | settings 注册表、类型校验、缓存刷新和生产 readiness | 部分覆盖：默认值、类型校验和 readiness 已覆盖；仍需缓存刷新边界 | 证明配置不是散落字符串，关键配置错误能阻止生产实例接流量。 |
| 5 | 多 key、多 base URL、`upstreams` 优先级和模型重写 | 待补真实 Relay 请求效果 | 证明通道高级配置不会产生不可解释的随机行为。 |
| 6 | 日志、usage、扣费和用户账单聚合一致 | 部分覆盖：已有单次成功账单；仍需多次成功/失败混合 | 证明账单不是从多个互相矛盾的事实源拼出来。 |
| 7 | `routerx.route` 合法、忽略、拒绝和无候选路径 | 待补 | 证明用户偏好不能绕过管理员策略。 |
| 8 | SSE 流式、客户端断开和流式 usage 结算 | P1 待补 | 进入 P1 前补齐最容易出现资源泄漏和账单偏差的路径。 |
| 9 | Anthropic/Gemini 入口错误格式和字段降级 | 部分覆盖：API Key 错误外形已覆盖；仍需成功和字段降级矩阵 | 证明多入口协议不是只注册路由，而是 SDK 可用。 |

## 推荐顺序

1. 收口 P0 非流式 Chat、日志、扣费和错误兼容。
2. 完成流式响应、限流、重试和熔断。
3. 扩展 OpenAI、Anthropic、Gemini 入口协议和主要上游。
4. 建立价格表、倍率、访问控制和账单快照。
5. 增加充值码、支付插件、OAuth/OIDC、审计和观测。

## 文档维护要求

- 已实现能力不得继续写成空泛描述。
- 后续能力必须标注阶段、边界和验收标准。
- 产品定位和阶段边界以 `docs/DESIGN.md` 为准。
- API、数据、Relay、计费、运维和账号专题文档必须与本路线图保持一致。
