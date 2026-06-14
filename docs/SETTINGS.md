# RouterX settings 注册表

## 目标

`settings` 是 RouterX 运行时配置的权威来源。本文档负责把配置从“原则”落到可实现的注册表：配置 key、分类、类型、默认值、生效方式、敏感级别、阶段边界和校验规则。

环境变量只承载启动必须项、跨实例必须一致的密钥和外部服务密钥，例如 `SQL_DSN`、`LOG_SQL_DSN`、`REDIS_CONN`、`JWT_SECRET`、`ENCRYPTION_KEY` 和支付密钥。业务运行时开关、默认值、计费倍率、限流、Relay 和日志策略应进入 `settings`。

## 设计规则

- 所有业务代码通过 typed accessor 读取配置，不在 handler、service 或 adapter 中散落字符串解析。
- 首次初始化和迁移只补缺失配置，不覆盖管理员已经修改的值。
- 修改配置必须先校验，再写 DB，再刷新缓存，再写审计摘要。
- 敏感配置在接口响应、日志和审计中脱敏。
- 配置的生效方式必须明确：热更新、刷新缓存、重启、仅初始化生效。
- 生产 readiness 必须检查关键配置是否存在、格式是否合法，以及跨实例必须一致的密钥是否可用。

## 字段合同

当前 `settings` 表可以保持 `key/value/category/description` 的简单结构，但服务层应维护等价的注册表元数据。

| 字段 | 说明 |
|------|------|
| `key` | 全局唯一配置键，例如 `relay.timeout` |
| `category` | 分类，例如 `relay`、`billing` |
| `value_type` | `string`、`int`、`float`、`bool`、`json` |
| `default_value` | 初始化或补缺失时使用的默认值 |
| `stage` | `current`、`P0 target`、`P1`、`P2` |
| `sensitive` | 是否需要脱敏 |
| `effect` | `hot`、`cache_refresh`、`restart`、`init_only` |
| `validator` | 校验规则或校验器名称 |
| `owner` | 主要消费模块 |

## 当前初始化配置

以下 key 来自当前 `SetupService` 初始化默认值，是 P0 已存在的配置事实。

| key | category | type | 默认值 | sensitive | effect | owner | 校验 |
|-----|----------|------|--------|-----------|--------|-------|------|
| `server.port` | `server` | int | `3000` | 否 | restart | server | `1..65535` |
| `server.mode` | `server` | string | `release` | 否 | restart | server | `debug/test/release` |
| `jwt.secret` | `jwt` | string | `JWT_SECRET` 或初始化生成 | 是 | restart/cache_refresh | auth | 长度不少于 32，生产必须固定 |
| `jwt.admin_expire_hours` | `jwt` | int | `24` | 否 | cache_refresh | auth | `>0` |
| `jwt.user_expire_hours` | `jwt` | int | `168` | 否 | cache_refresh | auth | `>0` |
| `rate_limit.enabled` | `rate_limit` | bool | `true` | 否 | hot | rate limit | bool |
| `rate_limit.global_per_min` | `rate_limit` | int | `1000` | 否 | hot | rate limit | `>=0` |
| `rate_limit.per_token_per_min` | `rate_limit` | int | `60` | 否 | hot | rate limit | `>=0` |
| `rate_limit.per_ip_per_min` | `rate_limit` | int | `30` | 否 | hot | rate limit | `>=0` |
| `relay.timeout` | `relay` | int | `120` | 否 | hot | relay | `>0` |
| `relay.retry_count` | `relay` | int | `0` | 否 | hot | relay | `>=0` |
| `relay.error_auto_ban` | `relay` | bool | `true` | 否 | hot | relay | bool |
| `relay.error_ban_threshold` | `relay` | int | `10` | 否 | hot | relay | `>0` |
| `relay.log_body_max_bytes` | `relay` | int | `0` | 否 | hot | relay/log | `>=0`，`0` 表示不记录 body |
| `routing.channel_cache.enabled` | `routing` | bool | `true` | 否 | hot | relay | bool |
| `routing.channel_cache.preload` | `routing` | bool | `true` | 否 | cache_refresh | relay | bool |
| `routing.channel_cache.ttl_seconds` | `routing` | int | `60` | 否 | hot | relay | `>=0` |
| `routing.channel_cache.version` | `routing` | int | `1` | 否 | cache_refresh | relay | `>=1` |
| `billing.default_ratio` | `billing` | float | `1.0` | 否 | hot | billing | `>0` |
| `log.body_max_bytes` | `log` | int | `0` | 否 | hot | log | `>=0`，`0` 表示不记录 body |

