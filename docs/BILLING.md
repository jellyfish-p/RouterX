# RouterX 计费与额度设计

## 目标

计费系统负责将模型调用 usage 转换为 RouterX 内部额度消耗，并保证并发调用下额度扣减准确、可追溯、可审计。

计费是 RouterX 商业级能力的核心，但在线支付不是最小可用路径的前置依赖。默认设计必须先保证额度、价格、扣费、日志和账单一致；Stripe、易支付、充值码等能力作为可选运营增强接入。

目标能力：

- 支持用户额度、API Key 最大消耗额度。
- 支持 API Key 无限额度标记。
- 支持系统级模型价格配置，系统模型价格存储在 `model_prices` SQL 表中。
- 支持通道级模型价格覆盖，通道覆盖规则存储在 `channel_model_prices` SQL 表中，优先级高于系统模型价格。
- 支持通道模型设置是否允许普通用户使用。
- 支持通道/模型分组倍率，倍率存储在 `settings.value` JSON 字符串中，不直接拥有模型价格。
- 支持通道/模型分组默认普通用户可用白名单数组配置。
- 支持用户分组倍率。
- 支持用户分组在使用指定通道分组时设置独立组合倍率、折扣或加价；组合倍率命中时覆盖“用户分组倍率 x 通道分组倍率”的结果。
- 支持指定用户分组通过系统配置额外允许或拒绝通道/模型分组。
- 支持调用日志和账单统计一致。
- 支持下游未返回 usage 时估算。
- 支持后台配置计费规则和预扣 token 量。
- 支持 Stripe 和易支付在线充值入账。
- 支持全局默认 tokenizer 作为 usage 缺失时的补充估算配置。
- 支持按次、按秒、按 token、阶梯等计费模板，但所有价格计费最终都保存为计费表达式执行。

## 当前实现边界

当前代码已经具备基础额度预检、调用后 usage 写入、`quota_used` 记录、API Key/用户扣减、用户账单统计接口、用户/管理端额度流水查询接口、基于 settings 的用户分组 x 通道分组访问控制，系统模型价格表 `model_prices` 的管理端 API、规则版本和用户侧模型价格就绪状态展示，以及通道模型价格覆盖 `channel_model_prices` 的管理端 API、规则版本、普通用户可见性和用户侧通道级价格状态展示。`channel_model_prices.user_enabled=false` 已同时作用于 `/v0/user/models` 和普通用户调用候选过滤。成功调用后的扣费热路径已读取启用的通道级价格表达式，未命中时读取启用的系统模型价格表达式，并在表达式后应用 `billing.default_ratio`、用户分组倍率、通道分组倍率或组合覆盖倍率；实际执行表达式、变量、规则 ID、规则版本、倍率快照和最终 `quota_used` 会写入 `billing_snapshot`。无价格规则或表达式不可执行时回退 P0 usage/minimum 后仍应用倍率；上游成功响应缺少 usage 时由 `billing.usage_missing_strategy` 决定最低扣费或拒绝且不扣费。目标口径已调整为用户余额 + Key 预算双约束，旧 Key 余额划拨语义需要迁移；完整访问控制快照和更多事件仍属于目标增强。

文档中的部分商业级 `billing_*_snapshot` 字段仍是目标设计，不应误读为当前迁移已经全部存在。`model_prices` 和 `channel_model_prices` 当前已经落库并用于管理端维护、`/v0/user/models` 价格状态/可见性展示，以及成功调用后的基础价格表达式执行；`multiplier_snapshot` 当前已记录默认倍率、用户分组倍率、通道分组倍率、组合倍率、倍率模式和最终 `effective_ratio`。调用事实快照的统一字段、脱敏和测试要求以 `docs/SNAPSHOTS.md` 为准。

## 额度单位

当前常量：

```text
QuotaPerUnit = 100000000
```

说明：

- 所有额度字段使用 `int64` 整数，避免浮点误差。
- `100000000` 个数据库 token / 基础额度单位 = `1` 个用户额度。
- 数据库中 `quota`、`quota_used`、`remain_quota`、`quota_limit`、价格表达式结果等均以基础额度单位存储和计算。
- 展示层通过 `quota / QuotaPerUnit` 转为用户可见小数额度。

## 相关字段

