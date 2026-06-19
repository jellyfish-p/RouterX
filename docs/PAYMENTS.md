# RouterX 支付与充值插件契约

本文档定义 RouterX 的支付、充值码、退款、人工补账和额度流水契约。支付是可选运营模块，不是 P0 自部署最小闭环前置条件；但一旦启用，必须达到商业级账务安全：金额可信、入账幂等、额度可追溯、审计完整、密钥不泄露。

本文不展开部署教程，不设计网页页面，不复刻 Stripe 或易支付官方文档。它只定义 RouterX 内部必须稳定的 provider 插件边界、订单状态机、额度入账事务和异常处理。

相关文档：

- 计费事实链：`docs/BILLING.md`
- 支付接口：`docs/API.md`
- 数据模型：`docs/DATA_MODEL.md`
- 安全威胁：`docs/SECURITY.md`
- 控制台能力：`docs/CONSOLE.md`
- 观测审计：`docs/OBSERVABILITY.md`
- 故障处理：`docs/RUNBOOKS.md`
- settings 注册表：`docs/SETTINGS.md`

## 1. 设计原则

### 支付不阻塞开箱

RouterX 的 P0 开箱路径是初始化、通道、API Key、首次调用、日志和额度变化。支付、充值码、退款、人工补账和报表属于运营增强；未启用支付时，系统仍必须能通过管理员用户额度调整和 API Key 预算控制完成基础运营。

### 服务端事实优先

支付入账只信任服务端事实：

| 事实 | 权威来源 |
|------|----------|
| 商品金额 | `payment_products` 或服务端商品配置 |
| 币种 | `payment_products` 或 provider 配置 |
| 入账额度 | `payment_products.quota + bonus_quota` 的服务端快照 |
| 订单归属 | `payment_orders.user_id` |
| 订单状态 | `payment_orders.status` |
| 回调可信度 | provider 签名、金额、币种、订单号和事件幂等 |
| 最终额度变化 | 额度流水和 `users.quota` 同事务结果 |

客户端提交的 `quota`、金额、币种、用户 ID、订单号和支付状态只能作为请求输入或跳转状态，不是入账依据。

### Provider 可插拔，账务不可插拔

Stripe、易支付、充值码、人工补账和后续 provider 可以有不同接入方式，但它们最终都必须汇入同一套内部事实：

```text
external event or admin action
    -> validate source
    -> resolve local order or adjustment target
    -> write idempotency record
    -> apply quota transaction
    -> update order or code status
    -> write audit event
    -> expose safe status
```

Provider 适配器只能负责外部协议差异，不能绕过 RouterX 的商品、订单、额度、审计和幂等规则。

## 2. 能力分层

| 阶段 | 能力 | 目标 |
|------|------|------|
| P0 | 管理员调整额度 | 不依赖支付也能运营小规模实例 |
| P1 | 充值码 | 支持离线发码、兑换、幂等和审计 |
| P2 | 在线支付插件 | Stripe、易支付、订单、事件、签名、幂等入账 |
| P2 | 退款和人工修正 | 支持退款记录、补账、扣回、争议处理和审计 |
| P2/P3 | 报表和导出 | 收入、额度、退款、人工修正、账务核对 |

阶段约束：

- P0 不能依赖支付 provider。
- P1 充值码也不能破坏模型消费事实链。
- P2 在线支付必须先有幂等事件、订单状态机和额度流水，再开放真实 provider。

## 3. 核心对象

### 充值商品

`payment_products` 定义用户可以买什么。

必备字段：

| 字段 | 说明 |
|------|------|
| `product_id` | 服务端商品 ID |
| `name` | 商品名称 |
| `amount` | 支付金额，建议用定点字符串或最小货币单位 |
| `currency` | 币种，例如 `usd`、`cny` |
| `quota` | 基础入账额度 |
| `bonus_quota` | 赠送额度 |
| `enabled` | 是否可购买 |
| `provider_config_json` | provider 限定配置，例如 Stripe price id |

商品快照要求：

