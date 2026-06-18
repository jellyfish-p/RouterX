# RouterX API Key 管理契约

本文档定义 RouterX API Key 的产品语义、生命周期、安全边界、额度口径、审计要求和阶段验收。它不设计网页页面，只规定控制台、接口和后端服务必须承载的能力。

API Key 是 RouterX 给调用方使用的模型调用凭据。对外文档、控制台和 SDK 接入说明统一写 API Key；`Token` 只作为数据库表、服务层和内部日志中的实体名。

## 1. 设计目标

- 小白用户能在初始化和首个通道之后创建第一把 API Key，并完成第一次 `/v1` 调用。
- 技术用户能按项目、环境、团队或应用拆分 API Key，并理解额度、作用域、轮换和审计。
- 运营方能快速禁用异常 API Key，解释最近使用、额度消耗、错误峰值和泄露窗口。
- 实现者能按本文档一次性补齐 API Key 的字段、接口、缓存、审计和测试边界。

不在本文档范围：

- 网页布局、视觉样式、组件交互动画。
- OAuth/OIDC 登录本身，见 `docs/ACCOUNTS.md`。
- 支付 provider、充值码、退款和人工补账细节，见 `docs/PAYMENTS.md`。
- 上游通道密钥管理，见 `docs/SECURITY.md`、`docs/RELAY.md` 和 `docs/DATA_MODEL.md`。

## 2. 设计原则

| 原则 | 要求 |
|------|------|
| 窄权限 | API Key 只允许调用 `/v1/*` 模型接口，不能调用 `/v0/user/*`、`/v0/admin/*` 或修改任何配置。 |
| 一次性明文 | API Key 明文只在创建响应中出现一次，后续列表、日志、审计和缓存都不能保存完整明文。 |
| 哈希长期保存 | 数据库长期保存 SHA256 哈希，兼容早期明文存量时只允许验证成功后迁移为哈希。 |
| 后台策略优先 | API Key 作用域、`routerx` 偏好和调用方参数只能收窄能力，不能绕过用户、通道、额度、熔断和安全策略。 |
| 账本可解释 | API Key 最大消耗额度是预算上限，不是余额划拨；模型消费以成功调用日志和扣费事务为准。 |
| 可轮换 | 丢失、泄露、离职、环境迁移和定期安全策略都应通过创建新 Key、切换客户端、禁用旧 Key 完成。 |
| 可审计 | 创建、禁用、删除、轮换、额度调整、作用域变更和泄露处理必须留下脱敏证据。 |

## 3. 当前代码事实

当前 P0 后端已经具备这些 API Key 基础能力：

- 用户登录后通过 User JWT 调用 `/v0/user/token` 创建、列表、编辑和删除自己的 API Key。
- 创建响应返回一次性 `sk-` 明文，`tokens.key` 保存 SHA256 哈希。
- 校验时支持早期明文存量兼容，验证成功后迁移为 SHA256 哈希。
- `status`、`expired_at`、软删除和所属用户状态会影响鉴权结果。
- 普通用户编辑 API Key 时不能修改 `remain_quota` 或 `unlimited`，避免绕过预算上限。
- 有限 API Key 创建不扣用户余额，`remain_quota` 或目标字段只表示 Key 剩余预算上限；成功调用同时扣用户余额和 Key 预算。
- `unlimited=true` 或 `remain_quota=-1` 表示 API Key 自身不限额；成功调用仍扣用户额度。
- API Key 支持用户轮换、泄露上报、单 Key 用量摘要、按 Key 过滤用户日志和账单聚合、显式禁用、管理员跨用户脱敏查询，以及按 `token_ids`/`user_id` 批量禁用和批量过期。
- 管理员 API Key 风险视图已支持按时间窗口聚合失败数、成功数、额度消耗、低剩余额度、泄露上报、禁用、过期和最近错误风险；泄露风险会返回基础轮换建议，响应只返回脱敏 Key 摘要。
- `tokens.rotated_from_id` 保存轮换来源，`tokens.revoked_reason` 保存禁用原因；轮换会创建替换 Key、返回新明文一次并禁用旧 Key。
- API Key 创建、编辑、禁用、删除、轮换、泄露上报、批量禁用、批量过期、批量操作缺少筛选条件拒绝和用户端额度/无限标记编辑拒绝会写入 `api_key.*` 管理审计，审计摘要不包含完整明文 Key 或哈希。
- `tokens.scope_json` 已支持基础模型 allow-list、APIType allow-list、通道分组 allow-list、入口协议 allow-list、IP/CIDR allow-list、方法路径 allow-list、日预算、月预算、并发上限和 RPM/TPM：用户可通过 `PUT /v0/user/token/:id/scope` 写入 `allow_models`、`api_types`、`channel_groups`、`entry_protocols`、`ip_cidrs`、`methods`、`daily_quota`、`monthly_quota`、`max_concurrency`、`rpm` 与 `tpm`，系统在上游调用前返回 `model_not_allowed`、`token_forbidden`、`route_forbidden`、`insufficient_quota` 或 `rate_limit_exceeded` 并写失败日志。
- API Key 鉴权成功后向请求上下文注入当前用户和当前 Token，供 Relay、限流、日志和计费使用。
- API Key 鉴权热路径已使用 Redis 缓存 `SHA256(api_key) -> token_id` 的 lookup 映射；缓存命中后仍回源数据库加载 Token、User 和用户分组，状态、过期、额度、scope、用户状态和软删除仍以数据库为准。

