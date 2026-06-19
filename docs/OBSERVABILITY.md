# RouterX 观测与审计设计

本文档是 RouterX 日志、指标、审计、告警和数据保留的权威设计入口。它不设计网页看板，只定义控制台、接口、数据表和运维系统需要承载的事实。

观测目标：

- 小白用户能看到“第一次调用是否成功、用了多少额度、失败该看哪里”。
- 管理员能解释用户、API Key、通道、模型、错误和额度之间的关系。
- 技术用户能追踪路由偏好、上游错误、重试、熔断和计费快照。
- 运维人员能用指标和日志判断系统是否适合继续接流量。

## 当前代码基础

当前代码已经具备以下基础：

- `logs` 表保存模型调用日志，包含 user、token、channel、model、usage、usage_source、quota、status、request_id、error_code、error_source、upstream_status、error 和 IP。
- `LogService.Record` 写入调用日志。
- `GET /v0/user/log` 查询当前用户日志。
- `GET /v0/user/billing` 聚合当前用户成功调用的次数、token 和额度。
- `GET /v0/admin/log` 查询全局调用日志。
- `GET /v0/admin/log/export` 按过滤条件导出脱敏 CSV 调用日志。
- `DELETE /v0/admin/log` 要求 `before` 时间范围后清理日志。
- `GET /v0/admin/dashboard` 返回用户数、通道数、API Key 数、当日调用、当日额度和可用通道数。
- `GET /v0/admin/channel` 返回计算型通道健康摘要，包含 `health_status`、`health_reason` 和 `cooldown_remaining_seconds`，便于把手工状态、熔断阈值和冷却窗口区分开。
- `admin_audit_logs` 表保存基础管理审计日志，字段包含 actor、action、resource、before/after 摘要、request_id、IP 和 User-Agent。
- `GET /v0/admin/audit` 已注册为超级管理员查询接口，支持按 `action`、`resource_type`、`resource_id`、`actor_user_id`、`result`、`error_code` 和时间范围过滤。
- `GET /metrics` 已注册为 Prometheus 文本指标接口，默认由 `observability.metrics_enabled=false` 关闭；启用后暴露用户数、API Key 数、通道数、可用通道数、当日调用/额度、ready、DB/Redis/日志库 up、DB/Redis 错误计数、日志补写 outbox 状态、HTTP 请求量和耗时、调用日志状态、Relay 请求数、Relay 错误维度、token 用量、按模型/供应商/用户组的额度消耗、API Key 鉴权、生命周期、最近使用年龄、可用有限额度、轮换和泄露事件、逐通道可用状态、逐通道错误计数、后台熔断探测结果计数、限流拒绝、计费失败、支付订单、支付事件和审计事件指标。
- API Key 创建、编辑、禁用、删除、scope 更新、批量禁用、批量过期、批量操作缺少筛选条件拒绝和用户端额度/无限标记编辑拒绝会写入 `api_key.*` 管理审计摘要，完整 Key 明文和哈希不会写入审计摘要。
- 普通用户成功登录、创建、编辑、禁用、删除和拒绝角色变更会写入 `user.*` 管理审计摘要，密码和 JWT 不会写入审计摘要。
- 支付商品创建、更新、启用和禁用会写入 `payment_product.*` 管理审计摘要。
- 系统模型价格创建、更新、启用和禁用会写入 `model_price.*` 管理审计摘要，`resource_id` 使用模型名便于直接过滤。
- 通道模型价格覆盖创建、更新、启用和禁用会写入 `channel_model_price.*` 管理审计摘要，`resource_id` 使用 `channel_id:model` 便于直接过滤。
- `PUT /v0/admin/setting` 批量更新成功后会按 key 写入 `setting.create` 或 `setting.update` 管理审计摘要；校验失败会写 `setting.denied`，敏感值只保存脱敏摘要。
- `PATCH /v0/admin/user/:id/quota` 调整普通用户额度时会写入 `user.quota_update` 管理审计摘要，并关联调额原因。
- 充值码生成、导入、作废和兑换会写入 `redem_code.*` 管理审计摘要，完整兑换码不会明文写入审计摘要。
- 支付订单创建会写入 `payment_order.create` 管理审计摘要，checkout URL 不会写入审计摘要。
- 支付 provider 成功回调会写入 `payment_webhook.processed` 和 `payment_order.paid` 管理审计摘要；provider 明确失败通知会写入 `payment_webhook.failed`；管理端向 Stripe 或易支付发起 provider 退款请求会写入 `payment_refund.requested`；Stripe 全额或部分退款会写入 `payment_refund.processed`，自动扣回成功时写入 `payment_refund.deducted`；Stripe 争议/拒付生命周期会写入 `payment_dispute.created`、`payment_dispute.updated`、`payment_dispute.closed` 或 `payment_dispute.funds_changed`，并在自动禁用策略开启时记录禁用的 API Key 数量。
- 支付相关人工补账/扣回会写入 `payment_manual_adjust.credit` 或 `payment_manual_adjust.debit` 管理审计摘要，并记录原因、幂等键和前后余额；人工退款会写入 `payment_refund.manual`，记录订单、退款额度、订单状态、原因、幂等键和前后余额。
- 通道创建、编辑、启用、禁用、删除、测试和拉取模型会写入 `channel.*` 管理审计摘要，下游密钥只记录数量或是否配置。
- 管理员账号创建、编辑、禁用、删除和超级管理员权限拒绝会写入 `admin.*` 管理审计摘要，密码不会写入审计摘要。
- 管理员按 `before` 清理调用日志会写入 `log.clear` 管理审计摘要，记录清理截止时间。
- 管理员导出调用日志会写入 `log.export` 管理审计摘要，记录过滤条件、导出上限和导出条数，CSV 不包含请求/响应体、IP、错误原文或 snapshot。
- HTTP Logger 中间件已经记录基础访问日志。