- 创建订单时必须保存商品快照，包括金额、币种、额度、赠送额度和商品版本。
- 商品后续修改不影响已创建订单。
- 已禁用商品不能创建新订单，但历史订单仍可查询和完成回调。

### 支付订单

`payment_orders` 是一次购买意图。

状态机：

```text
pending
    -> paid
    -> failed
    -> closed
    -> refunded
    -> partially_refunded
```

状态含义：

| 状态 | 含义 | 是否入账 |
|------|------|----------|
| `pending` | 已创建，等待支付 | 否 |
| `paid` | provider 成功且已入账 | 是 |
| `failed` | provider 明确失败；签名、金额或订单快照校验失败只拒绝入账，不直接改订单 | 否 |
| `closed` | 用户取消、过期或管理员关闭 | 否 |
| `refunded` | 全额退款已记录 | 按退款策略处理 |
| `partially_refunded` | 部分退款已记录 | 按退款策略处理 |

状态转换规则：

- `pending -> paid` 必须在同一事务内完成订单状态更新、额度入账和额度流水写入。
- `pending -> closed` 可由当前用户取消自己的未支付订单触发，不能增加额度；重复取消已关闭订单应幂等返回当前状态。
- `paid -> refunded` 或 `paid -> partially_refunded` 不应删除原始入账事实，只追加退款事实。
- `failed`、`closed` 不能直接转 `paid`，除非 provider 后续给出可信成功事件，并保留状态修正审计。
- 重复成功回调只能返回已处理结果，不重复加额度。

### 支付事件

`payment_events` 是 provider 回调、同步返回、查询结果或忽略事件的事实记录。

要求：

- `(provider, provider_event_id)` 唯一。
- 签名失败也要记录脱敏事件，便于风控和排障。
- 原始 payload 默认脱敏或加密保存。
- 事件处理结果必须明确：`ignored`、`rejected`、`processed`、`duplicate`、`error`。

易支付没有稳定事件 ID 时，可以使用以下派生键：

```text
epay:{out_trade_no}:{trade_no}:{trade_status}:{money}
```

如果 provider 无可靠事件 ID，必须以本地订单号、provider 交易号和状态组合做幂等键。

### 额度流水

支付、充值码、退款、人工补账和扣回都应进入统一额度流水。目标表建议命名为 `quota_transactions`。

目标字段：

| 字段 | 说明 |
|------|------|
| `id` | 主键 |
| `user_id` | 额度归属用户 |
| `type` | `payment_grant`、`redem_redeem`、`admin_adjust`、`refund_deduct`、`manual_credit`、`manual_debit` |
| `amount` | 正数增加额度，负数减少额度 |
| `balance_before` | 变更前用户额度 |
| `balance_after` | 变更后用户额度 |
| `source_type` | `payment_order`、`payment_event`、`redem_code`、`admin_action`、`refund` |
| `source_id` | 来源 ID 或本地订单号 |
| `idempotency_key` | 幂等键 |
| `reason` | 管理员或系统原因 |
| `actor_user_id` | 管理员或系统操作者 |
| `request_id` | HTTP 请求 ID |
| `created_at` | 创建时间 |

约束：

- `idempotency_key` 应唯一，防止同一来源重复改变额度。
- 写额度流水和更新 `users.quota` 必须在同一事务内完成。
- 模型消费日志 `logs.quota_used` 不等同于充值流水；两者可以共同构成账务报表，但不能混为同一类事实。
- 当前后端已提供 `GET /v0/user/quota-transactions` 和 `GET /v0/admin/quota-transactions` 查询该流水；用户侧强制只看自己，管理侧可按用户、类型、来源和时间过滤。

## 4. Provider 插件接口

每个支付 provider 适配器都应满足同一组内部接口。

```text
Provider.CreateCheckout(order, product, user) -> checkout_result
Provider.VerifyWebhook(raw_body, headers, query) -> verified_event
Provider.NormalizeEvent(verified_event) -> payment_event
Provider.ResolveOrder(payment_event) -> order_no
Provider.SuccessPredicate(payment_event) -> bool
Provider.ProviderID() -> string
```

