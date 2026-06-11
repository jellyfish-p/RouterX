# RouterX 计费与额度设计

## 目标

计费系统负责将模型调用 usage 转换为 RouterX 内部额度消耗，并保证并发调用下额度扣减准确、可追溯、可审计。

目标能力：

- 支持用户额度、API Key 额度。
- 支持 API Key 无限额度标记。
- 支持系统级模型价格配置，系统模型价格存储在 `model_prices` SQL 表中。
- 支持渠道级模型价格覆盖，渠道覆盖规则存储在 `channel_model_prices` SQL 表中，优先级高于系统模型价格。
- 支持渠道模型设置是否允许普通用户使用。
- 支持渠道/模型分组倍率，倍率存储在系统配置数据库 JSONB 中，不直接拥有模型价格。
- 支持渠道/模型分组默认普通用户可用白名单数组配置。
- 支持用户分组倍率。
- 支持用户分组在使用指定渠道分组时设置独立额外倍率、折扣或加价。
- 支持指定用户分组通过配置文件或系统配置额外启用或禁用渠道/模型分组。
- 支持调用日志和账单统计一致。
- 支持下游未返回 usage 时估算。
- 支持后台配置计费规则和预扣 token 量。
- 支持 Stripe 和易支付在线充值入账。
- 支持全局默认 tokenizer 作为 usage 缺失时的补充估算配置。
- 支持按次、按秒、按 token、阶梯等计费模板，但所有价格计费最终都保存为计费表达式执行。

## 额度单位

当前常量：

```text
QuotaPerUnit = 100000000
```

说明：

- 所有额度字段使用 `int64` 整数，避免浮点误差。
- `100000000` 个数据库 token / 基础额度单位 = `1` 个用户额度。
- 数据库中 `quota`、`quota_used`、`remain_quota`、价格表达式结果等均以基础额度单位存储和计算。
- 展示层通过 `quota / QuotaPerUnit` 转为用户可见小数额度。

## 相关字段

| 表 | 字段 | 说明 |
|----|------|------|
| `users` | `quota` | 用户总可用额度，单位为基础额度单位 |
| `tokens` | `remain_quota` | API Key 可用额度，`-1` 表示不限 Token 自身额度 |
| `tokens` | `unlimited` | API Key 是否无限制自身额度 |
| `payment_products` | `quota` | 支付商品对应增加的基础额度单位 |
| `payment_orders` | `status` | 支付订单状态，如 `pending`、`paid`、`failed`、`closed`、`refunded` |
| `payment_orders` | `quota` | 支付成功后应增加的基础额度单位 |
| `payment_events` | `provider_event_id` | 支付渠道事件 ID，用于幂等处理 |
| `system_configs` | `billing.user_group_ratios` | 用户分组倍率 JSONB 配置 |
| `model_prices` | `price_expression` | 系统模型价格表达式，返回倍率前的 `base_quota` |
| `model_prices` | `variables_json` | 系统模型价格变量默认值 |
| `model_prices` | `unit_tokens` | token 计价单位 |
| `model_prices` | `rule_version` | 系统模型价格规则版本 |
| `channel_model_prices` | `price_expression` | 渠道级模型价格表达式覆盖，优先于 `model_prices` |
| `channel_model_prices` | `price_mode` | 表达式模板类型，如 `request`、`second`、`token`、`tiered` |
| `channel_model_prices` | `variables_json` | 渠道级模型价格变量覆盖 |
| `channel_model_prices` | `unit_tokens` | 渠道级 token 计价单位覆盖 |
| `channel_model_prices` | `user_enabled` | 该渠道模型是否允许普通用户使用 |
| `system_configs` | `billing.channel_group_ratios` / `billing.model_group_ratios` | 渠道/模型分组倍率 JSONB 配置 |
| `system_configs` | `billing.default_user_channel_group_access` | 默认普通用户可用渠道/模型分组白名单数组配置 |
| `system_configs` | `billing.user_group_channel_ratios` | 用户分组 x 渠道/模型分组额外倍率 JSONB 配置 |
| `system_configs` | `billing.user_group_channel_group_access` | 用户分组额外启用/禁用渠道/模型分组 JSONB 配置 |
| `logs` | `prompt_tokens` | 输入 token 数 |
| `logs` | `completion_tokens` | 输出 token 数 |
| `logs` | `total_tokens` | 总 token 数 |
| `logs` | `quota_used` | 本次最终消耗额度，单位为基础额度单位 |
| `logs` | `billing_status` | 计费状态，如 `settled`、`failed` |
| `logs` | `billing_expression_id` | 本次请求使用的表达式 ID |
| `logs` | `billing_expression_version` | 本次请求使用的表达式版本 |
| `logs` | `billing_expression_source` | 表达式来源，如 `model_prices`、`channel_model_prices` |
| `logs` | `billing_expression_snapshot` | 实际执行的计费表达式快照 |
| `logs` | `multiplier_snapshot` | 用户分组、渠道分组、用户分组 x 渠道分组倍率快照 |
| `logs` | `access_rule_snapshot` | 渠道模型和用户分组访问控制快照 |
| `logs` | `usage_source` | usage 来源，如 `upstream`、`adapter`、`tokenizer`、`estimate` |

