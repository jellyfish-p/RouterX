# RouterX 能力到验收追踪矩阵

本文档用于把 RouterX 的商业级设计拆成可实现、可验收、可回归的能力清单。它不是新的产品设计来源，而是把 `docs/DESIGN.md`、`docs/FLOWS.md`、`docs/API.md`、`docs/DATA_MODEL.md`、`docs/RELAY.md`、`docs/PROTOCOLS.md`、`docs/MODULE_BOUNDARIES.md`、`docs/SNAPSHOTS.md`、`docs/BILLING.md`、`docs/SETTINGS.md`、`docs/ACCEPTANCE.md` 和 `docs/TESTING.md` 连接起来。

实现者读完一项能力后，应能回答四个问题：

- 这项能力服务哪类用户路径。
- 哪些文档定义了边界。
- 应落到哪些后端模块、表或接口。
- 用什么测试或证据证明完成。

## 使用规则

- 产品边界以 `docs/DESIGN.md` 为准。
- 关键技术取舍和确认窗口以 `docs/DECISIONS.md` 为准。
- 术语边界以 `docs/GLOSSARY.md` 为准。
- 用户任务路径以 `docs/FLOWS.md` 为准。
- 控制台和等价接口能力以 `docs/CONSOLE.md` 为准。
- 调用方接入和开发者体验以 `docs/DEVELOPER_EXPERIENCE.md` 为准。
- 支付、充值码、退款、人工补账和额度流水以 `docs/PAYMENTS.md` 为准。
- 安全威胁和控制点以 `docs/SECURITY.md` 为准。
- 错误 code、重试、扣费和日志语义以 `docs/ERRORS.md` 为准。
- 日志、审计、指标、告警和保留以 `docs/OBSERVABILITY.md` 为准。
- 调用事实快照、字段封套、脱敏和历史解释以 `docs/SNAPSHOTS.md` 为准。
- 故障处理路径和安全动作以 `docs/RUNBOOKS.md` 为准。
- 具体接口以 `docs/API.md` 为准。
- 表、字段、迁移和密钥存储以 `docs/DATA_MODEL.md` 为准。
- Relay 行为以 `docs/RELAY.md` 为准。
- 模块责任、依赖方向和禁止跨层行为以 `docs/MODULE_BOUNDARIES.md` 为准。
- 额度、价格和账单以 `docs/BILLING.md` 为准。
- settings key、默认值和 readiness 以 `docs/SETTINGS.md` 为准。
- 阶段能否宣称完成以 `docs/ACCEPTANCE.md` 的证据等级和验收门禁为准。
- 测试名称、fixture 和断言以 `docs/TESTING.md` 为准。

## P0 开箱闭环

P0 的目标是让自部署实例从空数据库进入第一次可验证调用，并且失败时能明确定位问题。

