# RouterX 策略与访问控制契约

本文档定义 RouterX 的策略决策语义：谁能调用什么模型、走哪些通道、使用哪些路由偏好、应用哪些倍率、何时拒绝、如何审计。它不设计网页页面，也不替代 `docs/API_KEYS.md`、`docs/RELAY.md`、`docs/BILLING.md` 或 `docs/SNAPSHOTS.md`；它负责把这些模块共同依赖的访问控制规则统一起来。

策略在 RouterX 中有两个目标：

- 让小白用户在默认配置下不需要理解策略，也能完成第一次成功调用。
- 让技术用户和运营方在进阶配置下能安全地限制模型、通道分组、额度、频率、路由偏好和计费倍率。

## 1. 总原则

| 原则 | 要求 |
|------|------|
| 默认可用 | P0 默认不要求配置复杂策略；启用通道、模型匹配、用户和 API Key 有额度即可完成基础调用。 |
| 收窄不放大 | API Key scope、用户请求中的 `routerx.route`、provider-specific 参数只能收窄候选能力，不能扩大后台允许范围。 |
| 后台优先 | 用户状态、API Key 状态、额度、通道启用状态、模型匹配、通道分组访问控制、熔断、限流和安全过滤优先于用户偏好。 |
| 先拒绝再上游 | 认证失败、权限不足、余额不足、限流、访问控制失败和请求格式错误默认不调用上游。 |
| 可解释 | 每次拒绝、忽略偏好、命中通道、使用倍率和扣费都应能通过日志、快照或审计解释。 |
| 不混账 | 访问控制、计费倍率、额度扣减和支付入账是不同语义；可以组合，但不能互相冒充。 |
| 配置权威 | 运行时策略默认来自数据库 `settings`、业务表和后续策略表；环境变量只承载启动期和密钥类配置。 |

## 2. 当前代码事实

当前 P0 已经具备这些策略相关基础：

- `/v1/*` 只接受 API Key，`/v0/user/*` 和 `/v0/admin/*` 只接受 User JWT。
- API Key 校验包含格式、哈希、状态、过期、软删除、所属用户状态和额度预检。
- 普通用户不能通过用户端 API Key 编辑接口修改 `remain_quota` 或 `unlimited`。
- Relay 会在调用上游前检查用户余额和 API Key 预算。
- API Key scope 已支持 `allow_models` 模型 allow-list、`api_types` APIType allow-list、`channel_groups` 通道分组 allow-list、`entry_protocols` 入口协议 allow-list、`ip_cidrs` IP/CIDR allow-list、`methods` 方法路径 allow-list、`daily_quota` 日预算、`monthly_quota` 月预算、`max_concurrency` 并发上限、`rpm` 和 `tpm`，未命中或达到上限时在上游调用前返回 `model_not_allowed`、`token_forbidden`、`route_forbidden`、`insufficient_quota` 或 `rate_limit_exceeded` 并写失败日志。
- 通道选择会过滤禁用通道、模型不匹配通道、错误计数过高通道和不可用 Adapter。
- 通道候选按 `priority DESC, idx ASC, error_count ASC, response_ms ASC, id ASC` 排序，并在最高 priority 组内按 `weight` 加权选择。
- 管理端已支持 `GET/POST/PUT/DELETE /v0/admin/groups` 维护用户分组；删除会保护 `default` 和仍被用户引用的分组，并写 `user_group.*` 审计。
- 通道已具备 `channel_group` 字段，新通道默认写入 `default`；Relay 已按 `billing.default_user_channel_group_access` 和 `billing.user_group_channel_group_access` 在候选阶段过滤用户可访问通道分组。完整策略快照仍属于目标增强。
- Redis 限流已有全局、IP、Token、用户和模型维度的基础设置与执行路径；阈值来自 `rate_limit.*`，`0` 表示关闭对应维度。

因此，本文档中的 P1/P2 策略能力是对现有 P0 闭环的增强，不应破坏当前开箱路径。

## 3. 策略来源

策略来源按“系统硬约束 -> 管理员配置 -> 用户/API Key 约束 -> 调用方偏好”的顺序合成。