## 后台配置

模型价格表达式存储在专用 SQL 表中：系统模型价格存储在 `model_prices`，渠道级模型价格覆盖存储在 `channel_model_prices`，它们不是 JSON blob。运行时开关、全局默认值、全局默认 tokenizer、用户分组倍率、渠道/模型分组倍率、用户分组 x 渠道/模型分组额外倍率、默认普通用户渠道/模型分组可用白名单、用户分组渠道/模型分组访问覆盖存储在系统配置 SQL 表（如 `system_configs`）的 JSONB 中。倍率和可用性配置不是模型价格实体。

| 配置键 | 示例值 | 说明 |
|--------|--------|------|
| `billing.precharge_tokens_per_request` | `4096` | 每次请求开始时默认预扣或预留的输出 token 数 |
| `payment.stripe.enabled` | `false` | 是否启用 Stripe 支付 |
| `payment.epay.enabled` | `false` | 是否启用易支付 |

配置要求：

- 系统模型价格、渠道级模型价格覆盖、普通用户可用开关、渠道分组倍率、用户分组 x 渠道分组倍率、用户分组渠道/模型分组访问配置、计费规则版本变更都必须记录审计日志。
- 每次计费规则变更必须生成新的 `rule_version` 或表达式版本，已完成请求不受新规则影响。

## 价格配置

模型价格表达式存储在专用 SQL 表中：系统模型价格存储在 `model_prices`，渠道级模型价格覆盖存储在 `channel_model_prices`，它们不是 JSON blob。倍率/比例配置存储在系统配置 SQL 表（如 `system_configs`）的 JSONB 中。按次、按秒、按 token、阶梯等价格模式都是表达式模板：服务代码可以生成模板，但持久化后的价格规则始终是 `price_expression` 文本和 `variables_json` 变量。

### SQL 表

#### `model_prices`

系统模型价格表，提供模型全局默认价格表达式。

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

渠道级模型价格覆盖表，优先级高于 `model_prices`。

| 字段 | 说明 |
|------|------|
| `id` | 主键 |
| `channel_id` | 渠道 ID |
| `model` | 模型名 |
| `enabled` | 是否启用该渠道覆盖 |
| `price_mode` | 表达式模板类型，如 `request`、`second`、`token`、`tiered` |
| `override_mode` | 覆盖模式，如 `override` 或 `merge_variables` |
| `price_expression` | 渠道级价格表达式覆盖 |
| `variables_json` | 渠道级变量覆盖 |
| `unit_tokens` | 渠道级计价 token 单位覆盖 |
| `user_enabled` | 是否允许普通用户通过该渠道调用该模型；`false` 表示仅管理员、内部任务或显式允许的后台流程可用 |
| `rule_version` | 渠道价格规则版本 |
| `created_at` | 创建时间 |
| `updated_at` | 更新时间 |

#### `system_configs`

系统配置表用于存储运行时开关、全局默认值和倍率 JSONB。用户提到的“模型分组倍率”在本文档中按现有术语记录为渠道/模型分组倍率配置；如果实现使用渠道分组命名，保持 `channel_group` 术语。

如果使用配置文件维护用户分组访问控制，配置文件结构必须与 `system_configs.value_jsonb` 一致，并在加载后写入或同步到系统配置数据库；运行时以系统配置数据库中的 JSONB 为准。

| 字段 | 说明 |
|------|------|
| `key` | 配置键 |
| `value_jsonb` | JSONB 配置值 |
| `version` | 配置版本 |
| `enabled` | 是否启用 |
| `created_at` | 创建时间 |
| `updated_at` | 更新时间 |

建议配置键：

| 配置键 | 说明 |
|--------|------|
| `billing.user_group_ratios` | 用户分组倍率，例如 `{ "vip": 0.8, "default": 1 }` |
| `billing.channel_group_ratios` / `billing.model_group_ratios` | 渠道/模型分组倍率，例如 `{ "premium": 1.2, "default": 1 }` |
| `billing.user_group_channel_ratios` | 用户分组 x 渠道/模型分组额外倍率，例如 `{ "vip": { "premium": 0.9 } }` |
| `billing.default_user_channel_group_access` | 默认普通用户可用渠道/模型分组白名单数组，例如 `["default", "standard"]` |
| `billing.user_group_channel_group_access` | 用户分组额外启用/禁用渠道/模型分组，例如 `{ "vip": { "enable": ["premium"], "disable": ["experimental"] }}` |

倍率配置从 `system_configs.value_jsonb` 读取，不再维护为 `groups.ratio`、`channel_groups.ratio` 或 `user_group_channel_ratios.ratio` 等独立 SQL 列，除非未来实现明确选择该 schema。

### 可用性控制

渠道模型可用性和计费价格是两套独立规则。

普通用户请求选择渠道前必须先做可用性判断：