适配器责任：

| 责任 | 说明 |
|------|------|
| 创建支付会话 | 生成 checkout URL、provider_order_id 或跳转参数 |
| 校验签名 | 使用 provider 要求的原始 body、query 或表单参数 |
| 标准化事件 | 提取事件 ID、订单号、金额、币种、状态和交易号 |
| 判断成功 | 只接受明确成功状态 |
| 生成幂等键 | provider 事件 ID 或可靠派生键 |
| 脱敏 payload | 删除签名密钥、敏感 header、用户隐私和无关大字段 |

适配器不得：

- 直接修改 `users.quota`。
- 信任客户端传入的额度。
- 跳过本地订单金额和币种校验。
- 在 provider 回调里返回内部堆栈。
- 将支付密钥写入日志、事件 payload 或审计明文。

## 5. Stripe 契约

推荐方式：Checkout Session + Webhook。

创建订单：

```text
user selects product
    -> create local payment_order(pending)
    -> create Stripe Checkout Session
    -> metadata: order_no, user_id, product_id
    -> save provider_order_id=session.id
    -> return checkout_url
```

Webhook：

```text
receive raw body + Stripe-Signature
    -> verify signature with webhook secret
    -> normalize event
    -> write payment_event
    -> if event is checkout.session.completed or trusted success:
        verify order_no, amount_total, currency, status
        mark order paid and grant quota in one transaction
    -> if event is charge.refunded or charge.dispute.*:
        verify original order amount and currency
        record refund/dispute fact and configured risk action
```

Stripe 要求：

- 必须用原始 request body 校验 `Stripe-Signature`。
- `metadata.order_no` 必须能回到本地订单。
- `amount_total`、`currency`、`product_id` 和订单快照必须一致。
- 非成功事件可以记录为 `ignored`，不能入账。
- 退款事件先记录为 refund fact；是否扣回额度由退款策略决定。
- 争议/拒付事件先记录为 dispute fact；是否冻结 API Key 由风控策略决定。

当前基础实现已支持创建 Stripe Checkout Session：当 `PAYMENT_STRIPE_SECRET_KEY` 和绝对 `return_url` 齐全时，用户创建订单会向 Stripe 写入 Checkout Session，metadata 包含 `order_no`、`user_id`、`product_id`，本地订单保存 `provider_order_id=session.id` 和 `checkout_url=session.url`；配置不足时仍返回本地安全 checkout 占位链接，便于开发演示但不适合作为生产收银台。`POST /v0/payment/stripe/webhook` 支持 `checkout.session.completed` 成功事件、`Stripe-Signature` 校验、订单 metadata 校验、金额/币种快照校验、`payment_events` 幂等、`quota_transactions` 入账，并写入 `payment_webhook.processed` 和 `payment_order.paid` 审计摘要。`charge.refunded` 全额或部分退款事件会记录 `payment_refund.processed`，自动扣回成功时额外记录 `payment_refund.deducted`；`charge.dispute.created/updated/closed/funds_withdrawn/funds_reinstated` 会更新 `payment_disputes` 并记录 `payment_dispute.*` 生命周期审计，created 阶段可按 settings 禁用该用户已启用的 API Key。

Stripe `checkout.session.async_payment_failed` 当前也会在签名、金额、币种和 metadata 校验通过且本地订单仍为 `pending` 时，把订单置为 `failed`，写 `payment_webhook.failed` 审计且不增加额度。

## 6. 易支付契约

易支付适配器用于跳转收银台和异步通知。

创建订单：

```text
user selects product
    -> create local payment_order(pending)
    -> build epay params
    -> sign params
    -> return pay_url or form params
```

异步通知：

```text
receive notify
    -> verify sign
    -> verify pid
    -> verify out_trade_no
    -> verify money and success status
    -> derive provider_event_id
    -> write payment_event
    -> mark order paid and grant quota in one transaction
    -> return provider-required success text
```

