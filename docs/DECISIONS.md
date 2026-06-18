# RouterX 技术决策记录

## 目标

本文档记录 RouterX 当前设计中最关键、最容易影响实现方向的技术取舍。它不是替代主设计稿，而是把需要长期保持一致、并适合与你确认的决策集中管理。

状态说明：

| 状态 | 含义 |
|------|------|
| Active | 当前默认设计，后续实现按此推进。 |
| Confirm | 建议你重点确认的技术取舍；默认先按本文档推进，但到确认窗口前应再次核对。 |
| Later | 后续阶段再展开，当前不阻塞 P0。 |

确认窗口说明：

| 窗口 | 含义 |
|------|------|
| 已定 | 当前不需要再确认，除非你主动改变设计理念。 |
| P0 gate | 实现 P0 闭环和验收门禁前必须确认。 |
| P1 gate | 进入 P1 计费、访问控制、流式或多协议实现前确认。 |
| P2 gate | 进入企业、支付、KMS、审计或高级 API 前确认。 |

Confirm 项不是阻塞当前文档工作的开放问题。它们是“默认已选，但实现到对应阶段前值得再问你一次”的设计锚点。

## 决策索引

| ID | 决策 | 状态 | 确认窗口 | 主要影响文档 |
|----|------|------|----------|--------------|
| RXD-001 | 商业级指工程质量和运营能力，不区分授权边界 | Active | 已定 | `DESIGN`、`README` |
| RXD-002 | 本轮文档不设计网页，只约束控制台和接口能力 | Active | 已定 | `DESIGN`、`CONSOLE`、`API` |
| RXD-003 | DB `settings` 是运行时配置权威来源 | Active | 已定 | `SETTINGS`、`OPERATIONS`、`DATA_MODEL` |
| RXD-004 | 环境变量只承载启动项和密钥类配置 | Active | 已定 | `SETTINGS`、`OPERATIONS` |
| RXD-005 | 自部署商业级默认关闭公开自助注册 | Active | 已定 | `ACCOUNTS`、`SETTINGS` |
| RXD-006 | User JWT 和 API Key 能力边界严格分离 | Active | 已定 | `API`、`ACCOUNTS`、`DESIGN` |
| RXD-007 | API Key 明文只返回一次，数据库保存 SHA256 哈希 | Active | 已定 | `API`、`DATA_MODEL`、`ACCOUNTS` |
| RXD-008 | 下游通道密钥使用 `ENCRYPTION_KEY` 或 KMS 加密 | Active | 已定 | `DATA_MODEL`、`OPERATIONS` |
| RXD-009 | 有最大消耗额度的 API Key 是预算上限，不在创建时划拨用户余额 | Active | 已定 | `API_KEYS`、`BILLING`、`API`、`TESTING` |
| RXD-010 | API Key 调用成功后始终扣用户余额；有限 Key 还要同时消耗自身预算上限 | Active | 已定 | `API_KEYS`、`BILLING`、`API`、`TESTING` |
| RXD-011 | `relay.retry_count=0` 默认单次调用；大于 0 按 `relay.retry_on_status` 开启非流式安全重试 | Active | 已定 | `SETTINGS`、`RELAY`、`IMPLEMENTATION` |
| RXD-012 | P0 默认不记录请求和响应 body | Active | 已定 | `SETTINGS`、`OPERATIONS`、`RELAY` |
| RXD-013 | `/v1` 必须返回入口协议兼容响应和错误格式 | Active | 已定 | `API`、`PROTOCOLS`、`RELAY`、`TESTING` |
| RXD-014 | `routerx.route` 只能表达偏好，不能绕过管理员策略 | Active | 已定 | `RELAY`、`API`、`DESIGN` |
| RXD-015 | 通道内部 `upstreams` 优先于外层 key/base URL 数组 | Active | 已定 | `RELAY`、`DATA_MODEL`、`TESTING` |
| RXD-016 | P0 先保证 OpenAI-compatible Chat/Models 闭环 | Active | 已定 | `DEVELOPER_EXPERIENCE`、`RELAY`、`ROADMAP`、`IMPLEMENTATION` |
| RXD-017 | 多协议和多上游同等重要，但分 P1/P2 展开 | Active | 已定 | `DESIGN`、`PROTOCOLS`、`RELAY`、`ROADMAP` |
| RXD-018 | 支付是可选运营模块，不阻塞自部署最小闭环 | Active | 已定 | `PAYMENTS`、`BILLING`、`API`、`DESIGN` |
| RXD-019 | 生产模式缺少关键配置时 `/ready` 应不就绪 | Confirm | P0 gate | `SETTINGS`、`OPERATIONS`、`ROADMAP`、`ACCEPTANCE` |
| RXD-020 | 自动化测试使用本地下游桩，不依赖真实模型厂商 | Active | 已定 | `TESTING`、`IMPLEMENTATION` |
| RXD-021 | 删除有限 API Key 不产生余额退回；只释放未使用预算上限 | Active | 已定 | `API_KEYS`、`BILLING`、`PAYMENTS` |
| RXD-022 | 失败调用默认不扣费，未来失败成本必须由 settings 和日志快照解释 | Confirm | P1 gate | `BILLING`、`ERRORS`、`TESTING` |
| RXD-023 | 初始化超级管理员默认获得启动额度，只服务首次验证和管理员自测 | Confirm | P0 gate | `SETTINGS`、`BILLING`、`IMPLEMENTATION` |
| RXD-024 | 计费和访问控制优先使用 `channel_group`，后续新增 `model_group` 需显式迁移 | Confirm | P1 gate | `BILLING`、`POLICIES`、`DATA_MODEL` |
| RXD-025 | 新用户和新通道默认归入 `default` 分组，空分组在策略层归一为 `default` | Active | 已定 | `ACCOUNTS`、`POLICIES`、`DATA_MODEL`、`SETTINGS` |
| RXD-026 | 单镜像 SQLite 模式可不配置 Redis；外部数据库或集群模式必须配置 Redis | Active | 已定 | `OPERATIONS`、`ARCHITECTURE`、`README` |
| RXD-027 | 可通过 `LOG_SQL_DSN` 单独配置日志数据库，但结算最小事实必须保留在主库或 outbox | Active | 已定 | `OPERATIONS`、`OBSERVABILITY`、`BILLING`、`DATA_MODEL` |
| RXD-028 | 模型到通道的候选集应预加载和缓存，集群一致性通过 Redis 版本和失效广播保证 | Active | 已定 | `ARCHITECTURE`、`RELAY`、`POLICIES`、`SETTINGS` |