| 表 | 字段 | 说明 |
|----|------|------|
| `users` | `quota` | 用户总可用额度，单位为基础额度单位 |
| `tokens` | `remain_quota` | 当前字段；目标语义为 API Key 剩余预算上限，`-1` 表示不限 Token 自身额度 |
| `tokens` | `unlimited` | API Key 是否无限制自身额度 |
| `tokens` | `quota_limit` / `quota_used` | 目标字段；分别表示 Key 最大消耗额度和累计已用额度 |
| `payment_products` | `quota` | 支付商品对应增加的基础额度单位 |
| `payment_orders` | `status` | 支付订单状态，如 `pending`、`paid`、`failed`、`closed`、`refunded` |
| `payment_orders` | `quota` | 支付成功后应增加的基础额度单位 |
| `payment_events` | `provider_event_id` | 支付渠道事件 ID，用于幂等处理 |
| `quota_transactions` | `amount`、`idempotency_key` | 支付入账、充值码、退款、人工调整的额度流水核心字段 |
| `settings` | `billing.user_group_ratios` | 用户分组倍率 JSON 配置 |
| `model_prices` | `price_expression` | 系统模型价格表达式，返回倍率前的 `base_quota` |
| `model_prices` | `variables_json` | 系统模型价格变量默认值 |
| `model_prices` | `unit_tokens` | token 计价单位 |
| `model_prices` | `rule_version` | 系统模型价格规则版本 |
| `channel_model_prices` | `price_expression` | 通道级模型价格表达式覆盖，优先于 `model_prices` |
| `channel_model_prices` | `price_mode` | 表达式模板类型，如 `request`、`second`、`token`、`tiered` |
| `channel_model_prices` | `variables_json` | 通道级模型价格变量覆盖 |
| `channel_model_prices` | `unit_tokens` | 通道级 token 计价单位覆盖 |
| `channel_model_prices` | `user_enabled` | 该通道模型是否允许普通用户使用 |
| `settings` | `billing.channel_group_ratios` / `billing.model_group_ratios` | 通道/模型分组倍率 JSON 配置 |
| `settings` | `billing.default_user_channel_group_access` | 默认普通用户可用通道/模型分组白名单数组配置 |
| `settings` | `billing.user_group_channel_ratios` | 用户分组 x 通道/模型分组组合覆盖倍率 JSON 配置 |
| `settings` | `billing.user_group_channel_group_access` | 用户分组额外允许/拒绝通道/模型分组 JSON 配置 |
| `logs` | `prompt_tokens` | 输入 token 数 |
| `logs` | `completion_tokens` | 输出 token 数 |
| `logs` | `total_tokens` | 总 token 数 |
| `logs` | `quota_used` | 本次最终消耗额度，单位为基础额度单位 |
| `logs` | `billing_status` | 计费状态，如 `settled`、`failed` |
| `logs` | `billing_expression_id` | 本次请求使用的表达式 ID |
| `logs` | `billing_expression_version` | 本次请求使用的表达式版本 |
| `logs` | `billing_expression_source` | 表达式来源，如 `model_prices`、`channel_model_prices` |
| `logs` | `billing_expression_snapshot` | 实际执行的计费表达式快照 |
| `logs` | `multiplier_snapshot` | 默认倍率、用户分组倍率、通道分组倍率、用户分组 x 通道分组组合覆盖倍率和最终倍率快照 |
| `logs` | `access_rule_snapshot` | 通道模型和用户分组访问控制快照 |
| `logs` | `usage_source` | usage 来源，如 `upstream`、`adapter`、`tokenizer`、`estimate` |

`/v0/user/quota-transactions` 和 `/v0/admin/quota-transactions` 已用于查询 `quota_transactions` 中的余额变更流水；模型调用消费查询仍使用调用日志和账单聚合接口。

## 计费事实链

商业级计费的核心不是支付，而是每一次模型调用都能形成可复核的事实链。

```text
请求进入
    -> 鉴权和额度预检
    -> 通道和访问控制决策
    -> 上游调用
    -> usage 提取或估算
    -> 价格表达式和倍率快照
    -> 条件扣费事务
    -> 写入日志和账单事实
    -> 用户账单和管理统计聚合
```

事实来源分层：