这些能力构成 P0 的可见闭环，并补上了 API Key 管理、API Key 批量操作拒绝、用户管理、支付商品管理、系统/通道模型价格管理、支付入账/明确失败/退款回调、Stripe/易支付 provider 退款请求、Stripe 争议生命周期、支付人工修正与人工退款、settings 变更、用户调额、充值码管理、通道管理、管理员账号管理、日志清理和日志导出审计的基础切片。当前已提供可配置 HTTP/Panic JSON line 结构化日志；商业级增强需要继续补更完整的审计覆盖、结构化字段、指标、告警、追踪和保留策略。

## 观测事实分层

| 层级 | 事实类型 | 主要读者 | 权威来源 |
|------|----------|----------|----------|
| L1 | HTTP 访问事实 | 运维、开发 | 应用结构化日志 |
| L2 | 模型调用事实 | 用户、管理员、计费 | `logs` 表或后续调用事实表 |
| L3 | 请求、策略、路由和错误事实 | 管理员、技术用户 | `logs.request_snapshot`、`logs.policy_snapshot`、`logs.route_snapshot`、`error_code`、Relay 结构化日志 |
| L4 | 计费事实 | 用户、运营、财务 | `logs.quota_used`、计费快照、账单聚合 |
| L5 | 管理审计事实 | 超级管理员、运维、安全 | 审计日志表 |
| L6 | 指标和告警事实 | 运维、SRE | `/metrics`、日志聚合、ready 状态 |

规则：

- 用户账单不得重新解释历史规则，只能聚合调用事实或明确账本事实。
- 审计日志不能替代模型调用日志，模型调用日志也不能替代管理审计。
- 指标用于趋势和告警，不作为账单权威来源。
- 请求和响应 body 默认不记录；开启后必须脱敏、截断和控制保留时间。

## Request ID 和链路追踪

每个请求应拥有稳定 `request_id`，用于关联 HTTP 访问日志、模型调用日志、管理审计日志和错误响应。

目标规则：

