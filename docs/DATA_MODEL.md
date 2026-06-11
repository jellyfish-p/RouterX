# RouterX 数据模型设计

## 总体原则

RouterX 的数据模型围绕“用户、API Key、下游通道、调用日志、配置”展开。

设计原则：

- 用户资料和登录身份分离，支持多种账号体系。
- API Key 是调用 `/v1/*` 的唯一凭据，不直接使用用户登录态调用模型。
- 调用日志作为审计和计费事实表，不依赖下游厂商日志。
- settings 存储运行时可调整配置，环境变量承载启动必须项和跨实例必须一致的密钥。
- 使用版本化 SQL 迁移管理 schema，不在生产运行 AutoMigrate。

## ER 关系

```mermaid
erDiagram
    groups ||--o{ users : contains
    users ||--o{ user_identities : owns
    users ||--o{ tokens : owns
    users ||--o{ logs : generates
    users ||--o{ redem_codes : redeems
    users ||--o{ payment_orders : pays
    tokens ||--o{ logs : authorizes
    channels ||--o{ logs : serves
    payment_products ||--o{ payment_orders : sells
    payment_orders ||--o{ payment_events : receives

    users {
        uint id
        string username
        string display_name
        string email
        string phone
        int role
        int64 quota
        int status
        uint group_id
    }

    user_identities {
        uint id
        uint user_id
        string method
        string provider
        string identifier
        string password_hash
        time verified_at
        time last_used_at
    }

    tokens {
        uint id
        uint user_id
        string name
        string key
        int status
        time expired_at
        int64 remain_quota
        bool unlimited
    }

    channels {
        uint id
        int type
        string name
        string models
        string base_url
        string api_key
        int priority
        int weight
        int status
        int response_ms
        int64 balance
        int error_count
    }

    logs {
        uint id
        uint user_id
        uint token_id
        uint channel_id
        string model
        int prompt_tokens
        int completion_tokens
        int total_tokens
        int64 quota_used
        int status
    }
```

## 表设计

### `users`

核心用户资料表，不保存密码。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `username` | nullable string | 主展示用户名，可为空 |
| `display_name` | string | 显示名，默认空字符串 |
| `email` | nullable string | 主邮箱，可为空 |
| `phone` | nullable string | 主手机号，可为空 |
| `role` | int | `0` 用户，`1` 管理员，`2` 超级管理员 |
| `quota` | int64 | 用户剩余额度，单位为 `1 / QuotaPerUnit` |
| `status` | int | `0` 禁用，`1` 启用 |
| `group_id` | nullable uint | 用户分组 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `deleted_at` | nullable time | GORM 软删除 |

索引：

- `idx_users_username`
- `idx_users_email`
- `idx_users_phone`
- `idx_users_deleted_at`

说明：

- `username`、`email`、`phone` 在 `users` 中是资料字段，不作为全局登录唯一约束。
- 登录唯一性由 `user_identities(method, provider, identifier)` 保证。

### `user_identities`

用户登录身份表。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `user_id` | uint | 所属用户 |
| `method` | string | `username`、`email`、`phone`、`oauth`、`oidc` |
| `provider` | string | `local` 或第三方提供方，例如 `github`、`google`、企业 IdP 名称 |
| `identifier` | string | 登录标识，例如用户名、邮箱、手机号、OAuth subject |
| `password_hash` | string | 本地登录密码哈希，第三方身份为空 |
| `verified_at` | nullable time | 邮箱、手机号或第三方身份验证时间 |
| `last_used_at` | nullable time | 最近使用时间 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `deleted_at` | nullable time | 软删除 |

索引：

- `idx_user_identities_identity`，唯一索引 `(method, provider, identifier)`。
- `idx_user_identities_user_id`。
- `idx_user_identities_user_method`。
- `idx_user_identities_deleted_at`。

使用场景：

- 用户名密码登录：`method=username`，`provider=local`。
- 邮箱密码登录：`method=email`，`provider=local`。
- 手机号验证码或密码登录：`method=phone`，`provider=local`。
- GitHub OAuth：`method=oauth`，`provider=github`，`identifier=<github user id>`。
- 企业 OIDC：`method=oidc`，`provider=<issuer alias>`，`identifier=<sub>`。

### `groups`

用户分组表，用于计费倍率和权限策略扩展。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `name` | string | 分组名 |
| `ratio` | float64 | 计费倍率，默认 `1.0` |
| `created_at` | time | 创建时间 |

### `tokens`

API Key 表，用于 `/v1/*` 鉴权。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `user_id` | uint | 所属用户 |
| `name` | string | Token 备注名 |
| `key` | string | `sk-` 前缀 API Key |
| `status` | int | `0` 禁用，`1` 启用 |
| `expired_at` | nullable time | 过期时间，空表示不过期 |
| `remain_quota` | int64 | Token 剩余额度，`-1` 表示无限制 |
| `unlimited` | bool | 是否无限制 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `deleted_at` | nullable time | 软删除 |