| ID | 能力 | 用户价值 | 主要文档 | 落地位置 | 验收证据 |
|----|------|----------|----------|----------|----------|
| P0-C1 | 首次初始化 | 小白用户能创建超级管理员并进入系统。 | `DESIGN`、`FLOWS`、`API`、`SETTINGS` | `SetupHandler`、`SetupService`、`users`、`user_identities`、`settings` | `TestSetupBootstrapAdminQuota`、`TestP0BackendFlow` |
| P0-C2 | 默认 settings 写入 | 空库实例有可预测默认配置。 | `SETTINGS`、`OPERATIONS`、`DATA_MODEL` | `SettingService`、`SetupService.buildDefaultSettings`、Redis settings cache | `TestSetupBootstrapAdminQuotaAndSettingsDefaults`、`TestSettingsValidationAndReadiness`；`relay.max_request_body_bytes` 和 `relay.max_response_body_bytes` 默认值已覆盖 |
| P0-C3 | 生产 readiness | 生产缺关键配置时不会被误认为可接流量。 | `SETTINGS`、`OPERATIONS`、`ARCHITECTURE`、`RUNBOOKS` | `/ready`、配置注册表、密钥检查、Redis 运行模式 | `TestSettingsValidationAndReadiness`、`TestInitRedisSkipsEmptyConfig`、`TestReadinessRequiresRedisForExternalDatabaseMode`、`TestReadinessRequiresEncryptionKeyForEncryptedChannelSecrets` |
| P0-C4 | 用户登录和权限 | 用户用 User JWT 管理自身资源，管理员管理系统资源。 | `ACCOUNTS`、`API`、`ARCHITECTURE` | auth middleware、user/admin routes、role checks | `TestAdminPrivilegeBoundaries` |
| P0-C5 | API Key 生命周期 | 用户能创建模型调用凭据，明文只出现一次，生命周期操作可审计。 | `API_KEYS`、`API`、`ACCOUNTS`、`DATA_MODEL`、`GLOSSARY` | `TokenService`、`tokens`、API Key auth cache、`admin_audit_logs` | `TestP0BackendFlow`、`TestUserAPIKeyManagementAuditLogs` |
| P0-C6 | API Key 预算语义 | 有限 API Key 是预算上限，调用时同时受用户余额和 Key 预算约束；无限 API Key 扣用户额度。 | `API_KEYS`、`BILLING`、`API`、`DATA_MODEL`、`DECISIONS` | `TokenService.CreateToken`、`TokenService.DeductQuota`、`users.quota`、`tokens.remain_quota`/`quota_limit` | `TestUserBillingMatchesLogs` |
| P0-C7 | 通道管理 | 管理员能添加可用上游配置，并且密钥脱敏。 | `API`、`DATA_MODEL`、`RELAY`、`FLOWS` | `ChannelService`、`channels`、通道 CRUD routes | `TestP0BackendFlow`、`TestChannelExtendedManagement` |
| P0-C8 | `/v1/models` | 调用方能用 API Key 验证模型入口可用。 | `API`、`RELAY`、`PROTOCOLS`、`TESTING` | `RelayHandler`、`RelayService`、Adapter model list | `TestP0BackendFlow` |
| P0-C9 | Chat 非流式成功链路 | 调用方能完成一次 OpenAI-compatible Chat 调用。 | `RELAY`、`API`、`PROTOCOLS`、`BILLING`、`TESTING` | `RelayService.Handle`、Adapter、`LogService`、`TokenService` | `TestChatCompletionSuccessLogsAndDeductsQuota` |
| P0-C10 | 预检拒绝 | 无效 Key、禁用用户、余额不足、禁用通道、请求体超限等在上游调用前失败。 | `SECURITY`、`RELAY`、`API`、`BILLING`、`RUNBOOKS` | API Key middleware、quota precheck、channel filter、Relay body limit | `TestRelayPrecheckRejectsBeforeUpstream`、`TestRelayMaxRequestBodyBytesRejectsBeforeUpstream` |
| P0-C11 | 错误映射 | `/v1` 错误保持入口协议兼容，用户和管理员能判断责任。 | `SECURITY`、`API`、`RELAY`、`OPERATIONS`、`RUNBOOKS` | error mapper、Relay failure logs、adapter errors | `TestChatCompletionInvalidRequestDoesNotCallUpstream`、`TestRelayMaxRequestBodyBytesRejectsBeforeUpstream`、`TestRelayMaxResponseBodyBytesRejectsOversizedUpstream`、`TestChatCompletionUpstreamErrorMapping` |
| P0-C12 | 日志和账单一致 | 成功调用写日志、扣额度，账单聚合与日志一致。 | `BILLING`、`DATA_MODEL`、`TESTING`、`RUNBOOKS` | `logs`、billing endpoint、quota transaction | `TestUserBillingMatchesLogs` |
| P0-C13 | 密钥安全默认 | 用户 API Key、上游密钥、数据库 DSN 不进入响应和日志。 | `SECURITY`、`ACCOUNTS`、`DATA_MODEL`、`OPERATIONS`、`RUNBOOKS` | encryption helper、response DTO、log sanitizer | `TestP0BackendFlow`、`TestChannelExtendedManagement` |
| P0-C14 | 通道高级字段事实 | 多 key、多 Base URL、`upstreams`、模型重写和通道分组行为可解释。 | `DATA_MODEL`、`RELAY`、`TESTING` | `ChannelService.ResolveUpstream`、`SelectChannel`、channel DTO | `TestChannelRoutingConfigResolution` |
| P0-C15 | 控制台能力闭环 | 小白、用户和管理员能看见状态、证据、错误处理入口和安全边界。 | `CONSOLE`、`FLOWS`、`API`、`OBSERVABILITY`、`RUNBOOKS` | `/v0/setup`、`/v0/user`、`/v0/admin`、dashboard、log routes | 控制台能力契约测试或端到端接口验收 |
| P0-C16 | 开发者最小接入 | 调用方能用 RouterX API Key、base URL 和非流式 Chat 完成迁移。 | `DEVELOPER_EXPERIENCE`、`API`、`RELAY`、`ERRORS`、`BILLING` | `/v1/models`、`/v1/chat/completions`、API Key auth、logs | SDK/HTTP 兼容验收、`TestP0BackendFlow`、`TestChatCompletionSuccessLogsAndDeductsQuota` |