| 项目 | 要求 |
|------|------|
| 来源 | 默认优先读取 `X-Request-Id`；可通过 `observability.request_id_header` 改为其他合法 HTTP header；缺失时服务端生成。 |
| 响应 | 所有 HTTP 响应通过当前配置的 request id header 返回请求 ID。 |
| 上下文 | Gin context 中写入 `request_id`。 |
| `/v1` 上游 | 调用真实上游时传递或生成可追踪 request id，避免覆盖上游鉴权 header。 |
| 多层 RouterX | 传递 `X-RouterX-Hop` 和 `X-RouterX-Chain`，防止循环并保留链路摘要。 |
| 日志 | HTTP 日志、调用日志、审计日志和系统错误日志都写 request_id。 |

当前 HTTP 中间件会按 `observability.request_id_header` 读取或生成请求 ID，并通过同名响应头返回；模型调用日志和管理审计已持久化 `request_id`；`/v1` 调用真实上游时会用当前配置的 request id header 透传同一个请求 ID。`observability.structured_logs_enabled=false` 时维持原文本 HTTP/Panic 日志；设为 `true` 后 HTTP 访问日志输出一行 JSON，包含 `event=http_request`、`request_id`、method、path、path_group、status、latency_ms 和 client_ip，Recovery 系统错误日志输出 `event=panic`、request_id、method、path、client_ip、panic_type 和 stack，但不会写入原始 panic 值；`/v1` panic 响应会复用入口协议识别，分别返回 OpenAI、Anthropic 或 Gemini 兼容的 500。跨实例追踪仍需按目标规则继续补齐。

## 模型调用日志

日志库边界：

- 默认写入主业务数据库。
- 配置 `LOG_SQL_DSN` 后，启动时初始化独立日志数据库的 `logs` schema；模型调用日志、调用事实快照和历史诊断数据会经由主库 `log_replication_outboxes` 补写到独立日志数据库，管理端日志列表、日志清理和看板今日调用/额度优先读取日志库，列表与看板查询失败时回退读取主库事实。
- 扣费所需的最小结算事实必须保留在主库或主库 outbox，不能只存在独立日志库。
- 当前实现会先在主库事务内保存调用事实、更新 API Key 最近使用摘要并创建日志补写 outbox，再写独立日志库副本；运行期日志库写入失败会保留 pending outbox、主库事实并写应用告警，后台 worker 会在恢复后重放。
- `/metrics` 暴露 `routerx_log_db_configured`、`routerx_log_db_up` 和 `routerx_log_replication_outbox_items{status}`；未配置独立日志库时 `routerx_log_db_configured=0`，日志存储健康随主库状态判断。
- 独立日志库主要服务查询、归档、清理和备份，不替代用户余额与 Key 预算的事务事实。

当前 `logs` 字段：

| 字段 | 语义 |
|------|------|
| `user_id` | 调用所属用户 |
| `token_id` | 调用使用的 API Key |
| `channel_id` | 选中的通道，预检失败或未选中时可为空 |
| `model` | 调用方请求模型 |
| `prompt_tokens` / `completion_tokens` / `total_tokens` | usage |
| `usage_source` | usage 来源；当前已落地 upstream 和 minimum |
| `quota_used` | 本次消耗额度 |
| `status` | 成功或失败 |
| `request_id` | 串联访问日志、调用日志和审计 |
| `error_code` | `docs/ERRORS.md` 中的稳定 code；成功调用为空 |
| `error_source` | 失败来源，例如 request、auth、quota、route、channel、upstream、billing、system |
| `upstream_status` | 上游 HTTP 状态；未调用上游或非上游错误时为空/0 |
| `content` / `response` | 默认为空；显式开启 body 日志并配置正数上限后，保存截断和脱敏后的非流式请求/响应片段 |
| `request_snapshot` | 脱敏请求快照；当前包含入口协议、API 类型、请求模型、stream 标记和安全路由摘要 |
| `policy_snapshot` | 脱敏策略快照；当前包含成功 allow、额度预检、基础 scope allow、API Key scope 拒绝、基础余额预检拒绝、用户分组 x 通道分组访问控制拒绝、无可用候选 `no_available_channel` 拒绝、`breaker_snapshot`，以及 Redis 全局/IP/Token/User/Model/Channel 限流拒绝摘要和 `rate_limit_snapshot` |
| `route_snapshot` | 脱敏路由快照；当前包含基础选择事实、候选过滤原因、模型重写摘要和非流式重试摘要 |
| `billing_snapshot` | 脱敏计费快照；当前包含结算状态、usage_source、价格表达式或 P0 回退表达式摘要、规则 ID/版本、倍率摘要、Key 预算前后、用户余额前后和最终扣费；扣费失败时包含试算额度和失败原因 |
| `content` / `response` | 截断和脱敏后的请求/响应快照 |
| `error_msg` | 脱敏错误摘要 |
| `ip` | 调用方 IP |
| `created_at` | 调用时间 |