易支付要求：

- `out_trade_no` 必须由 RouterX 生成。
- 签名时排除 `sign`、`sign_type` 和空值字段。
- 金额必须和本地订单一致。
- 同步返回页只展示本地订单状态，不作为入账依据。
- 管理端向易支付发起退款请求时必须使用独立的 `payment.epay.refund_url`，携带 `pid`、`out_trade_no`、`money`、`reason`、`idempotency_key` 和 MD5 签名；返回成功只代表 provider 已受理，本地订单先进入 `refund_pending`。
- 不同易支付实现的参数名和签名规则可能不同，适配器必须把差异限制在 provider 层。

## 7. 充值码契约

充值码是支付不可用或离线运营时的可选能力。

对象：`redem_codes`。当前代码命名为 `RedemCode`，文档沿用现有命名。

创建要求：

- 管理员生成，或系统批量生成。
- code 唯一，建议使用不可猜测随机值。
- quota 使用基础额度单位。
- 可选：过期时间、批次、备注、发放对象、创建者。

兑换流程：

```text
user submits code
    -> lock redem_codes row
    -> verify unused, not expired, enabled
    -> write quota_transaction(type=redem_redeem)
    -> users.quota += redem_codes.quota
    -> mark redem_codes used
    -> write audit
```

当前实现：

- 用户可通过 `POST /v0/user/redem` 兑换未使用充值码。
- 兑换在同一数据库事务内完成过期校验、`redem_codes.status/used_by/used_at` 更新、`users.quota` 增加和 `quota_transactions` 写入。
- 同一个充值码只能成功兑换一次；已使用、已作废、已过期或不存在的充值码不会写入额度流水，重复兑换不会重复入账。
- 兑换成功会写入 `redem_code.redeem` 管理审计，摘要记录脱敏兑换码、额度、使用人和兑换后余额，不保存完整兑换码。
- 管理员额度调整已写入 `quota_transactions`，记录 `actor_user_id`、`reason`、变更前后余额和幂等键。
- 用户可通过 `POST /v0/user/payment/orders/:order_no/cancel` 取消自己的 `pending` 订单，订单置为 `closed` 并写 `payment_order.cancel` 审计；已 `closed` 订单幂等返回，已支付、退款中或已退款订单不能取消。
- 管理员可通过 `/v0/admin/redem` 生成随机充值码或导入指定充值码，可写入 `batch_no`、`note` 和未来 `expired_at`，并可作废未使用充值码；这些管理操作会写入 `redem_code.*` 管理审计，完整兑换码只进入脱敏摘要。
- 管理员可通过 `/v0/admin/payment/products` 创建、更新、启用和禁用支付商品；用户侧只展示启用商品，禁用商品不能创建新订单；支付商品管理成功操作会写入 `admin_audit_logs`。
- 用户侧支付商品列表和本地 `pending` 订单创建/查询已具备基础实现；创建订单要求对应 provider 已在 settings 启用，并会写 `payment_order.create` 管理审计，摘要不保存 checkout URL。Stripe secret 和绝对 `return_url` 齐全时会创建 Stripe Checkout Session；易支付网关、商户号、回调 URL 和 `PAYMENT_EPAY_KEY` 配置齐全时会返回签名收银台 URL；pending 订单不会入账。
- Stripe webhook 已支持 `checkout.session.completed` 签名校验、金额/币种/metadata 校验、`payment_events` 幂等、入账审计和入账；`checkout.session.async_payment_failed` 会在快照校验通过且订单仍为 pending 时置为 `failed`，写 `payment_webhook.failed` 审计且不入账；`charge.refunded` 全额或部分退款事件可幂等记录订单退款状态，写入退款审计，并可按 settings 全额或比例扣回额度；`charge.dispute.created/updated/closed/funds_withdrawn/funds_reinstated` 可幂等更新 `payment_disputes` 争议事实，写入争议生命周期审计，并可在 created 阶段按 settings 禁用用户已启用 API Key。
- 易支付异步通知已支持 MD5 签名校验、金额校验、`payment_events` 幂等记录、成功订单置为 `paid`、明确失败订单置为 `failed`、`quota_transactions` 入账、用户额度增加和基础 webhook/入账/失败审计；重复通知不重复入账。
- 易支付同步返回页已支持本地订单状态只读展示，不作为入账依据。
- 支付相关人工补账/扣回已支持 `POST /v0/admin/payment/adjustments`，会写 `manual_credit` 或 `manual_debit` 额度流水，并在同一事务中写 `payment_manual_adjust.credit` 或 `payment_manual_adjust.debit` 审计。
- 管理员确认后的人工退款已支持 `POST /v0/admin/payment/refunds`，会校验 `paid` 订单，按退款额度写 `refund_deduct` 额度流水，将订单置为 `refunded` 或 `partially_refunded`，并写 `payment_refund.manual` 审计。
- Stripe 和易支付 provider 侧退款请求已支持 `POST /v0/admin/payment/refund-requests`，会创建 provider refund、记录退款请求并将订单置为 `refund_pending`；Stripe 最终状态仍由 webhook 确认，易支付需等待可信后续通知或人工收尾。
- 更多 provider 会话创建、更多 provider 自动退款适配和更多 provider 争议生命周期仍属于后续增强，不能把当前实现误写成完整支付闭环。