```text
if channel_model_prices.user_enabled == false:
    普通用户请求拒绝或跳过该渠道模型

access = system_configs["billing.user_group_channel_group_access"][user_group]
if channel_group in access.disable:
    拒绝或跳过该渠道/模型分组
else if channel_group in access.enable:
    额外允许该渠道/模型分组
else if channel_group in system_configs["billing.default_user_channel_group_access"]:
    默认普通用户允许该渠道/模型分组
else:
    默认普通用户拒绝或跳过该渠道/模型分组
```

规则说明：

- `channel_model_prices.user_enabled` 控制某个渠道下某个模型是否允许普通用户使用，不影响管理员测试、内部任务或后台显式授权流程。
- `billing.default_user_channel_group_access` 控制普通用户默认可用的渠道/模型分组，使用数组表达，是默认白名单。
- `billing.user_group_channel_group_access` 只控制指定用户分组对渠道/模型分组的额外启用或禁用，不参与价格表达式，也不参与倍率计算。
- 默认白名单和用户分组配置都命中时，用户分组配置优先；用户分组配置中同时命中 `enable` 和 `disable` 时，`disable` 优先。
- 未配置用户分组访问覆盖时，按默认白名单判断；未配置默认白名单时，不额外限制默认可用性。
- 最终日志应记录 `access_rule_snapshot`，便于审计为什么某次请求可以或不可以使用某个渠道分组。

### 计费规则层级与优先级

计费配置按以下层级合成最终规则：

| 优先级 | 层级 | 能力 | 说明 |
|--------|------|------|------|
| 1 | 渠道模型价格 | 模型级表达式、价格模式和 `unit_tokens` 配置 | 存储在 `channel_model_prices`；覆盖系统模型价格 |
| 2 | 系统模型价格 | 全局表达式、价格模式、`unit_tokens`、版本和启用状态 | 存储在 `model_prices` |
| 3 | 渠道模型普通用户可用性 | 普通用户是否可以使用指定渠道模型 | 存储在 `channel_model_prices.user_enabled`；不是价格表达式的一部分 |
| 4 | 默认普通用户渠道/模型分组可用性 | 普通用户默认可用的渠道/模型分组白名单数组 | 从 `system_configs.value_jsonb` 的 `billing.default_user_channel_group_access` 读取；不是价格表达式的一部分 |
| 5 | 用户分组渠道/模型分组访问覆盖 | 指定用户分组额外启用或禁用渠道/模型分组 | 从 `system_configs.value_jsonb` 的 `billing.user_group_channel_group_access` 读取；不是价格表达式的一部分 |
| 6 | 用户分组倍率 | 用户分组倍率 | 从 `system_configs.value_jsonb` 的 `billing.user_group_ratios` 读取；不是价格表达式的一部分 |
| 7 | 渠道/模型分组倍率 | 渠道/模型分组倍率 | 从 `system_configs.value_jsonb` 的 `billing.channel_group_ratios` 或 `billing.model_group_ratios` 读取；不是价格表达式的一部分 |
| 8 | 用户分组 x 渠道/模型分组倍率 | 指定用户分组使用指定渠道/模型分组时的额外倍率 | 从 `system_configs.value_jsonb` 的 `billing.user_group_channel_ratios` 读取；不是价格表达式的一部分 |

解析规则：

- 先按渠道和模型读取 `channel_model_prices`；存在启用规则时优先使用渠道级价格表达式。
- 渠道级规则不存在或未启用时，读取 `model_prices` 作为系统模型价格。
- 普通用户请求必须先通过 `channel_model_prices.user_enabled`、`billing.default_user_channel_group_access` 和 `billing.user_group_channel_group_access` 可用性判断，未通过的渠道不参与后续计费和路由选择。
- `request`、`second`、`token`、`tiered` 都只是模板类型，最终执行的是 SQL 中保存的 `price_expression` 和 `variables_json`。
- 倍率字段独立于价格表达式，服务代码不得把用户分组倍率、渠道分组倍率或用户分组 x 渠道分组倍率嵌入 `price_expression`。

### 表达式与倍率

价格表达式只计算倍率前的 `base_quota`，单位为数据库 token / 基础额度单位。倍率从系统配置 JSONB 读取，并在表达式求值后应用。

```text
base_quota = expression_engine.evaluate(price_expression, variables)
effective_ratio = user_group_ratio * channel_group_ratio * user_group_channel_ratio
quota_used = ceil(base_quota * effective_ratio)
quota_used = max(min_charge_quota, quota_used) 仅当 billable 且 base_quota > 0
```

倍率组合是账单聚合规则，不是价格表达式的一部分。价格表达式不得引用倍率变量，也不得把倍率计算硬编码到表达式文本中。

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

在线支付用于购买用户额度。支付金额、货币、赠送额度和最终入账额度必须由服务端商品配置决定，客户端不能直接提交要增加的 `quota`。

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

支付开关和非敏感配置可以存储在 `system_configs.value_jsonb`。敏感配置必须加密存储，或通过环境变量/KMS 注入后写入运行时配置。

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
