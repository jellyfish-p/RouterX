# RouterX 数据模型设计

## 总体原则

RouterX 的数据模型围绕“用户、API Key、下游通道、调用日志、配置”展开。

设计原则：

- 用户资料和登录身份分离，支持多种账号体系。
- API Key 是调用 `/v1/*` 的唯一凭据，不直接使用用户登录态调用模型。
- 调用日志作为审计和计费事实表，不依赖下游厂商日志。
- settings 存储运行时可调整配置，环境变量承载启动必须项和跨实例必须一致的密钥。
- 使用版本化 SQL 迁移管理 schema，不在生产运行 AutoMigrate。
- 简单配置和进阶配置使用同一套核心实体；例如通道既支持 `base_url + api_key` 的开箱路径，也支持多上游、多 key、模型重写和通道分组的进阶路径。
- 策略、访问控制、scope、分组和快照语义以 `docs/POLICIES.md` 为准；入口协议、APIType、上游厂商和能力等级以 `docs/PROTOCOLS.md` 为准；调用事实快照封套、脱敏和测试语义以 `docs/SNAPSHOTS.md` 为准。本文只记录数据模型字段和关系。

## ER 关系

```mermaid
erDiagram
    groups ||--o{ users : contains
    users ||--o{ user_identities : owns
    users ||--o{ tokens : owns
    users ||--o{ logs : generates
    users ||--o{ redem_codes : redeems
    users ||--o{ payment_orders : pays
    users ||--o{ quota_transactions : changes
    users ||--o{ admin_audit_logs : audits
    tokens ||--o{ logs : authorizes
    channels ||--o{ logs : serves
    payment_products ||--o{ payment_orders : sells
    payment_orders ||--o{ payment_events : receives
    channels ||--o{ channel_model_prices : overrides

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
        json scope_json
        int64 quota_limit
        int64 quota_used
    }

    channels {
        uint id
        int idx
        int type
        string name
        string models
        string base_url
        json base_urls
        string api_key
        json api_keys
        json upstreams
        json model_rewrites
        string channel_group
        json upstream_options
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
        string usage_source
        text usage_snapshot
        text error_snapshot
        int64 quota_used
        int status
    }

    admin_audit_logs {
        uint id
        uint actor_user_id
        int actor_role
        string action
        string resource_type
        string resource_id
        string result
    }

    model_prices {
        uint id
        string model
        string price_mode
        string price_expression
        json variables_json
        int64 unit_tokens
        int64 rule_version
        bool enabled
    }

    channel_model_prices {
        uint id
        uint channel_id
        string model
        bool enabled
        bool user_enabled
        string price_mode
        string override_mode
        string price_expression
        json variables_json
        int64 unit_tokens
        int64 rule_version
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
| `group_id` | nullable uint | 用户分组；目标默认归入 code 为 `default` 的分组，空值在策略层归一为 `default` |
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

用户分组表，用于用户归类、策略扩展和运营展示。当前管理端已提供 `GET/POST/PUT/DELETE /v0/admin/groups`；删除会保护 `default` 分组和仍被 `users.group_id` 引用的分组。

默认分组要求：

- 初始化或迁移应保证存在 code/name 为 `default` 的默认用户分组，或在策略层把空 `group_id` 归一为 `default`。
- 新用户默认使用 `default`。
- 默认分组展示倍率为 `1`，不应自动获得管理员权限。
- 成功调用后的实际扣费倍率以 `settings` 中的 `billing.user_group_ratios`、`billing.channel_group_ratios` 和 `billing.user_group_channel_ratios` 为准，`groups.ratio` 不直接参与热路径扣费。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `name` | string | 分组名 |
| `ratio` | float64 | 分组元数据/兼容展示倍率，默认 `1.0` |
| `created_at` | time | 创建时间 |

### `tokens`

API Key 表，用于 `/v1/*` 鉴权。
API Key 生命周期、轮换、泄露处理、作用域、缓存一致性和高级管理字段以 `docs/API_KEYS.md` 为准；本文记录数据模型层的当前字段和目标扩展。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `user_id` | uint | 所属用户 |
| `name` | string | Token 备注名 |
| `key` | string | API Key 的 SHA256 哈希；兼容早期明文存量时会在验证成功后迁移为哈希 |
| `status` | int | `0` 禁用，`1` 启用 |
| `expired_at` | nullable time | 过期时间，空表示不过期 |
| `remain_quota` | int64 | 当前字段；目标语义为 Key 剩余预算上限，`-1` 表示无限制 |
| `unlimited` | bool | 是否无限制 |
| `rotated_from_id` | nullable uint | 轮换来源 Token ID |
| `revoked_reason` | string | 禁用原因，例如 `rotated`、`reported_leak`、`admin_batch_disable` |
| `scope_json` | json | API Key 收窄策略；当前支持 `allow_models` 模型 allow-list、`api_types` APIType allow-list、`channel_groups` 通道分组 allow-list、`entry_protocols` 入口协议 allow-list、`ip_cidrs` IP/CIDR allow-list、`methods` 方法路径 allow-list、`daily_quota` 日预算、`monthly_quota` 月预算、`max_concurrency` 并发上限、`rpm` 每分钟请求上限和 `tpm` 每分钟模型 token 上限 |
| `metadata_json` | json | 环境、应用、团队、标签、外部系统关联 ID、服务账号主体和备注等非安全元数据；用于管理查询和脱敏导出，不保存 API Key 明文或密钥类配置 |
| `last_used_at` | nullable time | 最近成功或失败调用时间 |
| `last_used_ip_hash` | string | 最近来源 IP 的 SHA-256 摘要 |
| `last_user_agent_hash` | string | 最近 User-Agent 的 SHA-256 摘要 |
| `last_model` | string | 最近请求模型名 |
| `last_error_code` | string | 最近失败的协议化错误 code，最近调用成功时为空 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `deleted_at` | nullable time | 软删除 |

安全要求：

- API Key 明文只在创建时返回一次。
- 数据库长期保存 SHA256 哈希，不保存 API Key 明文。
- Redis 缓存使用 `SHA256(key)` 作为缓存键，避免明文出现在 Redis key。
- 创建、编辑、禁用、删除、轮换、泄露上报、scope 更新、批量禁用、批量过期和用户端额度/无限标记编辑拒绝会写入 `admin_audit_logs`，审计摘要只包含 `tokens` 的公开字段，不保存完整 Key 明文或哈希。

额度语义：

- 有限额度 API Key 的额度表示最大消耗预算，不来自创建时的用户余额划拨。
- 有限额度 API Key 调用前必须同时检查 `users.quota` 和 Key 剩余预算；调用成功后同时扣用户余额并消耗 Key 预算。
- `unlimited=true` 或 `remain_quota=-1` 表示 Token 自身不限额，调用成功后扣 `users.quota`。
- 创建有限 API Key 不写模型消费日志，也不改变用户余额；模型消费以成功调用的 `logs.quota_used` 为准。

目标增强字段：

| 字段 | 说明 |
|------|------|
| `prefix` | 可展示的短摘要，用于用户识别和工单排障，不用于鉴权。 |
| `hash_version` | 哈希算法或迁移版本。 |
| `quota_limit` | Key 最大消耗额度，`null` 或 `-1` 表示不限 Key 自身额度。 |
| `quota_used` | Key 累计已消耗额度，用于计算剩余预算并支持历史统计。 |
| `created_by_user_id` | 创建操作者。 |
| `updated_by_user_id` | 最近管理操作者。 |

### `channels`

下游模型通道表。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `idx` | int | 前端或管理列表排序序号，值越小越靠前 |
| `type` | int | 厂商类型 |
| `name` | string | 通道名称 |
| `models` | string | 支持模型列表，逗号分隔，`*` 表示所有 |
| `base_url` | string | 下游 Base URL |
| `base_urls` | json | 多 Base URL 数组，适合同一 provider 多节点 |
| `api_key` | string | 单下游 API Key，加密存储，JSON 输出隐藏 |
| `api_keys` | json | 多下游 API Key 数组，加密存储，JSON 输出隐藏 |
| `key_selection_mode` | string | 多 key 选择策略，如 `round_robin`、`random` |
| `key_cursor` | int | 多 key 轮询游标 |
| `upstreams` | json | 多上游对象数组，可同时保存 base_url 与加密 api_key |
| `model_rewrites` | json | 客户端模型名到上游真实模型名的映射 |
| `channel_group` | string | 通道分组，用于路由、计费倍率和访问控制；空值在策略层归一为 `default`，新通道默认写入 `default` |
| `upstream_options` | json | provider 扩展配置，如 Azure api-version、Anthropic version |
| `priority` | int | 优先级，值越大越优先 |
| `weight` | int | 同优先级内权重 |
| `status` | int | `0` 禁用，`1` 启用，`2` 手动维护 |
| `response_ms` | int | 最近平均响应时延 |
| `balance` | int64 | 下游余额或人工记录余额 |
| `error_count` | int | 连续错误次数 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `deleted_at` | nullable time | 软删除 |

通道配置层次：

- 小白路径可以只填写 `type`、`name`、`models`、`base_url`、`api_key`、`priority` 和 `weight`。
- 进阶路径可以使用 `base_urls`、`api_keys`、`upstreams`、`model_rewrites`、`channel_group` 和 `upstream_options`。
- 单字段和多字段同时存在时，Service 层必须按固定优先级解析，避免同一通道产生不可解释的上游选择。
- 所有下游密钥字段都不得在 JSON 响应、日志或审计摘要中明文出现。

通道解析优先级：

| 优先级 | 字段 | 行为 |
|--------|------|------|
| 1 | `upstreams` | 每个元素保存一组 `base_url` 和 `api_key`，二者绑定选择，适合多节点或多账号精确配对。 |
| 2 | `api_key`、`api_keys`、`base_url`、`base_urls` | 多 key 和多地址分开配置；key 按 `key_selection_mode` 选择，base URL 当前随机选择。 |
| 3 | provider 默认 Base URL | 当适配器存在明确默认地址且通道仍有可用 key 时使用。 |

当前代码约束：

- `upstreams` 非空时优先使用，不与外层 `api_keys` 或 `base_urls` 交叉组合。
- `key_selection_mode` 当前支持 `round_robin` 和 `random`；空值或未知值归一为 `round_robin`。
- `key_cursor` 用于 `round_robin` 游标推进，必须在并发场景下避免长期偏斜或重复写冲突。
- `base_urls` 当前随机选择；未来如增加顺序、权重、区域或健康优先策略，需要新增明确字段，不能复用含糊字符串。
- `weight <= 0` 在通道选择中按 `1` 处理，避免配置错误导致通道永远不可达。
- `model_rewrites` 只改变调用上游的真实模型名，日志中应同时保留客户端请求模型和重写后的上游模型，便于账单和排障。

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

日志、审计、指标、保留和脱敏契约以 `docs/OBSERVABILITY.md` 为准；调用事实快照的结构语义以 `docs/SNAPSHOTS.md` 为准。本文只记录数据模型层字段和索引。

日志数据库边界：

- 默认情况下 `logs` 位于主业务数据库。
- 配置 `LOG_SQL_DSN` 后，启动时会为独立日志数据库迁移 `logs` schema，高流量调用日志和诊断快照会通过主库 outbox 补写到独立日志数据库，便于定期备份、归档和清理。
- 用户余额扣减、Key 预算消耗和结算最小事实必须在主业务数据库同事务内保留，或先写主库 outbox，再异步写入日志数据库；当前实现会在主库保留完整调用事实，并在配置独立日志库时同事务写入 `log_replication_outboxes`。
- 独立日志数据库不可成为扣费成功的唯一证据；运行期日志库写入失败时必须保留主库事实并产生可排查告警。

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
| `usage_source` | string | usage 来源；当前已落地 `upstream` 和 `minimum` |
| `usage_snapshot` | text/json string | 脱敏 usage 快照；当前包含 usage 来源、token 数、安全 raw_usage_summary 和最低用量原因 |
| `quota_used` | int64 | 本次消耗额度 |
| `status` | int | `0` 未知，`1` 成功，`2` 失败 |
| `request_id` | nullable string | HTTP 请求追踪 ID |
| `error_code` | string | 失败时的稳定协议化错误 code，成功调用为空 |
| `error_source` | string | 失败来源，例如 `upstream`、`quota`、`route` |
| `upstream_status` | int | 上游 HTTP 状态；非上游错误为 `0` |
| `error_snapshot` | text/json string | 脱敏错误快照；失败日志包含稳定 code、失败来源、上游状态、可重试判断、扣费标记和安全摘要 |
| `request_snapshot` | text/json string | 脱敏请求快照；当前包含 request_id、入口协议、API 类型、请求模型、stream 标记和安全路由摘要 |
| `policy_snapshot` | text/json string | 脱敏策略快照；当前包含成功 allow、额度预检、基础 scope allow、API Key scope 拒绝、基础余额预检拒绝、用户分组 x 通道分组访问控制拒绝、无可用候选 `no_available_channel` 拒绝、熔断拒绝 `breaker_snapshot`，以及 Redis 全局/IP/Token/User/Model/Channel 限流拒绝摘要和 `rate_limit_snapshot` |
| `route_snapshot` | text/json string | 脱敏路由快照；当前包含请求模型、候选数量、候选过滤原因、选中通道、provider、分组、优先级、权重、模型重写摘要和非流式重试摘要 |
| `billing_snapshot` | text/json string | 脱敏计费快照；当前包含结算状态、usage_source、价格表达式或 P0 回退表达式摘要、规则 ID/版本、倍率摘要、Key 预算前后、用户余额前后和最终扣费 |
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
- `idx_logs_request_id`
- `idx_logs_error_code`
- `idx_logs_usage_source`
- `idx_logs_error_source`
- `idx_logs_upstream_status`

目标账单快照字段：

当前 `logs` 已保存基础 usage、`usage_source`、`quota_used`、`billing_snapshot`、状态和结构化错误事实，其中基础 `billing_snapshot` 已包含价格表达式或 P0 回退表达式摘要、规则 ID/版本、倍率快照、Key 预算前后、用户余额前后和最终扣费。商业级计费增强可以继续补充以下字段，或拆分出独立账单事实表，但必须保证历史账单可还原：

| 字段 | 说明 |
|------|------|
| `billing_expression_id` | 使用的模型价格或通道价格规则 ID |
| `billing_expression_version` | 使用的价格规则版本 |
| `billing_expression_source` | 当前可为 `channel_model_prices`、`model_prices`、`p0_usage` 或 `minimum` |
| `billing_expression_snapshot` | 当前已记录实际执行的价格规则表达式、变量、规则 ID、规则版本和 `base_quota`；无规则时记录 usage/minimum 回退表达式摘要 |
| `multiplier_snapshot` | 当前已记录默认倍率、用户分组倍率、通道分组倍率、用户分组 x 通道分组组合覆盖倍率、倍率模式和最终 `effective_ratio` |
| `access_rule_snapshot` | 用户、Token、模型、通道分组访问控制快照 |
| `billing_snapshot.expression` / `billing_snapshot.multiplier` | 当前基础快照已覆盖价格规则版本和业务倍率摘要；后续可拆成独立列或独立账单事实表 |

数据生命周期：

- 高频生产环境建议按月分区或归档。
- `content` 和 `response` 默认截断，支持配置关闭。
- 管理端清理日志应按时间范围执行，避免无条件全表删除。

### `log_replication_outboxes`

独立日志数据库补写队列表，位于主业务数据库。它只记录“主库日志事实是否已经镜像到 `LOG_SQL_DSN`”，不参与用户余额扣减。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `log_id` | uint | 主库 `logs.id`，唯一 |
| `status` | string | `pending`、`completed` 或 `failed` |
| `attempts` | int | 补写尝试次数 |
| `last_error` | text | 最近一次补写失败摘要 |
| `next_attempt_at` | time | 下次可补写时间 |
| `completed_at` | nullable time | 成功补写时间 |
| `created_at` / `updated_at` | time | 创建和更新时间 |

索引：

- `log_id` 唯一索引
- `status`
- `next_attempt_at`

当前 `LogService` 会在主库事务内创建 pending outbox，外部日志库写入成功后标记 completed；失败时保留 pending 并记录错误。服务启动后后台 worker 会周期性重放 pending outbox，将恢复后的日志库补齐。

### `redem_codes`

充值码表。当前代码命名为 `RedemCode` 和 `redem_codes`，设计文档沿用现有命名，避免引入破坏性重命名。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `code` | string | 充值码，唯一 |
| `quota` | int64 | 充值额度 |
| `status` | int | `0` 未使用，`1` 已使用，`2` 已作废 |
| `batch_no` | string | 可选批次号，用于运营发放和筛选 |
| `note` | string | 可选备注，仅管理端可见 |
| `expired_at` | nullable time | 过期时间，空表示永不过期 |
| `used_by` | nullable uint | 使用者 |
| `created_at` | time | 创建时间 |
| `used_at` | nullable time | 使用时间 |

### `model_prices`

系统模型价格表。该表保存模型级默认价格表达式和规则版本；当前已用于 `/v0/user/models` 的 `pricing_ready`/`price_rule` 展示，并在成功调用后作为通道覆盖缺失时的计费表达式来源写入 `billing_snapshot`。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `model` | string | 模型名，唯一，例如 `gpt-4o-mini` |
| `price_mode` | string | 价格模板类型，当前允许 `request`、`token`、`second`、`tiered` |
| `price_expression` | text | 价格表达式，后续计费接入时返回倍率前的基础额度 |
| `variables_json` | json/text | 表达式变量默认值 |
| `unit_tokens` | int64 | token 计价单位，未传时默认 `1000` |
| `rule_version` | int64 | 规则版本；创建为 `1`，更新、启用和禁用都会递增 |
| `enabled` | bool | 是否启用；禁用后用户侧模型列表回退到 `minimum_usage` |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

索引：

- `idx_model_prices_model`，模型名唯一索引。
- `idx_model_prices_enabled`，用户侧读取启用价格规则。

### `channel_model_prices`

通道级模型价格覆盖表。该表优先于 `model_prices`，同时可通过 `user_enabled` 控制某个通道下某个模型是否向普通用户暴露和参与普通用户调用候选；当前已用于 `/v0/user/models` 的可见性、热路径候选过滤和价格状态展示，并在选中通道存在启用覆盖时作为成功调用后的优先计费表达式来源写入 `billing_snapshot`。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `channel_id` | uint | 所属通道 |
| `model` | string | 通道中的模型名 |
| `enabled` | bool | 是否启用该通道价格覆盖；禁用后价格状态可回退系统模型价格 |
| `user_enabled` | bool | 普通用户是否可见并可调用该通道模型；为 `false` 时该通道不再贡献用户侧模型可见性，也不会进入普通用户调用候选 |
| `price_mode` | string | 价格模板类型，当前允许 `request`、`token`、`second`、`tiered` |
| `override_mode` | string | 覆盖模式，当前允许 `override`、`merge_variables` |
| `price_expression` | text | 通道级价格表达式 |
| `variables_json` | json/text | 通道级表达式变量 |
| `unit_tokens` | int64 | token 计价单位，未传时默认 `1000` |
| `rule_version` | int64 | 规则版本；创建为 `1`，更新、启用和禁用都会递增 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

索引：

- `idx_channel_model_prices_channel_model`，唯一索引 `(channel_id, model)`。
- `idx_channel_model_prices_channel_id`，通道维度查询。
- `idx_channel_model_prices_enabled`，启用价格覆盖过滤。
- `idx_channel_model_prices_user_enabled`，普通用户可见性和调用候选过滤。

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
| `status` | string/int | `pending`、`paid`、`failed`、`closed`、`refund_pending`、`refund_failed`、`refunded`、`partially_refunded` |
| `provider_order_id` | nullable string | provider 会话或订单 ID，如 Stripe Checkout Session ID |
| `provider_payment_id` | nullable string | provider 支付流水号 |
| `checkout_url` | nullable text | 支付跳转地址，过期后不可继续使用 |
| `paid_at` | nullable time | 支付成功时间 |
| `expired_at` | nullable time | 订单过期时间，创建时按 `payment.order_expire_minutes` 生成快照 |
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

### `payment_refund_requests`

RouterX 主动向 provider 发起的退款请求表。它记录出站退款请求，不替代 provider webhook 事实；最终退款状态仍由 `payment_events` 中的可信退款事件确认。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `order_no` | string | RouterX 本地订单号 |
| `user_id` | uint | 订单所属用户 |
| `provider` | string | 当前基础实现支持 `stripe` 和 `epay` |
| `provider_refund_id` | string | provider 退款对象 ID，例如 Stripe `re_...` 或易支付退款号 |
| `amount` | decimal/string | 本次请求退款金额 |
| `amount_minor` | int64 | 本次请求退款的货币最小单位金额 |
| `currency` | string | 货币 |
| `refund_quota` | int64 | 按订单金额比例推导的退款额度 |
| `status` | string | provider 请求状态或本地收尾状态，例如 `pending`、`succeeded`、`refunded`、`partially_refunded` |
| `idempotency_key` | string | 管理端发起退款请求的幂等键，唯一 |
| `reason` | text | 退款原因 |
| `actor_user_id` | uint | 发起退款请求的管理员 |
| `request_id` | nullable string | HTTP 请求 ID |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

索引：

- `idx_payment_refund_requests_order_no`
- `idx_payment_refund_requests_user_id`
- `idx_payment_refund_requests_provider`
- `idx_payment_refund_requests_provider_refund_id`
- `idx_payment_refund_requests_status`
- `idx_payment_refund_requests_actor_user_id`
- `idx_payment_refund_requests_idempotency_key`，唯一索引。

### `payment_disputes`

Stripe 争议/拒付当前事实表。`payment_events` 仍保存每个 webhook 事件，本表按 `(provider, provider_dispute_id)` 聚合 created、updated、closed 和资金变更事件的最新状态。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `provider` | string | 当前基础实现支持 `stripe` |
| `provider_dispute_id` | string | provider 争议 ID，例如 Stripe `dp_...` |
| `order_no` | string | RouterX 本地订单号 |
| `user_id` | uint | 订单所属用户 |
| `provider_payment_id` | string | provider 支付流水号，例如 Stripe PaymentIntent |
| `amount_minor` | int64 | 争议金额，货币最小单位 |
| `currency` | string | 货币 |
| `status` | string | 争议状态，例如 `needs_response`、`under_review`、`won`、`lost` |
| `reason` | string | provider 争议原因 |
| `funds_status` | string | 资金状态，例如 `withdrawn`、`reinstated`，无变化时为空 |
| `last_event_id` | string | 最近一次 provider event id |
| `last_event_type` | string | 最近一次 provider event type |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

索引：

- `idx_payment_disputes_provider_dispute`，唯一索引 `(provider, provider_dispute_id)`。
- `idx_payment_disputes_order_no`
- `idx_payment_disputes_user_id`
- `idx_payment_disputes_provider_payment_id`
- `idx_payment_disputes_status`

### `quota_transactions`

额度流水目标表。支付入账、充值码兑换、退款扣回、人工补账和人工扣回都应进入统一额度流水；模型调用消费仍以 `logs.quota_used` 为消费事实，不和充值流水混为同一口径。

当前后端已提供 `GET /v0/user/quota-transactions` 和 `GET /v0/admin/quota-transactions` 查询该表；用户侧查询会强制限定当前用户，管理侧可按 `user_id`、`type`、`source_type`、`source_id` 和时间范围查账。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `user_id` | uint | 额度归属用户 |
| `type` | string | `payment_grant`、`redem_redeem`、`admin_adjust`、`refund_deduct`、`manual_credit`、`manual_debit` |
| `amount` | int64 | 正数增加额度，负数减少额度 |
| `balance_before` | int64 | 变更前用户额度 |
| `balance_after` | int64 | 变更后用户额度 |
| `source_type` | string | `payment_order`、`payment_event`、`redem_code`、`admin_action`、`refund` |
| `source_id` | string | 来源 ID、本地订单号或事件 ID |
| `idempotency_key` | string | 幂等键，防止同一来源重复改变额度 |
| `reason` | text | 人工原因或系统摘要 |
| `actor_user_id` | nullable uint | 管理员或系统操作者 |
| `request_id` | nullable string | HTTP 请求 ID |
| `created_at` | time | 创建时间 |

索引：

- `idx_quota_transactions_user_id_created_at`，用户额度流水列表。
- `idx_quota_transactions_idempotency_key`，唯一索引。
- `idx_quota_transactions_source`，按来源查账。

事务要求：

- 写 `quota_transactions` 和更新 `users.quota` 必须在同一数据库事务内完成。
- 人工补账和扣回必须写 `reason` 和 `actor_user_id`。
- 支付相关人工补账/扣回使用 `manual_credit` 或 `manual_debit`，传入订单号时 `source_type=payment_order`、`source_id=<order_no>`。
- 支付人工退款使用 `refund_deduct`，`source_type=refund`、`source_id=<order_no>`，并用 `idempotency_key` 防止同一退款动作重复扣回。
- 支付成功、充值码兑换和退款扣回不得直接更新 `users.quota` 而不写流水。

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

当前 P0 可以保持 `key/value/category/description` 的简单结构，但设计上每个配置项都应具备可验证的元数据。具体配置 key、默认值、类型、校验和生效方式以 `docs/SETTINGS.md` 为准。实现进阶配置时优先补齐以下目标字段或等价结构：

当前 `oauth.*.client_secret` 和 `oidc.*.client_secret` 由 `SettingService` 透明加解密：写入时在配置 `ENCRYPTION_KEY` 的实例上存为 `enc:v1:` 密文，Redis settings 缓存同样保存密文，业务读取时返回解密后的明文用于 provider token 交换。

| 字段 | 说明 |
|------|------|
| `value_type` | `string`、`int`、`float`、`bool`、`json` |
| `default_value` | 默认值，用于重置和文档展示 |
| `is_sensitive` | 是否敏感，敏感值在响应、日志和审计中脱敏 |
| `is_public` | 是否允许普通用户或公开接口读取 |
| `restart_required` | 修改后是否需要重启或重新加载实例 |
| `validation_rule` | 简单校验规则或校验器名称 |
| `version` | 配置版本，用于缓存失效和并发修改控制 |
| `updated_by` | 最近修改管理员 |

推荐分类：

- `server`
- `jwt`
- `auth`
- `oauth`
- `oidc`
- `rate_limit`
- `relay`
- `billing`
- `payment`
- `log`
- `security`

### `admin_audit_logs`

统一管理审计日志表，记录管理端高风险操作的可复核摘要。当前基础实现已覆盖 API Key 创建、编辑、禁用、删除、scope 更新、用户端额度编辑拒绝，普通用户成功登录、自助改密码、创建、编辑、禁用、删除、拒绝角色变更，支付商品创建、更新、启用、禁用，系统模型价格和通道模型价格创建、更新、启用、禁用，支付订单创建、创建拒绝和取消拒绝，支付 webhook 入账、provider 明确失败通知、全额/部分退款扣回、Stripe 争议生命周期、支付人工补账/扣回、支付人工退款、Stripe/易支付 provider 退款请求，settings 批量更新和校验拒绝，用户调额，充值码生成、导入、创建拒绝、作废、兑换和兑换拒绝，通道创建、编辑、启用、禁用、删除、测试、拉取模型，管理员账号创建、编辑、禁用、删除和超级管理员权限拒绝，以及按时间清理和按过滤条件导出调用日志审计，后续继续扩展到更多支付失败分支和更多拒绝操作。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `request_id` | nullable string | HTTP 请求追踪 ID |
| `actor_user_id` | uint | 操作管理员 |
| `actor_role` | int | 操作时角色 |
| `action` | string | 动作，例如 `payment_product.create` |
| `resource_type` | string | 资源类型，例如 `payment_product` |
| `resource_id` | string | 资源 ID |
| `before_summary` | text/json | 变更前脱敏摘要 |
| `after_summary` | text/json | 变更后脱敏摘要 |
| `result` | string | `success`、`failed`、`denied` |
| `error_code` | string | 失败或拒绝时的稳定 code |
| `ip` | string | 操作来源 IP |
| `user_agent` | string | 操作客户端摘要 |
| `created_at` | time | 创建时间 |

索引：

- `idx_admin_audit_actor_created`，按操作者和时间追查。
- `idx_admin_audit_resource`，按资源类型和资源 ID 追查。
- `idx_admin_audit_action`，按动作追查。
- `idx_admin_audit_request_id`，按请求链路关联。

安全要求：

- `before_summary` 和 `after_summary` 只能保存脱敏摘要，不保存完整请求体。
- 支付密钥、下游密钥、JWT secret、API Key 明文和数据库 DSN 不得进入审计摘要。
- 审计查询当前仅超级管理员可用。

### `alert_events`

管理员告警收件箱表，用于把需要主动处置的安全或运营事件沉淀为可查询、可确认的事实。当前基础实现由 API Key 泄露上报创建 `api_key.leak_reported` 告警，并可通过 Webhook、邮件网关和 IM 网关 outbox 推送到外部系统；后续可扩展到支付异常、通道持续故障和额度异常消耗等更多告警来源。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `type` | string | 告警类型，例如 `api_key.leak_reported` |
| `severity` | string | `critical`、`warning` 或 `info` |
| `status` | string | `open` 或 `acknowledged` |
| `resource_type` | string | 关联资源类型，例如 `api_key` |
| `resource_id` | string | 关联资源 ID |
| `user_id` | nullable uint | 关联用户 |
| `token_id` | nullable uint | 关联 API Key |
| `title` | string | 脱敏标题 |
| `message` | text | 脱敏处置提示 |
| `details_json` | json | 脱敏上下文，不保存完整 API Key、Key 哈希或用户提交的可疑原文 |
| `acked_at` | nullable time | 确认时间 |
| `acked_by_user_id` | nullable uint | 确认人 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |

索引：

- `type`、`severity`、`status`，支撑告警收件箱过滤。
- `resource_type + resource_id`，按资源回溯。
- `user_id`、`token_id`，按用户或 API Key 排障。
- `acked_by_user_id`、`created_at`，按处理人和时间审计。

### `alert_delivery_outboxes`

管理员告警外部投递队列表。当前支持 `target=webhook`、`target=email` 和 `target=im`；表内只保存投递状态、重试时间和失败摘要，实际外部 payload 在重放时从 `alert_events` 的脱敏事实重建，避免把 API Key 明文、Key 哈希、通道密钥或用户提交的可疑原文写入 outbox。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `alert_id` | uint | 关联 `alert_events.id` |
| `target` | string | 投递目标，当前支持 `webhook`、`email`、`im` |
| `status` | string | `pending`、`completed` 或 `failed` |
| `attempts` | int | 投递尝试次数 |
| `last_error` | text | 最近一次投递失败摘要 |
| `next_attempt_at` | time | 下次可投递时间 |
| `completed_at` | nullable time | 成功投递时间 |
| `created_at` / `updated_at` | time | 创建和更新时间 |

索引：

- `target + alert_id` 唯一索引，避免同一告警对同一目标重复入队。
- `alert_id`，按告警追查投递状态。
- `target`、`status`、`next_attempt_at`，支撑后台 worker 和管理端过滤。

### `setting_audit_logs`

目标设计中的配置变更审计表。P0 可以先复用统一管理审计日志，P2 应保证关键 settings 变更可独立查询和回滚。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | uint | 主键 |
| `actor_user_id` | uint | 操作管理员 |
| `setting_key` | string | 被修改的配置键 |
| `category` | string | 配置分类 |
| `old_value_summary` | text/json | 旧值摘要，敏感值脱敏 |
| `new_value_summary` | text/json | 新值摘要，敏感值脱敏 |
| `change_reason` | string | 可选变更原因 |
| `request_id` | string | 请求追踪 ID |
| `created_at` | time | 创建时间 |

## 额度单位

当前常量：

```text
QuotaPerUnit = 100000000
QuotaUnlimited = -1
```

含义：

- 数据库中 `quota`、`remain_quota`、`quota_limit`、`quota_used` 使用整数存储，避免浮点误差。
- `100000000` 个基础单位等于 1 个展示额度单位。
- `-1` 表示无限制，只能用于 Key 预算等明确允许无限制的字段。

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
| `003_channel_routing_config` | 扩展通道路由配置，增加排序、多 base URL、多 key、上游数组、模型重写、通道分组和扩展配置 |
| `004_admin_audit_logs` | 新增统一管理审计日志表和 actor、resource、action、request_id 索引 |
| `005_token_advanced_management` | 新增 API Key 轮换、禁用原因和最近使用摘要基础字段 |
| `006_token_scope` | 新增 API Key scope JSON 字段 |
| `007_token_last_usage_summary` | 新增 API Key 最近来源摘要、最近模型和最近错误 code |
| `008_log_request_context` | 新增调用日志 request_id、error_code 和对应索引 |
| `009_log_usage_source` | 新增调用日志 usage_source 和对应索引 |
| `010_log_error_facts` | 新增调用日志 error_source、upstream_status 和对应索引 |
| `011_log_route_snapshot` | 新增调用日志 route_snapshot 脱敏 JSON 字符串 |
| `012_log_billing_snapshot` | 新增调用日志 billing_snapshot 脱敏 JSON 字符串 |
| `013_log_request_snapshot` | 新增调用日志 request_snapshot 脱敏 JSON 字符串 |
| `014_log_policy_snapshot` | 新增调用日志 policy_snapshot 脱敏 JSON 字符串 |
| `015_payment_refund_requests` | 新增 provider 退款请求表和订单、provider、状态、幂等键索引 |
| `016_payment_disputes` | 新增 provider 争议当前事实表和订单、用户、状态索引 |
| `017_model_prices` | 新增系统模型价格表、模型唯一索引和启用状态索引 |
| `018_channel_model_prices` | 新增通道模型价格覆盖表、通道模型唯一索引和启用/用户可见性索引 |
| `019_log_replication_outbox` | 新增主库调用日志补写 outbox 和状态/重试时间索引 |
| `020_alert_events` | 新增管理员告警收件箱表和类型、状态、资源、用户、API Key 索引 |
| `021_alert_delivery_outbox` | 新增管理员告警外部投递 outbox、目标/告警唯一索引和状态/重试时间索引 |
| `022_token_metadata` | 新增 API Key 非安全元数据 JSON 字段，用于环境、团队、应用、标签、服务账号主体和脱敏导出 |
| `023_log_usage_snapshot` | 新增调用日志 usage_snapshot 脱敏 JSON 字符串 |
| `024_log_error_snapshot` | 新增调用日志 error_snapshot 脱敏 JSON 字符串 |

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
| `tokens` | `key` unique | API Key SHA256 哈希鉴权 |
| `channels` | `idx`、`type`、`priority`、`status`、`deleted_at` | 排序、筛选、路由选择和软删除过滤 |
| `logs` | `user_id`、`token_id`、`channel_id`、`created_at`、`request_id`、`error_code`、`usage_source`、`error_source`、`upstream_status` | 日志查询、统计和链路排障 |
| `settings` | `key` unique | 配置读取 |
| `admin_audit_logs` | `actor_user_id + created_at`、`resource_type + resource_id`、`action`、`request_id` | 管理审计查询和链路追踪 |
| `alert_events` | `type`、`severity`、`status`、`resource_type + resource_id`、`user_id`、`token_id`、`acked_by_user_id`、`created_at` | 管理告警过滤、按资源回溯和确认处理 |
| `alert_delivery_outboxes` | `target + alert_id` unique、`alert_id`、`target`、`status`、`next_attempt_at` | Webhook/邮件/IM 告警投递 worker、手动重放和失败追查 |
| `model_prices` | `model` unique、`enabled` | 系统模型价格唯一性和用户侧启用规则读取 |
| `channel_model_prices` | `channel_id + model` unique、`channel_id`、`enabled`、`user_enabled` | 通道模型价格覆盖唯一性、管理查询、用户侧可见性读取和普通用户调用候选过滤 |

后续建议：

- 为 `tokens(user_id, status)`、`tokens(user_id, created_at)`、`tokens(user_id, last_used_at)` 和 `tokens(status, expired_at)` 增加组合索引，支撑 API Key 列表、排障、过期处理和高级管理。
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
- API Key：数据库长期保存 SHA256 哈希，兼容早期明文存量时验证成功后迁移为哈希。
- 下游通道 API Key：应使用 `ENCRYPTION_KEY` 或 KMS 派生的服务端密钥加密后存储；当前通道 `api_key`、`api_keys` 和 `upstreams.api_key` 可通过 `POST /v0/admin/security/rotate-secrets` 用旧主密钥解密后重加密到当前 `ENCRYPTION_KEY`。
- OAuth client_secret 和 OIDC client_secret：当前 `oauth.*.client_secret` 与 `oidc.*.client_secret` 已通过 `ENCRYPTION_KEY` 加密存储，也可通过 `POST /v0/admin/security/rotate-secrets` 重加密到当前主密钥，后续可接入 KMS。

必须脱敏的数据：

- 响应日志中的 Authorization、API Key、Cookie。
- 请求体中的用户隐私字段。
- 管理端列表中的密钥字段。