商业级目标字段以 `docs/SNAPSHOTS.md` 的调用事实快照契约为统一语义来源；本文只说明它们在日志、审计、指标和保留策略中的使用方式。

| 字段 | 语义 |
|------|------|
| `upstream_provider` | 实际上游 provider |
| `upstream_model` | 模型重写后的上游模型 |
| `billing_snapshot.expression` / `billing_snapshot.multiplier` | 商业价格规则版本和业务倍率摘要 |
| `multiplier_snapshot` | 用户分组倍率、通道分组倍率、组合覆盖倍率、倍率模式和最终 `effective_ratio` |
| `access_rule_snapshot` | 访问控制事实 |
| `breaker_snapshot` | 自动熔断过滤事实和被挡通道摘要 |
| `key_budget_snapshot` | API Key 最大消耗额度、调用前后剩余预算或累计已用 |
| `retry_count` | 本次调用重试次数 |
| `latency_ms` | RouterX 端到端耗时 |
| `upstream_latency_ms` | 上游调用耗时 |

写入规则：

- 成功调用必须写 success 日志并记录 `quota_used`。
- 预检拒绝可以写 failed 日志，且 `channel_id` 可为空。
- 上游已调用但失败时，应写 failed 日志，记录上游状态和脱敏摘要。
- 扣费失败必须写 failed 日志，并记录 `billing_status=failed`、`attempted_quota_used` 和 `deduction_error_code`，便于账单核查。
- 日志写入失败本身需要系统错误日志和指标，不能静默吞掉。

## 管理审计日志

管理审计记录“谁在什么时候改变了什么”。它不保存完整密钥和完整请求体，只保存可复核摘要。

推荐字段：

| 字段 | 说明 |
|------|------|
| `id` | 主键 |
| `request_id` | 关联请求 |
| `actor_user_id` | 操作人 |
| `actor_role` | 操作人角色 |
| `action` | 动作 |
| `resource_type` | 资源类型 |
| `resource_id` | 资源 ID |
| `before_summary` | 修改前摘要，敏感值脱敏 |
| `after_summary` | 修改后摘要，敏感值脱敏 |
| `result` | success、failed、denied |
| `error_code` | 失败或拒绝时的 code |
| `ip` | 操作 IP |
| `user_agent` | 操作客户端摘要 |
| `created_at` | 时间 |

必须审计的动作：

| 资源 | 动作 |
|------|------|
| 用户 | 成功登录、创建、禁用、删除、调整额度、修改角色、修改分组 |
| 管理员 | 创建、编辑、删除、禁用、权限拒绝 |
| API Key | 创建、编辑、禁用、删除、scope 更新、用户端额度或无限标记编辑拒绝、调整额度或无限标记、批量操作 |
| 通道 | 创建、编辑、启用、禁用、删除、测试、拉取模型 |
| settings | 修改、批量修改、类型校验失败、高风险配置拒绝 |
| 计费 | 模型价格、通道价格、倍率、访问控制、规则版本变更 |
| 支付 | 商品变更、订单创建、回调接收、入账、退款、人工修正 |
| 企业身份 | OAuth/OIDC provider 变更、绑定、解绑、恢复账号 |
| 日志 | 管理员清理日志、导出数据 |