| 层级 | 事实 | 权威来源 | 要求 |
|------|------|----------|------|
| 1 | 请求身份 | `current_user`、`current_token` | 用户、Token、状态和额度必须在调用前校验。 |
| 2 | 路由事实 | 通道选择结果、路由决策快照 | 记录候选过滤、最终通道、模型重写和访问控制结果。 |
| 3 | 使用量事实 | 上游 usage、Adapter usage、本地估算 | 必须记录 `usage_source`，不能无声使用 0 usage 免费放行。 |
| 4 | 价格事实 | `model_prices`、`channel_model_prices`、`settings` 倍率 | 必须保存表达式、变量、倍率和访问控制快照。 |
| 5 | 扣费事实 | 用户余额和 Key 预算条件更新事务 | 并发场景不能透支，失败时必须可判断是否已调用下游。 |
| 6 | 账单事实 | `logs`、`quota_transactions` 和账单聚合 | 模型消费来自调用日志，余额变更来自额度流水；聚合结果不能重新解释历史规则。 |

如果启用 `LOG_SQL_DSN` 独立日志数据库，主业务数据库仍必须保留扣费事务所需的最小结算事实，或在同事务中写入 outbox。当前实现采用主库完整调用事实 + `log_replication_outboxes` 补写队列 + 独立日志库副本：余额扣减后的账单解释不依赖日志库唯一副本，独立日志库可以承载高流量调用日志、诊断快照和清理归档；运行期日志库不可用时，后台 worker 会在恢复后重放 pending outbox。

拒绝路径：

- API Key 无效、用户禁用、Token 禁用或过期：返回 401/403，不调用下游。
- 余额不足、预留额度不足或访问控制不通过：返回 429/403，不调用下游。
- 通道不可用、模型不匹配、provider 不支持：返回当前入口协议兼容错误，不扣费；provider 能力等级和阶段以 `docs/PROTOCOLS.md` 为准。
- 下游已调用但 usage 缺失：当前可通过 `billing.usage_missing_strategy=minimum` 使用最低计费并记录 `usage_source=minimum`，或通过 `reject` 返回 `usage_missing`、写失败日志且不扣费；tokenizer/estimate 仍属后续增强。
- 扣费事务失败：必须写失败日志；非流式响应在返回前发现失败时应返回错误，流式响应已输出时需按后续补偿策略处理。

### P0 扣费事务边界

P0 暂不要求完整价格表达式，但必须保证成功调用、日志和额度变化一致。

P0 计费规则：

```text
if usage.total_tokens > 0:
    quota_used = usage.total_tokens
else:
    quota_used = 1
```

P0 扣费顺序：

```text
1. 请求开始前同时检查用户余额和 API Key 预算上限
2. 下游成功返回后提取 usage
3. 计算 quota_used
4. 在数据库事务中扣减用户余额，并更新 API Key 预算计数
5. 扣费成功后写 success 日志
6. 扣费失败时写 failed 日志并返回 429；日志 `quota_used=0`，`billing_snapshot.billing_status=failed`，并记录 `attempted_quota_used` 和 `deduction_error_code`
```

额度扣减规则：

- Token `unlimited=true` 或 `remain_quota=-1` 时，不限制 Token 自身预算，只扣用户额度。
- API Key 有限额度时，调用前必须同时满足用户余额和 Key 剩余预算；调用成功后同时扣用户额度并消耗 Key 预算。
- 创建有限 API Key 不从用户额度划拨，也不冻结用户余额；它只设置该 Key 的最大消耗额度。
- 所有扣减都必须使用数据库条件更新或事务，不能在并发请求下透支。
- 失败调用默认不扣费；如未来启用失败最低成本，必须写入配置和日志快照。

余额与消费口径：

| 事件 | `users.quota` | `tokens.remain_quota` | `logs.quota_used` | 口径 |
|------|---------------|-----------------------|-------------------|------|
| 创建有限额度 API Key | 不变 | 设置最大消耗额度或剩余预算上限 | 不写消费日志 | 预算上限，不是余额划拨 |
| 有限 API Key 成功调用 | 减少本次 `quota_used` | 减少剩余预算或增加累计已用 | 写入本次消耗 | 模型消费，受用户余额和 Key 预算双约束 |
| 无限 Token 成功调用 | 减少本次 `quota_used` | 保持 `-1` | 写入本次消耗 | 模型消费 |
| 失败且未产生有效 usage | 不变 | 不变 | 可写失败日志，`quota_used=0` | 不计消费 |

