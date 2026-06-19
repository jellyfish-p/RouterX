# RouterX 调用事实快照契约

## 目标

调用事实快照用于回答一次模型调用的核心问题：

- 谁发起了请求，用了哪个 API Key。
- 请求进入时是什么协议、API 类型和模型。
- 哪些策略允许或拒绝了本次调用。
- 为什么选择或跳过某个通道。
- usage 从哪里来，是否经过估算或最低计费。
- 价格、倍率和扣费事务为什么得到当前 `quota_used`。
- 失败发生在哪一层，是否调用过上游，是否产生扣费。

它是路由、策略、计费、错误、观测和排障之间的共同事实语言，不替代 `docs/POLICIES.md`、`docs/BILLING.md`、`docs/OBSERVABILITY.md` 或 `docs/DATA_MODEL.md`。专题文档负责定义各自模块规则；本文负责定义一次调用落到日志和审计中时应保留哪些可解释事实。

## 非目标

- 不保存完整 prompt、响应正文、Authorization、Cookie、上游密钥、数据库 DSN、支付密钥或其他敏感原文。
- 不要求 P0 立即具备完整 JSON 快照字段；P0 可先保证基础日志、额度扣减和错误摘要一致。
- 不设计网页页面，也不定义控制台布局。
- 不重新计算历史账单；历史调用按当时写入的快照和日志事实解释。

## 阶段边界

| 阶段 | 快照要求 |
|------|----------|
| P0 | 以现有 `logs` 基础字段为主，保证 user、token、channel、model、usage、`quota_used`、status、脱敏错误和扣费事务一致。可用文本摘要记录路由和错误原因。 |
| P1 | 增加结构化 `request_id`、`error_code`、`request_snapshot`、`route_snapshot`、`policy_snapshot`、`usage_snapshot`、`billing_snapshot`、`usage_source` 和关键版本摘要。 |
| P2 | 增加审计关联、导出、长期保留、指标追踪、企业账号维度和生产事故证据链。 |

P0 不能因为快照字段尚未完整而牺牲账单一致性。P1/P2 不能用当前 settings 或当前价格规则倒推历史调用。

当前代码已在 `logs` 中基础落地 `request_id`、`error_code`、`error_source`、`upstream_status`、`usage_source`、基础 `request_snapshot`、基础 `policy_snapshot`、基础 `route_snapshot` 和基础 `billing_snapshot`。`request_snapshot` 目前覆盖 request_id、入口协议、API 类型、请求模型、stream 标记、安全的 `routerx.route` 摘要，以及 Anthropic/Gemini 基础字段降级摘要 `adapter_degradations`。`policy_snapshot` 目前覆盖成功 allow、额度预检、基础 scope allow 摘要，API Key scope 的模型/API 类型/通道分组/入口协议/IP/方法/日预算/月预算/并发/RPM/TPM 拒绝摘要，基础余额预检拒绝摘要，用户分组 x 通道分组访问控制拒绝摘要，无可用候选或通道硬过滤的 `no_available_channel` 拒绝摘要，以及 Redis 全局/IP/Token/User/Model/Channel 限流拒绝摘要；限流拒绝还会写 `rate_limit_snapshot`，记录维度、分钟窗口、阈值、当前计数、剩余和拒绝决策；因 `health_blocked` 无可用候选时还会写 `breaker_snapshot`，记录熔断决策、阈值、冷却窗口和被挡通道摘要。`route_snapshot` 目前覆盖请求模型、候选数量、候选过滤原因、选中通道、provider、分组、优先级、权重、模型重写摘要和非流式重试摘要。`billing_snapshot` 目前覆盖结算状态、usage_source、价格表达式或 P0 回退表达式摘要、价格规则 ID/版本、倍率快照、Key 预算前后、用户余额前后和最终扣费；更完整访问控制快照和更多失败事实仍需继续补齐。`usage_source` 目前覆盖 `upstream` 与 `minimum`，`adapter`、`tokenizer` 和 `estimate` 需要随对应能力实现后再写入。

## 通用封套

结构化快照使用统一封套，便于后续做 schema 校验、版本迁移和审计导出。

```json
{
  "schema": "routerx.snapshot.v1",
  "kind": "route",
  "request_id": "req_...",
  "stage": "p1",
  "source": "relay",
  "created_at": "2026-06-12T00:00:00Z",
  "redacted": true
}
```