## 指标目录

指标命名建议使用 Prometheus 风格。

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `routerx_http_requests_total` | counter | method、path_group、status | HTTP 请求数 |
| `routerx_http_request_duration_seconds` | histogram | method、path_group | HTTP 耗时 |
| `routerx_relay_requests_total` | counter | protocol、api_type、model、status | Relay 请求数 |
| `routerx_relay_errors_total` | counter | protocol、api_type、error_code、source | Relay 错误数 |
| `routerx_logs_total` | counter | status | 当前已落地的调用日志状态计数 |
| `routerx_relay_duration_seconds` | histogram | protocol、api_type、provider | Relay 总耗时 |
| `routerx_upstream_duration_seconds` | histogram | provider、channel_id、status | 上游耗时 |
| `routerx_tokens_used_total` | counter | model、provider、usage_source | 模型 token 用量 |
| `routerx_quota_used_total` | counter | model、provider、user_group | 额度消耗 |
| `routerx_api_key_auth_total` | counter | result、reason | API Key 鉴权成功和失败趋势 |
| `routerx_api_key_active_total` | gauge | status | API Key 启用、禁用和过期状态计数 |
| `routerx_api_key_last_used_age_seconds` | histogram | status | 已有最近使用时间的 API Key 距离上次使用的秒数 |
| `routerx_api_key_quota_remaining` | gauge | user_group、key_type | 当前可用且未过期的有限额度 API Key 剩余额度；无限额度 Key 不混入该指标 |
| `routerx_api_key_rotation_total` | counter | reason | API Key 轮换记录计数 |
| `routerx_api_key_leak_events_total` | counter | source | 禁用原因为泄露相关的 API Key 事件计数 |
| `routerx_channel_available` | gauge | channel_id、provider | 通道可用状态 |
| `routerx_channel_error_count` | gauge | channel_id、provider | 通道连续错误数 |
| `routerx_channel_probe_total` | counter | result | 后台熔断探测结果计数；只使用 `success`/`failed` 低基数标签，不按 channel_id 拆分 |
| `routerx_rate_limit_rejections_total` | counter | dimension | 限流拒绝次数 |
| `routerx_billing_failures_total` | counter | reason | 计费失败次数 |
| `routerx_payment_orders_total` | gauge | provider、status | 当前已落地的支付订单状态计数 |
| `routerx_payment_events_total` | gauge | provider、event_type、processed | 支付事件处理状态 |
| `routerx_audit_events_total` | counter | action、resource_type、result | 审计事件数 |
| `routerx_db_up` | gauge | 无 | 当前已落地的数据库 ping 状态 |
| `routerx_redis_up` | gauge | 无 | 当前已落地的 Redis ping 状态；未配置 Redis 时为 0 |
| `routerx_log_db_configured` | gauge | 无 | 独立日志库配置状态；配置 `LOG_SQL_DSN` 且与主库不同为 1 |
| `routerx_log_db_up` | gauge | 无 | 日志存储 ping 状态；未配置独立日志库时跟随主库状态 |
| `routerx_log_replication_outbox_items` | gauge | status | 主库日志补写 outbox 当前条数；status 归一为 `pending`、`completed`、`failed` 或 `unknown` |
| `routerx_redis_errors_total` | counter | operation | 当前已落地的 Redis 错误数；`operation=ping` 表示健康探测失败 |
| `routerx_db_errors_total` | counter | operation | 当前已落地的 DB 错误数；`operation=ping`、`log_ping`、`migration_status` 分别表示主库 ping、日志库 ping 和迁移状态读取失败 |
| `routerx_ready` | gauge | reason | 就绪状态，1 为 ready，0 为 not ready |

标签控制：