用户可用余额只以 `users.quota` 为准；有限 API Key 的剩余预算不是额外余额，也不应加回用户余额展示。用户消费账单按成功调用的 `logs.quota_used` 聚合。Key 预算调整和用户余额调整不能混在同一口径里解释，否则会出现“创建 Key 被当成消费”或“删除 Key 被当成退款”的账本错误。

## 后台配置

模型价格表达式存储在专用 SQL 表中：系统模型价格存储在 `model_prices`，通道级模型价格覆盖存储在 `channel_model_prices`，它们不是 JSON blob。运行时开关、全局默认值、全局默认 tokenizer、用户分组倍率、通道/模型分组倍率、用户分组 x 通道/模型分组组合覆盖倍率、默认普通用户通道/模型分组可用白名单、用户分组通道/模型分组访问覆盖存储在 `settings.value` 的 JSON 字符串中。倍率和可用性配置不是模型价格实体。

| 配置键 | 示例值 | 说明 |
|--------|--------|------|
| `billing.bootstrap_admin_quota` | `100000000` | 初始化超级管理员启动额度，用于首次验证调用和管理员自测 |
| `billing.precharge_tokens_per_request` | `4096` | 每次请求开始时默认预扣或预留的输出 token 数 |
| `payment.stripe.enabled` | `false` | 是否启用 Stripe 支付 |
| `payment.epay.enabled` | `false` | 是否启用易支付 |

配置要求：

- 系统模型价格创建、更新、启用和禁用当前已写入 `model_price.*` 管理审计；通道级模型价格覆盖创建、更新、启用和禁用当前已写入 `channel_model_price.*` 管理审计，`user_enabled` 变更随更新审计记录；通道分组倍率、用户分组 x 通道分组倍率、用户分组通道/模型分组访问配置、计费规则版本变更也必须记录审计日志。
- 每次计费规则变更必须生成新的 `rule_version` 或表达式版本，已完成请求不受新规则影响。
- `billing.bootstrap_admin_quota` 只解决开箱验证体验，不代表正式运营赠送策略；生产运营应通过商品、充值码、管理员额度调整或支付入账管理用户额度。

## 价格配置

模型价格表达式存储在专用 SQL 表中：系统模型价格存储在 `model_prices`，通道级模型价格覆盖存储在 `channel_model_prices`，它们不是 JSON blob。倍率/比例配置存储在 `settings.value` 的 JSON 字符串中。按次、按秒、按 token、阶梯等价格模式都是表达式模板：服务代码可以生成模板，但持久化后的价格规则始终是 `price_expression` 文本和 `variables_json` 变量。

### SQL 表

#### `model_prices`

系统模型价格表，提供模型全局默认价格表达式。

当前已实现后台管理 API：`GET/POST /v0/admin/model-prices`、`PUT /v0/admin/model-prices/:id`、`PATCH /v0/admin/model-prices/:id/disable|enable`。启用规则会让 `/v0/user/models` 对应模型返回 `pricing_ready=true` 和 `model_price:<price_mode>:v<rule_version>`；成功调用后若没有命中通道级覆盖，热路径会执行该表达式并写入 `billing_snapshot`。禁用或未配置时返回 `minimum_usage`，调用扣费回退 P0 usage/minimum。

| 字段 | 说明 |
|------|------|
| `id` | 主键 |
| `model` | 模型名 |
| `price_mode` | 表达式模板类型，如 `request`、`second`、`token`、`tiered` |
| `price_expression` | 价格表达式，返回倍率前的 `base_quota` |
| `variables_json` | 表达式变量默认值 |
| `unit_tokens` | 计价 token 单位，通常为 `1000` 或 `1000000` |
| `rule_version` | 规则版本 |
| `enabled` | 是否启用 |
| `created_at` | 创建时间 |
| `updated_at` | 更新时间 |

#### `channel_model_prices`

通道级模型价格覆盖表，优先级高于 `model_prices`。