要求：

- 同一个充值码只能成功兑换一次。
- 并发兑换必须通过行锁或唯一流水幂等键保护。
- 兑换失败不能泄露是否存在某批内部发放策略。
- 管理员作废未使用充值码必须写审计。

## 8. 退款契约

退款不应删除原入账事实。退款是新的账务事件。

默认策略建议：

| 场景 | 默认策略 |
|------|----------|
| provider 通知退款 | 记录退款事件和订单退款状态 |
| 用户余额足够扣回 | 可按配置自动扣回，写 `refund_deduct` 流水 |
| 用户余额不足 | 不自动产生负余额，转人工处理或风控冻结 |
| 部分退款 | 按比例或固定额度策略记录，必须可解释 |
| 争议或拒付 | 记录 dispute lifecycle，按风控策略冻结用户或 API Key |

退款状态：

- `refunded`：全额退款。
- `partially_refunded`：部分退款。
- `refund_pending`：等待 provider 或人工处理。
- `refund_failed`：退款失败。

关键约束：

- 退款事件必须幂等。
- 原支付订单和原额度入账流水不可删除。
- 自动扣回策略必须进入 settings，并在审计中保留策略快照。
- 人工处理必须记录操作者、原因、前后余额和关联订单。

当前基础实现：

- Stripe `charge.refunded` 退款事件会校验原订单金额和币种，写入 `payment_events` 幂等事实；全额退款将订单置为 `refunded`，部分退款将订单置为 `partially_refunded`，并可收尾此前由管理端发起后进入 `refund_pending` 的订单。
- 管理员可通过 `POST /v0/admin/payment/refund-requests` 向 Stripe 或易支付发起 provider 退款请求；接口会调用 Stripe Refund API 或配置的易支付退款地址，写入 `payment_refund_requests`，订单进入 `refund_pending`，并写 `payment_refund.requested` 审计。退款是否最终成功仍以后续可信 provider 事件或人工收尾为准。
- 默认 `payment.refund.auto_deduct=false`，退款只记录订单状态，不扣用户额度。
- 退款处理成功会写入 `payment_refund.processed` 管理审计，摘要记录 provider、event_id、订单号、金额、币种、额度和扣回策略快照。
- 开启 `payment.refund.auto_deduct=true` 后，如果用户余额足够，写入 `quota_transactions(type=refund_deduct, source_type=refund)` 并扣回原订单额度或按退款金额比例扣回额度，同时写入 `payment_refund.deducted` 审计；重复退款事件不重复扣回。
- `payment.refund.allow_negative_balance=false` 时余额不足不会自动扣成负数，需转人工处理。
- 管理员可通过 `POST /v0/admin/payment/refunds` 对已经确认的退款事实做人工落账；接口要求 `order_no`、`refund_quota`、`reason` 和 `idempotency_key`，只接受 `paid` 订单，`refund_quota` 不能超过订单额度。
- 人工退款成功后写入 `quota_transactions(type=refund_deduct, source_type=refund, source_id=<order_no>)`，全额退款将订单置为 `refunded`，部分退款置为 `partially_refunded`，并写入 `payment_refund.manual` 管理审计；重复 `idempotency_key` 不会重复扣回。
- Stripe `charge.dispute.created/updated/closed/funds_withdrawn/funds_reinstated` 争议生命周期事件会校验原订单金额和币种，写入 `payment_events` 幂等事实，并按 `provider_dispute_id` upsert `payment_disputes` 当前事实。
- 争议生命周期审计会按事件写入 `payment_dispute.created`、`payment_dispute.updated`、`payment_dispute.closed` 或 `payment_dispute.funds_changed`，审计资源为 `payment_dispute`，便于按 Stripe dispute id 串联整个生命周期；created 事件也保留 `payment_event` 资源审计以兼容事件排障。
- 默认 `payment.dispute.auto_disable_tokens=false`，争议只记录事实；开启后会在 created 阶段把该用户已启用的 API Key 置为禁用并记录 `revoked_reason=payment_dispute`，不直接修改用户额度或订单状态。

