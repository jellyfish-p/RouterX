# RouterX 安全威胁模型

本文档是 RouterX 安全设计的总入口，负责把分散在账号、Relay、计费、数据模型、settings 和运维文档中的安全要求汇总为可执行、可验证的威胁模型。

安全目标不是让系统变得难用，而是让默认路径足够安全：小白用户按开箱路径配置时不泄露密钥、不越权、不误扣费；技术用户使用进阶路由和 provider-specific 参数时不能突破后台策略；运营方启用支付、企业身份和审计后能追踪关键事实。

## 保护资产

| 资产 | 说明 | 泄露或篡改影响 |
|------|------|----------------|
| User JWT | 登录 `/v0/user/*` 和 `/v0/admin/*` 的会话凭据 | 账号接管、管理员越权操作 |
| API Key 明文 | 调用 `/v1/*` 的一次性凭据 | 模型额度被盗用 |
| API Key 哈希 | `tokens.key` 中的 SHA256 哈希 | 被撞库或错误当明文展示 |
| 上游密钥 | 通道中调用 OpenAI、Anthropic、Gemini 等厂商的密钥 | 上游账号被盗刷、供应商风控 |
| `ENCRYPTION_KEY` / KMS | 加密上游密钥和第三方 client secret 的主密钥 | 已加密密钥不可恢复或被批量解密 |
| `JWT_SECRET` | JWT 签名密钥 | 跨实例登录异常、伪造 JWT |
| 额度、Key 预算和账单事实 | `users.quota`、`tokens.remain_quota`/`quota_limit`、`logs.quota_used` | 透支、预算绕过、重复扣费、账单争议 |
| settings | 运行时配置权威来源 | 关闭安全开关、错误计费、错误日志策略 |
| 支付密钥和回调 | Stripe、易支付等支付 provider 的签名凭据和事件 | 伪造充值、重复入账 |
| 日志和提示内容 | 请求、响应、错误、审计摘要 | 隐私泄露、密钥泄露、合规风险 |

## 信任边界

```text
调用方 / SDK
    -> /v1 入口协议边界
    -> API Key 鉴权和额度预检
    -> RouterX Relay
    -> 上游厂商 API

用户 / 管理员控制台
    -> /v0 User JWT 边界
    -> 用户、管理员、超级管理员权限
    -> settings / 通道 / 计费 / 审计

支付 provider
    -> webhook 签名边界
    -> 订单金额和状态校验
    -> 入账事务

RouterX 运行时
    -> DB / Redis / KMS / 环境变量
    -> 配置缓存和密钥解密
```

边界原则：

- `/v1/*` 只接受 API Key，不接受 User JWT。
- `/v0/user/*` 和 `/v0/admin/*` 只接受 User JWT，不接受 API Key。
- `routerx.route` 只表达偏好，不能覆盖管理员策略、访问控制、额度、熔断或密钥安全。
- 支付回调只信任 provider 签名、服务端订单和服务端商品配置，不信任客户端提交的额度。
- Redis 是缓存和短期状态，不是计费、支付、settings 的最终事实源。

## 威胁矩阵