## P1 商业核心增强

P1 的目标是让 RouterX 从“可用闭环”进入“可运营、可扩展、可解释”的商业默认体验。

| ID | 能力 | 用户价值 | 主要文档 | 落地位置 | 验收证据 |
|----|------|----------|----------|----------|----------|
| P1-C1 | SSE 流式转发 | 主流 SDK 的流式调用可用，并能结算 usage。 | `DEVELOPER_EXPERIENCE`、`PROTOCOLS`、`RELAY`、`API`、`TESTING` | stream handler、adapter chunk converter、stream log summary | `TestChatCompletionStreamForwardsSSEAndDeductsUsage`、`TestCompletionsStreamForwardsSSEAndDeductsUsage`、`TestChatCompletionStreamCancelsUpstreamWhenClientWriteFails`、`TestChatCompletionStreamRejectsNonOpenAISSEUpstream`、`TestAnthropicMessagesStreamConvertsOpenAISSEAndDeductsUsage`、`TestGeminiStreamGenerateContentConvertsOpenAISSEAndDeductsUsage`；仍需 Anthropic/Gemini 原生上游流式和更多 usage fallback 测试 |
| P1-C2 | 多入口协议 | OpenAI、Anthropic、Gemini 基础入口可并行服务。 | `DEVELOPER_EXPERIENCE`、`PROTOCOLS`、`API`、`RELAY`、`TESTING` | protocol detector、request/response translators | 基础非流式成功、Gemini embedContent/batchEmbedContents、Anthropic/Gemini 基础流式、鉴权错误和下游错误外形测试；完整协议矩阵和 SDK 行为测试仍需补齐 |
| P1-C3 | 多上游转换 | 同一入口协议可路由到主流上游厂商。 | `PROTOCOLS`、`RELAY`、`DATA_MODEL`、`GLOSSARY` | provider adapters、conversion matrix、adapter registry | Azure Chat/Completions/Embeddings deployment 路径、Azure Responses/Image Generations/Image Edits/Audio `/openai/v1` 路径、api-key 和 deployment 列表拉取测试已覆盖；仍需完整上游转换矩阵测试 |
| P1-C4 | `routerx` 扩展参数 | 技术用户能表达路由偏好和 provider-specific 参数。 | `POLICIES`、`SNAPSHOTS`、`DEVELOPER_EXPERIENCE`、`SECURITY`、`API`、`RELAY`、`FLOWS` | reserved field parser、sanitizer、route policy | 私有字段剥离、`routerx.route` 越权拒绝、筛选后无候选的拒绝 `policy_snapshot`、路由快照、`X-RouterX-Options` header 路由偏好、`routerx.upstream` 安全 header/query/body 补充、`routerx.provider.<provider>` 选中 provider JSON body 补充和 RouterX-Compatible `X-RouterX-Hop` 循环保护 / `X-RouterX-Chain` 链路摘要已覆盖，`relay.routerx_max_hops` 配置收紧由 `TestRouterXCompatibleUpstreamUsesConfiguredHopLimit` 覆盖；完整 provider 字段映射和降级摘要仍需补齐 |
| P1-C5 | 价格表达式 | 运营方能按模型、通道和用量规则定价。 | `BILLING`、`SNAPSHOTS`、`DATA_MODEL`、`SETTINGS` | `model_prices`、`channel_model_prices`、expression engine | 系统模型价格表、通道模型价格覆盖、规则版本、用户侧价格就绪状态、成功调用后的表达式扣费热路径、倍率扣费热路径、`billing_snapshot` 规则/倍率快照和 `model_price.*`/`channel_model_price.*` 审计已覆盖；完整访问控制快照和更多历史解释测试仍需补齐 |
| P1-C6 | 访问控制 | 普通用户只能使用允许的通道、模型和分组。 | `POLICIES`、`SNAPSHOTS`、`SECURITY`、`BILLING`、`API_KEYS`、`API`、`RELAY` | channel access policy、user group policy、API Key scope | 通道模型 `user_enabled=false` 已能影响 `/v0/user/models` 可见性和普通用户 Relay 候选过滤，并由 `TestChannelModelUserEnabledFiltersRelayCandidates` 覆盖，拒绝会写入 `channel_model=deny` policy 快照；用户分组 x 通道分组访问控制已由 `TestUserGroupChannelGroupAccessFiltersRelayCandidates` 覆盖，拒绝会写入基础 `policy_snapshot`；模型、APIType、通道分组、入口协议、IP/CIDR、方法路径 allow-list、日/月预算、并发上限和 RPM/TPM scope 收窄已由 `TestAPIKeyModelScopeRestrictsRelayBeforeUpstream`、`TestAPIKeyAPIScopeRestrictsRelayBeforeUpstream`、`TestAPIKeyChannelGroupScopeFiltersRelayCandidates`、`TestAPIKeyEntryProtocolScopeRejectsBeforeRelay`、`TestAPIKeyIPScopeRejectsBeforeRelay`、`TestAPIKeyMethodScopeRejectsBeforeRelay`、`TestAPIKeyDailyQuotaScopeRejectsAfterDailyBudgetUsed`、`TestAPIKeyMonthlyQuotaScopeRejectsAfterMonthlyBudgetUsed`、`TestAPIKeyMaxConcurrencyScopeRejectsOnlyWhileInFlight`、`TestAPIKeyRPMScopeRejectsWithinMinuteBeforeRelay`、`TestAPIKeyTPMScopeRejectsAfterMinuteTokenBudgetUsed` 覆盖，API Key scope 拒绝会写入基础 `policy_snapshot`；`routerx.route` 筛选后无候选和通道硬过滤的 `no_available_channel` 拒绝已由 `TestRouterXRoutePreferenceFiltersChannels` 覆盖；其他失败层的完整 error、限流和熔断快照仍需补齐 |
| P1-C7 | 重试、限流和熔断 | 上游临时故障和本地频率保护能被隔离，错误原因可解释。 | `RELAY`、`OPERATIONS`、`SETTINGS`、`RUNBOOKS` | retry policy、rate limit counters、error counter、channel health state | 非流式安全重试、Redis Token 限流、响应读取上限和 error_count 自动熔断候选过滤测试已覆盖；Redis Token 限流拒绝会写失败日志和基础 `policy_snapshot`；半开恢复、更多限流维度和完整限流/熔断快照仍需补齐 |
| P1-C8 | 观测指标 | 管理员能看见调用量、错误、延迟、额度和通道健康。 | `OBSERVABILITY`、`OPERATIONS`、`ARCHITECTURE`、`BILLING`、`RUNBOOKS` | structured logs、metrics endpoint、dashboard API | `/metrics` 已覆盖用户数、API Key 数、通道数、可用通道数、当日调用/额度、ready、DB/Redis/日志库 up、HTTP 请求量/耗时、Relay/上游耗时、调用日志状态、Relay 请求数、Relay 错误维度、token 用量、按模型/供应商/用户组的额度消耗、逐通道可用状态、逐通道错误计数、限流拒绝、计费失败、支付订单、支付事件和审计事件；`observability.request_id_header` 已由 `TestRequestIDHeaderUsesConfiguredSetting` 覆盖，上游 request id 透传已由 `TestChatCompletionSuccessLogsAndDeductsQuota` 覆盖；调用日志已持久化 request_id、error_code、usage_source、error_source、upstream_status、基础 request_snapshot、含成功、API Key scope 拒绝、基础余额预检拒绝、用户分组访问控制拒绝、无可用候选拒绝和 Redis Token 限流拒绝分支的基础 policy_snapshot、含过滤/模型重写/重试摘要的基础 route_snapshot 和含价格表达式或 P0 回退表达式/规则版本/倍率/预算前后摘要的基础 billing_snapshot；更细错误维度和告警仍需补齐 |
| P1-C9 | 通道候选缓存 | 高并发下无需每次全量查询通道，且管理员修改后集群能一致失效。 | `ARCHITECTURE`、`RELAY`、`POLICIES`、`SETTINGS` | route candidate cache、routing version、Redis invalidation | 进程内候选缓存、TTL/version 配置校验、版本失效和回源已由 `TestChannelCandidateCacheUsesVersionInvalidation`、`TestSetupBootstrapAdminQuotaAndSettingsDefaults`、`TestSettingsValidationAndReadiness` 覆盖；启动预加载、Redis 共享快照和跨实例广播仍需补齐 |
| P1-C10 | 独立日志数据库 | 大量调用日志可独立备份清理，账单最小事实仍可恢复。 | `OPERATIONS`、`OBSERVABILITY`、`BILLING`、`DATA_MODEL` | `LOG_SQL_DSN`、LogService、billing outbox/minimal facts | `LOG_SQL_DSN` 初始化、主库事实保留、日志库副本写入、运行期写入失败降级、主库 outbox 补写、日志库列表读取、查询失败回退和日志库健康指标已由 `TestInitLogDBUsesConfiguredDSN`、`TestLogServiceWritesMainFactAndExternalLogDB`、`TestLogServiceFallsBackWhenExternalLogDBWriteFails`、`TestLogServiceReplaysPendingExternalLogOutbox`、`TestLogServiceWorkerReplaysPendingExternalLogOutbox`、`TestLogServiceListsFromExternalLogDBWhenConfigured`、`TestLogServiceListFallsBackToMainDBWhenExternalLogDBFails`、`TestMetricsEndpointReportsIndependentLogDBHealth` 覆盖；冷热归档仍需补齐 |