## 9. 人工补账和扣回

人工额度调整用于客服、异常修正、退款争议、促销补偿和账务纠错。

类型：

| 类型 | 说明 |
|------|------|
| `manual_credit` | 人工增加用户额度 |
| `manual_debit` | 人工扣减用户额度 |
| `admin_adjust` | 一般管理员调整，适合 P0/P1 |
| `refund_deduct` | 因退款或拒付产生的扣回 |

要求：

- 必须只允许管理员或超级管理员执行。
- 必须填写原因。
- 必须写 `quota_transactions`。
- 必须写管理审计。
- 必须可从用户、订单、事件或工单追溯。
- 高风险大额调整目标支持二次确认或双人审批。

禁止：

- 直接手工改 `users.quota` 而不写流水。
- 用人工补账掩盖支付签名失败。
- 把模型消费扣费和人工调整混在同一日志口径。

当前基础实现：

- 管理员可通过 `POST /v0/admin/payment/adjustments` 做支付相关人工补账或扣回。
- `amount > 0` 写 `quota_transactions(type=manual_credit)`，`amount < 0` 写 `quota_transactions(type=manual_debit)`；扣回默认不允许余额扣成负数。
- `reason` 默认必填，由 `payment.manual_adjust.require_reason=true` 控制；`idempotency_key` 必填，用于防止同一人工动作重复改变余额。
- 传入 `order_no` 时会校验订单属于目标用户，并把流水来源记为 `source_type=payment_order`、`source_id=<order_no>`。
- 成功后写 `payment_manual_adjust.credit` 或 `payment_manual_adjust.debit` 审计，摘要包含用户、订单号、金额、原因、前后余额、幂等键和来源。
- 管理员可通过 `POST /v0/admin/payment/refunds` 对支付订单做人工退款落账；该路径使用 `refund_deduct` 表示退款扣回，不复用 `manual_debit`，便于财务和客服按退款口径追踪。
- 人工退款遵循 `payment.manual_adjust.require_reason` 和 `payment.refund.allow_negative_balance`，成功后审计摘要包含订单、用户、退款额度、订单状态、原因、前后余额、幂等键和 provider 快照。
- `payment.manual_adjust.large_amount_threshold` 已注册为非负整数配置，后续可用于二次确认或双人审批。

## 10. 订单创建接口契约

用户创建支付订单时：

```text
POST /v0/user/payment/orders
```

服务端必须：