| 来源 | 示例 | 阶段 | 说明 |
|------|------|------|------|
| 系统硬约束 | 认证、用户禁用、API Key 禁用、过期、软删除、额度不足、通道禁用 | P0 | 不可被任何请求参数覆盖。 |
| settings | `rate_limit.*`、`billing.default_user_channel_group_access`、`billing.user_group_channel_group_access` | P0/P1 | 热更新运行时配置，必须校验类型和敏感级别。 |
| 用户分组 | `users.group_id`、`groups.ratio` | P1 | 支撑倍率、可用通道分组和运营套餐。 |
| API Key scope | `tokens.scope_json` | P1/P2 | 当前支持模型 allow-list、APIType allow-list、通道分组 allow-list、入口协议 allow-list、IP/CIDR allow-list、方法路径 allow-list、日预算、月预算、并发上限和 RPM/TPM。 |
| 通道配置 | `channels.models`、`channel_group`、priority、weight、熔断状态 | P0/P1 | 决定候选通道、模型匹配、分组和可用性。 |
| 模型价格规则 | `model_prices`、`channel_model_prices` | P1 | 决定基础费用，不直接表达身份权限。 |
| 调用方偏好 | `routerx.route` | P1 | 只在已允许候选集中继续收窄。 |
| 管理审计 | 变更 actor、reason、before/after snapshot | P2 | 证明策略变化是谁做的、为什么做、影响什么。 |

如果多个来源冲突，按更严格的限制生效。允许规则不能覆盖更高优先级的拒绝规则。

## 4. 决策顺序

统一策略决策顺序如下：

```text
1. 入口协议和请求解析
2. 凭据鉴权
3. 用户和 API Key 状态校验
4. 额度、预算和限流预检
5. API Key scope 收窄
6. 模型和 API 类型校验
7. 通道硬过滤
8. 用户分组和通道分组访问控制
9. routerx.route 偏好收窄
10. priority 和 weight 选择
11. 模型重写和上游解析
12. usage、价格规则、倍率和扣费
13. 日志、快照、指标和审计
```

每一步的拒绝都必须尽量在调用上游前发生。

| 步骤 | 拒绝示例 | code | 是否调用上游 |
|------|----------|------|--------------|
| 凭据鉴权 | API Key 缺失、格式错误、哈希不存在 | `invalid_api_key` | 否 |
| 状态校验 | 用户禁用、API Key 禁用、API Key 过期 | `user_disabled`、`token_forbidden`、`expired_api_key` | 否 |
| 额度预算 | 用户额度不足、有限 Key 额度不足、预算不足 | `insufficient_quota` | 否 |
| 限流 | 全局、IP、API Key、用户、模型或通道限流 | `rate_limit_exceeded` | 否 |
| scope | Key 不允许该模型、入口或请求类型 | `token_forbidden` 或 `model_not_allowed` | 否 |
| 通道硬过滤 | 无 Adapter、通道禁用、模型不匹配、熔断 | `no_available_channel` 或 `unsupported_channel` | 否 |
| 访问控制 | 用户分组不能访问通道分组 | `route_forbidden` | 否 |
| 路由偏好 | `routerx.route` 指向未授权 provider 或通道分组 | `route_forbidden` | 否 |

## 5. 默认策略

P0 默认策略应偏向开箱成功：

- 已初始化实例默认允许超级管理员创建通道和 API Key 后完成首次调用。
- 默认 settings 允许普通用户访问 `default` 通道分组；新通道默认写入 `default`，因此开箱路径仍可用。
- 未配置 API Key scope 时，API Key 继承所属用户和系统策略。
- 未配置复杂价格规则时，按 P0 usage 或最低计费规则结算。
- 未配置 `routerx.route` 时，系统按通道 priority 和 weight 自动选择。

P1/P2 启用访问控制后，默认策略应偏向安全：

- 如果显式配置了 allow-list，未命中 allow-list 的资源不可访问。
- 如果同一资源同时命中 allow 和 deny，deny 生效。
- 如果策略配置格式非法，不应静默放行；生产模式下应让 `/ready` 或策略保存接口暴露风险。
- 管理员修改策略后，应影响后续请求，并通过缓存失效或版本号保证不会长期使用旧策略。

## 6. 用户分组与通道分组

用户分组和通道分组用于表达套餐、价格、访问范围和运营策略。新用户和新通道默认归入 `default` 分组；存量空分组在策略层归一为 `default`。

| 概念 | 数据来源 | 用途 | 不应承担 |
|------|----------|------|----------|
| 用户分组 | `users.group_id`、`groups` | 用户套餐、倍率、默认访问范围；默认 `default` | 管理员角色权限 |
| 通道分组 | `channels.channel_group` | 路由、套餐、倍率、访问控制；默认 `default` | provider 类型或真实地域的唯一来源 |
| 用户分组 x 通道分组 | `billing.user_group_channel_group_access`、`billing.user_group_channel_ratios` | 组合访问和组合倍率 | 模型价格表达式 |