## P2 企业和生产增强

P2 的目标是支撑长期运营、企业接入、安全加固和支付插件。

| ID | 能力 | 用户价值 | 主要文档 | 落地位置 | 验收证据 |
|----|------|----------|----------|----------|----------|
| P2-C1 | OAuth/OIDC 和企业身份 | 企业用户能接入组织身份体系。 | `SECURITY`、`ACCOUNTS`、`API`、`OPERATIONS` | identity providers、account binding、login audit | 绑定、恢复、重复身份防护测试 |
| P2-C2 | 管理审计 | 关键管理行为可追溯。 | `OBSERVABILITY`、`OPERATIONS`、`DATA_MODEL`、`ACCOUNTS` | audit logs、admin service hooks | API Key 管理、用户管理、支付商品管理、系统模型价格管理、通道模型价格管理、支付入账/退款回调、Stripe/易支付 provider 退款请求、Stripe 争议生命周期、支付人工修正、settings 更新和校验拒绝、用户调额、充值码管理、通道管理、管理员账号管理、日志清理、日志导出审计、基础 `/metrics` 和超级管理员查询边界已覆盖；更多失败和拒绝操作审计测试仍需补齐 |
| P2-C3 | 支付与充值插件 | 在线充值、充值码、退款和人工修正可选接入，不影响基础运营路径。 | `PAYMENTS`、`SECURITY`、`BILLING`、`API`、`OPERATIONS` | payment products、orders、events、quota transactions、provider webhook | 充值码生成/导入/作废/兑换审计、充值码批次/备注/过期策略、管理员调额、支付商品管理及审计、支付订单创建审计、本地 pending 订单、Stripe Checkout Session 创建、Stripe/易支付 provider 退款请求、Stripe webhook 入账审计、Stripe 全额/部分退款和扣回审计、Stripe 争议生命周期和可选 API Key 禁用测试、易支付异步通知审计、支付人工补账/扣回、支付人工退款落账和同步返回基础测试已覆盖；更多 provider 自动退款流程测试仍需补齐 |
| P2-C4 | 密钥轮换和 KMS | 生产密钥可轮换，数据库密文可迁移。 | `SECURITY`、`DATA_MODEL`、`OPERATIONS`、`SETTINGS` | encryption versioning、KMS provider、rotation jobs | 轮换、解密兼容和脱敏测试 |
| P2-C5 | 高级 API | Responses、Embeddings、Images、Audio、Moderations 等能力可按阶段打开。 | `PROTOCOLS`、`API`、`RELAY`、`TESTING` | APIType handlers、adapter extensions、upload limits | Responses/Embeddings 基础透传、Azure Responses `/openai/v1/responses` 转发和 usage 映射、Azure Embeddings deployment 转发和 usage 映射已覆盖；Image Generations/Moderations 基础透传和最低计费已覆盖，其中 Azure Image Generations 已覆盖 `/openai/v1/images/generations`、`api-key` 和最低计费；Image Edits/Variations multipart 表单透传、路由偏好和最低计费已覆盖，其中 Azure Image Edits 已覆盖 `/openai/v1/images/edits`、`api-key`、图像/遮罩文件字段保留和最低计费；Audio Speech 二进制响应透传已覆盖，其中 Azure Audio Speech 已覆盖 `/openai/v1/audio/speech`、`api-key`、Content-Type 透传和最低计费；Audio Transcriptions/Translations multipart 表单透传、路由偏好和最低计费已覆盖，其中 Azure Audio Transcriptions/Translations 已覆盖 `/openai/v1/audio/transcriptions|translations`、`api-key`、文件字段保留和最低计费；`relay.max_request_body_bytes` 和 `relay.max_response_body_bytes` 已覆盖高级接口共享请求/响应体上限的基础入口保护；更细粒度的协议字段限制和安全扫描测试仍需补齐 |
| P2-C6 | 高级 API Key 管理 | 技术用户能批量管理、轮换和审计 API Key。 | `API_KEYS`、`SECURITY`、`API`、`ACCOUNTS`、`DATA_MODEL`、`OBSERVABILITY` | token metadata、batch API、usage filters、audit events | 基础生命周期审计、轮换、泄露上报、单 Key 用量摘要、最近使用来源摘要、管理员跨用户查询、批量禁用、批量过期、基础风险视图、模型/APIType/通道分组/入口协议/IP/方法路径 allow-list scope、日/月预算、并发上限和 RPM/TPM 已覆盖；缓存失效和更完整泄露窗口分析仍需补齐 |