当前代码事实是后续目标能力的基础，额度语义以 `docs/DECISIONS.md` 的 RXD-009/RXD-010 为目标口径；后续仍需补 `quota_limit`/`quota_used` 目标字段、更完整策略和泄露窗口分析。

## 4. 身份边界

| 身份 | 允许 | 禁止 |
|------|------|------|
| 未登录用户 | 查看公开健康状态、按配置注册或登录 | 创建、查看、禁用 API Key |
| 普通用户 User JWT | 创建、列表、改名、禁用、删除自己的 API Key，查看自己的日志和账单 | 修改自身额度、把有限 Key 改成无限 Key、查看完整明文、管理他人 Key |
| 管理员 User JWT | 在管理权限范围内查看用户、日志、账单和异常 API Key 摘要 | 用 API Key 管理系统、绕过超级管理员限制 |
| 超级管理员 User JWT | 管理 settings、高风险安全策略、管理员账号和后续企业级 API Key 策略 | 恢复或查看历史 API Key 明文 |
| API Key | 调用 `/v1/*`，受用户、Token、额度、通道和作用域约束 | 调用 `/v0/*`、覆盖上游密钥、修改账号或配置 |
| 系统任务 | 过期 Key 清理、缓存失效、指标聚合、审计归档 | 生成不受审计的长期凭据 |

## 5. 生命周期

```text
User JWT 登录
    -> 创建 API Key
    -> 明文只展示一次
    -> 客户端用 Bearer sk-* 调用 /v1/*
    -> 鉴权、额度预检、路由、上游调用、日志和扣费
    -> 观察用量与错误
    -> 轮换、禁用、删除或过期归档
```

| 阶段 | 触发 | 系统动作 | 对用户的证据 | 安全要求 |
|------|------|----------|--------------|----------|
| 创建 | 用户或管理员为自己创建 Key | 生成 `sk-` 明文，保存哈希，写入状态、额度和过期时间 | 返回一次性明文、Key 摘要、额度口径 | 明文只出现在本次响应 |
| 使用 | 调用方请求 `/v1/*` | 校验 Bearer、状态、过期、用户状态、额度和作用域 | 成功响应或稳定错误 code | 不把用户 Key 转发给上游 |
| 观察 | 用户查看列表、日志和账单 | 展示名称、状态、额度、过期时间、最近使用目标字段和脱敏摘要 | 能解释哪把 Key 在何时消耗多少 | 不展示完整明文 |
| 轮换 | 定期安全策略、泄露、人员变动、环境迁移 | 创建新 Key，迁移客户端，禁用旧 Key，保留日志关联 | 新旧 Key 摘要和切换时间 | 旧 Key 禁用后立即失效 |
| 禁用 | 用户主动停用、管理员风控、用户禁用 | 更新状态、清理缓存、后续鉴权失败 | `token_forbidden` 或等价错误 | 禁用动作可审计 |
| 删除 | 用户删除、清理长期不用 Key | 软删除 Token，保留历史日志和账单引用 | 列表不再作为可用 Key 展示 | 不删除账单事实 |
| 过期 | 到达 `expired_at` | 鉴权失败，后续可清理或提示重建 | `expired_api_key` 目标错误 | 不自动延长有效期 |
| 泄露处置 | Key 出现在外部仓库、日志、工单或异常流量中 | 立即禁用、清缓存、查最近使用、查询泄露窗口、创建替换 Key | 泄露窗口和额度消耗可解释 | 不把泄露明文再次写入系统 |