当前已实现后台管理 API：`GET/POST /v0/admin/channel-model-prices`、`PUT /v0/admin/channel-model-prices/:id`、`PATCH /v0/admin/channel-model-prices/:id/disable|enable`。当默认可见通道存在启用覆盖时，`/v0/user/models` 返回 `channel_model_price:<price_mode>:v<rule_version>`；`user_enabled=false` 时该通道不再向普通用户贡献该模型可见性，也不会进入普通用户调用候选。成功调用后如果选中的通道存在启用覆盖，热路径优先执行该通道级表达式并写入 `billing_snapshot`。

| 字段 | 说明 |
|------|------|
| `id` | 主键 |
| `channel_id` | 通道 ID |
| `model` | 模型名 |
| `enabled` | 是否启用该通道覆盖 |
| `price_mode` | 表达式模板类型，如 `request`、`second`、`token`、`tiered` |
| `override_mode` | 覆盖模式，如 `override` 或 `merge_variables` |
| `price_expression` | 通道级价格表达式覆盖 |
| `variables_json` | 通道级变量覆盖 |
| `unit_tokens` | 通道级计价 token 单位覆盖 |
| `user_enabled` | 是否允许普通用户通过该通道调用该模型；`false` 表示仅管理员、内部任务或显式允许的后台流程可用 |
| `rule_version` | 通道价格规则版本 |
| `created_at` | 创建时间 |
| `updated_at` | 更新时间 |

#### `settings`

`settings` 是 RouterX 的运行时配置权威来源，用于存储运行时开关、全局默认值和倍率 JSON。用户提到的“模型分组倍率”在本文档中按现有术语记录为通道/模型分组倍率配置；如果实现使用通道分组命名，保持 `channel_group` 术语。

运行时以 `settings` 数据库记录为准。环境变量只承载启动必须项和密钥类配置，不作为计费倍率和访问控制的权威来源。

| 字段 | 说明 |
|------|------|
| `key` | 配置键 |
| `value` | JSON 字符串或标量字符串 |
| `category` | 配置分类，计费配置使用 `billing` |
| `description` | 配置说明 |
| `created_at` | 创建时间 |
| `updated_at` | 更新时间 |

建议配置键：

| 配置键 | 说明 |
|--------|------|
| `billing.user_group_ratios` | 用户分组倍率，例如 `{ "vip": 0.8, "default": 1 }` |
| `billing.channel_group_ratios` / `billing.model_group_ratios` | 通道/模型分组倍率，例如 `{ "premium": 1.2, "default": 1 }` |
| `billing.user_group_channel_ratios` | 用户分组 x 通道/模型分组组合覆盖倍率，例如 `{ "vip": { "premium": 0.9 } }` |
| `billing.default_user_channel_group_access` | 默认普通用户可用通道/模型分组白名单数组，例如 `["default", "standard"]` |
| `billing.user_group_channel_group_access` | 用户分组额外允许/拒绝通道/模型分组，例如 `{ "vip": { "allow": ["premium"], "deny": ["experimental"] }}` |

倍率配置从 `settings.value` 读取，不再维护为 `groups.ratio`、`channel_groups.ratio` 或 `user_group_channel_ratios.ratio` 等独立 SQL 列，除非未来实现明确选择该 schema。

默认分组要求：用户分组和通道分组的空值都应归一为 `default`；新用户和新通道应直接写入 `default`。`default` 用户分组和 `default` 通道分组的倍率默认均为 `1`，访问白名单默认包含 `default`。

### 可用性控制

通道模型可用性和计费价格是两套独立规则。

普通用户请求选择通道前必须先做可用性判断：

```text
if channel_model_prices.user_enabled == false:
    普通用户请求拒绝或跳过该通道模型

access = settings["billing.user_group_channel_group_access"][user_group]
if channel_group in access.deny:
    拒绝或跳过该通道/模型分组
else if channel_group in access.allow:
    额外允许该通道/模型分组
else if channel_group in settings["billing.default_user_channel_group_access"]:
    默认普通用户允许该通道/模型分组
else:
    默认普通用户拒绝或跳过该通道/模型分组
```

规则说明：