| 字段 | 说明 |
|------|------|
| `schema` | 快照 schema 版本，变更字段含义时必须升级。 |
| `kind` | `request`、`policy`、`route`、`usage`、`billing`、`rate_limit`、`error`、`settings`、`audit`。 |
| `request_id` | 串联访问日志、调用日志、错误响应和管理审计。 |
| `stage` | 写入该快照的目标阶段，如 `p0`、`p1`、`p2`。 |
| `source` | 写入模块，如 `middleware`、`policy`、`relay`、`billing`、`rate_limit`。 |
| `created_at` | 快照创建时间。 |
| `redacted` | 必须为 `true`，表示已执行脱敏和截断。 |

## 请求上下文

`request_snapshot` 描述调用进入 RouterX 时的安全上下文和协议上下文。

| 字段 | 说明 |
|------|------|
| `user_id` | 调用所属用户 ID。 |
| `token_id` | API Key 记录 ID，不记录完整 API Key。 |
| `token_prefix` | API Key 存储哈希的安全前缀，格式为 `sha256:<prefix>`；不记录完整 API Key。 |
| `ingress_protocol` | 入口协议，如 `openai`、`anthropic`、`gemini`。 |
| `api_type` | API 类型，如 chat、responses、embeddings。 |
| `requested_model` | 调用方请求模型。 |
| `stream` | 是否为流式请求。 |
| `trace_id` | 从合法 W3C `traceparent` 提取的 trace id，用于关联入口日志、调用日志和上游请求。 |
| `traceparent` | 规范化后的合法 W3C `traceparent`；非法或缺失时不保存。 |
| `tracestate` | 与合法 `traceparent` 一起进入的合法 W3C `tracestate`；非法或缺失时不保存。 |
| `client_ip_summary` | 客户端 IP 的哈希前缀摘要，格式为 `sha256:<prefix>`；不记录原始 IP。 |
| `user_agent_summary` | 归一化并截断后的 User-Agent 摘要。 |
| `routerx_summary` | `routerx` 扩展字段的安全摘要。 |
| `adapter_degradations` | 入口协议转 OpenAI-compatible 上游时发生的脱敏字段降级摘要；只记录协议、字段名、动作和原因，不保存原始字段值。 |

## 策略快照

`policy_snapshot` 解释本次请求是否允许继续。

| 字段 | 说明 |
|------|------|
| `user_status` | 用户状态和角色摘要。 |
| `token_status` | API Key 状态、过期时间和是否无限额度摘要。 |
| `scope_result` | 模型、API 类型、通道分组、IP、预算等 scope 判断结果。 |
| `quota_precheck` | 调用前额度或预留判断。 |
| `access_decision` | `allow`、`deny`、`skip_candidate`。 |
| `reject_code` | 拒绝时对应 `docs/ERRORS.md` 中的稳定 code。 |
| `policy_version` | 策略或 settings 版本摘要。 |

API Key scope、用户分组和通道分组只能收窄权限，不能把系统或用户本来不允许的能力放大。

## 路由快照

`route_snapshot` 解释候选通道如何被筛选、排序和最终选择。

| 字段 | 说明 |
|------|------|
| `requested_model` | 调用方模型名。 |
| `normalized_model` | RouterX 归一化后的模型名。 |
| `candidate_count` | 初始候选通道数量。 |
| `filtered_reasons` | 按原因汇总的过滤数量，如 disabled、model_mismatch、access_denied、health_blocked。 |
| `route_preference` | 被接受、忽略或拒绝的 `routerx.route` 摘要；已接受时写入 `decision=accepted`。 |
| `selected_channel_id` | 最终通道 ID；拒绝或无候选时为空。 |
| `selected_provider` | 最终 provider。 |
| `selected_channel_group` | 最终通道分组。 |
| `priority` | 选中通道优先级。 |
| `weight` | 权重选择摘要。 |
| `model_rewrite` | 调用方模型到上游模型的改写摘要。 |
| `upstream_base_url_index` | 多 Base URL 场景下的安全索引或策略摘要。 |
| `retry_attempts` | 尝试次数和每次失败摘要。 |

路由偏好只允许收窄后台允许的候选集。越权偏好必须拒绝或按策略忽略，并写入快照。

## 使用量快照

`usage_snapshot` 解释 token 或其他 usage 从哪里来。