## 6. 小白开箱路径

第一把 API Key 的体验应尽量少让用户理解内部概念：

1. 用户完成初始化并登录。
2. 用户添加首个可用通道。
3. 用户创建默认 API Key，可填写名称、过期时间和额度。
4. 系统只在创建成功响应里展示一次 `sk-` 明文。
5. 用户把 Key 放入 SDK 或环境变量，使用 RouterX Base URL 调用 `/v1/chat/completions`。
6. 用户能看到一次成功调用日志、usage、额度扣减和当前余额。

默认说明应强调：

- “这是调用凭据，不是登录密码。”
- “关闭后无法再次查看完整 Key，丢失就重新创建。”
- “Key 的最大消耗额度是预算上限，不会在创建时扣账户余额。”
- “在浏览器前端、公开仓库、工单和日志中暴露 Key 都应视为泄露。”

## 7. 技术用户进阶体验

技术用户通常会为不同用途拆分 API Key：

| 场景 | 推荐拆分 | 设计收益 |
|------|----------|----------|
| 开发、测试、生产 | 每个环境独立 Key | 泄露或超额时影响范围小 |
| 多个应用 | 每个应用独立 Key | 日志、账单和告警能按应用解释 |
| 团队接入 | 每个团队或服务账号独立 Key | 后续企业审计和权限收口更清晰 |
| 高成本模型 | 单独 Key + 有限额度 + 模型作用域 | 降低误调用和滥用风险 |
| 定期轮换 | 新旧 Key 短时间并行 | 不中断业务迁移 |

进阶能力应围绕这些维度设计：

- 名称、环境、标签和备注。
- 过期时间和轮换提示。
- 模型、通道分组、协议入口和请求类型作用域。
- 单 Key 额度、日/月预算、请求频率和并发限制。
- 最近使用时间、最近错误、最近模型和调用来源摘要。
- 按 Key 过滤日志、账单、错误、限流和路由结果。
- 批量禁用、批量过期、批量导出脱敏摘要。

## 8. 数据模型契约

### 当前核心字段

`tokens` 是 API Key 的内部实体。

| 字段 | 说明 |
|------|------|
| `id` | 内部 Token ID，日志和账单引用它。 |
| `user_id` | 所属用户。 |
| `name` | 用户可见备注名。 |
| `key` | API Key SHA256 哈希，响应中不返回。 |
| `status` | 启用或禁用。 |
| `expired_at` | 过期时间，空表示不过期。 |
| `remain_quota` | 当前字段。目标语义中表示 Key 剩余预算上限；`-1` 表示 Key 自身不限额。旧实现中可能表示已划拨余额，迁移时需消除该歧义。 |
| `unlimited` | 是否 Key 自身不限额。 |
| `rotated_from_id` | 轮换来源 Key，用于解释迁移链路。 |
| `revoked_reason` | 禁用原因，例如用户主动、泄露、轮换或风控。 |
| `scope_json` | API Key 收窄策略 JSON；当前支持 `allow_models` 模型 allow-list、`api_types` APIType allow-list、`channel_groups` 通道分组 allow-list、`entry_protocols` 入口协议 allow-list、`ip_cidrs` IP/CIDR allow-list、`methods` 方法路径 allow-list、`daily_quota` 日预算、`monthly_quota` 月预算、`max_concurrency` 并发上限、`rpm` 每分钟请求上限和 `tpm` 每分钟模型 token 上限。 |
| `last_used_at` | 最近成功或失败调用时间。 |
| `last_used_ip_hash` | 最近来源 IP 的 SHA-256 摘要，不保存到 API Key 响应原文。 |
| `last_user_agent_hash` | 最近 User-Agent 的 SHA-256 摘要，不保存到 API Key 响应原文。 |
| `last_model` | 最近请求模型名，便于用户识别业务来源。 |
| `last_error_code` | 最近失败的协议化错误 code；最近调用成功时为空。 |
| `created_at` / `updated_at` / `deleted_at` | 创建、更新和软删除时间。 |

### 目标增强字段

这些字段用于商业级运营和进阶体验，可按阶段加入：