- 不把 user_id、token_id、API Key、完整 model 动态长尾、prompt 或错误全文作为高基数指标标签。
- 高基数事实进入日志或审计，不进入 metrics label。
- `channel_id` 可用于内部部署；如通道数量大，应改用 provider、channel_group 或采样。

## 告警建议

| 告警 | 触发条件 | 处理动作 |
|------|----------|----------|
| 服务不就绪 | `routerx_ready=0` 持续超过阈值 | 看 `/ready` 原因、DB、Redis、JWT、ENCRYPTION_KEY 和迁移状态 |
| 5xx 增高 | HTTP 5xx 比例持续升高 | 看系统错误日志和 DB/Redis/日志库指标 |
| Relay 错误增高 | `routerx_relay_errors_total` 按 error_code 异常增长 | 按 `docs/ERRORS.md` 分类，并按 `docs/RUNBOOKS.md` 执行 |
| 上游 401/403 | 某通道上游认证或权限错误出现 | 检查通道密钥和上游账号权限 |
| 上游 429 | provider 或通道限流持续出现 | 降低并发、调整路由、增加通道 |
| 计费失败 | `routerx_billing_failures_total` 增长 | 停止相关调用，核对日志和余额事务 |
| API Key 轮换建议 | `/v0/admin/token/risk` 返回 `rotation_recommended=true` | 轮换对应 Key，排查泄露来源并保留工单证据 |
| 日志写入失败 | 调用成功但日志写入失败 | 保护账单事实，检查 DB 和事务 |
| 日志补写积压 | `routerx_log_replication_outbox_items{status="pending"}` 持续增长，或 `status="failed"` 大于 0 | 检查 `LOG_SQL_DSN`、日志库连接、迁移状态和后台 worker |
| 支付回调失败 | 支付签名、金额或状态校验失败增长 | 检查 provider 配置和恶意回调 |
| 审计写入失败 | 管理操作成功但审计失败 | 暂停高风险管理操作或进入降级 |

## 数据保留和隐私

| 数据 | 默认建议 | 说明 |
|------|----------|------|
| HTTP 访问日志 | 30 天 | 聚合后可长期保存趋势 |
| 模型调用日志 | 90 天 | 账单争议期内保留；大规模生产按月分区 |
| 计费快照 | 账单周期 + 争议期 | 不能早于账单事实删除 |
| 管理审计日志 | 至少 180 天 | 高风险操作建议更久 |
| 支付事件 | 按财务要求长期保存 | 原始 payload 脱敏或加密 |
| 请求/响应 body 快照 | 默认关闭 | 开启时短保留、强脱敏 |
| 指标时序 | 30-180 天 | 取决于 Prometheus 或外部系统 |

隐私规则：

- prompt、响应、Authorization、Cookie、API Key、上游密钥、支付密钥默认不进入日志。
- 用户请求导出或删除个人数据时，需要保留账单、审计和风控所需的最小事实。
- 日志导出必须进入管理审计。

## 控制台和接口能力

本文不设计页面，只定义控制台或接口需要承载的能力：

| 能力 | 要求 |
|------|------|
| 用户日志 | 用户只能看自己的调用记录、状态、模型、usage、额度和错误摘要。 |
| 用户账单 | 聚合成功调用，展示 token、额度和时间范围。 |
| 管理日志 | 管理员可按用户、API Key、通道、模型、状态、时间筛选。 |
| 通道健康 | 展示通道状态、错误计数、最近错误、延迟和最近成功时间。 |
| 审计查询 | 超级管理员可按 actor、资源、动作、结果、错误 code 和时间范围查询。 |
| 指标接口 | `/metrics` 可由 Prometheus 抓取；当前由 `observability.metrics_enabled` 控制启用。 |
| 诊断详情 | 单次调用能关联 request_id、error_code、基础 request_snapshot、基础 policy_snapshot、含过滤/模型重写/重试摘要的基础 route_snapshot 和含价格表达式或 P0 回退表达式、规则版本、业务倍率、预算前后的基础 billing_snapshot；更完整结构化失败事实仍需补齐。 |