## 重点确认项

### RXD-009 / RXD-010：API Key 预算口径

默认设计：

- 创建带最大消耗额度的 API Key 时，不从用户余额划拨或冻结额度。
- 有限 API Key 的额度表示预算上限或剩余可消费上限，不是独立余额。
- 有限 API Key 调用前必须同时检查用户余额和 Key 剩余预算；任一不足都拒绝，不调用上游。
- 有限 API Key 调用成功后扣用户 `quota`，同时消耗 Key 的剩余预算或增加 Key 的累计已用。
- 无限 API Key 只表示 Key 自身没有预算上限，调用成功后仍扣用户 `quota`。

为什么这样选：

- 创建 Key 不改变用户余额，小白更容易理解“余额只在真实调用后减少”。
- 技术用户仍能用每个 Key 的最大消耗额度控制项目、环境或人员预算。
- 调用时同时检查用户余额和 Key 预算，可以避免用户余额不足时有限 Key 继续透支。
- 用户账单可以按成功调用的 `logs.quota_used` 聚合，不需要把创建 Key 解释成账务动作。

代价：

- 有限 Key 需要记录预算上限、剩余预算或累计已用，不能只靠用户余额表达。
- 扣费事务必须同时更新用户余额和 Key 预算计数，保证并发下二者都不透支。
- 旧的划拨式 `remain_quota` 存量如存在，需要迁移或在文档和代码中标记为 legacy 语义。

### RXD-011：默认不自动重试

默认设计：

- 当前 `relay.retry_count=0`。
- 默认只做一次明确的下游调用，先保证错误归因、日志、扣费和排障稳定。
- 当前已支持通过 `relay.retry_count > 0` 为非流式请求开启有限候选通道重试，HTTP 状态码由 `relay.retry_on_status` 白名单控制。
- `error_count` 熔断、基础限流和冷却窗口后的半开候选探测已进入 P1；后台探测任务、通道限流维度和完整熔断快照继续推进。

为什么这样选：

- P0 更容易证明“是否调用了下游、是否扣费、失败属于谁处理”。
- 避免默认重试导致多次下游请求、重复 usage、错误归因和账单解释复杂化。
- 商业级开箱体验首先需要稳定可解释，而不是一开始追求自动容灾。

代价：