| 字段 | 说明 |
|------|------|
| `prefix` | 可展示的短摘要，例如 `sk-abc...wxyz`，只用于识别，不用于鉴权。 |
| `hash_version` | 哈希算法或迁移版本，支持未来升级。 |
| `quota_limit` | 目标字段。Key 最大消耗额度；`null` 或 `-1` 表示不限 Key 自身额度。 |
| `quota_used` | 目标字段。Key 已累计消耗额度，用于和 `quota_limit` 计算剩余预算。 |
| `metadata_json` | 环境、应用名、团队、标签、外部系统关联 ID 等非安全元数据。 |
| `created_by_user_id` | 创建动作的登录用户。 |
| `updated_by_user_id` | 最近一次管理动作的登录用户。 |

索引建议：

| 索引 | 用途 |
|------|------|
| `idx_tokens_user_id_status` | 用户列表和可用 Key 查询。 |
| `idx_tokens_user_id_created_at` | 用户按创建时间排序。 |
| `idx_tokens_user_id_last_used_at` | 用户排查最近使用。 |
| `idx_tokens_status_expired_at` | 系统任务处理过期 Key。 |
| `idx_tokens_rotated_from_id` | 轮换链路追踪。 |

## 9. 额度和预算语义

API Key 最大消耗额度必须和账单文档保持一致。

| 事件 | 用户额度 | API Key 预算 | 模型消费日志 | 解释 |
|------|----------|--------------|--------------|------|
| 创建有限 API Key | 不变 | 设置最大消耗额度或剩余预算上限 | 不写消费日志 | Key 预算上限，不是余额划拨 |
| 有限 API Key 成功调用 | 减少本次 `quota_used` | 减少剩余预算或增加累计已用 | 写成功日志 | 用户余额和 Key 预算同时约束 |
| 创建无限 API Key | 不变 | `remain_quota=-1` | 不写消费日志 | Key 自身不限额 |
| 无限 API Key 成功调用 | 减少本次 `quota_used` | 保持 `-1` | 写成功日志 | 本次模型消费由用户额度支付 |
| API Key 禁用或删除 | 不变 | 未用预算失效 | 历史日志保留 | 没有余额退回动作 |
| 管理员调整额度 | 不直接改变用户余额，除非是独立额度调整 | 更新 Key 预算上限 | 不写模型消费日志 | 属于预算修正，不是模型消费 |

商业级默认解释：

- 有限 API Key 的额度是“最大消耗预算”，不是用户额外余额，也不是已冻结余额。
- 删除有限 API Key 不退回余额；未用预算只是失效。
- API Key 预算调整应进入审计；真正影响用户余额的充值、退款、充值码和人工补账应统一进入 `quota_transactions` 目标表，避免与模型消费日志混淆。
- 调用前必须同时检查用户余额和 Key 预算；调用成功后的扣费事务必须同时更新用户余额和 Key 预算计数。

## 10. 作用域和策略

P0 API Key 默认继承所属用户和系统策略。当前已支持基础模型 allow-list、APIType allow-list、通道分组 allow-list、入口协议 allow-list、IP/CIDR allow-list、方法路径 allow-list、日预算、月预算、并发上限与 RPM/TPM scope；后续作用域能力继续遵守只收窄、不放大的原则。
跨模块策略决策顺序、访问控制、分组、限流、`routerx.route` 冲突规则和策略快照以 `docs/POLICIES.md` 为准；入口协议、APIType 和能力等级以 `docs/PROTOCOLS.md` 为准；调用事实快照封套和脱敏规则以 `docs/SNAPSHOTS.md` 为准。本节只说明 API Key scope 对调用凭据的影响。