- `channel_model_prices.user_enabled` 控制某个通道下某个模型是否允许普通用户使用，不影响管理员测试、内部任务或后台显式授权流程。
- `billing.default_user_channel_group_access` 控制普通用户默认可用的通道/模型分组，使用数组表达，是默认白名单。
- `billing.user_group_channel_group_access` 只控制指定用户分组对通道/模型分组的额外允许或拒绝，不参与价格表达式，也不参与倍率计算。
- 默认白名单和用户分组配置都命中时，用户分组配置继续合成；用户分组配置中同时命中 `allow` 和 `deny` 时，`deny` 优先。
- 未配置用户分组访问覆盖时，按默认白名单判断；默认白名单缺失或格式非法时，服务端回退到 `["default"]`，避免静默放开全部通道分组。
- 最终日志应记录 `access_rule_snapshot`，便于审计为什么某次请求可以或不可以使用某个通道分组。

### 计费规则层级与优先级

计费配置按以下层级合成最终规则：

| 优先级 | 层级 | 能力 | 说明 |
|--------|------|------|------|
| 1 | 通道模型价格 | 模型级表达式、价格模式和 `unit_tokens` 配置 | 存储在 `channel_model_prices`；覆盖系统模型价格 |
| 2 | 系统模型价格 | 全局表达式、价格模式、`unit_tokens`、版本和启用状态 | 存储在 `model_prices` |
| 3 | 通道模型普通用户可用性 | 普通用户是否可以使用指定通道模型 | 存储在 `channel_model_prices.user_enabled`；不是价格表达式的一部分 |
| 4 | 默认普通用户通道/模型分组可用性 | 普通用户默认可用的通道/模型分组白名单数组 | 从 `settings.value` 的 `billing.default_user_channel_group_access` 读取；不是价格表达式的一部分 |
| 5 | 用户分组通道/模型分组访问覆盖 | 指定用户分组额外允许或拒绝通道/模型分组 | 从 `settings.value` 的 `billing.user_group_channel_group_access` 读取；不是价格表达式的一部分 |
| 6 | 用户分组倍率 | 用户分组倍率 | 从 `settings.value` 的 `billing.user_group_ratios` 读取；不是价格表达式的一部分 |
| 7 | 通道/模型分组倍率 | 通道/模型分组倍率 | 从 `settings.value` 的 `billing.channel_group_ratios` 或 `billing.model_group_ratios` 读取；不是价格表达式的一部分 |
| 8 | 用户分组 x 通道/模型分组倍率 | 指定用户分组使用指定通道/模型分组时的组合覆盖倍率 | 从 `settings.value` 的 `billing.user_group_channel_ratios` 读取；不是价格表达式的一部分 |

解析规则：

- 访问控制、用户分组、通道分组、API Key scope、限流和策略快照的统一语义以 `docs/POLICIES.md` 为准；协议、APIType 和 provider 能力等级以 `docs/PROTOCOLS.md` 为准；一次调用的快照封套和脱敏要求以 `docs/SNAPSHOTS.md` 为准。本文只说明它们如何影响计费和账单解释。
- 先按通道和模型读取 `channel_model_prices`；存在启用规则时优先使用通道级价格表达式。
- 通道级规则不存在或未启用时，读取 `model_prices` 作为系统模型价格。
- 普通用户请求必须先通过 `channel_model_prices.user_enabled`、`billing.default_user_channel_group_access` 和 `billing.user_group_channel_group_access` 可用性判断，未通过的通道不参与后续计费和路由选择。
- `request`、`second`、`token`、`tiered` 都只是模板类型，最终执行的是 SQL 中保存的 `price_expression` 和 `variables_json`。
- 倍率字段独立于价格表达式，服务代码不得把用户分组倍率、通道分组倍率或用户分组 x 通道分组组合倍率嵌入 `price_expression`。

可解释性要求：

- 每次已结算调用都应能还原使用的价格规则、变量、倍率、访问控制和最终 `quota_used`。
- 价格表达式计算基础费用，倍率配置只在表达式之后应用，两者不能互相隐藏。
- 访问控制拒绝和余额不足拒绝都不应调用下游。
- 规则变更必须产生新版本或新快照，历史日志继续按当时规则解释。

### 表达式与倍率

价格表达式只计算倍率前的 `base_quota`，单位为数据库 token / 基础额度单位。倍率从 `settings.value` JSON 读取，并在表达式求值后应用。

```text
base_quota = expression_engine.evaluate(price_expression, variables)
if user_group_channel_ratio is configured:
    effective_ratio = default_ratio * user_group_channel_ratio
else:
    effective_ratio = default_ratio * user_group_ratio * channel_group_ratio
quota_used = ceil(base_quota * effective_ratio)
quota_used = max(min_charge_quota, quota_used) 仅当 billable 且 base_quota > 0
```

