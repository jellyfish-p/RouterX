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
| P0-C2 | 默认 settings 写入 | 空库实例有可预测默认配置。 | `SETTINGS`、`OPERATIONS`、`DATA_MODEL` | `SettingService`、`SetupService.buildDefaultSettings`、Redis settings cache | `TestSettingsRegistryAndReadiness` |
| P0-C3 | 生产 readiness | 生产缺关键配置时不会被误认为可接流量。 | `SETTINGS`、`OPERATIONS`、`ARCHITECTURE`、`RUNBOOKS` | `/ready`、配置注册表、密钥检查 | `TestSettingsRegistryAndReadiness` |
| P0-C4 | 用户登录和权限 | 用户用 User JWT 管理自身资源，管理员管理系统资源。 | `ACCOUNTS`、`API`、`ARCHITECTURE` | auth middleware、user/admin routes、role checks | `TestAdminPrivilegeBoundaries` |
| P0-C5 | API Key 生命周期 | 用户能创建模型调用凭据，明文只出现一次。 | `API_KEYS`、`API`、`ACCOUNTS`、`DATA_MODEL`、`GLOSSARY` | `TokenService`、`tokens`、API Key auth cache | `TestP0BackendFlow` |
| P0-C6 | API Key 预算语义 | 有限 API Key 是预算上限，调用时同时受用户余额和 Key 预算约束；无限 API Key 扣用户额度。 | `API_KEYS`、`BILLING`、`API`、`DATA_MODEL`、`DECISIONS` | `TokenService.CreateToken`、`TokenService.DeductQuota`、`users.quota`、`tokens.remain_quota`/`quota_limit` | `TestUserBillingMatchesLogs` |
| P0-C7 | 通道管理 | 管理员能添加可用上游配置，并且密钥脱敏。 | `API`、`DATA_MODEL`、`RELAY`、`FLOWS` | `ChannelService`、`channels`、通道 CRUD routes | `TestP0BackendFlow`、`TestChannelExtendedManagement` |
| P0-C8 | `/v1/models` | 调用方能用 API Key 验证模型入口可用。 | `API`、`RELAY`、`PROTOCOLS`、`TESTING` | `RelayHandler`、`RelayService`、Adapter model list | `TestP0BackendFlow` |
| P0-C9 | Chat 非流式成功链路 | 调用方能完成一次 OpenAI-compatible Chat 调用。 | `RELAY`、`API`、`PROTOCOLS`、`BILLING`、`TESTING` | `RelayService.Handle`、Adapter、`LogService`、`TokenService` | `TestChatCompletionSuccessLogsAndDeductsQuota` |
| P0-C10 | 预检拒绝 | 无效 Key、禁用用户、余额不足、禁用通道等在上游调用前失败。 | `SECURITY`、`RELAY`、`API`、`BILLING`、`RUNBOOKS` | API Key middleware、quota precheck、channel filter | `TestRelayPrecheckRejectsBeforeUpstream` |
| P0-C11 | 错误映射 | `/v1` 错误保持入口协议兼容，用户和管理员能判断责任。 | `SECURITY`、`API`、`RELAY`、`OPERATIONS`、`RUNBOOKS` | error mapper、Relay failure logs、adapter errors | `TestChatCompletionInvalidRequestDoesNotCallUpstream`、`TestChatCompletionUpstreamErrorMapping` |
| P0-C12 | 日志和账单一致 | 成功调用写日志、扣额度，账单聚合与日志一致。 | `BILLING`、`DATA_MODEL`、`TESTING`、`RUNBOOKS` | `logs`、billing endpoint、quota transaction | `TestUserBillingMatchesLogs` |
| P0-C13 | 密钥安全默认 | 用户 API Key、上游密钥、数据库 DSN 不进入响应和日志。 | `SECURITY`、`ACCOUNTS`、`DATA_MODEL`、`OPERATIONS`、`RUNBOOKS` | encryption helper、response DTO、log sanitizer | `TestP0BackendFlow`、`TestChannelExtendedManagement` |
| P0-C14 | 通道高级字段事实 | 多 key、多 Base URL、`upstreams`、模型重写和通道分组行为可解释。 | `DATA_MODEL`、`RELAY`、`TESTING` | `ChannelService.ResolveUpstream`、`SelectChannel`、channel DTO | `TestChannelRoutingConfigResolution` |
| P0-C15 | 控制台能力闭环 | 小白、用户和管理员能看见状态、证据、错误处理入口和安全边界。 | `CONSOLE`、`FLOWS`、`API`、`OBSERVABILITY`、`RUNBOOKS` | `/v0/setup`、`/v0/user`、`/v0/admin`、dashboard、log routes | 控制台能力契约测试或端到端接口验收 |
| P0-C16 | 开发者最小接入 | 调用方能用 RouterX API Key、base URL 和非流式 Chat 完成迁移。 | `DEVELOPER_EXPERIENCE`、`API`、`RELAY`、`ERRORS`、`BILLING` | `/v1/models`、`/v1/chat/completions`、API Key auth、logs | SDK/HTTP 兼容验收、`TestP0BackendFlow`、`TestChatCompletionSuccessLogsAndDeductsQuota` |

## P1 商业核心增强

P1 的目标是让 RouterX 从“可用闭环”进入“可运营、可扩展、可解释”的商业默认体验。

