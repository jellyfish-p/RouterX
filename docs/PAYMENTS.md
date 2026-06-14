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
| `failed` | provider 失败或校验失败 | 否 |
| `closed` | 用户取消、过期或管理员关闭 | 否 |
| `refunded` | 全额退款已记录 | 按退款策略处理 |
| `partially_refunded` | 部分退款已记录 | 按退款策略处理 |

状态转换规则：

- `pending -> paid` 必须在同一事务内完成订单状态更新、额度入账和额度流水写入。
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
```

Stripe 要求：

- 必须用原始 request body 校验 `Stripe-Signature`。
- `metadata.order_no` 必须能回到本地订单。
- `amount_total`、`currency`、`product_id` 和订单快照必须一致。
- 非成功事件可以记录为 `ignored`，不能入账。
- 退款事件先记录为 refund fact；是否扣回额度由退款策略决定。

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
- 兑换在同一数据库事务内完成 `redem_codes.status/used_by/used_at` 更新和 `users.quota` 增加。
- 同一个充值码只能成功兑换一次；已使用或不存在的充值码返回统一失败。
- `quota_transactions` 和管理审计仍属于后续增强，不能把当前实现误写成完整账务流水闭环。

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
| 争议或拒付 | 记录 dispute event，按风控策略冻结用户或 API Key |

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

## 10. 订单创建接口契约

用户创建支付订单时：

```text
POST /v0/user/payment/orders
```

服务端必须：

1. 校验用户登录态。
2. 查询服务端商品。
3. 校验商品启用、provider 可用、金额和币种。
4. 创建 `payment_orders(pending)`，保存商品快照。
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
- 生产开启 provider 时 `/ready` 目标检查应确认必需密钥可用。
- 轮换时保留短窗口双密钥或按 provider 支持方式处理。

## 13. 观测和审计

指标：

| 指标 | 标签 | 说明 |
|------|------|------|
| `routerx_payment_orders_total` | provider、status | 支付订单数 |
| `routerx_payment_events_total` | provider、event_type、result | 支付事件处理数 |
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
- 人工补账和扣回。
- 支付 settings 和密钥引用变更。

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
| 易支付成功通知 | 签名正确、金额一致、返回 success、额度只入账一次 |
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