倍率组合是账单聚合规则，不是价格表达式的一部分。`billing.user_group_channel_ratios` 表示指定用户分组使用指定通道/模型分组时的最终业务倍率覆盖值，不会再额外乘一次用户分组倍率和通道分组倍率。价格表达式不得引用倍率变量，也不得把倍率计算硬编码到表达式文本中。

示例表达式：

```text
prompt_tokens * prompt_price + completion_tokens * completion_price
request_price
ceil(duration_seconds * second_price)
```

## 表达式关键字

表达式引擎只允许使用白名单变量、函数和运算符。未提供的变量必须使用规则中定义的默认值或显式置为 `0`、`false`。

### Token 变量

| 关键字 | 说明 |
|--------|------|
| `prompt_tokens` | 输入 token 数 |
| `completion_tokens` | 输出 token 数 |
| `total_tokens` | 总 token 数 |
| `cached_tokens` | 命中缓存的 token 数 |
| `cache_write_tokens` | 写入缓存的 token 数 |
| `reasoning_tokens` | 推理 token 数 |
| `get_response_var($variable)` | 获取返回报文中指定变量的值 |
| `get_request_var($variable)` | 获取请求报文中指定变量的值 |

### 时间变量

| 关键字 | 说明 |
|--------|------|
| `now_time` | 请求结束 Unix 时间戳 |
| `duration_seconds` | 请求持续秒数，用于按秒计费模板 |

### 价格变量

| 关键字 | 说明 |
|--------|------|
| `prompt_price` | 输入 token 单价，按 `unit_tokens` 计价 |
| `completion_price` | 输出 token 单价，按 `unit_tokens` 计价 |
| `cached_prompt_price` | 缓存命中输入 token 单价 |
| `cache_write_price` | 缓存写入 token 单价 |
| `request_price` | 每次请求固定价格 |
| `second_price` | 每秒价格 |
| `min_charge_quota` | 大于 0 的计费事件最低扣费额度 |
| `price_list[idx\|name]` | 自定义价格列表 |

### 函数和运算符

| 类型 | 白名单 |
|------|--------|
| 算术运算符 | `+`、`-`、`*`、`/`、`%`、括号 |
| 比较运算符 | `==`、`!=`、`>`、`>=`、`<`、`<=` |
| 逻辑运算符 | `&&`、`\|\|`、`!` |
| 条件函数 | `if(condition, true_value, false_value)` |
| 数学函数 | `ceil`、`floor`、`round`、`abs`、`min`、`max`、`clamp` |
| 阶梯函数 | `tier` |
| 空值函数 | `coalesce`、`default` |

## 支付和充值

在线支付用于购买用户额度。支付金额、货币、赠送额度和最终入账额度必须由服务端商品配置决定，客户端不能直接提交要增加的 `quota`。支付 provider、充值码、退款、人工补账和额度流水的完整契约以 `docs/PAYMENTS.md` 为准；本文保留计费事实和入账口径摘要。

支付模块是可选插件式能力。未启用支付时，系统仍应能通过管理员额度调整、API Key 预算控制和充值码完成基础运营；启用支付后必须满足签名校验、金额校验、幂等入账和审计要求。

支持渠道：

| 渠道 | provider | 适用方式 |
|------|----------|----------|
| Stripe | `stripe` | Checkout Session、Webhook 异步确认 |
| 易支付 | `epay` | 跳转收银台、异步通知、同步返回 |

支付核心流程：

```text
用户选择充值商品
    -> 创建 payment_orders(status=pending)
    -> 按 provider 创建支付会话或跳转参数
    -> 用户完成支付
    -> provider 异步通知 RouterX
    -> 校验签名和金额
    -> 幂等写 payment_events
    -> payment_orders pending -> paid
    -> users.quota += payment_orders.quota
    -> 写审计日志
```

入账要求：

- `payment_orders.quota` 使用基础额度单位，`100000000` 个数据库 token / 基础额度单位 = `1` 个用户额度。
- 支付成功后增加用户额度必须和订单状态更新在同一数据库事务中完成。
- 同一个 provider 事件、同一个 provider 支付单号或同一个本地订单号重复通知时，只能入账一次。
- 金额、货币、商品 ID、订单号任一不匹配都不能入账。
- 用户取消、超时、支付失败不增加额度。