安全建议：

- API Key 明文只在创建时返回一次。
- 数据库中长期保存应改为 `key_hash`，当前模型字段为 `key`，后续可通过迁移升级。
- Redis 缓存使用 `SHA256(key)` 作为缓存键，避免明文出现在 Redis key。

### `channels`

下游模型通道表。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `type` | int | 厂商类型 |
| `name` | string | 通道名称 |
| `models` | string | 支持模型列表，逗号分隔，`*` 表示所有 |
| `base_url` | string | 下游 Base URL |
| `api_key` | string | 下游 API Key，JSON 输出隐藏 |
| `priority` | int | 优先级，值越大越优先 |
| `weight` | int | 同优先级内权重 |
| `status` | int | `0` 禁用，`1` 启用，`2` 手动维护 |
| `response_ms` | int | 最近平均响应时延 |
| `balance` | int64 | 下游余额或人工记录余额 |
| `error_count` | int | 连续错误次数 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `deleted_at` | nullable time | 软删除 |

厂商类型：

| 值 | 类型 |
|----|------|
| `1` | OpenAI |
| `2` | Azure OpenAI |
| `3` | Anthropic / Claude |
| `4` | Gemini |
| `5` | Qwen |
| `6` | DeepSeek |
| `7` | xAI / Grok |
| `100` | OpenAI-Compatible 通用通道 |
| `101` | RouterX-Compatible 上游 |

### `logs`

模型调用日志表，是审计、统计和计费追踪的事实表。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `user_id` | uint | 用户 |
| `token_id` | nullable uint | API Key |
| `channel_id` | nullable uint | 下游通道 |
| `model` | string | 请求模型 |
| `prompt_tokens` | int | 输入 token 数 |
| `completion_tokens` | int | 输出 token 数 |
| `total_tokens` | int | 总 token 数 |
| `quota_used` | int64 | 本次消耗额度 |
| `status` | int | `0` 未知，`1` 成功，`2` 失败 |
| `content` | text | 请求体快照，需截断和脱敏 |
| `response` | text | 响应体快照，需截断和脱敏 |
| `error_msg` | text | 错误信息 |
| `ip` | string | 调用方 IP |
| `created_at` | time | 创建时间 |

索引：

- `idx_logs_user_id`
- `idx_logs_token_id`
- `idx_logs_channel_id`
- `idx_logs_created_at`

数据生命周期：

- 高频生产环境建议按月分区或归档。
- `content` 和 `response` 默认截断，支持配置关闭。
- 管理端清理日志应按时间范围执行，避免无条件全表删除。

### `redem_codes`

充值码表。当前代码命名为 `RedemCode` 和 `redem_codes`，设计文档沿用现有命名，避免引入破坏性重命名。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `code` | string | 充值码，唯一 |
| `quota` | int64 | 充值额度 |
| `status` | int | `0` 未使用，`1` 已使用 |
| `used_by` | nullable uint | 使用者 |
| `created_at` | time | 创建时间 |
| `used_at` | nullable time | 使用时间 |

### `payment_products`

充值商品表。商品决定支付金额、货币和入账额度，客户端不能直接指定入账额度。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `product_id` | string | 商品 ID，唯一，例如 `quota_100` |
| `name` | string | 商品名称 |
| `amount` | decimal/string | 支付金额，按货币最小单位或定点小数字符串存储 |
| `currency` | string | 货币，如 `usd`、`cny` |
| `quota` | int64 | 支付成功后增加的基础额度单位 |
| `bonus_quota` | int64 | 赠送额度，基础额度单位 |
| `enabled` | bool | 是否启用 |
| `provider_config_json` | json/text | 可选 provider 限制或价格 ID，如 Stripe price id |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

### `payment_orders`

支付订单表。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `order_no` | string | RouterX 本地订单号，唯一 |
| `user_id` | uint | 下单用户 |
| `product_id` | string | 充值商品 ID |
| `provider` | string | `stripe` 或 `epay` |
| `amount` | decimal/string | 本地订单金额 |
| `currency` | string | 货币 |
| `quota` | int64 | 本订单最终入账基础额度单位，包含赠送额度 |
| `status` | string/int | `pending`、`paid`、`failed`、`closed`、`refunded` |
| `provider_order_id` | nullable string | provider 会话或订单 ID，如 Stripe Checkout Session ID |
| `provider_payment_id` | nullable string | provider 支付流水号 |
| `checkout_url` | nullable text | 支付跳转地址，过期后不可继续使用 |
| `paid_at` | nullable time | 支付成功时间 |
| `expired_at` | nullable time | 订单过期时间 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

索引：

- `idx_payment_orders_order_no`，唯一索引。
- `idx_payment_orders_user_id_created_at`，用户订单列表。
- `idx_payment_orders_provider_order_id`，provider 回调查找。

### `payment_events`