| 作用域 | 示例 | 拒绝时错误 |
|--------|------|------------|
| `allow_models` 模型 allow-list | 只能调用 `gpt-4o-mini`、`claude-3-5-sonnet` | `model_not_allowed` |
| `api_types` APIType allow-list | 只允许 `openai.chat` 或 `openai.embeddings` | `token_forbidden` |
| `channel_groups` 通道分组 allow-list | 只能使用 `default` 或 `cheap` 分组 | `route_forbidden` |
| `entry_protocols` 入口协议 allow-list | 只允许 `openai`、`anthropic` 或 `gemini` 入口；`gemini` 覆盖 generateContent、streamGenerateContent、countTokens、embedContent 和 batchEmbedContents | `token_forbidden` |
| `ip_cidrs` IP/CIDR allow-list | 只允许固定出口 IP 或网段 | `token_forbidden` |
| `methods` 方法路径 allow-list | 只允许 `POST /v1/chat/completions` | `token_forbidden` |
| `daily_quota` 日预算 | 当日成功日志已消耗额度达到上限后拒绝 | `insufficient_quota` |
| `monthly_quota` 月预算 | 当月成功日志已消耗额度达到上限后拒绝 | `insufficient_quota` |
| `max_concurrency` 并发上限 | 同一 Key 同时在途请求达到上限后拒绝 | `rate_limit_exceeded` |
| `rpm` 每分钟请求上限 | 当前分钟该 Key 已写入日志的请求数达到上限后拒绝 | `rate_limit_exceeded` |
| `tpm` 每分钟模型 token 上限 | 当前分钟该 Key 成功日志的 `total_tokens` 达到上限后拒绝 | `rate_limit_exceeded` |

策略决策顺序：

1. 校验 API Key 格式、哈希、状态、过期和软删除。
2. 校验所属用户状态、用户额度和用户级策略。
3. 校验 API Key 自身额度、作用域、预算和限流。
4. 校验请求体格式、模型名和入口协议。
5. 选择候选通道，再应用通道分组、模型匹配、熔断和 `routerx.route` 偏好。
6. 写入策略快照、路由快照、日志和指标。

## 11. 接口契约

### 当前用户接口

| 方法 | 路径 | 语义 |
|------|------|------|
| GET | `/v0/user/token` | 当前用户 API Key 列表，不能返回完整明文。 |
| POST | `/v0/user/token` | 创建 API Key，返回一次性明文，并写 `api_key.created` 审计。 |
| PUT | `/v0/user/token/:id` | 编辑名称、状态和过期时间；普通编辑写 `api_key.updated`，禁用写 `api_key.disabled`，额度/无限标记编辑拒绝写 `api_key.quota_limit_denied`。 |
| DELETE | `/v0/user/token/:id` | 删除自己的 API Key，并写 `api_key.deleted` 审计。 |
| POST | `/v0/user/token/:id/disable` | 显式禁用自己的 API Key，可记录原因并写 `api_key.disabled` 审计。 |
| POST | `/v0/user/token/:id/rotate` | 创建替换 Key，继承安全属性，禁用旧 Key，并写 `api_key.rotated` 审计。 |
| POST | `/v0/user/token/:id/report-leak` | 上报泄露并立即禁用 Key，写 `api_key.leak_reported` 审计。 |
| PUT | `/v0/user/token/:id/scope` | 更新 `allow_models` 模型 allow-list、`api_types` APIType allow-list、`channel_groups` 通道分组 allow-list、`entry_protocols` 入口协议 allow-list、`ip_cidrs` IP/CIDR allow-list、`methods` 方法路径 allow-list、`daily_quota` 日预算、`monthly_quota` 月预算、`max_concurrency` 并发上限、`rpm` 和 `tpm`，并写 `api_key.scope_updated` 审计。 |
| GET | `/v0/user/token/:id/usage` | 查看单 Key 调用数、成功/失败数、额度消耗、总 tokens 和最近调用摘要。 |
| GET | `/v0/user/token/:id/leak-window` | 查看单 Key 最近窗口调用摘要；`window_hours` 默认 24、最大 720，返回模型、错误 code 和来源 IP 哈希计数。 |

用户接口必须保持这些边界：

- 用户只能操作自己的 API Key。
- 用户端编辑不能调整 `remain_quota`、`quota_limit`、`quota_used`、`unlimited`、所属用户或哈希字段。
- 列表和详情只展示脱敏摘要、状态、额度、过期时间和最近使用字段。

### 目标增强接口

目标增强接口可以按阶段加入，路径可在实现时统一，但语义必须保持：