### 支付配置

支付开关和非敏感配置可以存储在 `settings.value`。敏感配置必须加密存储，或通过环境变量/KMS 注入后写入运行时配置。

建议配置键：

| 配置键 | 说明 |
|--------|------|
| `payment.products` | 充值商品列表，例如金额、货币、额度、赠送额度、启用状态 |
| `payment.stripe.enabled` | 是否启用 Stripe |
| `payment.stripe.currency` | 默认货币，如 `usd` |
| `payment.stripe.success_url` | Checkout 成功跳转 URL |
| `payment.stripe.cancel_url` | Checkout 取消跳转 URL |
| `payment.epay.enabled` | 是否启用易支付 |
| `payment.epay.gateway` | 易支付网关地址 |
| `payment.epay.pid` | 易支付商户 ID |
| `payment.epay.notify_url` | 易支付异步通知地址 |
| `payment.epay.return_url` | 易支付同步返回地址 |

敏感配置：

- Stripe `secret_key`、`webhook_secret` 必须来自 `PAYMENT_STRIPE_SECRET_KEY`、`PAYMENT_STRIPE_WEBHOOK_SECRET`、KMS 或加密配置。
- 易支付 `key` 必须来自 `PAYMENT_EPAY_KEY`、KMS 或加密配置。
- 支付密钥不得写入前端响应、日志、订单扩展字段或审计明文。

### Stripe

Stripe 推荐使用 Checkout Session：

```text
create order
    -> create Stripe Checkout Session
    -> save provider_order_id=session.id
    -> return checkout_url

stripe webhook
    -> verify Stripe-Signature with webhook_secret
    -> handle checkout.session.completed or payment_intent.succeeded
    -> verify amount_total, currency, metadata.order_no
    -> mark order paid and grant quota
```

Stripe 规则：

- Checkout Session metadata 必须包含 RouterX `order_no`、`user_id`、`product_id`。
- Webhook 必须使用原始 request body 校验 `Stripe-Signature`。
- 只处理可信事件类型，其他事件写入 `payment_events` 后忽略。
- `amount_total` 和 `currency` 必须和本地订单一致。
- 退款事件可先只记录，后续再按产品策略决定是否扣回额度。

### 易支付

易支付使用跳转支付和异步通知。

发起支付参数示例：

```text
pid={merchant_id}
type=alipay|wxpay|qqpay
out_trade_no={order_no}
notify_url={notify_url}
return_url={return_url}
name={product_name}
money={amount}
clientip={client_ip}
sign={sign}
sign_type=MD5
```

签名规则：

- 按易支付网关要求对参数排序。
- 排除 `sign`、`sign_type` 和空值字段。
- 拼接待签名字符串并追加商户密钥。
- 使用网关配置的算法生成签名，常见为 MD5。
- 不同易支付实现可能有细节差异，必须以实际网关文档为准。

异步通知处理：

```text
receive notify
    -> verify sign
    -> verify trade_status=TRADE_SUCCESS or provider success status
    -> verify pid, out_trade_no, money
    -> payment_orders pending -> paid
    -> users.quota += order.quota
    -> response plain text success
```

易支付规则：

- 入账只能依赖异步通知，不依赖同步返回页。
- 同步返回页只用于展示支付结果，应主动查询本地订单状态。
- 异步通知成功后必须返回网关要求的纯文本，例如 `success`。
- `out_trade_no` 必须由 RouterX 生成，不能接受客户端自定义。

### 支付安全和审计

支付回调失败、重复通知、金额不匹配和人工补账的处理路径以 `docs/RUNBOOKS.md` 为准。本文只定义计费事实、入账规则和审计字段。

需要审计的操作：

- 用户创建支付订单。
- Stripe Webhook 收到和处理结果。
- 易支付异步通知收到和处理结果。
- 支付订单入账、关闭、退款、人工修正。
- 支付商品和支付配置变更。

审计字段建议：

- `order_no`。
- `provider`。
- provider 事件 ID 或交易号。
- 金额和货币。
- 增加的基础额度单位。
- 签名校验结果。
- 处理前后订单状态。
- request id、IP、时间。