| ID | 威胁 | 风险场景 | 默认控制 | 验收证据 |
|----|------|----------|----------|----------|
| S1 | API Key 泄露 | 用户 API Key 出现在日志、管理响应、缓存键或错误信息中。 | 明文只返回一次；数据库保存 SHA256；日志和响应脱敏；缓存键使用哈希或内部 ID。 | `TestP0BackendFlow`、敏感词日志扫描、API Key 列表不返回明文。 |
| S2 | 上游密钥泄露 | 通道密钥被管理列表、测试接口、错误响应或调用日志暴露。 | 使用 `ENCRYPTION_KEY` 或 KMS 加密；管理响应只返回脱敏摘要；错误摘要不含密钥；已有加密通道密钥但缺少主密钥时 `/ready` 不就绪。 | `TestChannelExtendedManagement`、`TestReadinessRequiresEncryptionKeyForEncryptedChannelSecrets`、通道响应和日志不包含测试密钥。 |
| S3 | JWT 伪造或跨实例失效 | 多实例各自生成 `jwt.secret`，或弱密钥导致伪造。 | 生产必须显式配置一致 `JWT_SECRET` 或数据库 `jwt.secret`；`/ready` 检查关键配置。 | `TestSettingsValidationAndReadiness`、生产 readiness 缺关键密钥时不就绪。 |
| S4 | 管理权限越权 | 普通用户调用管理员接口，或普通管理员修改超级管理员配置。 | User JWT + role 校验；超级管理员能力单独判断；API Key 不继承管理权限。 | `TestAdminPrivilegeBoundaries`、权限矩阵接口测试。 |
| S5 | API Key 越权调用管理接口 | 管理员的 API Key 被拿来调用 `/v0/admin/*`。 | API Key 只在 `/v1/*` 生效；管理接口只看 User JWT。 | API 鉴权边界测试，API Key 调 `/v0` 返回未登录或权限错误。 |
| S6 | 路由或 scope 越权 | 调用方通过 `routerx.route` 强制使用无权通道、禁用通道或高价通道，或通过 API Key 调用 scope 未允许模型/APIType/通道分组/入口协议/IP/方法路径，或超过 Key 日/月预算/并发上限/RPM/TPM。 | 先做后台硬性过滤、用户分组访问控制、API Key scope 和访问控制，再应用偏好；拒绝原因写日志。 | `TestChannelRoutingConfigResolution`、`TestUserGroupChannelGroupAccessFiltersRelayCandidates`、`TestAPIKeyModelScopeRestrictsRelayBeforeUpstream`、`TestAPIKeyAPIScopeRestrictsRelayBeforeUpstream`、`TestAPIKeyChannelGroupScopeFiltersRelayCandidates`、`TestAPIKeyEntryProtocolScopeRejectsBeforeRelay`、`TestAPIKeyIPScopeRejectsBeforeRelay`、`TestAPIKeyMethodScopeRejectsBeforeRelay`、`TestAPIKeyDailyQuotaScopeRejectsAfterDailyBudgetUsed`、`TestAPIKeyMonthlyQuotaScopeRejectsAfterMonthlyBudgetUsed`、`TestAPIKeyMaxConcurrencyScopeRejectsOnlyWhileInFlight`、`TestAPIKeyRPMScopeRejectsWithinMinuteBeforeRelay`、`TestAPIKeyTPMScopeRejectsAfterMinuteTokenBudgetUsed`、`routerx.route` 合法/拒绝路径测试。 |
| S7 | 额度透支 | 并发请求同时通过余额检查，导致用户余额或 API Key 预算为负。 | 扣费使用事务或条件更新；`max_concurrency` 可在凭据层收窄单 Key 同时在途请求；成功日志与扣费事务一致。 | `TestUserBillingMatchesLogs`、`TestAPIKeyMaxConcurrencyScopeRejectsOnlyWhileInFlight`、并发扣费测试。 |
| S8 | API Key 预算和余额不同步 | 创建有限 API Key 时误扣用户余额，或调用时只扣用户余额/只扣 Key 预算。 | 创建有限 API Key 只设置预算上限；成功调用同事务扣用户余额并消耗 Key 预算。 | `TestUserBillingMatchesLogs`、有限和无限 API Key 扣费断言。 |
| S9 | 失败调用误扣 | 本地请求错误、无通道、余额不足或上游未调用时仍计费。 | 预检拒绝发生在上游前；失败默认 `quota_used=0`；如启用失败成本需写 settings 和快照。 | `TestRelayPrecheckRejectsBeforeUpstream`、失败日志断言。 |
| S10 | `/v1` 错误泄露内部细节 | 错误响应暴露 DSN、密钥、堆栈或原始上游敏感响应。 | 错误映射为入口协议兼容格式；内部错误只保存脱敏摘要。 | `TestChatCompletionUpstreamErrorMapping`、敏感词扫描。 |
| S11 | 请求/响应体日志泄露 | prompt、响应、Authorization、Cookie 或密钥被完整记录。 | body 日志默认关闭；开启后截断和脱敏；敏感 header 不记录。 | `relay.log_body_max_bytes=0` 默认检查、日志脱敏测试。 |
| S12 | settings 被错误修改 | 配置类型错误、敏感值明文返回、关键配置误改后无审计。 | settings 注册表校验类型和敏感级别；变更先校验再写 DB；写审计摘要。 | `TestSettingsValidationAndReadiness`、配置修改审计测试。 |
| S13 | 支付伪造入账 | 客户端提交额度、伪造 webhook、金额不一致或重复通知。 | 服务端商品决定额度；校验签名、金额、货币、订单状态；幂等写事件。 | 支付签名和幂等测试、重复通知只入账一次。 |
| S14 | OAuth/OIDC 账号接管 | 第三方 provider 返回相同 email 时自动绑定已有账号。 | 不因 email 自动接管；以 provider 稳定身份标识做 identity；绑定需已登录或明确恢复流程。 | 企业身份绑定和恢复测试。 |
| S15 | 上游不可用扩大故障 | 401/403 被无限重试，或流式输出后切换通道造成协议混乱。 | 401/403 归因通道配置不重试；流式写出后不跨通道重试；熔断和错误计数可解释。 | 上游错误映射、重试和熔断测试。 |
| S16 | Redis 缺失隐藏安全问题 | 外部数据库或集群模式下 Redis 不可用导致限流、API Key 禁用状态、settings 变化或通道候选缓存不一致。 | SQLite 单机可用进程内缓存；外部数据库或集群模式 Redis 缺失时不就绪；关键缓存更新失败可感知。 | Redis 缺失/故障测试、生产 readiness 策略测试。 |