支付回调事件表，用于幂等和审计。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `provider` | string | `stripe` 或 `epay` |
| `provider_event_id` | string | provider 事件 ID；易支付无事件 ID 时可使用交易号或 `provider:order_no:trade_no` 派生 |
| `order_no` | string | RouterX 本地订单号 |
| `event_type` | string | 事件类型，如 `checkout.session.completed`、`notify` |
| `payload` | text/json | 回调原始内容，需脱敏 |
| `signature_valid` | bool | 签名校验是否通过 |
| `processed` | bool | 是否已处理入账逻辑 |
| `processed_at` | nullable time | 处理完成时间 |
| `created_at` | time | 创建时间 |

索引：

- `idx_payment_events_provider_event_id`，唯一索引 `(provider, provider_event_id)`。
- `idx_payment_events_order_no`，按订单查询事件。

### `settings`

系统配置表。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `key` | string | 配置键，唯一 |
| `value` | text | 配置值，标量或 JSON 字符串 |
| `category` | string | 配置分类 |
| `description` | string | 描述 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

推荐分类：

- `server`
- `jwt`
- `auth`
- `oauth`
- `oidc`
- `rate_limit`
- `relay`
- `cors`
- `billing`
- `payment`
- `log`
- `security`

## 额度单位

当前常量：

```text
QuotaPerUnit = 100000000
QuotaUnlimited = -1
```

含义：

- 数据库中 `quota`、`remain_quota`、`quota_used` 使用整数存储，避免浮点误差。
- `100000000` 个基础单位等于 1 个展示额度单位。
- `-1` 表示无限制，只能用于 Token 额度等明确允许无限制的字段。

## 迁移策略

运行时迁移机制：

- SQL 文件位于 `internal/migrate/<dialect>`。
- 通过 `//go:embed postgres/*.sql mysql/*.sql sqlite/*.sql` 打包进二进制。
- 启动时 `internal.InitDB()` 调用 `migrate.Run(SQL_DSN)`。
- `migrate.ErrNoChange` 视为成功。

支持方言：

| 方言 | DSN 示例 |
|------|----------|
| PostgreSQL | `postgres://user:pass@host:5432/db?sslmode=disable` |
| MySQL | `mysql://user:pass@tcp(host:3306)/db?charset=utf8mb4&parseTime=True&loc=Local` |
| SQLite | `sqlite://data/routerx.db` |
| SQLite file | `file:data/routerx.db` |

已有迁移：

| 版本 | 内容 |
|------|------|
| `001_init` | 创建初始表，历史上 `users` 包含 `username` 和 `password_hash` |
| `002_user_identities` | 拆出 `user_identities`，迁移本地用户名密码身份，`users` 增加 `phone` 并移除 `password_hash` |

重要说明：

- 新库会依次执行 `001` 和 `002`，最终 schema 与当前 GORM 模型一致。
- 已执行过 `001` 的旧库会通过 `002` 保留原用户名密码为本地身份。
- `002` 的 down 迁移会丢失非本地用户名身份，是向旧结构回滚时不可避免的数据降级。

## 索引策略

| 表 | 索引 | 用途 |
|----|------|------|
| `users` | `username`、`email`、`phone` | 管理端搜索和主资料查询 |
| `user_identities` | `(method, provider, identifier)` unique | 登录身份唯一性和登录查询 |
| `user_identities` | `(user_id, method)` | 用户身份列表和绑定检查 |
| `tokens` | `key` unique | API Key 鉴权 |
| `channels` | `deleted_at` | 软删除过滤 |
| `logs` | `user_id`、`token_id`、`channel_id`、`created_at` | 日志查询和统计 |
| `settings` | `key` unique | 配置读取 |

后续建议：

- 将 `tokens.key` 升级为 `key_hash` 唯一索引。
- 为 `channels(type, status)`、`channels(priority, weight)` 增加组合索引。
- 为 `logs(user_id, created_at)` 增加组合索引，提高用户日志分页性能。
- 日志量大时使用 PostgreSQL 分区表或按月归档表。

## 数据保留策略

| 数据 | 默认策略 |
|------|----------|
| 用户 | 软删除，保留审计关系 |
| API Key | 软删除或禁用，删除后缓存立即失效 |
| 通道 | 软删除，历史日志保留 channel_id |
| 调用日志 | 默认保留 90 天，可按设置调整 |
| 管理审计日志 | 建议至少保留 180 天 |
| OAuth state | Redis 短 TTL，消费后删除 |
| 验证码 | Redis 短 TTL，失败次数限流 |

## 数据安全

必须加密或哈希的数据：

- 用户密码：bcrypt 哈希。
- API Key：目标设计应只存哈希。
- 下游通道 API Key：应使用 `ENCRYPTION_KEY` 或 KMS 派生的服务端密钥加密后存储。
- OAuth client_secret 和 OIDC client_secret：应使用 `ENCRYPTION_KEY` 或 KMS 加密存储。

必须脱敏的数据：

- 响应日志中的 Authorization、API Key、Cookie。
- 请求体中的用户隐私字段。
- 管理端列表中的密钥字段。
