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
| `TestP0BackendFlow` | 初始化、登录、API Key 创建、用户禁止编辑 Key 额度、通道创建、模型列表、密钥脱敏、无效 Key、空额度 Key 的基础余额预检拒绝日志和 `policy_snapshot`、禁用用户 |
| `TestUserAPIKeyManagementAuditLogs` | API Key 创建、编辑、用户端额度/无限标记编辑拒绝、禁用和删除写入 `api_key.*` 管理审计，审计摘要不泄露 `sk-` 明文，并覆盖审计 `result`/`error_code`/时间范围过滤 |
| `TestUserAPIKeyAdvancedManagement` | 用户查看单 Key 用量摘要、轮换 Key、泄露上报禁用、轮换链路和禁用原因落库，相关审计不泄露明文 Key |
| `TestAdminAPIKeyQueryAndBatchDisable` | 管理员跨用户脱敏查询 API Key，批量禁用必须带筛选条件，批量禁用只影响命中 Key 并写 `api_key.batch_disabled` 审计 |
| `TestAdminAPIKeyRiskViewSummarizesRiskyKeys` | 管理员风险视图按窗口聚合异常 Key，识别失败峰值和低剩余额度，返回风险等级、原因和建议动作，响应不包含明文 Key 或明文前缀 |
| `TestAdminAPIKeyBatchExpire` | 管理员批量过期必须带筛选条件，只影响命中 Key，过期后 `/v1` 鉴权拒绝，并写 `api_key.batch_expired` 审计 |
| `TestAPIKeyModelScopeRestrictsRelayBeforeUpstream` | 用户更新 API Key `allow_models` scope；允许模型成功转发，未允许模型返回 `model_not_allowed`，不调用上游、不额外扣费，并写失败日志、拒绝分支 `policy_snapshot` 和 `api_key.scope_updated` 审计 |
| `TestAPIKeyAPIScopeRestrictsRelayBeforeUpstream` | 用户更新 API Key `api_types` scope；允许 APIType 成功转发，未允许 APIType 返回 `token_forbidden`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyChannelGroupScopeFiltersRelayCandidates` | 用户更新 API Key `channel_groups` scope；候选通道按允许分组过滤，越权 `routerx.route` 返回 `route_forbidden`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyEntryProtocolScopeRejectsBeforeRelay` | 用户更新 API Key `entry_protocols` scope；允许入口协议成功转发，未允许入口协议按当前协议错误外形返回 `token_forbidden`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestUserGroupChannelGroupAccessFiltersRelayCandidates` | 默认用户分组只能访问 settings 允许的通道分组；更高优先级的未授权通道会被过滤，越权 `routerx.route` 返回 `route_forbidden` 且不调用上游，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyIPScopeRejectsBeforeRelay` | 用户更新 API Key `ip_cidrs` scope；允许 IP 成功转发，未允许 IP 返回 `token_forbidden`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyMethodScopeRejectsBeforeRelay` | 用户更新 API Key `methods` scope；允许方法路径成功转发，未允许方法路径返回 `token_forbidden`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyDailyQuotaScopeRejectsAfterDailyBudgetUsed` | 用户更新 API Key `daily_quota` scope；当日成功日志已消耗额度达到上限后返回 `insufficient_quota`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyMonthlyQuotaScopeRejectsAfterMonthlyBudgetUsed` | 用户更新 API Key `monthly_quota` scope；当月成功日志已消耗额度达到上限后返回 `insufficient_quota`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyMaxConcurrencyScopeRejectsOnlyWhileInFlight` | 用户更新 API Key `max_concurrency` scope；同一 Key 在途请求达到上限后返回 `rate_limit_exceeded`，不调用第二个上游、不额外扣费，原在途请求结束后可继续成功，并写拒绝分支 `policy_snapshot` |
| `TestAPIKeyRPMScopeRejectsWithinMinuteBeforeRelay` | 用户更新 API Key `rpm` scope；当前分钟请求数达到上限后返回 `rate_limit_exceeded`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAPIKeyTPMScopeRejectsAfterMinuteTokenBudgetUsed` | 用户更新 API Key `tpm` scope；当前分钟成功日志的模型 token 达到上限后返回 `rate_limit_exceeded`，不调用上游、不额外扣费，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestAdminPrivilegeBoundaries` | 管理员和超级管理员权限边界、设置脱敏、管理员不能越权管理同级或自己 |
| `TestAdminAccountManagementAuditLogs` | 超级管理员创建、编辑、禁用和删除管理员写入 `admin.*` 审计，普通管理员越权访问超级管理员接口写 `admin.denied`，审计摘要不泄露密码 |
| `TestAdminUserManagementAuditLogs` | 管理员创建、编辑、禁用和删除普通用户写入 `user.*` 审计，用户接口拒绝角色变更写 `user.denied`，审计摘要不泄露密码 |
| `TestAdminUserGroupManagement` | 管理员创建、查询、更新和删除用户分组；`default` 分组和仍被用户引用的分组不能删除，成功变更写 `user_group.*` 管理审计 |
| `TestUserRedeemsRedemCodeOnce` | 用户兑换未使用充值码、额度增加、充值码标记 used/used_by/used_at，写入幂等额度流水和 `redem_code.redeem` 管理审计，重复兑换不再入账 |
| `TestRedemCodeBatchNoteAndExpirationPolicy` | 充值码支持 batch_no、note 和未来 expired_at；管理端可按 batch_no 筛选，过期码不可兑换且不改变余额 |
| `TestAdminQuotaAdjustmentWritesTransaction` | 管理员调整用户额度时写入额度流水和 `user.quota_update` 管理审计，记录 actor、reason、变更前后余额和幂等键 |
| `TestAdminManagesRedemCodes` | 管理员生成随机充值码、导入指定充值码、列表查询、作废未使用码，作废码不可兑换，并写入 `redem_code.*` 管理审计 |
| `TestAdminManagesPaymentProducts` | 管理员创建、更新、启用和禁用支付商品；用户侧只展示启用商品，禁用商品不能创建订单 |
| `TestAdminPaymentProductAuditLogs` | 支付商品创建、更新和禁用成功后写入 `admin_audit_logs`，超级管理员可按资源类型查询 |
| `TestAdminAuditRequiresSuperAdmin` | 普通管理员不能查询管理审计日志，超级管理员边界由路由层拦截 |
| `TestUserListsAvailableModels` | 用户查看当前启用通道的去重模型列表，禁用通道模型不可见，未配置系统价格时返回 `minimum_usage` 和 `pricing_ready=false` |
| `TestAdminModelPriceManagementUpdatesUserModelPricing` | 管理员创建、更新、启用和禁用系统模型价格；用户侧模型列表随启用价格返回版本化 `price_rule`，禁用后回退 `minimum_usage`，并写 `model_price.*` 审计 |
| `TestAdminChannelModelPriceControlsUserModelPricingAndVisibility` | 管理员创建、更新、启用和禁用通道模型价格覆盖；通道级价格优先于系统价格，`user_enabled=false` 会隐藏普通用户模型，并写 `channel_model_price.*` 审计 |
| `TestChatCompletionUsesModelPriceExpressionForBilling` | Chat 成功调用后按启用系统模型价格表达式扣减用户余额和 Key 预算；选中通道存在启用覆盖时优先按通道级表达式扣费，并在 `billing_snapshot` 记录规则来源、表达式、变量和版本 |
| `TestChatCompletionAppliesBillingMultipliers` | Chat 成功调用后在价格表达式之后应用倍率；用户分组 x 通道分组组合倍率命中时覆盖用户分组倍率和通道分组倍率乘积，未命中时再分别相乘，并在 `billing_snapshot` 记录倍率模式和最终 `effective_ratio` |
| `TestChannelModelUserEnabledFiltersRelayCandidates` | 普通用户调用会过滤 `channel_model_prices.user_enabled=false` 的通道模型；显式路由到隐藏通道时上游不被调用，失败日志写 `channel_model=deny` policy 快照 |
| `TestUserCreatesAndListsPaymentOrders` | 用户查看启用支付商品；未启用 provider 拒绝下单，启用后创建本地 pending 订单并写 `payment_order.create` 管理审计，订单按 settings 过期且不入账 |
| `TestStripeOrderCreatesCheckoutSessionWhenConfigured` | Stripe secret、测试 API base 和绝对 return_url 齐全时创建 Checkout Session，表单 metadata/金额/货币可复核，并保存 session id/url |
| `TestEpayOrderBuildsSignedCheckoutURL` | 易支付网关配置齐全时创建订单返回签名收银台 URL，参数和签名可复核 |
| `TestEpayNotifyPaysOrderIdempotently` | 易支付同步返回只读展示本地状态；异步通知校验签名和金额，成功通知订单 paid、入账并写 webhook/入账审计，重复通知不重复增加额度或流水 |
| `TestStripeWebhookPaysOrderIdempotently` | Stripe webhook 校验原始 body 签名、Checkout Session 金额和 metadata，成功事件 paid 入账并写 webhook/入账审计，重复事件不重复流水 |
| `TestStripeRefundWebhookRecordsAndOptionallyDeductsQuota` | Stripe 全额退款 webhook 幂等记录订单退款状态；默认不扣额度，开启自动扣回后写 refund_deduct 流水和退款/扣回审计且不重复扣 |
| `TestStripePartialRefundWebhookRecordsAndDeductsProportionally` | Stripe 部分退款 webhook 记录 `partially_refunded` 状态；开启自动扣回后按退款金额比例写 refund_deduct 流水和退款/扣回审计 |
| `TestStripeDisputeWebhookRecordsEventAndDisablesTokensByPolicy` | Stripe 争议 webhook 幂等记录争议事件和 `payment_dispute.created` 审计；开启自动禁用策略后禁用用户已启用 API Key，且不直接改用户额度 |
| `TestStripeDisputeLifecycleUpdatesDisputeFact` | Stripe 争议 created/closed 生命周期写入并更新 `payment_disputes`，按 dispute id 写 `payment_dispute.*` 审计，重复事件不重复审计 |
| `TestAdminPaymentManualAdjustmentRequiresReason` | 支付人工补账/扣回默认要求填写原因，缺少原因不改变用户余额 |
| `TestAdminPaymentManualAdjustmentWritesManualTransactionAndAudit` | 管理员通过支付人工修正接口扣回额度，写 `manual_debit` 流水、关联订单、记录操作者/原因/幂等键并写支付订单审计 |
| `TestAdminPaymentManualRefundMarksOrderAndDeductsQuota` | 管理员通过支付人工退款接口扣回额度，订单置为 `partially_refunded` 或 `refunded`，写 `refund_deduct` 流水、原因、操作者、幂等键和 `payment_refund.manual` 审计 |
| `TestAdminStripeRefundRequestCreatesProviderRefundAndPendingOrder` | 管理员向 Stripe 发起 provider 退款请求，调用 Refund API，写 `payment_refund_requests` 和 `payment_refund.requested` 审计，订单进入 `refund_pending`，后续退款 webhook 收尾为最终退款状态 |
| `TestAdminEpayRefundRequestCreatesProviderRefundAndPendingOrder` | 管理员向易支付发起 provider 退款请求，签名调用配置的退款地址，写 `payment_refund_requests` 和 `payment_refund.requested` 审计，订单进入 `refund_pending`，重复幂等键不重复调用 provider |
| `TestChannelExtendedManagement` | 多 key、多 base URL、模型重写、通道分组、扩展配置、密钥加密 |
| `TestAdminChannelManagementAuditLogs` | 通道创建、测试、拉取模型、编辑、禁用、启用和删除写入 `channel.*` 管理审计，且审计摘要不泄露下游密钥 |
| `TestAdminLogClearWritesAuditLog` | 管理员按 `before` 清理调用日志写入 `log.clear` 审计，并记录清理截止时间 |
| `TestAdminLogExportWritesAuditLogAndRedactsSensitiveFields` | 管理员按过滤条件导出调用日志 CSV 写入 `log.export` 审计，导出内容不包含请求/响应体、IP、错误原文、snapshot 或密钥 |
| `TestSetupBootstrapAdminQuotaAndSettingsDefaults` | 初始化管理员启动额度和 settings 默认值 |
| `TestMetricsEndpointRequiresSettingAndExposesPrometheusText` | `/metrics` 默认关闭，启用 `observability.metrics_enabled` 后返回 Prometheus 文本和基础实例指标 |
| `TestMetricsEndpointIncludesRelayPaymentAndInfrastructureSignals` | `/metrics` 输出 DB/Redis up、调用日志成功/失败计数、Relay 请求数、Relay 错误维度、token 用量、按模型/供应商/用户组的额度消耗、通道可用状态、逐通道错误计数、限流拒绝、计费失败、支付订单、支付事件和审计事件指标 |
| `TestMetricsEndpointReportsIndependentLogDBHealth` | `/metrics` 输出独立日志库配置和 ping 状态，日志库不可用时仍回退主库事实并保持指标可用 |
| `TestRequestIDHeaderUsesConfiguredSetting` | `observability.request_id_header` 修改后，请求 ID 从配置 header 读取并通过同名响应头返回，缺失时生成新 ID |
| `TestSettingsValidationAndReadiness` | settings 类型校验、`server.port`/`server.mode` 边界、request id header 名校验、限流阈值 `0` 禁用语义、JWT/生产 readiness、支付 provider 密钥和关键配置缺失 |
| `TestAdminSettingUpdateWritesAuditLog` | 超级管理员批量更新 settings 后按 key 写 `setting.update` 审计，敏感 payment 配置值不完整泄露 |
| `TestAdminSettingValidationFailureWritesDeniedAuditLog` | 超级管理员提交非法 settings 值时不落库，写 `setting.denied` 审计并脱敏敏感尝试值 |
| `TestSettingDefaultsBackfillPreservesExistingValues` | 启动默认配置回填不会覆盖已有值 |
| `TestSettingCacheRefreshesStaleRedisValues` | settings 读取缓存、单项更新和批量更新后的 Redis 刷新边界 |
| `TestSettingLoadCacheAppliesRequestIDHeaderRuntimeConfig` | 启动加载 settings 时会把已有 `observability.request_id_header` 应用到进程内 request id header 配置 |
| `TestUserRegisterRespectsRegistrationSettings` | 自助注册默认关闭；开启后仍受用户名注册和验证码开关约束，并应用默认额度/分组 |
| `TestUserLoginRespectsLoginMethodSettings` | 用户名密码登录保持可用；email/phone 密码登录默认关闭，开启对应 setting 后已有本地身份可登录 |
| `TestInitRedisSkipsEmptyConfig` | `REDIS_CONN` 为空时不隐式连接本机 Redis，SQLite 单机模式保持可降级 |
| `TestReadinessRequiresRedisForExternalDatabaseMode` | `SQL_DSN` 指向外部数据库且 Redis 不可用时 `/ready` 返回不就绪 |
| `TestReadinessRequiresEncryptionKeyForEncryptedChannelSecrets` | 数据库已有 `enc:v1:` 通道密钥但缺少 `ENCRYPTION_KEY` 时 `/ready` 返回不就绪 |
| `TestInitLogDBUsesConfiguredDSN` | 启动期读取 `LOG_SQL_DSN`，初始化独立日志数据库并迁移 `logs` schema |
| `TestLogServiceWritesMainFactAndExternalLogDB` | 配置独立日志库时，`LogService` 先在主库保留调用/结算事实并更新 API Key 最近使用摘要，再写日志库副本 |
| `TestLogServiceFallsBackWhenExternalLogDBWriteFails` | 独立日志库运行期写入失败时，主库调用事实和基础 `billing_snapshot` 仍可恢复 |
| `TestLogServiceReplaysPendingExternalLogOutbox` | 独立日志库恢复后，LogService 可将 pending outbox 中的主库日志补写到日志库并标记完成 |
| `TestLogServiceWorkerReplaysPendingExternalLogOutbox` | 服务后台补写 worker 会周期性重放 pending outbox |
| `TestLogServiceListsFromExternalLogDBWhenConfigured` | 配置独立日志库时，管理日志列表读取日志库数据 |
| `TestLogServiceListFallsBackToMainDBWhenExternalLogDBFails` | 独立日志库查询失败时，管理日志列表回退读取主库事实 |
| `TestAPIKeyAuthErrorsUseEntryProtocolShape` | Anthropic/Gemini 入口 API Key 鉴权错误外形 |
| `TestAnthropicAndGeminiEntrypointsConvertSuccessAndDegradeFields` | Anthropic/Gemini 非流式成功响应、usage、扣费和非文本 content/parts 降级 |
| `TestGeminiEmbedContentConvertsOpenAIEmbeddingsAndDeductsUsage` | Gemini embedContent 转 OpenAI-compatible Embeddings 上游，返回 Gemini `embedding.values` 外形，usage 写日志和扣费 |
| `TestGeminiBatchEmbedContentsConvertsOpenAIEmbeddingsAndDeductsUsage` | Gemini batchEmbedContents 转 OpenAI-compatible Embeddings 批量 input，上游 embedding list 返回 Gemini `embeddings[].values` 外形，usage 写日志和扣费 |
| `TestRateLimitUsesSettingsAndEntryProtocolErrorShape` | Redis Token 限流读取 `rate_limit.*`，本地 429 不调用上游，返回入口协议兼容错误，并写失败日志和拒绝分支 `policy_snapshot` |
| `TestChatCompletionInvalidRequestDoesNotCallUpstream` | 非法 JSON、缺少 model 在本地失败且不污染通道和账单 |
| `TestRelayMaxRequestBodyBytesRejectsBeforeUpstream` | `relay.max_request_body_bytes` 超限时本地返回 OpenAI-compatible 413 `request_body_too_large`，不调用上游、不扣用户额度或 API Key 预算 |
| `TestChannelRoutingConfigResolution` | `upstreams` 优先、密钥选择归一化、模型重写和真实 Relay 请求不泄密 |
| `TestUserBillingMatchesLogs` | 多次成功/失败混合后，用户账单、日志、余额和 Key 预算一致 |
| `TestChatCompletionSuccessLogsAndDeductsQuota` | Chat 非流式成功调用、request id 上游透传、基础 request_snapshot、基础 policy_snapshot、上游 usage_source、基础 route_snapshot、含 P0 回退表达式/倍率/预算前后摘要的基础 billing_snapshot、日志、用户额度、Key 预算和账单聚合 |
| `TestAzureChatCompletionUsesDeploymentPathAndAPIKey` | Azure OpenAI Chat 基础转发，deployment 路径、`api-version` query、`api-key` header、`model/routerx` 剥离、usage 日志和扣费 |
| `TestAzureCompletionsUsesDeploymentPathAndAPIKey` | Azure OpenAI Legacy Completions 基础转发，deployment 路径、`api-version` query、`api-key` header、`model/routerx` 剥离、usage 日志和扣费 |
| `TestAzureChannelFetchModelsUsesDeploymentsEndpoint` | Azure OpenAI 管理端模型拉取使用 `/openai/deployments`、`api-version` query 和 `api-key` header，并返回 deployment id |
| `TestAzureResponsesUsesV1EndpointAndUsage` | Azure OpenAI Responses 基础转发，`/openai/v1/responses?api-version=preview`、`api-key` header、保留 `model` deployment 名、`routerx` 剥离、`input_tokens/output_tokens/total_tokens` usage 日志和扣费 |
| `TestAzureEmbeddingsUsesDeploymentPathAndAPIKey` | Azure OpenAI Embeddings 基础转发，deployment 路径、`api-version` query、`api-key` header、`model/routerx` 剥离、usage 日志和扣费 |
| `TestAzureImageGenerationsUsesV1EndpointAndMinimumCharge` | Azure OpenAI Image Generations 基础转发，`/openai/v1/images/generations?api-version=preview`、`api-key` header、保留 `model` deployment 名、`routerx` 剥离、无 usage 最低计费日志和扣费 |
| `TestAzureImageEditsMultipartUsesV1EndpointAndMinimumCharge` | Azure OpenAI Image Edits multipart 基础转发，`/openai/v1/images/edits?api-version=preview`、`api-key` header、保留 `model` deployment 名、`routerx` 表单字段剥离、图像/遮罩文件字段保留、无 usage 最低计费日志和扣费 |
| `TestAzureAudioSpeechUsesV1EndpointAndMinimumCharge` | Azure OpenAI Audio Speech 基础转发，`/openai/v1/audio/speech?api-version=preview`、`api-key` header、保留 `model` deployment 名、`routerx` 剥离、音频 Content-Type 透传、无 usage 最低计费日志和扣费 |
| `TestAzureAudioMultipartUsesV1EndpointAndMinimumCharge` | Azure OpenAI Audio Transcriptions/Translations multipart 基础转发，`/openai/v1/audio/transcriptions|translations?api-version=preview`、`api-key` header、保留 `model` deployment 名、`routerx` 表单字段剥离、文件字段保留、无 usage 最低计费日志和扣费 |
| `TestResponsesPassthroughExtractsUsageAndDeductsQuota` | Responses 基础 JSON 透传、`routerx` 剥离、`input_tokens/output_tokens` usage 映射、日志和扣费 |
| `TestEmbeddingsPassthroughExtractsUsageAndDeductsQuota` | Embeddings 基础 JSON 透传、`routerx` 剥离、`prompt_tokens/total_tokens` usage 映射、日志和扣费 |
| `TestModerationsPassthroughUsesMinimumChargeWithoutUsage` | Moderations 基础 JSON 透传、`routerx` 剥离、上游无 usage 时按 P0 最低计费写日志、记录 minimum usage_source、minimum 表达式快照并扣费 |
| `TestUsageMissingStrategyRejectsWithoutDeductingQuota` | `billing.usage_missing_strategy=reject` 时，上游成功但缺少 usage 会返回 `usage_missing`、写 billing 失败日志且不扣费 |
| `TestImageGenerationsPassthroughUsesMinimumChargeWithoutUsage` | Image Generations 基础 JSON 透传、`routerx` 剥离、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestImageMultipartPassthroughUsesRouteAndMinimumCharge` | Image Edits/Variations multipart 表单透传、`routerx` 表单字段剥离与路由偏好、图像/遮罩文件字段保留、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestAudioSpeechPassthroughReturnsBinaryAndUsesMinimumCharge` | Audio Speech 基础 JSON 透传、`routerx` 剥离、二进制音频响应和 Content-Type 透传、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestAudioTranscriptionsMultipartPassthroughUsesRouteAndMinimumCharge` | Audio Transcriptions multipart 表单透传、`routerx` 表单字段剥离与路由偏好、文件字段保留、上游无 usage 时按 P0 最低计费写日志和扣费 |
| `TestRouterXOptionsHeaderRoutesMultipartRequest` | `X-RouterX-Options` header 为 multipart 请求提供路由偏好，且不向真实上游泄露 `routerx` 私有字段 |
| `TestRouterXUpstreamOptionsSupplementRequest` | `routerx.upstream` 安全补充上游 header/query/JSON body，敏感鉴权字段、`model`、`stream` 和原请求已存在字段不会被覆盖，`routerx` 私有字段不会泄露 |
| `TestRouterXProviderOptionsApplyOnlyToSelectedProvider` | `routerx.provider.<provider>` 只在选中 provider 匹配时补充 JSON body 字段，provider 专属补充值优先于通用 upstream 补充值，非选中 provider 参数不泄露 |
| `TestRouterXCompatibleUpstreamPreservesRouterXAndIncrementsHop` | RouterX-Compatible 上游保留 `routerx` 私有字段，转发递增后的 `X-RouterX-Hop`，并追加 `X-RouterX-Chain` 链路摘要 |
| `TestRouterXCompatibleUpstreamRejectsHopLimit` | RouterX-Compatible 上游在 `X-RouterX-Hop` 达到默认上限时本地拒绝且不调用上游 |
| `TestRouterXCompatibleUpstreamUsesConfiguredHopLimit` | `relay.routerx_max_hops` 可收紧 RouterX-Compatible 循环保护上限，达到配置值时本地拒绝、不调用上游且不扣费 |
| `TestChatCompletionStreamForwardsSSEAndDeductsUsage` | OpenAI-compatible Chat SSE chunk 转发、usage 提取、日志和扣费 |
| `TestCompletionsStreamForwardsSSEAndDeductsUsage` | Legacy Completions SSE chunk 转发、`routerx` 剥离、usage 提取、日志和扣费 |
| `TestChatCompletionStreamCancelsUpstreamWhenClientWriteFails` | 客户端写入失败时取消上游 SSE 请求，失败日志不扣费 |
| `TestChatCompletionStreamRejectsNonOpenAISSEUpstream` | 非 OpenAI SSE 通道在流式请求中被上游前拒绝 |
| `TestGeminiStreamGenerateContentConvertsOpenAISSEAndDeductsUsage` | Gemini streamGenerateContent 转 OpenAI-compatible Chat SSE，上游 OpenAI chunk 转 Gemini SSE 事件，usage 扣费和日志 |
| `TestAnthropicMessagesStreamConvertsOpenAISSEAndDeductsUsage` | Anthropic Messages stream 转 OpenAI-compatible Chat SSE，上游 OpenAI chunk 转 Anthropic SSE 事件，usage 扣费和日志 |
| `TestChatCompletionUpstreamBadRequestMapping` | 下游 400 错误映射、失败日志和密钥不泄露 |
| `TestChatCompletionUpstreamErrorStatusMapping` | 下游 401/403/429/5xx 错误映射、失败日志、通道错误计数和不扣费 |
| `TestRelayMaxResponseBodyBytesRejectsOversizedUpstream` | `relay.max_response_body_bytes` 超限时返回 OpenAI-compatible 502 `upstream_response_too_large`，不反射下游响应体、不扣额度，并写失败日志 |
| `TestChatCompletionUpstreamTimeoutMapping` | 下游超时错误映射、失败日志、通道错误计数和不扣费 |
| `TestRelayFailureLogPersistsRequestIDAndErrorCode` | 下游失败时调用日志和用户日志接口持久化 `request_id`、稳定 `error_code`、`error_source` 和 `upstream_status` |
| `TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce` | 非流式默认可重试状态码按 `relay.retry_count` 换候选通道，最终只按成功 usage 扣费一次，并在 route_snapshot 记录实际通道和重试摘要 |
| `TestChatCompletionUsesConfiguredRetryStatuses` | `relay.retry_on_status` 显式加入 400 后，非流式 400 可按 `relay.retry_count` 换候选通道并只按最终成功扣费一次 |
| `TestChatCompletionDoesNotRetryNonRetryableUpstreamStatus` | 默认白名单不含 400 时，下游 400 不触发候选通道重试 |
| `TestChatCompletionSkipsTrippedChannelAtConfiguredThreshold` | `relay.error_ban_threshold` 生效后跳过达到阈值的故障通道 |
| `TestChatCompletionHonorsDisabledAutoBanSetting` | `relay.error_auto_ban=false` 时高 `error_count` 通道仍可参与候选并在成功后恢复计数 |
| `TestAnthropicAndGeminiEntrypointsMapUpstreamErrorsToEntryProtocol` | Anthropic/Gemini 入口下游错误按各自协议外形返回且不泄密、不扣费 |
| `TestRelayPrecheckRejectsBeforeUpstream` | 无效 Key、禁用 Key、额度不足、禁用通道不调用下游 |
| `TestRouterXRoutePreferenceFiltersChannels` | `routerx.route` 被接受、未知字段忽略、非法结构拒绝和筛选后无候选；无候选返回 `no_available_channel` 且写拒绝分支 `policy_snapshot` |

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
| `logs` | `quota_used` | P0 等于 `total_tokens`；缺失 usage 时默认最低值 `1`，`billing.usage_missing_strategy=reject` 时失败且为 `0` |
| `logs` | `status` | 成功为 `1`，失败为 `2` |
| `tokens` | `remain_quota` / `quota_limit` / `quota_used` | 有限 API Key 按 `quota_used` 消耗预算 |
| `users` | `quota` | 有限和无限 API Key 调用成功时均扣减用户额度 |
| `channels` | `error_count` | 成功后清零或保持 0 |

失败调用至少检查：

- 预检失败时下游请求计数为 0。
- 默认不增加 `quota_used`。
- `logs.status=failed`，`request_id` 可关联本次请求，`error_code` 为稳定错误 code，`error_msg` 是脱敏摘要。
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
| 400 | 默认返回兼容 400，不重试；显式加入 `relay.retry_on_status` 后可按非流式重试规则换候选 |
| 401/403 | 返回兼容 502 或配置错误摘要，不重试，增加通道错误计数 |
| 429 | 默认在 `relay.retry_on_status` 中，`relay.retry_count > 0` 时非流式可换候选通道，否则返回兼容上游错误 |
| 500/502/503/504 | 默认在 `relay.retry_on_status` 中，`relay.retry_count > 0` 时非流式未写出前可重试候选通道 |
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
| API Key scope 未允许模型、APIType、通道分组、入口协议、IP 或方法路径 | 403，下游计数 0 |
| API Key scope 达到日/月预算 | 429，下游计数 0 |
| API Key scope 达到并发上限 | 429，下游计数 0 |
| API Key scope 达到 RPM/TPM 上限 | 429，下游计数 0 |

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
- 调用日志 `route_snapshot.model_rewrite` 记录客户端模型和上游模型。
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
| P1 | 调用事实快照 | 调用日志已覆盖 request_id、error_code、error_source、upstream_status、基础 request_snapshot、成功、API Key scope 拒绝、基础余额预检拒绝、用户分组访问控制拒绝、无可用候选拒绝、Redis Token 限流拒绝和 usage 缺失拒绝分支 policy/billing 事实，基础 usage_source、含过滤/模型重写/重试摘要的基础 route_snapshot 和含价格表达式或 P0 回退表达式/规则版本/倍率/预算前后摘要的基础 billing_snapshot；继续补完整 route、usage、完整 billing、error 快照脱敏和历史解释 |
| P1 | 计费规则 | 价格表达式、倍率、访问控制、规则快照和历史账单解释 |
| P1 | 可靠性 | 已覆盖非流式安全重试、Redis 全局/IP/Token 基础限流和 `error_count` 自动熔断候选过滤；继续补半开恢复、探测任务、更多限流维度和生产 fail-open/fail-closed 策略 |
| P1 | 运行模式 | 已覆盖 `REDIS_CONN` 为空不隐式连接本机 Redis、SQLite 单镜像无 Redis 可运行、外部数据库无 Redis 时 `/ready` 不就绪 |
| P1 | 通道候选缓存 | 已覆盖进程内缓存命中、`routing.channel_cache.preload` 启动预热/关闭 no-op/通道变更后预热、`routing.channel_cache.version` 变化后回源、默认 settings 和非法配置校验；继续补 Redis 共享快照和集群实例广播失效 |
| P1 | 独立日志数据库 | 已覆盖 `LOG_SQL_DSN` 初始化、日志库副本写入、运行期写入失败时主库事实可恢复、主库 outbox 异步补写、管理日志列表读取日志库、查询失败回退主库和日志库健康指标；继续补冷热归档策略 |
| P2 | 企业账号 | OAuth/OIDC state、nonce、subject 绑定、禁止 email 自动接管 |
| P2 | 高级 API Key 管理 | 基础生命周期审计、轮换、泄露上报、单 Key 用量摘要、最近使用来源摘要、管理员跨用户查询、批量禁用、批量过期、基础风险视图、模型/APIType/通道分组/入口协议/IP/方法路径 allow-list scope、日/月预算拒绝、并发上限拒绝和 RPM/TPM 拒绝已覆盖；缓存失效和更完整泄露窗口分析待补 |
| P2 | 支付充值 | 充值码批次/备注/过期策略、Stripe Checkout Session 创建、Stripe/易支付 provider 退款请求、Stripe/易支付签名、金额校验、订单状态、重复回调幂等、额度流水、webhook 入账审计、Stripe 全额/部分退款和扣回审计、Stripe 争议生命周期和可选 API Key 禁用审计、支付人工补账/扣回审计、支付人工退款落账审计；更多 provider 自动退款适配待补 |
| P2 | 观测审计 | API Key 管理、用户管理、支付商品管理、settings 更新和校验拒绝、用户调额、充值码管理、通道管理、管理员账号管理、日志清理/导出审计、调用日志 request_id/error_code/usage_source/error_source/upstream_status 和基础 `/metrics`、HTTP 请求量/耗时、Relay/上游耗时、Relay 请求/错误/token/通道/限流/计费/支付/审计/DB/Redis/日志库指标测试已覆盖；继续补更完整结构化日志、更多管理审计动作和生产 `/ready` |

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