## 跨能力硬性约束

这些约束不属于某一个功能，但任何阶段实现都必须保留：

| 约束 | 要求 | 违反时的风险 |
|------|------|--------------|
| `/v1` 协议兼容 | 成功和失败都保持入口协议外形。 | SDK 兼容性破坏。 |
| 错误语义稳定 | code、HTTP、重试、扣费和日志事实必须一致。 | 客户端、账单和排障互相矛盾。 |
| settings 权威 | 运行时业务配置从 `settings` 读取，环境变量只做启动和密钥。 | 多实例配置漂移，管理端改动不生效。 |
| 密钥不泄露 | API Key 明文只返回一次，上游密钥加密存储，日志脱敏。 | 生产安全事故。 |
| 威胁有控制点 | 关键威胁必须能映射到拦截点、日志或测试证据。 | 安全依赖人工经验，缺口难发现。 |
| 预检在上游前 | 鉴权、额度、访问控制和基础请求合法性在上游调用前完成。 | 余额不足仍消耗上游成本。 |
| 计费事实可复核 | `quota_used`、usage、价格规则、倍率和扣费事务可追溯。 | 账单争议无法解释。 |
| 路由可解释 | 通道过滤、选择、模型重写、错误来源和重试行为能还原。 | 管理员无法排障。 |
| 观测不泄露敏感信息 | 日志、指标、审计和导出默认脱敏并控制高基数标签。 | 安全事故和指标系统失控。 |
| 控制台不改变能力边界 | 控制台只能承载后端允许的动作，不能通过前端状态绕过权限、额度或密钥策略。 | UI 可用但实际安全边界被绕过。 |
| 开发者体验不虚假承诺 | 对外文档和 SDK 示例只能承诺当前阶段可验证能力，未支持能力必须明确阶段和错误。 | 调用方误以为兼容完整上游，生产接入失败。 |
| 支付入账幂等可追溯 | 支付、充值码、退款和人工调整必须通过订单、事件、额度流水和审计证明。 | 重复入账、资金争议、人工改账不可复核。 |
| 测试不依赖真实外部服务 | 上游、支付、OAuth/OIDC、短信和邮件使用本地桩或 fixture。 | 测试不稳定，成本和密钥风险增加。 |