说明：

- `jwt.secret` 可以由 `JWT_SECRET` 注入；生产和多实例部署必须显式固定，不能让各实例随机生成不同值。
- `rate_limit.global_per_min`、`rate_limit.per_token_per_min` 和 `rate_limit.per_ip_per_min` 为 `0` 时表示关闭对应维度；Redis 可用时这些 hot setting 会影响后续请求。
- `relay.retry_count` 默认是 `0`，表示不做自动重试；大于 0 时，非流式 Relay 只对 429、5xx、网络错误、超时和响应读取失败进行有限候选通道重试。
- `relay.error_auto_ban=false` 时仍会记录通道 `error_count`，但候选查询不会因为 `relay.error_ban_threshold` 排除通道。
- `relay.log_body_max_bytes` 和 `log.body_max_bytes` 当前默认是 `0`，表示默认不记录请求/响应 body。

## P0 目标配置

以下 key 是 P0 设计上应补齐的配置，用来让开箱路径和基础安全边界更清晰。

| key | category | type | 建议默认 | effect | owner | 说明 |
|-----|----------|------|----------|--------|-------|------|
| `billing.bootstrap_admin_quota` | `billing` | int | `100000000` | init_only | setup/billing | 初始化超级管理员启动额度，只服务首次验证和管理员自测 |
| `log.request_body_enabled` | `log` | bool | `false` | hot | log | 是否记录请求体；默认关闭 |
| `log.response_body_enabled` | `log` | bool | `false` | hot | log | 是否记录响应体；默认关闭 |
| `ready.production_strict` | `server` | bool | `true` | hot | ready | 生产模式缺少关键配置时 `/ready` 返回不就绪 |

P0 补齐这些配置时，应同时补测试：

- 空库初始化后存在默认值。
- 迁移补缺失 key 不覆盖已有值。
- `billing.bootstrap_admin_quota` 为 0 时，接口或文档必须给出明确额度调整路径。
- 开启 body 日志后仍然截断和脱敏。

## P1/P2 目标配置

### Auth

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `auth.register.enabled` | `false` | P1 | 是否开放公开自助注册 |
| `auth.register.username.enabled` | `true` | P1 | 开启注册后是否允许用户名注册 |
| `auth.register.email.enabled` | `false` | P1 | 是否允许邮箱注册 |
| `auth.register.phone.enabled` | `false` | P1 | 是否允许手机号注册 |
| `auth.register.captcha.required` | `true` | P1 | 注册是否强制验证码 |
| `auth.register.default_quota` | `0` | P1 | 自助注册默认额度 |
| `auth.register.default_group_id` | `default` | P1 | 自助注册默认用户分组；如果实现使用数字 ID，应解析 code 为 `default` 的分组 |

### Billing

访问控制和策略相关 settings 的语义以 `docs/POLICIES.md` 为准；协议、APIType、provider 和流式阶段以 `docs/PROTOCOLS.md` 为准。本节记录当前建议 key、默认值和阶段。

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `billing.precharge_tokens_per_request` | `4096` | P1 | 请求开始时的预留 token 量 |
| `billing.user_group_ratios` | `{}` | P1 | 用户分组倍率 |
| `billing.channel_group_ratios` | `{}` | P1 | 通道分组倍率 |
| `billing.model_group_ratios` | `{}` | P1 | 模型分组倍率；如实现统一使用 `channel_group`，保持术语一致 |
| `billing.user_group_channel_ratios` | `{}` | P1 | 用户分组 x 通道/模型分组额外倍率 |
| `billing.default_user_channel_group_access` | `["default"]` | P1 | 普通用户默认可用通道分组白名单 |
| `billing.user_group_channel_group_access` | `{}` | P1 | 用户分组对通道分组的额外启用或禁用 |
| `billing.usage_missing_strategy` | `minimum` | P1 | usage 缺失时使用最低计费、估算或拒绝 |

### Relay

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `relay.max_request_body_bytes` | `10485760` | P1 | 模型请求体最大字节数 |
| `relay.max_response_body_bytes` | `10485760` | P1 | 非流式下游响应读取上限 |
| `relay.stream_usage_strategy` | `provider_or_estimate` | P1 | 流式 usage 策略 |
| `relay.routerx_max_hops` | `3` | P1 | 多层 RouterX 最大跳数 |
| `relay.retry_on_status` | `[429,500,502,503,504]` | P1 | 可重试状态码白名单 |