| 字段 | 说明 |
|------|------|
| `usage_source` | `upstream`、`adapter`、`tokenizer`、`estimate`、`minimum`。 |
| `prompt_tokens` | 输入 token。 |
| `completion_tokens` | 输出 token。 |
| `total_tokens` | 总 token。 |
| `raw_usage_summary` | 上游 usage 的安全摘要，不保存完整响应。 |
| `tokenizer_version` | 本地估算使用的 tokenizer 或规则版本。 |
| `minimum_reason` | 触发最低计费时的原因。 |

下游已调用但 usage 缺失时，必须按 settings 选择拒绝、估算或最低计费。当前已落地 `billing.usage_missing_strategy=minimum|reject`：`minimum` 记录 `usage_source=minimum` 并最低扣费，`reject` 写 `usage_missing` 失败日志且不扣费；不能无声把 usage 当成 0 免费放行。

## 计费快照

`billing_snapshot` 解释本次 `quota_used` 的计算和扣费结果。

| 字段 | 说明 |
|------|------|
| `billing_status` | `pending`、`settled`、`failed`、`compensating`。 |
| `payer` | `token` 或 `user`。 |
| `price_source` | `model_prices`、`channel_model_prices`、`minimum` 或 P0 基础规则。 |
| `price_rule_id` | 价格规则 ID。 |
| `price_rule_version` | 价格规则版本。 |
| `price_expression` | 实际执行表达式；P0 可记录规则名。 |
| `variables` | 表达式变量摘要。 |
| `base_quota` | 倍率前基础费用。 |
| `multiplier_snapshot` | 用户分组倍率、通道分组倍率、组合覆盖倍率、倍率模式和最终 `effective_ratio`。 |
| `final_quota_used` | 最终扣费额度，必须等于日志 `quota_used`。 |
| `attempted_quota_used` | 扣费失败时本次按表达式和倍率试算出的额度。 |
| `key_budget_before` | 有限 Key 调用前的剩余预算或累计已用摘要。 |
| `key_budget_after` | 有限 Key 调用后的剩余预算或累计已用摘要。 |
| `deduction_result` | 用户余额和 Key 预算条件更新结果，如 `applied` 或 `failed`。 |
| `deduction_error_code` | 扣费失败的稳定原因，例如 `insufficient_user_quota` 或 `insufficient_token_quota`。 |

有限额度 API Key 调用同时扣用户余额并消耗 Key 预算；无限额度 API Key 调用只扣用户额度。失败调用默认不扣费；未来如启用失败最低成本，必须由 settings、错误快照和计费快照共同解释。

当前实现会在 `billing_expression_snapshot` 中记录实际执行规则的 `source`、`id`、`channel_id`、`model`、`price_mode`、`expression`、`unit_tokens`、`rule_version`、`variables` 和 `base_quota`。命中顺序为启用的 `channel_model_prices` 优先，其次启用的 `model_prices`；无规则或表达式不可执行时回退 P0 usage/minimum 快照。当前实现还会在 `multiplier_snapshot` 中记录 `default_ratio`、`user_group_ratio`、`channel_group_ratio`、`user_group_channel_ratio`、`ratio_mode` 和 `effective_ratio`；`ratio_mode=user_group_channel_override` 时组合倍率覆盖用户分组倍率和通道分组倍率的乘积。扣费失败时会写 `billing_status=failed` 的计费快照，`final_quota_used=0` 与日志保持一致，并用 `attempted_quota_used` 保留本次试算额度。

## 访问和限流快照

`access_rule_snapshot` 记录访问控制事实，`rate_limit_snapshot` 记录限流事实，`breaker_snapshot` 记录自动熔断过滤事实。

| 字段 | 说明 |
|------|------|
| `user_group` | 用户分组。 |
| `channel_group` | 通道分组。 |
| `model_access` | 模型访问判断。 |
| `api_type_access` | API 类型访问判断。 |
| `rule_sources` | 参与判断的 settings key、价格规则或 scope 摘要。 |
| `rate_dimensions` | 限流维度，如 user、token、ip、model、channel。 |
| `window` | 限流窗口。 |
| `threshold` | 阈值。 |
| `remaining` | 拒绝或允许时的剩余摘要。 |
| `decision` | `allow` 或 `deny`。 |
| `blocked_channels` | 熔断拒绝时的被挡通道摘要，包含 channel_id、provider、通道分组、错误计数和冷却剩余秒数。 |

