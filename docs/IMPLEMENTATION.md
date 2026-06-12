# RouterX 实现交接清单

## 目标

本文档面向后续实现者，回答“从哪里开始改、每步改到什么程度、怎么证明完成”。产品定位、阶段边界和跨模块原则以 `docs/DESIGN.md` 为准；关键技术取舍以 `docs/DECISIONS.md` 为准；术语边界以 `docs/GLOSSARY.md` 为准；用户任务路径以 `docs/FLOWS.md` 为准；控制台能力以 `docs/CONSOLE.md` 为准；开发者体验以 `docs/DEVELOPER_EXPERIENCE.md` 为准；API Key 生命周期和高级管理以 `docs/API_KEYS.md` 为准；策略与访问控制以 `docs/POLICIES.md` 为准；协议兼容和能力矩阵以 `docs/PROTOCOLS.md` 为准；模块责任边界以 `docs/MODULE_BOUNDARIES.md` 为准；支付和充值以 `docs/PAYMENTS.md` 为准；能力验收追踪以 `docs/TRACEABILITY.md` 为准；商业级完成门禁以 `docs/ACCEPTANCE.md` 为准；安全威胁边界以 `docs/SECURITY.md` 为准；错误语义以 `docs/ERRORS.md` 为准；观测审计以 `docs/OBSERVABILITY.md` 为准；故障处理以 `docs/RUNBOOKS.md` 为准。本文只做落地交接，不设计网页，不展开部署教程。

P0 的交付目标是：

```text
空库初始化 -> 管理员登录 -> 创建通道 -> 创建 API Key -> /v1/models -> /v1/chat/completions -> 日志和额度变化可查
```

任何实现动作都必须保护这条开箱路径。支付、OAuth/OIDC、多协议完整矩阵、流式响应、高级价格表达式和生产观测可以作为后续增强，但不能成为 P0 首次调用的前置条件。

## 固定技术决策

| 决策 | P0 默认 |
|------|---------|
| 配置来源 | DB `settings` 是运行时配置权威来源；环境变量只承载启动项和密钥。 |
| API Key 存储 | 明文只返回一次，数据库保存 SHA256 哈希，兼容早期明文存量迁移。 |
| 有限 API Key 预算 | 创建时只设置最大消耗额度，不扣 `users.quota`；调用成功同时扣用户余额并消耗 Key 预算。 |
| 无限 API Key 预算 | `unlimited=true` 或 `remain_quota=-1` 时，调用成功只扣用户 `quota`。 |
| P0 Relay 重试 | `relay.retry_count=0`，先保证无重试闭环稳定；P1 再开启可配置安全重试。 |
| P0 body 日志 | `log.body_max_bytes=0`、`relay.log_body_max_bytes=0`，默认不记录请求/响应 body。 |
| `/v1` 错误 | 必须返回入口协议兼容错误，不返回 RouterX `{success,data,message}` 包装。 |
| 用户路由偏好 | `routerx.route` 只能收窄管理员允许的候选集，不能绕过策略。 |

## 文件地图

| 区域 | 主要文件 | 实现职责 |
|------|----------|----------|
| 路由装配 | `internal/router/router.go`、`internal/router/user_router.go`、`internal/router/admin_router.go` | 初始化、用户、管理员和 `/v1` 路由挂载，中间件顺序。 |
| 初始化 | `internal/handler/setup_handler.go`、`internal/service/setup_service.go` | 初始化状态、超级管理员、本地身份、默认 settings、启动额度。 |
| settings | `internal/service/setting_service.go`、`internal/handler/setting_handler.go`、`internal/model/setting.go` | typed accessor、缓存、批量更新、脱敏、注册表默认值。 |
| 账号权限 | `internal/service/auth_service.go`、`internal/service/user_service.go`、`internal/service/admin_service.go`、`internal/middleware/jwt_auth.go` | 登录、JWT、管理员和超级管理员边界。 |
| API Key | `internal/service/token_service.go`、`internal/handler/token_handler.go`、`internal/middleware/apikey_auth.go`、`internal/model/token.go` | Key 创建、哈希校验、缓存、状态、额度预检和扣费。 |
| 通道 | `internal/service/channel_service.go`、`internal/handler/channel_handler.go`、`internal/model/channel.go` | 通道 CRUD、密钥加密、多 key、多 base URL、`upstreams`、模型重写、选择策略。 |
| Relay | `internal/handler/relay_handler.go`、`internal/service/relay_service.go`、`internal/relay/*` | 协议入口、请求解析、adapter、通道选择、上游调用、usage、扣费、日志。 |
| 日志账单 | `internal/service/log_service.go`、`internal/handler/log_handler.go`、`internal/model/log.go` | 调用日志、用户日志、管理员日志、账单聚合。 |
| 测试 | `internal/router/router_test.go` | P0 集成测试主入口，后续补本地下游桩和计费一致性测试。 |