## 阶段边界

| 阶段 | 目标 |
|------|------|
| P0 | 调用日志、用户日志、管理员日志、基础账单和基础 dashboard 可用；body 日志默认关闭。 |
| P1 | 已补调用日志 request_id、error_code、usage_source、error_source、upstream_status、基础 request_snapshot、覆盖成功、API Key scope 拒绝、基础余额预检拒绝、用户分组访问控制拒绝、无可用候选拒绝、`breaker_snapshot` 和 Redis 全局/IP/Token/User/Model/Channel 限流拒绝分支的基础 policy_snapshot 与 `rate_limit_snapshot`、含过滤/模型重写/重试摘要的基础 route_snapshot、含价格表达式或 P0 回退表达式/规则版本/业务倍率/预算前后摘要的基础 billing_snapshot，并可配置输出 HTTP/Panic JSON line 日志；继续补更完整结构化失败事实。 |
| P2 | 扩展管理审计覆盖、更多 Prometheus 指标、告警、长期保留、导出审计和生产 readiness 指标。 |

## 测试要求

| 测试方向 | 断言 |
|----------|------|
| 成功调用日志 | success 日志包含 user、token、channel、model、usage、quota_used。 |
| 失败调用日志 | failed 日志包含 request_id、error_code、error_source、upstream_status 和脱敏 error_msg，预检失败不调用上游。 |
| 用户日志隔离 | 用户只能看到自己的日志。 |
| 管理日志筛选 | 管理员可按 user、token、channel、model、status、时间筛选。 |
| 账单一致 | 用户账单聚合等于成功日志事实；启用独立日志库时主库结算最小事实可恢复。 |
| 脱敏 | 日志和导出不包含 API Key、上游密钥、DSN、支付密钥。 |
| 审计 | 高风险管理操作写审计，失败和拒绝也有摘要；当前已覆盖成功登录、API Key 管理和 scope 更新、用户管理、支付商品管理、settings 更新与校验拒绝、用户调额、充值码管理、通道管理、管理员账号管理、日志清理和日志导出操作。 |
| 指标 | `/metrics` 暴露基础实例、HTTP 请求量/耗时、Relay 日志、Relay 请求数、Relay/上游耗时、Relay 错误维度、token 用量、按模型/供应商/用户组的额度消耗、API Key 鉴权/生命周期/最近使用/额度/轮换/泄露指标、通道可用状态、逐通道错误计数、后台熔断探测结果计数、限流拒绝、计费失败、日志补写 outbox、支付、审计和 DB/Redis/日志库 up 与 DB/Redis 错误计数指标，不包含高基数或敏感 label；后续继续补更细错误维度和告警。 |

## 文档同步

观测和审计改动需要同步检查：

- `docs/POLICIES.md`：策略快照、访问控制、限流、路由偏好、倍率和审计事件。
- `docs/SNAPSHOTS.md`：调用事实快照封套、字段边界、脱敏和历史解释规则。
- `docs/API_KEYS.md`：API Key 最近使用、审计事件、轮换、泄露处理和指标。
- `docs/PROTOCOLS.md`：protocol、api_type、provider、能力等级和字段降级相关日志/指标维度。
- `docs/API.md`：日志、dashboard、审计和 metrics 接口。
- `docs/DATA_MODEL.md`：日志、审计、快照和索引字段。
- `docs/ERRORS.md`：error_code、日志事实和排障语义。
- `docs/SECURITY.md`：脱敏、审计和事故响应。
- `docs/PAYMENTS.md`：支付事件、额度流水、退款、人工修正和支付指标。
- `docs/RUNBOOKS.md`：告警触发后的检查顺序、证据和安全动作。
- `docs/SETTINGS.md`：观测相关 settings。
- `docs/OPERATIONS.md`：指标、告警、故障处理、保留和备份。
- `docs/TESTING.md`：日志、审计和指标测试。
- `docs/TRACEABILITY.md`：观测和审计能力验收。