### Routing Cache

通道候选预加载和缓存用于降低每次请求实时查询和解析通道配置的成本。单机 SQLite 模式可以使用进程内缓存；外部数据库或集群模式必须通过 Redis 保存版本、失效信号或共享快照。

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `routing.channel_cache.enabled` | `true` | P1 | 是否启用模型到通道候选集缓存 |
| `routing.channel_cache.preload` | `true` | P1 | 启动后或配置变更后是否预加载候选索引 |
| `routing.channel_cache.ttl_seconds` | `60` | P1 | 进程内缓存兜底 TTL，`0` 表示只靠版本失效 |
| `routing.channel_cache.version` | `1` | P1 | 管理端修改通道、分组、价格或访问策略后递增 |

### Observability

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `observability.metrics_enabled` | `false` | P2 | 是否暴露 `/metrics` |
| `observability.audit_enabled` | `true` | P2 | 是否记录管理审计 |
| `observability.request_id_header` | `X-Request-Id` | P2 | 请求 ID header |

### Payment

支付 provider、充值码、退款和人工补账策略以 `docs/PAYMENTS.md` 为准。支付密钥本身优先来自环境变量、KMS 或加密配置，不应以明文写入普通 `settings` 响应。

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `payment.stripe.enabled` | `false` | P2 | 是否启用 Stripe |
| `payment.epay.enabled` | `false` | P2 | 是否启用易支付 |
| `payment.currency` | `usd` | P2 | 默认币种 |
| `payment.order_expire_minutes` | `30` | P2 | 支付订单过期时间 |

## 读取和缓存

推荐服务接口：

```text
GetString(key) (string, error)
GetInt(key) (int64, error)
GetFloat(key) (float64, error)
GetBool(key) (bool, error)
GetJSON(key, target) error
Set(key, value, actor, reason) error
LoadCache() error
Invalidate(key) error
```

读取规则：

1. 优先读 Redis 或进程内缓存。
2. 缓存未命中时读 DB。
3. DB 缺失时，如果注册表有默认值，可以按阶段策略补写；关键配置缺失应让 `/ready` 不就绪。
4. 类型解析失败不能静默使用 0 值；必须返回错误并记录配置问题。
5. 敏感值只允许业务内部读取，管理端响应必须脱敏。

## 修改和审计

配置修改流程：

```text
validate key exists
    -> validate type and business rule
    -> compare old/new summary
    -> write DB
    -> refresh cache
    -> write audit log
    -> return effect hint
```

审计摘要要求：

- 敏感值只记录是否变化，不记录明文。
- JSON 配置记录结构化 diff 摘要，不记录超大完整 JSON。
- 高风险配置需要记录 `actor_user_id`、`request_id`、旧值摘要、新值摘要和可选变更原因。
- 失败的配置修改也应有安全日志，便于发现越权或误操作。

## readiness 要求

生产模式下，以下配置缺失或非法时 `/ready` 应返回不就绪：

- `jwt.secret` 缺失、过短或多实例不一致。
- `ENCRYPTION_KEY` 或 KMS 不可用，且数据库存在 `enc:v1:` 下游密钥。
- `relay.timeout <= 0`。
- `rate_limit.*` 类型非法。
- `billing.default_ratio <= 0`。
- `payment.epay.enabled=true` 但 `PAYMENT_EPAY_KEY` 不可用。
- `payment.stripe.enabled=true` 但 `PAYMENT_STRIPE_SECRET_KEY` 或 `PAYMENT_STRIPE_WEBHOOK_SECRET` 不可用。
- 迁移状态 dirty 或必要 settings 未加载。

开发/演示模式可以降级启动，但必须给出明确警告，不能让用户误以为该状态适合生产。

## 测试要求

settings 相关测试至少覆盖：

- 初始化默认值存在且不覆盖已有值。
- typed accessor 能正确解析 string/int/float/bool/json。
- 类型非法时返回错误，不静默降级。
- 修改配置刷新缓存。
- 敏感配置响应脱敏。
- 关键配置缺失时生产 `/ready` 不就绪。
- `billing.bootstrap_admin_quota` 能支撑首次验证路径，或为 0 时有明确额度调整路径。