| ID | 能力 | 用户价值 | 主要文档 | 落地位置 | 验收证据 |
|----|------|----------|----------|----------|----------|
| P1-C1 | SSE 流式转发 | 主流 SDK 的流式调用可用，并能结算 usage。 | `DEVELOPER_EXPERIENCE`、`PROTOCOLS`、`RELAY`、`API`、`TESTING` | stream handler、adapter chunk converter、stream log summary | 流式 chunk、断开取消、usage 结算测试 |
| P1-C2 | 多入口协议 | OpenAI、Anthropic、Gemini 基础入口可并行服务。 | `DEVELOPER_EXPERIENCE`、`PROTOCOLS`、`API`、`RELAY`、`TESTING` | protocol detector、request/response translators | 协议兼容响应和错误格式测试 |
| P1-C3 | 多上游转换 | 同一入口协议可路由到主流上游厂商。 | `PROTOCOLS`、`RELAY`、`DATA_MODEL`、`GLOSSARY` | provider adapters、conversion matrix、adapter registry | 上游转换矩阵测试 |
| P1-C4 | `routerx` 扩展参数 | 技术用户能表达路由偏好和 provider-specific 参数。 | `POLICIES`、`SNAPSHOTS`、`DEVELOPER_EXPERIENCE`、`SECURITY`、`API`、`RELAY`、`FLOWS` | reserved field parser、sanitizer、route policy | 私有字段剥离、越权拒绝和路由快照测试 |
| P1-C5 | 价格表达式 | 运营方能按模型、通道和用量规则定价。 | `BILLING`、`SNAPSHOTS`、`DATA_MODEL`、`SETTINGS` | `model_prices`、`channel_model_prices`、expression engine | 价格表达式、计费快照和历史解释测试 |
| P1-C6 | 访问控制 | 普通用户只能使用允许的通道、模型和分组。 | `POLICIES`、`SNAPSHOTS`、`SECURITY`、`BILLING`、`API_KEYS`、`API`、`RELAY` | channel access policy、user group policy、API Key scope | 访问允许/拒绝、scope 收窄和日志快照测试 |
| P1-C7 | 重试和熔断 | 上游临时故障能被隔离，错误原因可解释。 | `RELAY`、`OPERATIONS`、`SETTINGS`、`RUNBOOKS` | retry policy、error counter、channel health state | 故障注入、重试次数、熔断恢复测试 |
| P1-C8 | 观测指标 | 管理员能看见调用量、错误、延迟、额度和通道健康。 | `OBSERVABILITY`、`OPERATIONS`、`ARCHITECTURE`、`BILLING`、`RUNBOOKS` | structured logs、metrics endpoint、dashboard API | 指标字段和聚合一致性测试 |
| P1-C9 | 通道候选缓存 | 高并发下无需每次全量查询通道，且管理员修改后集群能一致失效。 | `ARCHITECTURE`、`RELAY`、`POLICIES`、`SETTINGS` | route candidate cache、routing version、Redis invalidation | 预加载、版本失效、回源和集群一致性测试 |
| P1-C10 | 独立日志数据库 | 大量调用日志可独立备份清理，账单最小事实仍可恢复。 | `OPERATIONS`、`OBSERVABILITY`、`BILLING`、`DATA_MODEL` | `LOG_SQL_DSN`、LogService、billing outbox/minimal facts | 日志库降级、归档清理和账单恢复测试 |

## P2 企业和生产增强

P2 的目标是支撑长期运营、企业接入、安全加固和支付插件。

| ID | 能力 | 用户价值 | 主要文档 | 落地位置 | 验收证据 |
|----|------|----------|----------|----------|----------|
| P2-C1 | OAuth/OIDC 和企业身份 | 企业用户能接入组织身份体系。 | `SECURITY`、`ACCOUNTS`、`API`、`OPERATIONS` | identity providers、account binding、login audit | 绑定、恢复、重复身份防护测试 |
| P2-C2 | 管理审计 | 关键管理行为可追溯。 | `OBSERVABILITY`、`OPERATIONS`、`DATA_MODEL`、`ACCOUNTS` | audit logs、admin service hooks | 配置、额度、通道和账号操作审计测试 |
| P2-C3 | 支付与充值插件 | 在线充值、充值码、退款和人工修正可选接入，不影响基础运营路径。 | `PAYMENTS`、`SECURITY`、`BILLING`、`API`、`OPERATIONS` | payment products、orders、events、quota transactions、provider webhook | 签名、金额、幂等、额度流水和入账事务测试 |
| P2-C4 | 密钥轮换和 KMS | 生产密钥可轮换，数据库密文可迁移。 | `SECURITY`、`DATA_MODEL`、`OPERATIONS`、`SETTINGS` | encryption versioning、KMS provider、rotation jobs | 轮换、解密兼容和脱敏测试 |
| P2-C5 | 高级 API | Responses、Images、Audio、Moderations 等能力可按阶段打开。 | `PROTOCOLS`、`API`、`RELAY`、`TESTING` | APIType handlers、adapter extensions、upload limits | 高级接口协议和安全限制测试 |
| P2-C6 | 高级 API Key 管理 | 技术用户能批量管理、轮换和审计 API Key。 | `API_KEYS`、`SECURITY`、`API`、`ACCOUNTS`、`DATA_MODEL`、`OBSERVABILITY` | token metadata、batch API、usage filters、audit events | 批量禁用、最近使用、审计和权限测试 |

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