| 动作 | 建议路径 | 权限 | 语义 |
|------|----------|------|------|
| 轮换 | `POST /v0/user/token/:id/rotate` | Key 所属用户或管理员 | 创建新 Key，复制安全的名称、作用域和过期策略，返回新 Key 明文一次。 |
| 禁用 | `POST /v0/user/token/:id/disable` | Key 所属用户或管理员 | 立即禁用并清理缓存。 |
| 泄露上报 | `POST /v0/user/token/:id/report-leak` | Key 所属用户或管理员 | 禁用 Key、写审计、提示创建替换 Key。 |
| 用量摘要 | `GET /v0/user/token/:id/usage` | Key 所属用户或管理员 | 返回该 Key 的调用量、额度消耗、错误和最近使用摘要。 |
| 泄露窗口 | `GET /v0/user/token/:id/leak-window`、`GET /v0/admin/token/:id/leak-window` | Key 所属用户或管理员 | 基于现有调用日志聚合窗口内调用、额度、模型、错误 code 和来源 IP 哈希，不返回明文 Key 或原始 IP。 |
| 作用域扩展 | `PUT /v0/user/token/:id/scope` | 管理员或具备策略权限的用户 | 在已实现 `allow_models`、`api_types`、`channel_groups`、`entry_protocols`、`ip_cidrs`、`methods`、`daily_quota`、`monthly_quota`、`max_concurrency`、`rpm` 和 `tpm` 基础上继续扩展更完整策略快照。 |
| 批量禁用 | `POST /v0/admin/token/batch-disable` | 管理员 | 按用户、标签、环境、异常条件批量禁用；缺少筛选条件时返回 400 并写 `api_key.batch_disable_denied`。 |
| 批量过期 | `POST /v0/admin/token/batch-expire` | 管理员 | 按 `token_ids` 或 `user_id` 立即设置过期时间，必须带筛选条件；缺少筛选条件时返回 400 并写 `api_key.batch_expire_denied`。 |
| 管理查询 | `GET /v0/admin/token` | 管理员 | 跨用户按状态、最近使用、错误、额度和标签检索脱敏摘要。 |
| 风险视图 | `GET /v0/admin/token/risk` | 管理员 | 按窗口聚合异常 Key，返回风险等级、原因、建议动作和基础轮换建议，不暴露明文 Key 或哈希。 |

## 12. 缓存和一致性

API Key 是热路径资源，缓存设计必须服务安全和性能。

当前实现采用保守的 Redis lookup cache：只保存 `api_key_auth:<SHA256(api_key)> -> token_id`，不保存完整明文 Key，也不缓存“已授权”结论。缓存命中后仍用 `token_id` 从主数据库加载 Token、User 和用户分组，并重新校验状态、过期时间、软删除、用户状态、额度和 scope。Redis 失败时回退数据库哈希查询；缓存 TTL 默认较短，且不会超过 API Key 剩余有效期。

当前 lookup cache 会在轮换、禁用、删除、scope 更新、普通编辑、批量禁用、批量过期和成功扣费后清理相关映射。用户状态变化即使未显式清理 lookup cache，也会因为每次缓存命中都重新加载 User 而立即按数据库状态生效。

| 场景 | 要求 |
|------|------|
| 鉴权缓存 | 缓存键使用 `SHA256(key)` 或内部 ID，不使用完整明文。 |
| 禁用或删除 | 必须让后续请求立即失效；缓存存在时同步清理。 |
| 过期时间 | 缓存 TTL 不得超过 API Key 的剩余有效期。 |
| 用户状态变化 | 用户禁用、软删除、额度策略变化后，该用户的 Key 不能继续成功调用。 |
| 作用域变化 | 模型、通道分组、限流或预算变化后，策略快照必须更新。 |
| Redis 不可用 | 高风险生产策略可 fail-closed；普通场景至少回退 DB 并暴露指标。 |
| 负缓存 | 无效 Key 可短 TTL 负缓存，但不得把完整 Key 写入日志或缓存键。 |

## 13. 审计和观测

### 审计事件

| 事件 | 触发 |
|------|------|
| `api_key.created` | 创建 API Key。 |
| `api_key.updated` | 名称、状态、过期时间或元数据变更。 |
| `api_key.scope_updated` | 作用域或策略变更。 |
| `api_key.rotated` | 轮换创建新 Key。 |
| `api_key.disabled` | 主动禁用或风控禁用。 |
| `api_key.deleted` | 软删除。 |
| `api_key.leak_reported` | 用户或管理员上报泄露。 |
| `api_key.batch_disabled` | 管理员批量禁用 Key。 |
| `api_key.batch_expired` | 管理员批量过期 Key。 |
| `api_key.batch_disable_denied` | 管理员批量禁用 Key 但缺少 `token_ids` 和 `user_id` 筛选条件。 |
| `api_key.batch_expire_denied` | 管理员批量过期 Key 但缺少 `token_ids` 和 `user_id` 筛选条件。 |
| `api_key.quota_limit_set` | 创建或修改 Key 最大消耗额度。 |
| `api_key.quota_adjusted` | 管理员调整 Key 预算上限或迁移旧额度口径。 |
| `api_key.quota_limit_denied` | 用户端尝试修改额度或无限标记被拒绝。 |