## P0 实现顺序

### 1. settings 注册表和 readiness

前置文档：`docs/SETTINGS.md`、`docs/OPERATIONS.md`。

落地动作：

1. 让 `SettingService` 拥有 current 阶段 key 的注册表元数据：类型、默认值、敏感级别、校验和生效方式。
2. 初始化或迁移补缺失 key，不覆盖已有管理员配置。
3. typed accessor 解析失败时返回错误，不静默使用 0 值。
4. 管理端响应脱敏 `jwt.secret` 等敏感值。
5. `/ready` 在生产模式下检查关键 settings。

验收：

- `TestSettingsRegistryAndReadiness` 覆盖默认值、类型错误、缓存刷新、脱敏和 production readiness。
- `go test ./...` 通过。

### 2. 初始化启动额度

前置文档：`docs/DESIGN.md`、`docs/BILLING.md`。

落地动作：

1. `SetupService.Init` 创建超级管理员和本地身份后，读取 `billing.bootstrap_admin_quota`。
2. 当启动额度大于 0 时，为超级管理员写入足够完成首次验证的 `users.quota`。
3. 当启动额度为 0 时，接口或文档必须明确提示先调整额度。
4. 重复初始化保持冲突错误，不覆盖 settings。

验收：

- `TestSetupBootstrapAdminQuota` 覆盖有启动额度和 0 启动额度两种路径。
- 初始化后的管理员能继续完成创建通道、创建 API Key 和首次调用。

### 3. API Key 预算和鉴权闭环

前置文档：`docs/API_KEYS.md`、`docs/API.md`、`docs/BILLING.md`。

落地动作：

1. 创建有限额度 API Key 时，只设置 Key 最大消耗额度或剩余预算上限，不扣用户 `quota`。
2. 创建无限 API Key 不设置 Key 预算上限，调用成功后扣用户额度。
3. `ValidateAndGetToken` 继续支持 SHA256 哈希，并兼容早期明文存量迁移。
4. 禁用、删除、过期、用户禁用和余额不足在调用下游前失败。
5. Redis 缓存失效必须跟随 Token 更新、删除和用户状态变化。

验收：

- 预检失败路径下游请求计数为 0。
- 有限 API Key 调用同时扣用户余额并消耗 Key 预算，二者任一不足都拒绝。
- 无限 Token 调用扣用户，Token `remain_quota` 保持 `-1`。

### 4. OpenAI-compatible Chat 非流式闭环

前置文档：`docs/RELAY.md`、`docs/API.md`、`docs/TESTING.md`。

落地动作：

1. `RelayHandler.ChatCompletions` 读取 body、取得 API Key 上下文，调用 `RelayService`。
2. `RelayService` 解析 model 和 stream；P0 遇到 `stream=true` 返回 `unsupported_stream`。
3. 先做用户/API Key/额度预检，再选择通道。
4. `ChannelService.SelectChannel` 按启用状态、模型匹配、错误计数、priority 和 weight 选择候选。
5. `ResolveUpstream` 按 `upstreams`、多 key/base URL、单 key/base URL 顺序解析上游。
6. Adapter 构造下游请求，剥离 `routerx` 私有字段，注入通道密钥。
7. 成功响应提取 usage，计算 `quota_used`，扣费成功后写 success 日志并返回 OpenAI-compatible 响应。
8. 扣费失败写 failed 日志；响应未返回前返回 429。

验收：

- `TestChatCompletionSuccessLogsAndDeductsQuota` 断言 HTTP 响应、下游收到的请求、日志和额度。
- 成功日志包含 user、token、channel、model、usage、quota 和 status。
- 下游密钥、用户 API Key 和 DSN 不出现在响应或日志中。

### 5. 错误映射和排障事实

前置文档：`docs/RELAY.md`、`docs/OPERATIONS.md`。

落地动作：

1. 非法 JSON、缺少 model、`stream=true` 直接返回 OpenAI-compatible 400。
2. API Key 无效返回 401，用户或 Token 禁用返回 403，余额不足返回 429。
3. 无可用通道返回 502 `no_available_channel`，不调用下游。
4. 下游 400 不重试；401/403 归因通道配置；429/5xx/超时在 P0 默认不自动重试。
5. 错误日志记录可排障摘要，不记录完整敏感响应体。

验收：

- `TestChatCompletionInvalidRequestDoesNotCallUpstream`。
- `TestChatCompletionUpstreamErrorMapping`。
- `TestRelayPrecheckRejectsBeforeUpstream`。

### 6. 通道高级字段事实校验

前置文档：`docs/DATA_MODEL.md`、`docs/RELAY.md`。

落地动作：

1. `upstreams` 非空时优先使用，不与外层 `api_keys` 或 `base_urls` 交叉组合。
2. `key_selection_mode=random` 从候选 key 中随机；空值或未知值归一为 `round_robin`。
3. `weight <= 0` 在选择时按 1 处理。
4. `model_rewrites` 在上游请求前生效。
5. 通道密钥响应和日志全程脱敏。