用户分组 CRUD 当前由管理端 `/v0/admin/groups` 提供，`groups.ratio` 保留为分组元数据和兼容展示倍率；成功调用后的实际扣费倍率仍以 `billing.user_group_ratios`、`billing.channel_group_ratios` 和 `billing.user_group_channel_ratios` settings 为准。

访问判断建议：

```text
allowed_groups = default_user_channel_group_access
allowed_groups += user_group_channel_group_access[user_group].allow
allowed_groups -= user_group_channel_group_access[user_group].deny
candidate channel is allowed only if channel.channel_group in allowed_groups
```

约束：

- 空用户分组或空 `channel_group` 必须在策略层归一为 `default`；创建新用户和新通道时应直接写入 `default`，减少热路径判断成本。
- 普通用户默认可访问哪些通道分组由 settings 决定，不由前端展示状态决定。
- 通道分组倍率只影响费用，不自动代表可访问；可访问性必须单独判断。
- 用户分组倍率只影响费用，不自动代表更高权限；管理员角色仍由 `users.role` 判断。

## 7. API Key Scope

API Key scope 是调用凭据层的收窄策略。

| scope | 示例 | 语义 |
|-------|------|------|
| `allow_models` | `["gpt-4o-mini", "claude-3-5-sonnet"]` | 当前已落地；只允许这些调用方模型名，`*` 表示不收窄模型。 |
| `channel_groups` | `["default", "cheap"]` | 当前已落地；只允许这些通道分组，`*` 表示不收窄通道分组。 |
| `api_types` | `["openai.chat", "openai.embeddings"]` | 当前已落地；只允许这些接口能力，`*` 表示不收窄 APIType。 |
| `entry_protocols` | `["openai", "anthropic"]` | 当前已落地；只允许这些客户端入口协议，`*` 表示不收窄入口协议。 |
| `methods` | `["GET /v1/models", "POST /v1/chat/completions"]` | 当前已落地；只允许这些路径和方法，`*` 表示不收窄方法路径。 |
| `ip_cidrs` | `["203.0.113.0/24"]` | 当前已落地；只允许指定来源网络，`*` 表示不收窄来源 IP。 |
| `daily_quota` | `100000` | 当前已落地；当日成功日志已消耗额度达到上限后拒绝。 |
| `monthly_quota` | `3000000` | 当前已落地；当月成功日志已消耗额度达到上限后拒绝。 |
| `max_concurrency` | `2` | 当前已落地；同一 Key 同时在途请求达到上限后拒绝，Redis 可用时使用 Redis 计数，否则单机回落进程内计数。 |
| `rpm` / `tpm` | `60` / `100000` | 当前已落地；每分钟请求和模型 token 上限，达到上限后返回 `rate_limit_exceeded`。 |

Scope 合成规则：

- API Key scope 不能打开用户分组禁止的通道分组。
- API Key scope 不能启用被禁用或熔断的通道。
- API Key scope 不能绕过用户余额、API Key 预算、限流和安全过滤。
- 空 scope 表示继承用户和系统策略，不表示超级权限。
- Scope、用户分组、通道分组、通道模型、价格或 settings 变更后，必须递增策略或路由版本，并清理或刷新 API Key 鉴权、策略缓存和通道候选缓存。

## 8. `routerx.route`

`routerx.route` 是调用方路由偏好，不是权限授予。

允许表达：

- 偏好 provider。
- 偏好或排除通道分组。
- 偏好特定通道摘要或后续稳定 route id。
- 指定是否接受降级或低成本通道。

禁止表达：

- 覆盖 `Authorization`、`Cookie`、`X-Api-Key`、`api-key` 等敏感 header。
- 启用已禁用通道。
- 使用无权访问的通道分组。
- 绕过 API Key scope、用户分组访问控制、额度、限流或熔断。
- 强制使用没有 Adapter 或无法解密密钥的通道。

处理结果必须能进入路由快照：

| 结果 | 含义 |
|------|------|
| `accepted` | 偏好合法且在允许候选集中进一步收窄。 |
| `ignored` | 偏好合法但没有改变最终候选，例如目标 provider 不在本次候选内。 |
| `rejected` | 偏好格式错误或越权。 |
| `no_candidate` | 偏好合法但筛选后没有可用候选。 |

## 9. 计费倍率与访问控制

计费倍率和访问控制需要一起快照，但语义不能混淆。