审计字段：

- `actor_user_id`
- `subject_user_id`
- `token_id`
- `token_prefix`
- `action`
- `before_snapshot`
- `after_snapshot`
- `request_id`
- `ip_hash`
- `user_agent_hash`
- `reason`
- `created_at`

禁止写入审计：

- 完整 API Key 明文。
- 上游密钥、支付密钥、JWT、数据库 DSN。
- 完整请求体或响应体，除非经过明确截断和脱敏。

### 指标

| 指标 | 类型 | 标签 | 用途 |
|------|------|------|------|
| `routerx_api_key_auth_total` | counter | result、reason | 鉴权成功和失败趋势。 |
| `routerx_api_key_active_total` | gauge | status | 活跃、禁用、过期 Key 数。 |
| `routerx_api_key_last_used_age_seconds` | histogram | status | 长期未使用 Key 清理和风险识别。 |
| `routerx_api_key_quota_remaining` | gauge | user_group、key_type | 额度风险和告警。 |
| `routerx_api_key_rotation_total` | counter | reason | 轮换策略执行情况。 |
| `routerx_api_key_leak_events_total` | counter | source | 泄露响应趋势。 |

## 14. 泄露处理剧本

当 API Key 被发现出现在公开仓库、日志、工单、聊天记录或异常调用中：

1. 根据脱敏摘要或用户提供的完整 Key 定位 Token。
2. 立即禁用或删除对应 Token。
3. 清理 API Key 鉴权缓存和相关负载均衡缓存。
4. 查询泄露窗口内的最近使用时间、IP 摘要、User-Agent 摘要、模型、错误和额度消耗。
5. 引导用户创建新 Key 并替换业务侧配置。
6. 如果泄露来自 RouterX 日志或响应，按安全事故处理，修复脱敏规则并补测试。
7. 如果发生异常消耗，按日志、扣费事务和额度流水解释账务事实。

处置过程不得把完整泄露 Key 再次写入工单、审计、聊天记录或日志。

## 15. 阶段验收

### P0 验收

- 用户能创建、列表、编辑、删除自己的 API Key。
- 创建响应返回一次性 `sk-` 明文，列表和日志不返回完整明文。
- 数据库保存 SHA256 哈希，兼容早期明文存量迁移。
- 禁用、过期、软删除、用户禁用和余额不足会阻止 `/v1` 调用。
- 有限 API Key 和无限 API Key 的扣费语义与 `docs/BILLING.md` 一致。
- API Key 不能调用 `/v0/user/*` 或 `/v0/admin/*`。
- 敏感信息扫描不出现用户 API Key、上游密钥、支付密钥或 DSN。
- 创建、编辑、禁用、删除、批量操作拒绝和用户端额度编辑拒绝都有可查询的脱敏管理审计。

### P1 验收

- API Key 用量摘要支持调用量、成功/失败数、额度消耗、最近模型和最近错误；Key 列表/详情持久化展示最近时间、模型、错误 code、IP 摘要和 User-Agent 摘要。
- 已支持轮换动作：创建新 Key、保留旧 Key 关联、禁用旧 Key、审计完整。
- 支持按 Key 查看用量摘要、过滤日志和账单聚合，并可按稳定 `error_code`、`error_source` 与 `upstream_status` 收窄调用日志和导出；错误和限流事件的统一视图仍待补。
- 已支持基础作用域：模型 allow-list、APIType allow-list、通道分组 allow-list、入口协议 allow-list、IP/CIDR allow-list、方法路径 allow-list、日预算、月预算、并发上限和 RPM/TPM。
- 基础 Redis 鉴权 lookup cache 已覆盖预热、命中回源校验、更新、禁用、删除、轮换、scope 变化、批量禁用、批量过期和扣费后失效；用户状态变化由缓存命中后的数据库权威校验生效。

### P2 验收