## 错误快照

`error_snapshot` 解释失败语义。

| 字段 | 说明 |
|------|------|
| `error_code` | `docs/ERRORS.md` 中的稳定 code。 |
| `error_source` | request、auth、quota、route、channel、upstream、billing、system。 |
| `http_status` | RouterX 返回状态。 |
| `upstream_status` | 上游状态；未调用上游时为空。 |
| `called_upstream` | 是否已经调用上游。 |
| `retryable` | 调用方是否适合重试。 |
| `charged` | 是否扣费。 |
| `safe_message` | 脱敏后的用户可见或运维可见摘要。 |

预检失败必须在上游调用前返回；上游已调用后的失败需要同时保留调用事实、usage 事实和扣费事实。

## Settings 快照

`settings_snapshot` 不保存完整 settings 表，只保存本次调用实际使用的配置摘要。

| 字段 | 说明 |
|------|------|
| `settings_keys` | 参与本次决策的 settings key 列表。 |
| `settings_versions` | 配置版本或更新时间摘要。 |
| `readiness_mode` | readiness 或生产严格模式摘要。 |
| `billing_defaults` | 预扣、最低计费、usage 缺失策略摘要。 |
| `relay_defaults` | 重试、超时、熔断、流式策略摘要。 |

敏感 settings 只记录来源和版本，不记录密钥值。

## 存储原则

- 优先把调用事实写入 `logs` 或明确的账单事实表；管理操作前后状态写入审计表。
- 快照字段可以是 JSON 字符串、JSON 类型或后续拆表，但外部语义必须稳定。
- 快照大小需要有上限；超过上限时保留关键字段并标记 `truncated=true`。
- 快照只能包含脱敏摘要，不能把 body 日志当成默认能力。
- 请求/响应 body 快照默认关闭；开启时必须短保留、强脱敏、可审计。
- 同一次调用的 `request_id` 必须贯穿访问日志、模型调用日志、错误响应和管理审计。

## 脱敏规则

快照和审计不得包含：

- 完整 RouterX API Key。
- 上游 API Key、Authorization、Cookie、Set-Cookie。
- 数据库 DSN、JWT Secret、加密主密钥、支付密钥。
- 完整 prompt、完整响应正文、文件内容或图片原文。
- 未截断的 IP、User-Agent 或高基数客户端自定义字段。

允许包含：

- 内部 ID、状态、枚举、版本、规则 ID。
- API Key 安全前缀或尾号摘要。
- 脱敏错误摘要。
- 聚合后的候选数量、过滤原因、token 数和额度数。

## 测试要求

| 测试方向 | 断言 |
|----------|------|
| schema | 快照包含 `schema`、`kind`、`request_id`、`source` 和 `redacted=true`。 |
| 脱敏 | 快照不包含完整 API Key、上游密钥、Authorization、Cookie、DSN 或支付密钥。 |
| 路由 | 候选过滤、`routerx.route`、模型重写和最终通道可复盘。 |
| 策略 | 访问拒绝、scope 收窄、限流和余额不足能解释且不调用上游。 |
| usage | 上游 usage、adapter usage、估算和最低计费都记录 `usage_source` 和 `usage_snapshot`。 |
| 计费 | `billing_snapshot.final_quota_used` 与 `logs.quota_used` 和扣费事务一致。 |
| 错误 | 失败日志能判断 source、是否调用上游、是否扣费、是否适合重试。 |
| 历史一致 | settings 或价格规则变更后，旧调用仍能按当时快照解释。 |

## 文档同步

任何新增或改变快照字段时，需要同步检查：

- `docs/POLICIES.md`：策略、访问控制、限流和路由偏好是否影响快照。
- `docs/BILLING.md`：价格、倍率、usage 和扣费事务是否影响账单解释。
- `docs/OBSERVABILITY.md`：日志字段、指标、保留、脱敏和导出是否需要调整。
- `docs/DATA_MODEL.md`：`logs` 字段、索引或独立事实表是否需要调整。
- `docs/ERRORS.md`：错误 code、扣费语义和重试语义是否需要调整。
- `docs/RELAY.md`：通道选择、模型重写、重试、熔断和流式行为是否影响路由快照。
- `docs/TESTING.md`：fixture、断言和历史一致性测试是否需要补充。
- `docs/ACCEPTANCE.md`：阶段门禁是否能用新证据证明。