- 默认配置下，单个上游临时故障不会自动换通道重试。
- 管理员需要显式设置 `relay.retry_count`，必要时调整 `relay.retry_on_status`，并继续通过多通道、监控、熔断和限流增强可用性。

### RXD-019：生产 readiness 严格化

默认设计：

- 开发/演示模式可以带警告降级启动。
- 生产模式缺少固定 `JWT_SECRET`、`ENCRYPTION_KEY` 或关键 settings 非法时，`/ready` 返回不就绪。

为什么这样选：

- 自部署项目最容易因为“看起来能启动”而带着错误密钥或不可解密通道进入生产流量。
- `/ready` 是阻止错误实例接流量的最后一道服务端边界。

代价：

- 生产部署需要更明确的密钥和配置管理。
- 小白用户在生产模式遇到 readiness 失败时，需要看到可读的修复路径。

### RXD-021：删除有限 API Key 的剩余额度

默认设计：

- 删除、禁用或过期有限 API Key 时，不发生用户余额变化。
- 未使用预算上限只是失效，不需要退回。
- 如果未来支持“转移 Key 未用预算到另一个 Key”，必须作为预算调整动作审计，不应写成用户余额入账。

为什么这样选：

- 新的 API Key 预算模型没有创建划拨，因此删除时也没有可退余额。
- 删除凭据和调整用户余额保持解耦，账本解释更简单。
- 审计只需要说明 Key 预算失效，不需要解释资金回流。

代价：

- 用户看到的是“Key 未用预算失效”，不是“余额损失”。
- 旧划拨模型迁移时需要单独定义存量 Key 的处理策略。

### RXD-022：失败调用默认不扣费

默认设计：

- 本地请求错误、鉴权失败、余额不足、访问控制拒绝、无可用通道且未产生有效 usage 时，默认不扣费。
- 下游已返回有效 usage、或流式已输出后需要补偿结算时，必须写入 usage 来源和日志快照。
- 如果未来启用失败最低成本，必须通过 settings 显式开启，并能在账单中解释。

为什么这样选：

- 小白用户能理解“没有成功调用就不消费额度”的默认行为。
- 管理员能通过失败日志判断是否调用过上游，而不是在账单里猜。
- P0 能把预检拒绝和上游调用后的失败清楚区分。

代价：

- 某些 provider 对失败请求也可能计费，P0 不覆盖这类成本回收。
- P1 需要更细的 usage 来源、流式补偿结算和失败成本配置。

### RXD-023：初始化启动额度

默认设计：

- `billing.bootstrap_admin_quota` 默认给超级管理员一笔启动额度，用于首次验证调用和管理员自测。
- 这不是正式运营赠送策略，也不代表普通注册用户默认有额度。
- 如果该值配置为 0，初始化或控制台能力必须给出明确额度调整路径。

为什么这样选：

- 开箱体验不能在创建第一个 API Key 或首次调用时被 0 额度卡死。
- 自部署管理员可以先验证通道、日志和扣费，再决定正式运营额度规则。
- 启动额度只影响第一个管理员，不把支付、充值码或商品系统提前带入 P0。

代价：

- 需要在文档和控制台文案中说明启动额度用途，避免被误解为赠送策略。
- 生产运营时应通过管理员额度调整、充值码、支付或人工补账建立正式额度来源。

### RXD-024：`channel_group` 优先于 `model_group`

默认设计：

- 当前计费、访问控制和路由优先使用 `channel_group` 表达套餐、通道分组、倍率和访问边界。
- 文档中提到模型分组时，优先解释为通道/模型分组目标能力，不在 P0 引入单独 `model_group` 实体。
- 如果未来需要独立 `model_group`，必须新增数据模型、迁移、策略顺序、价格规则和测试矩阵。

为什么这样选：

- 当前代码和数据模型已经有通道分组字段，能支撑 P0/P1 的访问控制和倍率设计。
- 过早引入独立模型分组会增加策略组合复杂度，影响小白开箱路径。
- 商业套餐通常可以先由通道分组和模型价格规则组合表达。

代价：

- 复杂运营场景下，模型分组和通道分组可能需要拆开。
- P2/P3 若引入独立模型分组，需要迁移历史规则并补充账单解释。

### RXD-025：默认分组

默认设计：

- 新用户默认归入 `default` 用户分组。
- 新通道默认归入 `default` 通道分组。
- 存量空分组或空 `channel_group` 在策略层归一为 `default`，管理端后续可提示管理员补齐显式值。

为什么这样选：