| 能力 | 语义 | 权威文档 |
|------|------|----------|
| 访问控制 | 判断本次请求是否允许访问模型、通道分组或 API 类型。 | 本文、`docs/API_KEYS.md`、`docs/RELAY.md` |
| 模型价格 | 计算倍率前的基础费用。 | `docs/BILLING.md` |
| 用户分组倍率 | 对基础费用做折扣或加价。 | `docs/BILLING.md` |
| 通道分组倍率 | 对不同套餐或成本通道做折扣或加价。 | `docs/BILLING.md` |
| 用户分组 x 通道分组倍率 | 对特定用户分组使用特定通道分组做组合覆盖倍率。 | `docs/BILLING.md` |

结算事实链：

```text
access allowed
    -> route selected
    -> usage resolved
    -> base_quota from price expression
    -> effective_ratio from settings (combination override or separate group factors)
    -> quota_used
    -> conditional deduction
    -> log snapshots
```

拒绝访问不应生成模型消费。管理员人工调整、支付入账、退款和充值码应进入额度流水，不进入模型调用消费日志。

## 10. 限流、预算和熔断

限流、预算和熔断都属于策略拒绝的一部分，但来源不同。

| 类型 | 维度 | 默认阶段 | 说明 |
|------|------|----------|------|
| 全局限流 | 全实例 RPM | P0 | 防止突发压垮实例。 |
| IP 限流 | 来源 IP RPM | P0 | 防止匿名或泄露 Key 滥用。 |
| API Key 限流 | Token RPM | P0 | 防止单 Key 抢占资源。 |
| 用户限流 | user RPM | P1 | 支撑团队和套餐级请求频率限制。 |
| 模型限流 | model RPM | P1 | 防止高成本模型被误用。 |
| 通道限流 | channel/provider RPM/TPM | P1 | 对齐上游配额。 |
| API Key 预算 | daily/monthly quota | P1/P2 | 控制项目或环境预算。 |
| API Key 并发 | max_concurrency | P1/P2 | 控制单 Key 同时在途请求。 |
| 熔断 | channel/provider error rate | P1 | 故障通道临时排除候选。 |

限流或预算拒绝必须返回稳定 code，并写入限流维度和 key 摘要。指标标签不得包含完整 API Key、prompt、响应正文或高基数长尾模型名。

当前已实现全局、IP、API Key、用户和模型五个 Redis 固定窗口维度。本地命中限流时不调用上游，并按入口协议返回兼容 429：OpenAI 为 `rate_limit_exceeded`，Anthropic 为 `rate_limit_error`，Gemini 为 `RESOURCE_EXHAUSTED`。Token、用户和模型维度限流拒绝会写失败日志和基础 `policy_snapshot`；全局/IP 日志摘要、通道维度以及完整 `rate_limit_snapshot` 仍属于后续增强。

当前自动熔断通过通道候选过滤实现：`relay.error_auto_ban=true` 时排除 `error_count >= relay.error_ban_threshold` 且仍处于 `relay.error_ban_cooldown_seconds` 冷却窗口内的通道；关闭自动熔断时仍记录错误计数，但不因阈值排除候选。冷却后的半开候选探测已落地，后台探测任务和完整熔断快照仍属于后续增强。

## 11. 快照和审计

调用事实快照的统一封套、字段边界、脱敏规则和测试要求以 `docs/SNAPSHOTS.md` 为准。本文只说明策略相关字段应表达什么决策事实。

策略相关的调用日志目标字段：

| 字段 | 内容 |
|------|------|
| `policy_snapshot` | 用户状态、API Key 状态、scope 命中、访问控制结果和拒绝原因摘要。 |
| `route_snapshot` | 候选通道、过滤原因、`routerx.route` 处理结果、最终通道和模型重写。 |
| `access_rule_snapshot` | 用户分组、通道分组、模型/API 类型访问判断和规则版本。 |
| `multiplier_snapshot` | 用户分组倍率、通道分组倍率、组合覆盖倍率、倍率模式和最终 `effective_ratio`。 |
| `rate_limit_snapshot` | 命中的限流维度、窗口、阈值和剩余量摘要。 |
| `billing_expression_snapshot` | 价格表达式、变量和基础费用。 |

策略相关审计事件：

| 事件 | 触发 |
|------|------|
| `policy.settings_updated` | 修改访问控制、限流、倍率或策略开关。 |
| `policy.user_group_updated` | 修改用户分组或分组倍率。 |
| `policy.channel_group_updated` | 修改通道分组、通道分组倍率或访问范围。 |
| `policy.api_key_scope_updated` | 修改 API Key scope。 |
| `policy.route_rejected` | 大量或高风险路由越权被拒绝。 |
| `policy.limit_changed` | 修改预算、RPM、TPM 或并发限制。 |