- 管理员已支持跨用户查询、批量禁用、批量过期、基础异常 Key 风险视图、泄露风险基础轮换建议和单 Key 泄露窗口分析；基础鉴权映射缓存失效已覆盖，主动告警通知仍待补。
- 支持企业团队、服务账号、标签、环境和导出脱敏摘要。
- 已支持入口协议 allow-list、IP/CIDR allow-list、日预算、月预算、并发上限和 RPM/TPM；更完整策略快照仍待补。
- 已支持泄露上报、替换建议、风险视图基础轮换建议和基于调用日志的窗口分析；主动告警通知仍待补。
- API Key 预算调整、支付入账、退款、充值码和人工补账统一走对应审计或额度流水。

## 16. 测试矩阵

| 场景 | 断言 |
|------|------|
| 创建 Key | 响应有一次性明文，数据库无完整明文。 |
| 列表 Key | 只返回脱敏摘要、状态、额度和时间字段。 |
| 有效 Key 调用 | `/v1/models` 和基础 Chat 成功。 |
| User JWT 调用 `/v1` | 返回认证错误。 |
| API Key 调用 `/v0` | 返回未登录或权限错误。 |
| 禁用 Key | 后续调用失败，缓存存在时立即失效。 |
| 过期 Key | 返回过期或认证错误，不调用上游。 |
| 用户禁用 | 所属 API Key 无法继续调用。 |
| 有限额度 | 创建不扣用户余额；调用同时扣 `users.quota` 和 Key 剩余预算或累计已用。 |
| 无限额度 | 调用扣 `users.quota`，Token 自身保持无限标记。 |
| 管理审计 | 创建、编辑、禁用、删除、批量操作缺少筛选条件拒绝和禁止用户端改额度会写 `api_key.*`，审计中不含 `sk-` 明文。 |
| 泄露处理 | 旧 Key 失效，新 Key 可用，审计不含明文。 |
| 风险视图 | 管理员能按窗口查看异常 Key 的失败峰值、低剩余额度、泄露上报、禁用、过期、最近错误风险和基础轮换建议，响应不包含明文 Key 或哈希。 |
| 泄露窗口 | 用户和管理员能查询单 Key 最近窗口内调用数、成功/失败数、额度、tokens、模型、错误 code 和来源 IP 哈希计数；响应不包含完整 API Key 或原始 IP。 |
| 作用域拒绝 | 模型 allow-list 未命中时返回 `model_not_allowed`，APIType、入口协议、IP/CIDR 或方法路径 allow-list 未命中时返回 `token_forbidden`，通道分组 allow-list 未命中时返回 `route_forbidden`，日/月预算达到上限时返回 `insufficient_quota`，并发上限或 RPM/TPM 命中时返回 `rate_limit_exceeded`；都会写失败日志且不调用上游。 |

## 17. 文档同步

修改 API Key 能力时，需要同步检查：

- `docs/DESIGN.md`：总体产品路径、阶段边界和文档阅读顺序。
- `docs/GLOSSARY.md`：API Key、Token、模型 token、额度和作用域术语。
- `docs/POLICIES.md`：策略决策顺序、访问控制、scope、限流、预算和缓存失效。
- `docs/SNAPSHOTS.md`：API Key 相关调用事实、脱敏和历史解释边界。
- `docs/PROTOCOLS.md`：协议入口、APIType、能力等级和 scope 可收窄范围。
- `docs/API.md`：接口、错误格式、鉴权矩阵和目标增强接口。
- `docs/DATA_MODEL.md`：`tokens` 字段、索引、日志引用和额度流水。
- `docs/ACCOUNTS.md`：User JWT、管理员权限、用户禁用和恢复边界。
- `docs/BILLING.md`：有限和无限 API Key 的扣费口径。
- `docs/SECURITY.md`：密钥保护、泄露响应、审计和缓存安全。
- `docs/OBSERVABILITY.md`：指标、日志字段、审计事件和告警。
- `docs/RUNBOOKS.md`：API Key 泄露、余额不足、异常调用和缓存失效处理。
- `docs/CONSOLE.md`：控制台和等价接口承载的状态、动作、证据和空状态。
- `docs/DEVELOPER_EXPERIENCE.md`：调用方迁移、轮换、环境隔离和错误处理。
- `docs/TRACEABILITY.md`：能力 ID、落地位置和验收证据。
- `docs/TESTING.md`：后端、集成、安全和回归测试。
