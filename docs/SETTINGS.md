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
| `auth.login.username_password.enabled` | `auth` | bool | `true` | 否 | hot | auth | 必须为 `true` |
| `auth.login.email_password.enabled` | `auth` | bool | `false` | 否 | hot | auth | bool；只作用于已有本地邮箱身份 |
| `auth.login.phone_password.enabled` | `auth` | bool | `false` | 否 | hot | auth | bool；只作用于已有本地手机号身份 |
| `auth.login.email_code.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制邮箱验证码登录入口；依赖 Redis 验证码记录 |
| `auth.login.phone_code.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制手机号验证码登录入口；依赖 Redis 验证码记录 |
| `auth.login.oauth.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制 OAuth 已绑定身份登录和绑定入口 |
| `auth.login.oidc.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制 OIDC 已绑定身份登录和绑定入口 |
| `auth.captcha.ttl_seconds` | `auth` | int | `300` | 否 | hot | auth | `>0`；发送验证码时写入 Redis TTL 的默认值 |
| `auth.captcha.max_attempts` | `auth` | int | `5` | 否 | hot | auth | `>0`；单个注册/登录验证码错误尝试上限 |
| `auth.register.enabled` | `auth` | bool | `false` | 否 | hot | auth | bool |
| `auth.register.username.enabled` | `auth` | bool | `true` | 否 | hot | auth | bool |
| `auth.register.email.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制 `register_method=email` 自助注册入口 |
| `auth.register.phone.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制 `register_method=phone` 自助注册入口 |
| `auth.register.oauth.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制 OAuth 首次登录是否可进入补齐注册 |
| `auth.register.oidc.enabled` | `auth` | bool | `false` | 否 | hot | auth | 控制 OIDC 首次登录是否可进入补齐注册 |
| `auth.register.captcha.required` | `auth` | bool | `true` | 否 | hot | auth | bool；为 true 时公开注册必须消费 Redis 注册验证码 |
| `auth.register.default_quota` | `auth` | int | `0` | 否 | hot | auth | `>=0` |
| `auth.register.default_group_id` | `auth` | string | `default` | 否 | hot | auth | 非空 group 名称或数字 ID |
| `rate_limit.enabled` | `rate_limit` | bool | `true` | 否 | hot | rate limit | bool |
| `rate_limit.global_per_min` | `rate_limit` | int | `1000` | 否 | hot | rate limit | `>=0` |
| `rate_limit.per_token_per_min` | `rate_limit` | int | `60` | 否 | hot | rate limit | `>=0` |
| `rate_limit.per_ip_per_min` | `rate_limit` | int | `30` | 否 | hot | rate limit | `>=0` |
| `rate_limit.per_user_per_min` | `rate_limit` | int | `0` | 否 | hot | rate limit | `>=0` |
| `rate_limit.per_model_per_min` | `rate_limit` | int | `0` | 否 | hot | rate limit | `>=0` |
| `rate_limit.per_channel_per_min` | `rate_limit` | int | `0` | 否 | hot | rate limit | `>=0` |
| `relay.timeout` | `relay` | int | `120` | 否 | hot | relay | `>0` |
| `relay.retry_count` | `relay` | int | `0` | 否 | hot | relay | `>=0` |
| `relay.retry_on_status` | `relay` | json_int_array | `[429,500,502,503,504]` | 否 | hot | relay | 非空，元素为 `400..599` 且不重复 |
| `relay.error_auto_ban` | `relay` | bool | `true` | 否 | hot | relay | bool |
| `relay.error_ban_threshold` | `relay` | int | `10` | 否 | hot | relay | `>0` |
| `relay.error_ban_cooldown_seconds` | `relay` | int | `300` | 否 | hot | relay | `>=0` |
| `relay.error_probe_enabled` | `relay` | bool | `true` | 否 | hot | relay | bool |
| `relay.error_probe_interval_seconds` | `relay` | int | `60` | 否 | hot | relay | `>=0` |
| `relay.error_probe_batch_size` | `relay` | int | `20` | 否 | hot | relay | `>0` |
| `relay.max_request_body_bytes` | `relay` | int | `10485760` | 否 | hot | relay | `>=0`，`0` 表示不限制 |
| `relay.max_multipart_file_bytes` | `relay` | int | `10485760` | 否 | hot | relay | `>=0`，`0` 表示不限制单个文件字段 |
| `relay.max_response_body_bytes` | `relay` | int | `10485760` | 否 | hot | relay | `>=0`，`0` 表示不限制 |
| `relay.routerx_max_hops` | `relay` | int | `3` | 否 | hot | relay | `>0` |
| `relay.log_body_max_bytes` | `relay` | int | `0` | 否 | hot | relay/log | `>=0`，`0` 表示不记录 body |
| `routing.channel_cache.enabled` | `routing` | bool | `true` | 否 | hot | relay | bool |
| `routing.channel_cache.preload` | `routing` | bool | `true` | 否 | cache_refresh | relay | bool |
| `routing.channel_cache.ttl_seconds` | `routing` | int | `60` | 否 | hot | relay | `>=0` |
| `routing.channel_cache.version` | `routing` | int | `1` | 否 | cache_refresh | relay | `>=1` |
| `billing.default_ratio` | `billing` | float | `1.0` | 否 | hot | billing | `>0` |
| `billing.usage_missing_strategy` | `billing` | string | `minimum` | 否 | hot | billing | `minimum/reject` |
| `log.body_max_bytes` | `log` | int | `0` | 否 | hot | log | `>=0`，`0` 表示不记录 body |
| `alert.webhook.enabled` | `alert` | bool | `false` | 否 | hot | alert | bool |
| `alert.webhook.url` | `alert` | string | `` | 否 | hot | alert | 空或绝对 URL |
| `alert.webhook.timeout_seconds` | `alert` | int | `5` | 否 | hot | alert | `>0` |
| `alert.webhook.max_attempts` | `alert` | int | `3` | 否 | hot | alert | `>0` |
| `alert.email.enabled` | `alert` | bool | `false` | 否 | hot | alert | bool |
| `alert.email.url` | `alert` | string | `` | 否 | hot | alert | 空或绝对 URL |
| `alert.email.timeout_seconds` | `alert` | int | `5` | 否 | hot | alert | `>0` |
| `alert.email.max_attempts` | `alert` | int | `3` | 否 | hot | alert | `>0` |
| `alert.im.enabled` | `alert` | bool | `false` | 否 | hot | alert | bool |
| `alert.im.url` | `alert` | string | `` | 否 | hot | alert | 空或绝对 URL |
| `alert.im.timeout_seconds` | `alert` | int | `5` | 否 | hot | alert | `>0` |
| `alert.im.max_attempts` | `alert` | int | `3` | 否 | hot | alert | `>0` |

说明：

- `jwt.secret` 可以由 `JWT_SECRET` 注入；生产和多实例部署必须显式固定，不能让各实例随机生成不同值。
- `auth.login.username_password.enabled=true` 是商业级基础登录硬约束，配置层会拒绝关闭；email/phone 密码登录默认关闭，只有已有本地 email/phone identity 且对应开关开启时才会命中。
- `auth.register.enabled=false` 是商业级自部署安全默认；当前统一注册入口会按 `register_method` 检查 `auth.register.username.enabled`、`auth.register.email.enabled` 或 `auth.register.phone.enabled`，并在 `auth.register.captcha.required=true` 时要求消费 Redis 注册验证码。验证码生成/发送接口仍按 `docs/ACCOUNTS.md` 分阶段补齐。
- `rate_limit.global_per_min`、`rate_limit.per_token_per_min`、`rate_limit.per_ip_per_min`、`rate_limit.per_user_per_min`、`rate_limit.per_model_per_min` 和 `rate_limit.per_channel_per_min` 为 `0` 时表示关闭对应维度；Redis 可用时这些 hot setting 会影响后续请求。
- `relay.retry_count` 默认是 `0`，表示不做自动重试；大于 0 时，非流式 Relay 只对 `relay.retry_on_status` 白名单状态码、网络错误、超时和响应读取失败进行有限候选通道重试。默认白名单为 429/500/502/503/504，生产环境不建议把 401/403 加入白名单。
- `relay.error_auto_ban=false` 时仍会记录通道 `error_count`，但候选查询不会因为 `relay.error_ban_threshold` 排除通道；`relay.error_ban_cooldown_seconds>0` 时，达到阈值的通道在最近一次健康状态更新超过冷却窗口后可重新进入候选做半开探测，后台 worker 也会按 `relay.error_probe_*` 定期复测这些通道；`relay.error_ban_cooldown_seconds=0` 表示关闭自动半开和后台探测恢复，只能人工测试或后续成功调用清零。
- `relay.max_request_body_bytes` 当前已在 `/v1` 模型入口生效，超过限制时按入口协议返回 413 且不调用上游。
- `relay.max_multipart_file_bytes` 当前已在 OpenAI-compatible Images/Audio multipart 文件字段生效，单个文件字段超过限制时返回 413 `request_file_too_large`，且不调用上游、不扣费。
- `relay.max_response_body_bytes` 当前已在非流式上游响应读取路径生效，超过限制时返回 502 `upstream_response_too_large`，不反射下游响应体且不扣费。
- `relay.routerx_max_hops` 当前已在 RouterX-Compatible 上游转发路径生效，达到或超过上限时返回 `routerx_hop_exceeded` 且不调用上游。
- `relay.log_body_max_bytes` 和 `log.body_max_bytes` 当前默认是 `0`，表示默认不记录请求/响应 body；显式开启 `log.request_body_enabled` / `log.response_body_enabled` 且配置正数上限后，非流式 Relay 日志会保存截断和脱敏后的请求/响应片段。
- `billing.usage_missing_strategy` 当前支持 `minimum` 和 `reject`；`minimum` 保持最低计费兼容行为，`reject` 在上游成功但缺少 usage 时返回 `usage_missing` 且不扣费。
- `alert.<target>.enabled=false` 时不会为新告警创建对应外部投递 outbox；当前 target 支持 `webhook`、`email` 和 `im`。开启且 `alert.<target>.url` 为绝对 URL 后，新告警会写入 `alert_delivery_outboxes`，后台 worker 或手动重放会发送脱敏告警 payload。

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
| `auth.login.username_password.enabled` | `true` | P1 | 当前已落地；用户名密码登录基线，配置层不得关闭 |
| `auth.login.email_password.enabled` | `false` | P1 | 当前已落地；已有本地邮箱身份可在开启后使用邮箱 + 密码登录 |
| `auth.login.phone_password.enabled` | `false` | P1 | 当前已落地；已有本地手机号身份可在开启后使用手机号 + 密码登录 |
| `auth.login.email_code.enabled` | `false` | P1 | 当前已落地；邮箱验证码登录依赖 Redis 短期验证码记录 |
| `auth.login.phone_code.enabled` | `false` | P1 | 当前已落地；手机号验证码登录依赖 Redis 短期验证码记录 |
| `auth.login.oauth.enabled` | `false` | P1 | 当前已落地；控制 OAuth 已绑定身份登录和登录用户绑定 OAuth identity |
| `auth.login.oidc.enabled` | `false` | P1 | 当前已落地；控制 OIDC Discovery、nonce/ID Token 校验、已绑定 subject 登录和登录用户绑定 OIDC identity |
| `auth.captcha.ttl_seconds` | `300` | P1 | 当前已注册默认值和校验；验证码发送侧应按该值写入 Redis TTL |
| `auth.captcha.max_attempts` | `5` | P1 | 当前已落地；注册/登录验证码错误次数达到上限后删除 Redis 记录 |
| `auth.register.enabled` | `false` | P1 | 当前已落地；是否开放公开自助注册，默认关闭 |
| `auth.register.username.enabled` | `true` | P1 | 当前已落地；开启注册后是否允许用户名注册 |
| `auth.register.email.enabled` | `false` | P1 | 当前已落地；开启后允许 `register_method=email`，仍要求用户名和密码 |
| `auth.register.phone.enabled` | `false` | P1 | 当前已落地；开启后允许 `register_method=phone`，仍要求用户名和密码 |
| `auth.register.oauth.enabled` | `false` | P2 | 当前已落地；配合 `oauth.{provider}.register_enabled=true` 允许 OAuth 回调返回注册票据 |
| `auth.register.oidc.enabled` | `false` | P2 | 当前已落地；配合 `oidc.{provider}.register_enabled=true` 允许 OIDC 回调返回注册票据 |
| `auth.register.captcha.required` | `true` | P1 | 当前已落地；为 true 时注册必须消费 Redis 注册验证码，为 false 时允许基础无验证码注册 |
| `auth.register.default_quota` | `0` | P1 | 当前已落地；自助注册默认额度，必须为非负整数 |
| `auth.register.default_group_id` | `default` | P1 | 当前已落地；自助注册默认用户分组，支持 group 名称或数字 ID；`default` 不存在时按空分组归一 |

### Billing

访问控制和策略相关 settings 的语义以 `docs/POLICIES.md` 为准；协议、APIType、provider 和流式阶段以 `docs/PROTOCOLS.md` 为准。本节记录当前建议 key、默认值和阶段。

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `billing.precharge_tokens_per_request` | `4096` | P1 | 请求开始时的预留 token 量 |
| `billing.user_group_ratios` | `{}` | P1 | 当前已落地；用户分组倍率，必须是 JSON 对象且值为正数 |
| `billing.channel_group_ratios` | `{}` | P1 | 当前已落地；通道分组倍率，必须是 JSON 对象且值为正数 |
| `billing.model_group_ratios` | `{}` | P1 | 当前已校验；模型分组倍率，必须是 JSON 对象且值为正数；如实现统一使用 `channel_group`，保持术语一致 |
| `billing.user_group_channel_ratios` | `{}` | P1 | 当前已落地；用户分组 x 通道/模型分组组合覆盖倍率，命中时覆盖用户分组倍率和通道分组倍率的乘积 |
| `billing.default_user_channel_group_access` | `["default"]` | P1 | 当前已落地；普通用户默认可用通道分组白名单，必须是 JSON 字符串数组 |
| `billing.user_group_channel_group_access` | `{}` | P1 | 当前已落地；用户分组对通道分组的额外 `allow`/`deny` JSON 对象 |
| `billing.usage_missing_strategy` | `minimum` | P1 | 当前已落地 `minimum` 和 `reject`；usage 缺失时最低计费或拒绝不扣费，估算仍属后续增强 |

### Relay

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `relay.max_request_body_bytes` | `10485760` | P1 | 当前已落地；模型请求体最大字节数，必须为非负整数，`0` 表示不限制 |
| `relay.max_multipart_file_bytes` | `10485760` | P1 | 当前已落地；OpenAI-compatible multipart 单个文件字段最大字节数，必须为非负整数，`0` 表示不限制 |
| `relay.max_response_body_bytes` | `10485760` | P1 | 当前已落地；非流式下游响应读取上限，必须为非负整数，`0` 表示不限制 |
| `relay.stream_usage_strategy` | `provider_or_estimate` | P1 | 流式 usage 策略 |
| `relay.routerx_max_hops` | `3` | P1 | 当前已落地；多层 RouterX 最大跳数，必须为正整数 |
| `relay.retry_on_status` | `[429,500,502,503,504]` | P1 | 当前已落地；可重试状态码白名单，必须是非空 JSON 整数数组，元素为 `400..599` 且不重复 |
| `relay.error_ban_cooldown_seconds` | `300` | P1 | 当前已落地；自动熔断冷却秒数，达到阈值的通道冷却后可作为半开探测候选，`0` 表示关闭自动半开探测 |
| `relay.error_probe_enabled` | `true` | P1 | 当前已落地；是否启用后台熔断通道探测 worker |
| `relay.error_probe_interval_seconds` | `60` | P1 | 当前已落地；后台探测间隔秒数，必须为非负整数，`0` 表示关闭后台探测 |
| `relay.error_probe_batch_size` | `20` | P1 | 当前已落地；每轮最多探测的熔断通道数量，必须为正整数 |

### Routing Cache

通道候选预加载和缓存用于降低每次请求实时查询和解析通道配置的成本。单机 SQLite 模式可以使用进程内缓存；外部数据库或集群模式必须通过 Redis 保存版本、失效信号或共享快照。

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `routing.channel_cache.enabled` | `true` | P1 | 当前已落地；是否启用通道候选缓存；Redis 可用时会同时使用共享候选快照和 pub/sub 失效广播，必须是 boolean |
| `routing.channel_cache.preload` | `true` | P1 | 当前已落地；启动后和通道版本变更后是否预加载候选索引并暖 Redis 共享快照；通道变更还会发布失效广播，必须是 boolean |
| `routing.channel_cache.ttl_seconds` | `60` | P1 | 当前已落地；进程内和 Redis 共享候选快照 TTL，`0` 表示只靠版本失效，必须是非负整数 |
| `routing.channel_cache.version` | `1` | P1 | 当前已落地；管理端修改通道后递增，跨实例会用该版本忽略旧候选快照并通过 Redis pub/sub 主动清理本地缓存，必须是正整数 |

### Observability

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `observability.metrics_enabled` | `false` | P2 | 是否暴露 `/metrics` |
| `observability.audit_enabled` | `true` | P2 | 是否记录管理审计 |
| `observability.request_id_header` | `X-Request-Id` | P2 | 请求 ID header；必须是合法 HTTP header 名，修改后后续请求会从该 header 读取并用同名响应头返回 request id |
| `observability.structured_logs_enabled` | `false` | P2 | 是否把 HTTP 访问日志和 Recovery panic 日志输出为 JSON line；默认保留原文本日志 |

### Alerting

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `alert.webhook.enabled` | `false` | P2 | 当前已落地；是否启用告警 Webhook 外部投递 |
| `alert.webhook.url` | `` | P2 | 当前已校验；Webhook 接收地址，必须为空或绝对 URL |
| `alert.webhook.timeout_seconds` | `5` | P2 | 当前已落地；单次 Webhook 请求超时时间，必须为正整数 |
| `alert.webhook.max_attempts` | `3` | P2 | 当前已落地；失败后最大投递尝试次数，必须为正整数 |
| `alert.email.enabled` | `false` | P2 | 当前已落地；是否启用邮件告警外部投递 |
| `alert.email.url` | `` | P2 | 当前已校验；邮件网关 HTTP 接收地址，必须为空或绝对 URL |
| `alert.email.timeout_seconds` | `5` | P2 | 当前已落地；单次邮件投递请求超时时间，必须为正整数 |
| `alert.email.max_attempts` | `3` | P2 | 当前已落地；邮件投递失败后最大尝试次数，必须为正整数 |
| `alert.im.enabled` | `false` | P2 | 当前已落地；是否启用 IM 告警外部投递 |
| `alert.im.url` | `` | P2 | 当前已校验；IM 网关 HTTP 接收地址，必须为空或绝对 URL |
| `alert.im.timeout_seconds` | `5` | P2 | 当前已落地；单次 IM 投递请求超时时间，必须为正整数 |
| `alert.im.max_attempts` | `3` | P2 | 当前已落地；IM 投递失败后最大尝试次数，必须为正整数 |

### Payment

支付 provider、充值码、退款和人工补账策略以 `docs/PAYMENTS.md` 为准。支付密钥本身优先来自环境变量、KMS 或加密配置，不应以明文写入普通 `settings` 响应。

| key | 默认 | stage | 说明 |
|-----|------|-------|------|
| `payment.stripe.enabled` | `false` | P2 | 是否启用 Stripe |
| `payment.epay.enabled` | `false` | P2 | 是否启用易支付 |
| `payment.epay.gateway` | `` | P2 | 易支付收银台网关地址 |
| `payment.epay.pid` | `` | P2 | 易支付商户 ID |
| `payment.epay.notify_url` | `` | P2 | 易支付异步通知地址 |
| `payment.epay.return_url` | `` | P2 | 易支付同步返回地址 |
| `payment.epay.refund_url` | `` | P2 | 易支付退款请求地址 |
| `payment.currency` | `usd` | P2 | 默认币种 |
| `payment.order_expire_minutes` | `30` | P2 | 支付订单过期时间 |
| `payment.refund.auto_deduct` | `false` | P2 | 退款成功后是否自动扣回原订单额度 |
| `payment.refund.allow_negative_balance` | `false` | P2 | 自动扣回是否允许用户余额变成负数 |
| `payment.dispute.auto_disable_tokens` | `false` | P2 | Stripe 争议/拒付事件成功记录后是否自动禁用该用户已启用 API Key |
| `payment.manual_adjust.require_reason` | `true` | P2 | 支付人工补账/扣回是否必须填写原因 |
| `payment.manual_adjust.large_amount_threshold` | `0` | P2 | 大额人工调整阈值，`0` 表示当前不触发额外二次确认 |

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

- 当前基础实现中，`PUT /v0/admin/setting` 成功后会按 key 写入统一管理审计日志，动作是 `setting.create` 或 `setting.update`；校验失败或高风险配置被拒绝会按 key 写入 `setting.denied`。
- 敏感值只记录是否变化，不记录明文。
- JSON 配置记录结构化 diff 摘要，不记录超大完整 JSON。
- 高风险配置需要记录 `actor_user_id`、`request_id`、旧值摘要、新值摘要和可选变更原因。
- 失败的配置修改会写 `result=denied` 和 `error_code=setting_validation_failed`，便于发现越权或误操作。

## readiness 要求

生产模式下，以下配置缺失或非法时 `/ready` 应返回不就绪：

- `jwt.secret` 缺失、过短或多实例不一致。
- `auth.login.*.enabled`、`auth.register.*.enabled` 或 `auth.register.captcha.required` 不是 boolean。
- `auth.login.username_password.enabled=false`。
- `auth.register.default_quota < 0`。
- `auth.register.default_group_id` 为空。
- `ENCRYPTION_KEY` 或 KMS 不可用，且数据库存在 `enc:v1:` 下游密钥或外部登录 `client_secret`。
- `SQL_DSN` 指向 PostgreSQL/MySQL 等外部数据库但 Redis 不可用。
- `relay.timeout <= 0`。
- `relay.max_request_body_bytes < 0`。
- `relay.max_multipart_file_bytes < 0`。
- `relay.max_response_body_bytes < 0`。
- `relay.routerx_max_hops <= 0`。
- `relay.retry_on_status` 不是非空 JSON 整数数组，或包含 `400..599` 之外/重复状态码。
- `relay.error_ban_cooldown_seconds < 0`。
- `relay.error_probe_enabled` 不是 boolean。
- `relay.error_probe_interval_seconds < 0`。
- `relay.error_probe_batch_size <= 0`。
- `rate_limit.*` 类型非法。
- `billing.default_ratio <= 0`。
- `billing.usage_missing_strategy` 不是 `minimum` 或 `reject`。
- `billing.user_group_ratios`、`billing.channel_group_ratios`、`billing.model_group_ratios` 或 `billing.user_group_channel_ratios` 不是 JSON 对象，或包含空 key、`<= 0` 的倍率值。
- `payment.epay.enabled=true` 但 `PAYMENT_EPAY_KEY` 不可用。
- `payment.stripe.enabled=true` 但 `PAYMENT_STRIPE_SECRET_KEY` 或 `PAYMENT_STRIPE_WEBHOOK_SECRET` 不可用。
- 迁移状态 dirty 或必要 settings 未加载。
- `observability.structured_logs_enabled` 不是布尔值。
- `alert.<target>.enabled` 不是布尔值，`alert.<target>.url` 不是绝对 URL，或 `alert.<target>.timeout_seconds` / `alert.<target>.max_attempts` 不是正整数；当前 target 为 `webhook`、`email`、`im`。

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