审计不得包含完整 API Key、上游密钥、支付密钥、JWT、数据库 DSN、完整 prompt 或完整响应。

## 12. 阶段验收

### P0 验收

- User JWT 和 API Key 使用范围互斥。
- 禁用用户、禁用 API Key、过期 API Key、余额不足、禁用通道和模型不匹配会在调用上游前失败。
- 用户请求不能覆盖上游鉴权 header。
- 默认开箱路径不要求配置用户分组、通道分组访问控制或 API Key scope。
- 限流 code 与余额不足 code 可区分。

### P1 验收

- 用户分组已可通过管理端 API 维护；用户分组和通道分组访问控制已可通过 settings 配置、验证和审计；完整调用快照仍需补齐。
- API Key scope 已支持模型、APIType、通道分组、入口协议、IP/CIDR、方法路径、日预算、月预算、并发上限和 RPM/TPM 收窄。
- `routerx.route` 合法、忽略、越权和无候选路径都有稳定行为和日志摘要。
- 计费倍率和访问控制分别快照，历史账单可解释。
- 限流覆盖用户、Token、模型和通道维度；SQLite 单镜像可进程内降级，外部数据库或集群模式必须依赖 Redis 保持一致性。

### P2 验收

- 支持团队、服务账号、环境标签和批量策略管理。
- 已支持入口协议 allow-list、IP/CIDR allow-list、日预算、月预算、并发上限和 RPM/TPM；异常告警仍待补。
- 管理审计能追踪策略变更 actor、reason、before/after snapshot 和 request_id。
- 策略缓存支持版本、失效、回滚和生产 readiness 检查。
- 报表能按用户分组、API Key、通道分组、模型、provider 和策略结果聚合。

## 13. 测试矩阵

| 场景 | 断言 |
|------|------|
| 默认开箱 | 不配置高级策略也能完成首次调用。 |
| API Key scope 收窄 | 允许模型/APIType/通道分组/入口协议/IP/方法路径、未达日/月预算、未超并发上限且未超 RPM/TPM 的请求成功；未允许模型、APIType、通道分组、入口协议、IP、方法路径、达到日/月预算、达到并发上限或达到 RPM/TPM 时拒绝且不调用上游。 |
| 用户分组管理 | `TestAdminUserGroupManagement` 覆盖分组创建、查询、更新、未使用删除、`default`/已引用分组删除保护和 `user_group.*` 审计。 |
| 用户分组访问 | `TestUserGroupChannelGroupAccessFiltersRelayCandidates` 覆盖默认用户只能访问允许的通道分组，越权路由偏好不调用上游。 |
| deny 优先 | 同时命中 allow 和 deny 时拒绝。 |
| `routerx.route` 合法 | 在允许候选集中继续收窄。 |
| `routerx.route` 越权 | 返回 403 或入口协议兼容权限错误，不调用上游。 |
| 限流 | 命中对应维度返回 `rate_limit_exceeded`，日志和指标有摘要。 |
| 熔断 | 故障通道被排除，快照记录原因。 |
| 倍率快照 | 成功调用能还原基础费用、倍率和最终 `quota_used`。 |
| 策略变更 | 缓存失效，后续请求按新策略执行。 |
| 脱敏 | 快照、审计、指标不包含完整密钥或敏感正文。 |

## 14. 文档同步

修改策略能力时，需要同步检查：

- `docs/DESIGN.md`：总体原则、阅读顺序和阶段边界。
- `docs/GLOSSARY.md`：策略、访问控制、scope、用户分组、通道分组和快照术语。
- `docs/API_KEYS.md`：API Key scope、预算、限流和缓存失效。
- `docs/API.md`：鉴权矩阵、错误 code、`routerx` 请求格式和权限拒绝。
- `docs/RELAY.md`：路由决策顺序、候选过滤、`routerx.route` 和路由快照。
- `docs/BILLING.md`：访问控制、倍率、价格表达式和账单快照。
- `docs/SNAPSHOTS.md`：调用事实快照封套、脱敏、存储和测试要求。
- `docs/SETTINGS.md`：策略相关 settings、类型校验、默认值和 readiness。
- `docs/SECURITY.md`：策略越权、密钥过滤、限流绕过和事故响应。
- `docs/OBSERVABILITY.md`：日志字段、指标、审计事件和告警。
- `docs/RUNBOOKS.md`：策略拒绝、路由偏好、限流和账单异常排查。
- `docs/TRACEABILITY.md`：能力 ID、落地位置和验收证据。
- `docs/TESTING.md`：策略、路由、计费、限流和脱敏测试。