验收：

- `TestChannelRoutingConfigResolution`。
- 测试断言下游收到的 Authorization 是通道密钥，不是用户 API Key。

### 7. 日志账单一致性

前置文档：`docs/BILLING.md`、`docs/DATA_MODEL.md`。

落地动作：

1. 用户日志只返回当前用户数据，管理员日志支持全局筛选。
2. 成功调用的 `logs.quota_used` 是账单聚合权威事实。
3. 创建有限 API Key 是预算上限设置，不写模型消费日志，也不改变用户余额。
4. 失败调用默认 `quota_used=0`，未来如启用失败成本必须写配置和快照。

验收：

- `TestUserBillingMatchesLogs`。
- `GET /v0/user/billing` 的调用数、token 数和消耗额度与 `logs` 聚合一致。

## P1 接续顺序

P0 通过后再进入 P1：

1. 流式 SSE、客户端断开取消和流式 usage 策略。
2. 非流式安全重试、熔断、半开恢复和更细路由决策快照。
3. Anthropic/Gemini 入口协议的成功和错误格式精确映射。
4. 多上游转换矩阵按 `docs/PROTOCOLS.md` 分级推进：OpenAI-Compatible、Anthropic、Gemini、Azure、xAI、Qwen、DeepSeek、RouterX-Compatible。
5. `model_prices`、`channel_model_prices`、倍率、访问控制和计费快照。
6. 充值码和可选支付插件接口。

进入 P1 前的门槛：

- P0 所有测试用例存在并通过。
- `/v1` 成功和失败均保持入口协议兼容。
- 日志、扣费和账单聚合一致。
- settings 注册表和 readiness 不再依赖散落字符串。

## 禁止事项

- 不为 P0 首次调用引入支付、OAuth/OIDC、流式响应或完整价格表达式前置依赖。
- 不让 `/v1` 返回 RouterX 管理端统一响应包装。
- 不在日志、响应、审计或 Redis key 中泄露 API Key、下游密钥、支付密钥或 DSN。
- 不在调用下游后才发现用户或 Token 明显无额度。
- 不让有限 API Key 调用只更新 Key 预算或只扣用户余额；二者必须在同一事务中保持一致。
- 不把 `routerx.route` 当成强制越权路由。
- 不把类型解析失败的配置静默当成 0、false 或空字符串。
- 不用真实外部模型厂商作为 P0 自动化测试依赖。

## 每次交付检查

每次交付能否宣称完成，先按 `docs/ACCEPTANCE.md` 判断证据等级和阶段门禁，再执行下面命令。

每次完成一个工作包，至少运行：

```bash
go test ./...
git diff --check
```

文档变更还需要检查：

- 搜索旧配置表名、旧 JSON 字段名、旧 API Key 明文字段术语、空泛填充描述和不确定状态描述，确保没有自相矛盾的遗留表达。

如果新增 settings key、API code、错误 code 或日志字段，必须同步更新：

- `docs/GLOSSARY.md`，仅当新增或改变术语边界。
- `docs/CONSOLE.md`，仅当新增或改变控制台状态、动作、证据链、空状态或权限边界。
- `docs/DEVELOPER_EXPERIENCE.md`，仅当新增或改变调用方接入、SDK 行为、API Key 使用、错误处理或企业集成体验。
- `docs/API_KEYS.md`，仅当新增或改变 API Key 生命周期、轮换、泄露处理、作用域、缓存或高级管理。
- `docs/POLICIES.md`，仅当新增或改变策略决策顺序、访问控制、限流、预算、分组或路由偏好语义。
- `docs/PROTOCOLS.md`，仅当新增或改变入口协议、APIType、上游厂商、能力等级、字段降级、流式或 SDK 兼容矩阵。
- `docs/MODULE_BOUNDARIES.md`，仅当新增模块、改变模块职责、依赖方向、跨层边界或测试边界。
- `docs/PAYMENTS.md`，仅当新增或改变支付 provider、充值码、订单、事件、退款、人工调整或额度流水。
- `docs/ACCEPTANCE.md`，仅当新增或改变阶段验收门禁、交付证据等级或不可接受状态。
- `docs/SECURITY.md`，仅当新增或改变安全边界。
- `docs/ERRORS.md`，仅当新增或改变错误语义。
- `docs/OBSERVABILITY.md`，仅当新增或改变观测审计语义。
- `docs/RUNBOOKS.md`，仅当新增或改变故障处理路径、降级策略或事故响应。
- `docs/SETTINGS.md`
- `docs/API.md`
- `docs/DATA_MODEL.md`
- `docs/RELAY.md`
- `docs/TESTING.md`
- `docs/TRACEABILITY.md`
- `docs/ROADMAP.md`