- 小白开箱不需要先理解套餐、分组和访问控制。
- P1 访问控制、倍率和路由偏好可以直接以 `default` 为稳定基线扩展。
- 集群缓存和路由预加载可以使用稳定分组 key，避免空值和 `default` 双语义。

代价：

- 如果数据库字段仍使用 `group_id`，需要保证存在 code 为 `default` 的用户分组记录，或在策略层把空值映射为 `default`。
- 管理员修改默认分组语义时，需要同步刷新通道路由和策略缓存。

### RXD-026：运行模式与 Redis

默认设计：

- 直接启动单个 Docker 镜像且不配置 `SQL_DSN` 时，RouterX 使用内置 SQLite，Redis 可省略。
- 一旦配置 PostgreSQL/MySQL 等外部数据库，或进入多实例/集群模式，必须配置可用 Redis。
- 不支持“只配置外部数据库但不配置 Redis”的生产形态；该状态应在启动或 `/ready` 中明确失败。

为什么这样选：

- SQLite 单机模式服务小白开箱，最少依赖。
- 外部数据库通常意味着更长期运行或多实例，API Key、settings、限流、通道候选缓存都需要跨实例一致性。
- Redis 缺失时继续运行会让缓存失效、限流和策略传播行为不可预测。

代价：

- 小规模生产部署必须同时准备数据库和 Redis。
- 开发测试需要覆盖 SQLite 单机和 DB+Redis 两条路径。

### RXD-027：独立日志数据库

默认设计：

- `LOG_SQL_DSN` 可选；为空时模型调用日志写入主数据库。
- 配置 `LOG_SQL_DSN` 时，高流量调用日志、诊断快照和可清理历史日志可写入独立日志数据库。
- 扣费事务所需的最小结算事实必须保留在主数据库同事务内，或先写主库 outbox，再异步投递到日志数据库。
- 当前实现会启动期初始化独立日志库的 `logs` schema，并在每次记录时先写主库完整调用事实和 `log_replication_outboxes`，再写入日志库副本；运行期日志库写失败不会删除主库事实，后台 worker 会在恢复后重放 pending outbox。

为什么这样选：

- 日志数据库可独立备份、压缩、归档和清理，不拖慢主业务库。
- 日志库故障不应导致已经扣费的调用完全失去结算证据。
- 主库和日志库无法天然保证跨库事务，必须明确账单权威边界。

代价：

- LogService 需要维护主库和日志库两条连接语义，并负责 outbox 状态更新、异步投递和重放。
- 查询日志时需要知道当前实例是否启用独立日志库；当前管理日志列表、清理和看板今日调用/额度会优先使用日志库，列表查询失败时回退主库事实。

### RXD-028：通道候选预加载和缓存

默认设计：

- 通道、模型、通道分组、用户分组访问规则和价格可用性应形成可预加载的路由索引。
- 单机 SQLite 模式可使用进程内缓存；DB+Redis 模式应使用 Redis 保存版本、失效信号或共享快照。
- 管理员修改通道、模型、分组、settings 或价格规则后，必须递增版本并广播失效，集群实例不能长期使用旧候选集。

为什么这样选：

- 每次请求直接全表查通道和解析模型配置成本高，影响热路径延迟。
- 预加载后可按 model、APIType、user_group、channel_group 和版本快速筛选候选通道。
- 版本号和 Redis 广播能让多实例在性能和一致性之间取得可解释平衡。

代价：

- 缓存 key 设计需要控制基数，避免按用户或 Key 生成过多路由缓存。
- API Key scope 等高基数收窄规则更适合在预加载候选集之后做轻量过滤。

## 变更规则

修改上述决策时，需要同步检查：

- 主设计稿是否仍然一致：`docs/DESIGN.md`
- 具体接口和错误格式是否仍然一致：`docs/API.md`
- 数据模型和迁移目标是否仍然一致：`docs/DATA_MODEL.md`
- Relay 行为是否仍然一致：`docs/RELAY.md`
- 计费口径是否仍然一致：`docs/BILLING.md`
- settings 注册表是否仍然一致：`docs/SETTINGS.md`
- 策略和协议矩阵是否仍然一致：`docs/POLICIES.md`、`docs/PROTOCOLS.md`
- 验收门禁是否仍然一致：`docs/ACCEPTANCE.md`
- 测试合同是否仍然一致：`docs/TESTING.md`
- 实现交接清单是否仍然一致：`docs/IMPLEMENTATION.md`

每次修改决策后，至少运行：

```bash
go test ./...
git diff --check
```