## 默认控制点

### 鉴权和权限

- User JWT 和 API Key 的使用范围必须互斥。
- 普通用户、管理员、超级管理员能力必须通过服务层权限检查，不只依赖路由分组。
- 管理员操作用户额度、API Key 无限额度、通道、settings、价格和支付配置时必须进入审计。

### 密钥和敏感数据

- API Key 明文只在创建时返回一次。
- API Key 数据库长期保存 SHA256 哈希，兼容早期明文存量时验证成功后迁移为哈希。
- 上游密钥、OAuth/OIDC client secret 和支付敏感密钥使用 `ENCRYPTION_KEY`、KMS 或环境变量注入。
- 管理响应只返回脱敏摘要，不返回可直接调用的上游密钥。
- 日志和错误响应不能包含 API Key、上游密钥、Cookie、数据库 DSN、支付密钥和内部堆栈。

### Relay 和路由

- Relay 先做鉴权、用户状态、API Key 状态、额度、限流、基础请求校验和 API Key scope 检查，再选择通道。
- 通道候选必须经过启用状态、模型匹配、Adapter 可用、熔断、访问控制和余额策略过滤。
- `routerx.route` 只能在已允许候选集中收窄；格式非法返回入口协议兼容 400；越权返回 403 或无可用通道错误。
- 真实上游调用前必须剥离 RouterX 私有字段，除非上游通道类型是 RouterX-Compatible。
- 用户请求不能覆盖上游鉴权 header。

### 计费和额度

- 所有成功消费必须形成 `usage -> quota_used -> 条件扣费 -> logs` 的事实链。
- 扣费和日志写入需要保持事务一致或具备明确补偿策略。
- 有限 API Key 是预算上限模型；无限 API Key 只表示 API Key 自身不限额，仍扣用户额度。
- 预检失败默认不扣费，也不调用上游。
- 计费规则、倍率和访问控制变更必须可审计，历史账单按当时快照解释。

### 支付

- 支付商品、金额、货币、赠送额度和入账额度只能来自服务端配置。
- webhook 必须校验 provider 签名、订单号、金额、货币、订单状态和事件幂等。
- 支付成功更新订单和增加用户额度必须在同一数据库事务中完成。
- 同步返回页不作为入账依据。
- 支付密钥和原始回调日志必须脱敏。

### 日志和观测

- 请求和响应 body 默认不记录。
- 开启 body 日志必须同时启用截断、脱敏和保留策略。
- 失败日志需要帮助排障，但不能暴露密钥或内部实现细节。
- 管理审计日志记录 actor、目标资源、动作、变更摘要、request_id 和时间。
- 指标暴露聚合事实，不暴露用户 prompt、响应正文和密钥。

## 阶段边界