1. 校验用户登录态。
2. 查询服务端商品。
3. 校验商品启用、provider 可用、金额和币种。
4. 创建 `payment_orders(pending)`，保存商品快照，并按 `payment.order_expire_minutes` 写入 `expired_at`。
5. 调用 provider 创建支付会话或跳转参数。
6. 保存 provider_order_id 和 checkout_url。
7. 返回安全响应。

响应不得包含：

- provider secret。
- webhook secret。
- 易支付商户 key。
- 内部签名原文。
- 用户不可见风控字段。

## 11. Webhook 契约

Provider webhook 不使用用户 JWT。

统一处理顺序：

```text
receive webhook
    -> capture request_id
    -> verify provider signature
    -> normalize event
    -> insert payment_event with idempotency key
    -> if duplicate: return provider success response when safe
    -> load local order
    -> verify amount, currency, product snapshot, user
    -> if success event: pay order and grant quota in transaction
    -> if refund event: record refund and apply refund strategy
    -> write audit and metrics
    -> return provider-required response
```

失败处理：

| 失败 | 处理 |
|------|------|
| 签名失败 | 记录脱敏事件，拒绝入账 |
| 订单不存在 | 记录事件，拒绝入账 |
| 金额或币种不匹配 | 记录事件，拒绝入账，触发告警 |
| 订单已 paid | 记录 duplicate，不重复入账 |
| DB 事务失败 | 返回 provider 可重试响应或记录待处理状态 |
| provider 重复通知 | 幂等返回，不重复入账 |

## 12. settings 和密钥

非敏感运行时配置可进入 `settings`：

| key | 说明 |
|-----|------|
| `payment.stripe.enabled` | 是否启用 Stripe |
| `payment.epay.enabled` | 是否启用易支付 |
| `payment.currency` | 默认币种 |
| `payment.order_expire_minutes` | 订单过期分钟数 |
| `payment.refund.auto_deduct` | 退款是否自动扣回余额 |
| `payment.refund.allow_negative_balance` | 是否允许退款造成负余额，默认不建议 |
| `payment.manual_adjust.require_reason` | 人工调整是否必须填写原因 |
| `payment.manual_adjust.large_amount_threshold` | 大额调整阈值 |

敏感密钥来源：

- `PAYMENT_STRIPE_SECRET_KEY`
- `PAYMENT_STRIPE_WEBHOOK_SECRET`
- `PAYMENT_EPAY_KEY`
- KMS 或加密配置

密钥要求：

- 不返回给控制台。
- 不写入日志、审计和支付事件明文。
- 生产开启 provider 时 `/ready` 会确认必需密钥可用。
- 轮换时保留短窗口双密钥或按 provider 支持方式处理。

## 13. 观测和审计

指标：

| 指标 | 标签 | 说明 |
|------|------|------|
| `routerx_payment_orders_total` | provider、status | 支付订单数 |
| `routerx_payment_events_total` | provider、event_type、processed | 支付事件处理状态 |
| `routerx_payment_grants_total` | provider、source_type | 入账次数 |
| `routerx_payment_quota_granted_total` | provider | 入账额度 |
| `routerx_payment_failures_total` | provider、reason | 支付处理失败 |
| `routerx_quota_adjustments_total` | type | 人工调整和退款扣回 |

审计动作：

- 创建、启用、禁用、修改支付商品。
- 创建支付订单。
- 接收和处理 webhook。
- 支付入账。
- 退款记录和扣回。
- 充值码创建、作废、兑换。
- 人工补账、扣回、人工退款落账、Stripe/易支付 provider 退款请求和争议生命周期。
- 支付 settings 和密钥引用变更。

当前基础实现已覆盖支付商品创建、修改、启用、禁用，支付订单创建，Stripe/易支付 webhook 入账，Stripe async payment failed 与易支付明确失败通知审计，Stripe 全额/部分退款和扣回，Stripe 争议生命周期记录和可选 API Key 禁用，支付相关人工补账/扣回、人工退款落账、Stripe/易支付 provider 退款请求，以及充值码生成、导入、批次/备注/过期策略、作废、兑换的成功审计；更多 provider 自动退款适配和更多失败分支审计仍需继续补齐。