## 变更同步清单

新增或改变能力时，同步检查：

- `docs/GLOSSARY.md` 是否需要新增术语。
- `docs/DECISIONS.md` 是否需要新增或调整 Active、Confirm、Later 决策和确认窗口。
- `docs/CONSOLE.md` 是否需要新增状态、动作、空状态、权限或证据链。
- `docs/DEVELOPER_EXPERIENCE.md` 是否需要新增接入方式、SDK 行为、API Key 规则、错误处理或企业集成约定。
- `docs/PAYMENTS.md` 是否需要新增 provider、订单状态、充值码、退款、人工调整或额度流水规则。
- `docs/SECURITY.md` 是否需要新增威胁、控制点或事故响应。
- `docs/ERRORS.md` 是否需要新增 code、重试、扣费或日志语义。
- `docs/OBSERVABILITY.md` 是否需要新增日志、审计、指标、告警或保留规则。
- `docs/SNAPSHOTS.md` 是否需要新增快照 kind、字段、脱敏规则或历史解释断言。
- `docs/RUNBOOKS.md` 是否需要新增小白路径、管理路径、生产故障或安全事故处理步骤。
- `docs/API.md` 是否需要新增接口、错误 code 或权限说明。
- `docs/DATA_MODEL.md` 是否需要新增字段、索引、迁移或安全规则。
- `docs/RELAY.md` 是否需要新增协议、provider、Adapter 行为或错误映射。
- `docs/PROTOCOLS.md` 是否需要新增入口协议、APIType、上游厂商、能力等级、字段降级或 SDK 兼容矩阵。
- `docs/MODULE_BOUNDARIES.md` 是否需要新增模块职责、禁止跨层行为、依赖方向或测试边界。
- `docs/POLICIES.md` 是否需要新增策略来源、决策顺序、冲突规则、快照或验收。
- `docs/BILLING.md` 是否需要新增额度、价格、访问控制或账单快照。
- `docs/ACCEPTANCE.md` 是否需要新增或调整阶段门禁、证据等级、不可接受状态或交付结论格式。
- `docs/SETTINGS.md` 是否需要新增 settings key、默认值、校验和 readiness 规则。
- `docs/TESTING.md` 是否需要新增测试名、fixture 或断言。
- 本文档是否需要新增能力 ID 或验收证据。