| 阶段 | 安全目标 | 必须完成 |
|------|----------|----------|
| P0 | 开箱路径安全可用 | API Key 哈希、上游密钥脱敏、基础权限、额度预检、失败不误扣、日志默认不记 body。 |
| P1 | 商业核心安全可解释 | `routerx` 安全过滤、访问控制快照、价格规则审计、重试熔断、流式安全边界。 |
| P2 | 生产运营安全增强 | 管理审计、支付幂等、OAuth/OIDC 防接管、KMS/密钥轮换、指标告警和生产 readiness。 |

## 测试要求

安全测试应优先使用本地桩和可断言数据，不调用真实上游、真实支付或真实企业身份服务。

| 测试方向 | 最小断言 |
|----------|----------|
| 鉴权边界 | User JWT 不能调用 `/v1`；API Key 不能调用 `/v0`；普通用户不能调用管理员接口。 |
| 密钥脱敏 | 响应、日志、错误和审计不包含用户 API Key 明文、上游密钥、DSN 或支付密钥。 |
| 预检拒绝 | 无效 Key、禁用用户、禁用 API Key、余额不足、用户分组未允许通道分组、scope 未允许模型/APIType/通道分组/入口协议/IP/方法路径、达到日/月预算、达到并发上限、达到 RPM/TPM、禁用通道不会调用上游。 |
| 路由越权 | `routerx.route` 不能启用无权通道或绕过通道分组。 |
| 计费一致 | 成功调用的 `logs.quota_used`、用户余额和 Key 预算一致；失败调用不误扣。 |
| 支付幂等 | 重复 webhook、金额不一致、签名失败和订单状态不匹配不会入账。 |
| settings 安全 | 类型错误不写入；敏感值响应脱敏；生产关键配置缺失时 `/ready` 不就绪。 |

## 事故响应

| 事故 | 立即动作 | 后续修复 |
|------|----------|----------|
| API Key 泄露 | 禁用或删除对应 API Key，清理缓存，检查近期日志和额度消耗。 | 引导用户重建 Key，增加泄露来源审计。 |
| 上游密钥泄露 | 禁用对应通道或移除密钥，在上游 provider 后台轮换密钥。 | 检查日志和响应脱敏，补回归测试。 |
| `ENCRYPTION_KEY` 丢失 | 停止写入新密钥，评估可解密数据范围。 | 从备份或 KMS 恢复；无法恢复时逐通道重新配置密钥。 |
| 支付伪造尝试 | 拒绝入账，保留 payment event 和签名摘要。 | 检查 provider 配置、回调来源和幂等键。 |
| 额度异常透支 | 暂停相关用户或 API Key，导出日志和扣费记录。 | 修复事务条件更新，按日志事实修正账本。 |
| 日志泄露 | 立即关闭 body 日志，限制导出，清理或加密敏感日志。 | 补脱敏规则和保留策略，回溯受影响用户。 |

## 文档同步

安全相关改动需要同步检查：

- `docs/POLICIES.md`：策略越权、访问控制、限流绕过、路由偏好和拒绝快照。
- `docs/API_KEYS.md`：API Key 生命周期、轮换、泄露处理、作用域、缓存和审计。
- `docs/PROTOCOLS.md`：入口协议、APIType、能力等级、字段降级、流式和 SDK 兼容边界。
- `docs/ACCOUNTS.md`：身份、权限、账号恢复和企业身份边界。
- `docs/API.md`：鉴权矩阵、错误格式、权限拒绝和支付回调规则。
- `docs/DATA_MODEL.md`：密钥字段、哈希、加密、审计和日志字段。
- `docs/RELAY.md`：`routerx` 过滤、通道选择、上游错误和重试边界。
- `docs/BILLING.md`：额度扣减、支付幂等、价格规则和账单事实。
- `docs/PAYMENTS.md`：支付 provider、充值码、退款、人工补账、额度流水和 webhook 安全。
- `docs/SETTINGS.md`：安全相关 settings、敏感值脱敏和 readiness。
- `docs/OPERATIONS.md`：生产密钥、备份恢复、日志、指标和事故处理。
- `docs/RUNBOOKS.md`：API Key 泄露、上游密钥泄露、`ENCRYPTION_KEY` 丢失、支付伪造和额度异常的处理步骤。
- `docs/TESTING.md`：安全测试场景和本地桩断言。
- `docs/TRACEABILITY.md`：能力 ID、落地位置和验收证据。