审计字段：

- actor、目标用户、订单号、provider、金额、币种、额度、事件 ID、处理结果、前后余额摘要、原因、request_id、IP 和时间。

## 14. 安全边界

支付模块必须防止：

- 客户端伪造额度。
- 伪造 webhook。
- 重复通知重复入账。
- 金额不匹配仍入账。
- 同步返回页作为入账依据。
- 支付密钥进入日志或控制台响应。
- 人工补账绕过审计。
- 退款扣回造成不可解释账本。

安全默认：

- 失败回调只记录事件，不入账。
- provider 成功状态必须白名单判断。
- 自动退款扣回默认保守，不建议造成负余额。
- 支付事件 payload 默认脱敏或加密。
- 大额人工调整需要更强审计，目标支持二次确认。

## 15. 测试要求

支付和充值测试必须使用本地 fake provider 或签名 fixture，不调用真实 Stripe、真实易支付或真实银行通道。

最小测试矩阵：

| 场景 | 断言 |
|------|------|
| 创建订单 | 保存商品快照，返回安全 checkout 信息 |
| Stripe 成功 webhook | 签名正确、金额一致、订单 paid、额度增加、流水写入 |
| Stripe 失败 webhook | 签名正确、金额一致、订单 failed、额度不增加、写失败审计 |
| 易支付成功通知 | 签名正确、金额一致、返回 success、额度只入账一次 |
| 易支付失败通知 | 签名正确、金额一致、返回 success、订单 failed、额度不增加、写失败审计 |
| 重复 webhook | payment_event 幂等，用户额度不重复增加 |
| 签名失败 | 拒绝入账，记录脱敏事件 |
| 金额不匹配 | 拒绝入账，订单不变，触发失败指标 |
| 充值码兑换 | 并发只成功一次，额度流水和 used 状态一致 |
| 退款事件 | 原入账保留，追加退款事实和策略结果 |
| 人工补账 | 必须有原因、审计、前后余额和流水 |
| 密钥脱敏 | 响应、日志、审计、事件 payload 不含支付密钥 |

## 16. 阶段验收

P1 充值码验收：

- 管理员可生成或导入充值码。
- 用户可兑换未使用充值码。
- 并发兑换不会重复入账。
- 兑换写额度流水和审计。

P2 在线支付验收：

- 用户可基于服务端商品创建订单。
- Stripe/易支付 provider 通过本地 fixture 完成签名、金额、状态和幂等测试。
- 支付成功只入账一次。
- 签名失败、金额不匹配、订单状态不匹配不入账。
- 支付事件、订单、额度流水和审计可以串联。

P2 退款和人工修正验收：

- 退款不删除原始支付和入账事实。
- 自动扣回策略可配置且有审计。
- 人工补账和扣回必须写原因、流水和审计。
- 报表能区分支付入账、充值码入账、人工调整、退款扣回和模型消费。

## 17. 文档同步

新增或改变支付、充值、退款或人工调整能力时，必须同步检查：

- `docs/API.md`：接口、鉴权、webhook 响应和错误。
- `docs/BILLING.md`：额度、流水、入账和账单口径。
- `docs/DATA_MODEL.md`：订单、事件、充值码、额度流水和索引。
- `docs/SECURITY.md`：支付伪造、密钥、人工调整和退款风险。
- `docs/OBSERVABILITY.md`：指标、审计、告警和保留。
- `docs/CONSOLE.md`：运营控制台状态、动作和证据链。
- `docs/RUNBOOKS.md`：支付失败、重复通知、退款争议和人工补账处理。
- `docs/SETTINGS.md`：支付开关、退款策略和人工调整策略。
- `docs/TESTING.md`：fake provider、签名 fixture 和幂等测试。
- `docs/TRACEABILITY.md`：能力 ID 和验收证据。

支付能力的商业级标准不是“能跳转收银台”，而是任何钱、额度、订单和人工修正都能被证明、审计、复核和安全回滚。
