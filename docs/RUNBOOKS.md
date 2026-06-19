# RouterX 故障处理 Runbooks

本文档面向小白用户、站长、运维和后续实现者，回答“出问题时先看什么、怎么判断责任、能安全做什么”。它不是部署教程，也不设计网页页面；控制台或接口只需要承载这里定义的排查信息、证据和动作。

原则：

- 错误 code、HTTP 状态、重试和扣费语义以 `docs/ERRORS.md` 为准。
- 协议兼容、APIType、上游能力等级和字段降级以 `docs/PROTOCOLS.md` 为准。
- 日志、审计、指标、告警和保留规则以 `docs/OBSERVABILITY.md` 为准。
- 调用事实快照、脱敏和历史解释以 `docs/SNAPSHOTS.md` 为准。
- 密钥、权限、支付回调和事故边界以 `docs/SECURITY.md` 为准。
- 支付 provider、充值码、退款、人工补账和额度流水以 `docs/PAYMENTS.md` 为准。
- 运行模式、密钥、迁移、备份和发布检查以 `docs/OPERATIONS.md` 为准。
- 小白路径以 `docs/FLOWS.md` 为准；本文把路径中的失败点展开成可执行排查步骤。

## 1. 排查总原则

RouterX 的商业级故障处理不追求“把所有错误包装成友好提示”，而是让每个失败都有稳定归因、证据链和安全处理动作。

排查顺序：

1. 看入口：请求进了 `/v0`、`/v1`、`/ready` 还是支付 webhook。
2. 看错误 code：不要只看 HTTP 状态；同一个 502 可以是通道不可用、密钥不可解密或上游临时故障。
3. 看是否调用上游：没有调用上游时不应产生模型消费扣费。
4. 看日志事实：模型调用日志、管理审计日志、HTTP 访问日志和指标要能互相解释。
5. 看配置来源：业务运行时配置看 `settings`，启动密钥和外部密钥看环境变量或 KMS。
6. 看安全边界：任何处理动作都不能泄露 API Key、下游密钥、支付密钥、数据库 DSN 或完整请求体。

严重级别建议：

| 级别 | 用户影响 | 示例 | 默认响应 |
|------|----------|------|----------|
| S0 | 多数请求失败或账单不可信 | DB 不可用、计费事务异常、生产密钥丢失 | 停止接收真实流量，优先保护账单和密钥 |
| S1 | 核心路径大面积异常 | `/v1` 大量 502、所有通道不可用、`/ready` 不就绪 | 降级或切换通道，保留证据 |
| S2 | 单用户或单通道异常 | 单个 Token 余额不足、某个上游 401 | 修正配置或额度 |
| S3 | 可解释的使用错误 | 不支持的流式入口/通道、模型名不存在 | 返回明确提示，不告警 |

## 2. 必备证据

每个 Runbook 的处理动作都应尽量绑定证据，避免凭感觉修改配置。

| 证据 | 当前或目标来源 | 用途 |
|------|----------------|------|
| `request_id` | HTTP 日志、模型调用日志、错误响应目标字段 | 串联一次请求 |
| `error_code` | `/v1` 协议兼容错误、`/v0` 统一错误 | 判断责任和动作 |
| `user_id` / `token_id` | API Key 鉴权、模型调用日志 | 判断用户、Token 和额度 |
| `channel_id` / `provider` | 路由选择、通道日志 | 判断通道和上游 |
| `model` | 请求体、路由日志、账单日志 | 判断模型匹配和计费 |
| `quota_used` / `usage` | 模型调用日志、账单记录 | 判断扣费是否一致 |
| `route_snapshot` | 目标模型调用日志，字段语义见 `docs/SNAPSHOTS.md` | 复盘当时路由规则 |
| `settings_version` | 目标配置审计 | 判断配置变更是否影响故障 |
| webhook event id | 支付事件表 | 判断支付回调幂等 |

证据保护：

- 用户 API Key 明文只出现一次，排查时只能使用脱敏摘要或哈希匹配。
- 下游 API Key 不得出现在日志、错误响应、导出文件和管理接口响应中。
- 请求和响应 body 默认不记录；开启时必须按上限截断并脱敏。
- 支付 provider 返回体和签名头属于敏感排查材料，保留摘要即可。

## 3. 小白开箱路径 Runbooks

### RB-001 `/ready` 不就绪

症状：

- `GET /ready` 返回 `503` 或 `status=not_ready`。
- 控制台或接口提示服务未就绪。

用户含义：

- 服务进程可能还活着，但不适合接收真实模型调用。

检查顺序：

1. 看返回字段：是 `database`、`jwt`、迁移、Redis、`ENCRYPTION_KEY` 还是 settings。
2. 如果是数据库：确认 `SQL_DSN` 指向正确环境，迁移没有 dirty 状态。
3. 如果是 JWT：生产和多实例必须固定 `JWT_SECRET` 或可用的数据库 `jwt.secret`。
4. 如果是密钥：生产必须固定 `ENCRYPTION_KEY` 或 KMS，且能解密已有 `enc:v1:` 密文。
5. 如果是 settings：检查 settings 注册表默认值、类型和 Redis 缓存刷新。

安全动作：

- 生产环境不要绕过 `/ready` 接真实流量。
- 开发/演示可以允许最小启动，但必须在响应和日志中给出明确风险。

验收信号：

- `/health` 正常表示进程活着。
- `/ready` 正常表示实例可以接流量。
- 关键配置缺失时生产 `/ready` 不就绪。

### RB-002 初始化失败

症状：

- 首次 `POST /v0/setup/init` 失败。
- 重复初始化被拒绝。

用户含义：

- 系统还没有完成超级管理员和默认 settings 的创建，或系统已经初始化过。

检查顺序：

1. 调用 `GET /v0/setup/status` 看 `initialized`。
2. 未初始化时检查用户名、密码、邮箱格式和数据库写入错误。
3. 已初始化时不要再次创建超级管理员；走登录、找回或管理员账号恢复流程。
4. 检查初始化是否写入默认 settings，尤其是 `jwt.secret`、限流、Relay 和日志默认值。

安全动作：

- 不允许未授权请求在已初始化后再次创建管理员。
- 不把初始化失败的数据库错误原样暴露给用户。

验收信号：

- 空库只能初始化一次。
- 初始化后可以登录管理员并签发 JWT。
- 默认 settings 可查询、类型正确、不会覆盖已有管理员配置。

### RB-003 第一次模型调用返回 `invalid_api_key`

症状：

- `/v1/chat/completions` 返回 401。
- code 为 `invalid_api_key`。

用户含义：

- 请求没有带可用的 RouterX 用户 API Key。

检查顺序：

1. 确认请求头是 `Authorization: Bearer sk-...`。
2. 确认使用的是用户 API Key，不是管理员登录 JWT，也不是下游厂商 API Key。
3. 确认 API Key 没被禁用、删除、过期或复制时缺字符。
4. 确认 API Key 明文只在创建时展示；忘记后只能新建或轮换。

安全动作：

- 不在日志中打印完整 `Authorization`。
- 不提供“找回 API Key 明文”能力。

验收信号：

- 无效 Key 不调用上游。
- 无效 Key 不扣费。
- 错误响应保持入口协议兼容。

### RB-004 第一次模型调用返回 `insufficient_quota`

症状：

- `/v1/chat/completions` 返回 429。
- code 为 `insufficient_quota`。

用户含义：

- 用户额度或当前 API Key 预算不足。

检查顺序：

1. 看 API Key 是有限额度还是无限额度。
2. 有限额度 API Key：同时检查用户总额度和 Key 剩余预算是否足够。
3. 无限额度 API Key：检查用户总额度是否足够。
4. 检查用户是否被禁用，或管理员是否调整过额度。
5. 看模型调用日志是否存在；预检失败不应调用上游，也不应产生模型消费扣费。

安全动作：

- 普通用户不能自己提高额度或把有限 Key 改成无限。
- 管理员调整额度需要审计。

验收信号：

- 预检余额不足时上游调用次数为 0。
- 成功调用后 `quota_used`、用户账单和 Token 余额一致。

### RB-005 返回 `unsupported_stream` / `unsupported_stream_channel` / `unsupported_api_type`

症状：

- 请求体包含 `stream=true`。
- 当前入口或 APIType 未开放流式时，返回 400，code 为 `unsupported_stream`。
- OpenAI Chat 流式请求命中非 OpenAI SSE 通道时，返回 502，code 为 `unsupported_stream_channel`。
- 选中通道 adapter 尚未支持该 APIType 时，返回 502，code 为 `unsupported_api_type`。

用户含义：

- OpenAI Chat 基础 SSE 只支持 OpenAI-compatible SSE 形态通道。
- Anthropic/Gemini 原生流式转换、断开取消断言和 usage 兜底仍属于 P1 增强。
- 某些 provider 端点需要逐项确认，不能把未确认 APIType 伪装成可用上游。

检查顺序：

1. 确认客户端 SDK 或业务代码是否默认打开 streaming。
2. 如果需要 OpenAI Chat 流式，确认路由命中 OpenAI、OpenAI-compatible、xAI、Qwen、DeepSeek 或 RouterX-compatible 通道。
3. 如果当前只能使用 Anthropic/Gemini 原生流式，临时改为非流式请求，直到对应 chunk 转换器落地。
4. 如果 code 为 `unsupported_api_type`，检查 `docs/PROTOCOLS.md` 中该 provider 与 APIType 的能力等级，或把请求路由到支持该 APIType 的通道。

安全动作：

- 不应该把非 OpenAI SSE 的上游流伪装成 OpenAI SSE。
- 非 OpenAI SSE 通道被拒绝时，不调用上游，避免误扣费和误导用户。
- adapter 明确不支持的 APIType 被拒绝时，不调用上游，不扣费，日志应归因到通道能力。

验收信号：

- OpenAI Chat 基础流式测试通过，能透传 SSE 并按 usage 扣费。
- 非 OpenAI SSE 通道拒绝测试通过，不写成功账单，不扣费。
- `unsupported_api_type` 回归测试通过，不调用上游、不扣费，并写失败日志。

### RB-006 返回 `no_available_channel`

症状：

- `/v1` 返回 502。
- code 为 `no_available_channel`。

用户含义：

- RouterX 没有找到可用于该模型的通道。

检查顺序：

1. 检查是否存在启用的通道。
2. 检查通道 `models` 是否包含请求模型，或模型重写规则是否正确。
3. 检查 `docs/PROTOCOLS.md` 中该入口协议、APIType 和 provider 组合是否达到可用等级，再检查 provider adapter 是否支持该请求类型。
4. 检查通道 `error_count` 是否超过熔断阈值。
5. 检查用户分组、模型分组、`channel_group` 和 `routerx.route` 是否把候选通道过滤掉。
6. 检查优先级和权重是否导致只有不可用通道被选中。

安全动作：

- 不把内部通道列表完整暴露给普通用户。
- 管理员视图可展示候选过滤摘要，不展示密钥明文。

验收信号：

- 无候选通道不调用上游。
- 失败日志能解释模型、过滤原因和候选摘要。

### RB-007 返回 `upstream_secret_error`

症状：

- `/v1` 返回 502。
- code 为 `upstream_secret_error`。

用户含义：

- 选中的通道缺少可用下游密钥，或密钥无法解密。

检查顺序：

1. 检查通道是否配置了 `api_key`、`api_keys` 或 `upstreams[].api_key`。
2. 检查生产 `ENCRYPTION_KEY` 或 KMS 是否可用。
3. 检查是否把加密密文迁移到了无法解密的新实例。
4. 检查密钥轮换是否只更新了一部分实例或一部分通道。

安全动作：

- 不在错误响应中返回解密异常细节。
- 不在日志中写下游密钥明文。
- 无法恢复 `ENCRYPTION_KEY` 时，只能重新配置受影响通道密钥。

验收信号：

- 通道响应只返回脱敏摘要。
- 解密失败不调用上游，不扣费。

## 4. 管理员和技术用户 Runbooks

### RB-101 下游返回 401 或 403

症状：

- `/v1` 返回上游错误映射。
- code 可能是 `upstream_401`、`upstream_403` 或当前兼容期的上游错误 code。

含义：

- 多数情况下是通道密钥、base URL、provider 配置或权限不正确。

检查顺序：

1. 确认 `channel_id`、provider、base URL 和模型。
2. 检查下游 API Key 是否有效、是否具备模型权限。
3. 检查 base URL 是否使用了正确协议、路径和地区。
4. 检查模型重写后传给上游的模型名。
5. 检查是否把 OpenAI-compatible Key 配到了非兼容 provider。

安全动作：

- 401/403 不自动重试其他通道，避免错误密钥造成异常放大。
- 记录通道配置错误摘要，通知管理员处理。

验收信号：

- 用户能看到兼容协议错误。
- 管理员能看到通道配置归因。
- 失败不扣模型消费额度，除非上游返回了可信 usage 且计费规则明确允许。

### RB-102 下游返回 429

症状：

- 上游限流、余额不足或 provider 配额不足。

检查顺序：

1. 区分 RouterX 本地 `rate_limit_exceeded` 和上游 `upstream_429`。
2. 检查上游账号余额、RPM/TPM 限制和地区限制。
3. 检查是否需要临时降低该通道权重或禁用通道。
4. P1 后检查非流式请求是否可以安全切换其他候选通道。

安全动作：

- 不把上游账号余额细节返回给普通用户。
- 不因上游 429 扣本地模型消费额度，除非有可信 usage。

验收信号：

- 本地限流和上游限流 code 可区分。
- 通道健康指标能体现限流增长。

### RB-102A 本地返回 `rate_limit_unavailable`

症状：

- `/v1/*` 返回 503。
- OpenAI-compatible code 为 `rate_limit_unavailable`，Anthropic/Gemini 为对应协议的服务不可用外形。

检查顺序：

1. 检查当前是否为外部数据库或集群模式；SQLite 单镜像通常可降级运行。
2. 检查 `/ready` 是否因 Redis 返回 not ready。
3. 检查 `routerx_redis_errors_total{operation="rate_limit_required|rate_limit_incr|rate_limit_expire"}`。
4. 恢复 Redis 连接后重试同一请求。

验收信号：

- 故障期间请求未调用上游、未扣模型消费额度。
- 调用日志写入 `error_code=rate_limit_unavailable`，且 `policy_snapshot.rate_limit_snapshot.dependency=redis`。

### RB-103 下游 5xx 或超时

症状：

- code 为 `upstream_5xx`、`upstream_timeout` 或当前兼容期的上游请求失败 code。

检查顺序：

1. 检查下游状态页、网络连通性、DNS 和代理配置。
2. 检查 `relay.timeout` 是否过短。
3. 检查是否只有单个通道异常，还是所有 provider 异常。
4. 检查 `relay.retry_count` 是否仍为 `0`；大于 0 时非流式会按候选通道进行有限重试。
5. 检查 `relay.retry_on_status` 是否包含该上游 HTTP 状态码；默认只包含 429/500/502/503/504。
6. 确认失败是否属于可重试错误；400/401/403 默认不重试，流式写出后不能跨通道切换。

安全动作：

- 不无限重试。
- 不把下游原始错误体完整返回给用户。
- 超时后如果没有可信 usage，不扣费。

验收信号：

- 超时和 5xx 可观测。
- 非流式重试会留下失败尝试日志和最终成功/失败日志，最终通道和失败原因可追踪。

### RB-104 账单和日志不一致

症状：

- 用户看到额度减少，但调用日志缺失。
- 调用日志有成功记录，但账单汇总不一致。
- `quota_used` 与 `usage.total_tokens` 或倍率计算不一致。

检查顺序：

1. 用 `request_id` 或时间窗口找到模型调用日志。
2. 检查 `usage` 来源：上游返回、估算、最小扣费还是人工修正。
3. 检查扣费口径：有限 API Key 同时扣用户额度和 Key 预算，无限 API Key 只扣用户额度。
4. 检查扣费和日志是否在同一事务内完成。
5. 检查是否有并发请求导致余额预检和实际扣费之间出现竞争。
6. 检查管理员人工调整额度是否有审计记录。

安全动作：

- 账单不可解释时，宁可拒绝继续产生新消费，也不要继续生成不可追溯账单。
- 人工修正必须写审计，保留修正前后摘要和原因。

验收信号：

- 每条成功消费都能从调用日志追到用户或 Token 余额变化。
- 失败且未调用上游的请求不扣费。
- 汇总账单能由明细日志重算。

### RB-105 `routerx.route` 没按预期生效

症状：

- 技术用户传了 `routerx.route`，但通道选择和预期不同。

检查顺序：

1. 确认 `routerx` 是保留扩展参数，不会透传到不认识该字段的上游。
2. 检查管理员策略是否禁止用户指定通道或分组。
3. 检查用户分组、模型分组、通道分组是否允许该通道。
4. 检查模型名重写前后的匹配规则。
5. 检查通道优先级、权重和熔断状态。

安全动作：

- 后端策略高于用户偏好；用户偏好不能越权。
- 拒绝越权时返回稳定错误 code，并写路由过滤摘要。
- 具体策略决策顺序和冲突规则以 `docs/POLICIES.md` 为准。

验收信号：

- 日志中有 route preference 和最终 route snapshot。
- 被拒绝的偏好有明确原因，不泄露其他租户信息。

## 5. 生产运维 Runbooks

### RB-201 Redis 不可用或缺失

症状：

- settings 缓存、限流、会话或短期状态异常。
- 指标出现 Redis 错误增长。

检查顺序：

1. 判断当前模式：SQLite 单镜像、外部数据库、集群。
2. 检查 Redis 连接字符串、认证、网络、容量和慢查询。
3. 确认 settings 读取是否能退回 DB。
4. 确认限流是 fail-open 还是 fail-closed，特别是登录、注册、验证码和 `/v1`。

安全动作：

- SQLite 单镜像可降级到进程内缓存，但要有告警。
- 外部数据库或集群模式缺 Redis 时应不就绪，不继续接收真实流量。
- 不因为 Redis 失败覆盖 DB 中的 settings。

验收信号：

- Redis 恢复后缓存能重新加载。
- `/ready` 在外部数据库或集群模式下能反映 Redis 风险。

### RB-202 数据库迁移失败

症状：

- 服务启动失败。
- 迁移版本 dirty 或 schema 不符合代码预期。

检查顺序：

1. 停止滚动发布，避免多个实例继续争抢迁移。
2. 查看迁移版本、dirty 状态和失败 SQL。
3. 确认是否有备份或 staging 验证记录。
4. 判断是 SQL 方言差异、权限不足、数据冲突还是迁移顺序错误。

安全动作：

- 不直接手工改业务表绕过迁移记录。
- 修复前不要让新版本接收真实流量。
- down 迁移涉及数据丢失时必须人工确认。

验收信号：

- 迁移状态干净。
- `/ready` 正常。
- 关键接口和 P0 后端测试通过。

### RB-203 API Key 泄露

症状：

- 用户发现 API Key 出现在外部仓库、日志、工单或异常流量中。

检查顺序：

1. 立即禁用或删除泄露 Token。
2. 查询最近使用时间、IP、模型、用量和错误峰值。
3. 判断是否只影响一个用户 Token，还是管理端或日志系统泄露。
4. 通知用户创建新 Key，并检查业务侧配置。

安全动作：

- 不尝试恢复旧 Key 明文。
- 不把泄露 Key 再次写入工单或日志。
- 如果泄露来自系统日志，按安全事故处理并修复脱敏规则。

验收信号：

- 旧 Key 无法继续调用。
- 新 Key 可正常调用。
- 泄露窗口内的调用和扣费能被审计。

### RB-204 `ENCRYPTION_KEY` 丢失或错误

症状：

- 已有通道密钥无法解密。
- 大量 `upstream_secret_error`。
- 新实例无法使用历史配置。

检查顺序：

1. 确认所有实例是否使用同一个 `ENCRYPTION_KEY` 或 KMS。
2. 检查密钥是否被轮换但没有完成重加密。
3. 判断是否还能从密钥管理系统或备份恢复。
4. 统计受影响的 `enc:v1:` 密文数量。

安全动作：

- 无法恢复主密钥时，不能解密历史下游密钥，只能逐通道重新配置。
- 暂停使用受影响通道，避免持续 502。
- 记录事故时间、影响范围和重新配置动作。

验收信号：

- 受影响通道密钥重配后恢复。
- `/ready` 在生产模式能检查密钥可用性。
- 管理响应和日志仍不泄露密钥。

### RB-205 支付回调失败或重复

症状：

- 用户支付成功但未到账。
- 支付 provider 多次回调。
- 回调签名失败、金额不匹配或订单状态异常。

检查顺序：

1. 查看 payment order、payment event 和 webhook event id。
2. 校验 provider 签名，不信任客户端提交的金额、额度和商品信息。
3. 检查订单金额、币种、商品、状态和用户归属。
4. 检查是否已经入账，重复回调必须幂等。
5. 检查支付密钥环境变量或 KMS 配置。

安全动作：

- 签名失败、金额不匹配、订单不匹配时拒绝入账，只记录事件。
- 重复成功回调不能重复加额度。
- 人工补账必须写审计。

验收信号：

- 每个支付事件有唯一幂等记录。
- 订单、事件、额度变动和审计能串联。
- 支付失败不影响基础手工额度运营。

## 6. 发布和回滚 Runbook

发布前：

1. 备份数据库。
2. 在 staging 执行迁移和 P0 后端测试。
3. 确认生产 `JWT_SECRET`、`ENCRYPTION_KEY`、`SQL_DSN`、可选 `LOG_SQL_DSN`、`REDIS_CONN` 和支付密钥策略。
4. 确认 `/health`、`/ready`、模型调用、日志、额度扣减和管理登录。
5. 确认日志没有 API Key、下游密钥、Cookie、DSN 和完整敏感 body。

发布中：

1. 迁移由单独 Job 或首个实例执行。
2. 迁移完成后再滚动业务实例。
3. 观察 `/ready`、错误率、延迟、通道健康和 Redis/DB 错误。
4. 一旦出现计费不可解释或密钥不可解密，停止继续放量。

回滚：

1. 应用回滚不等于数据库回滚。
2. 如果迁移包含不可逆数据变化，先评估数据恢复方案。
3. 回滚后重新校验 `/ready`、登录、API Key、通道调用和账单一致性。

## 7. Runbook 到验收的映射

| Runbook | 对应能力 | 主要文档 | 建议测试 |
|---------|----------|----------|----------|
| RB-001 | 生产 readiness | `SETTINGS`、`OPERATIONS`、`ARCHITECTURE` | `TestSettingsValidationAndReadiness`、`TestReadinessRequiresEncryptionKeyForEncryptedChannelSecrets` |
| RB-002 | 初始化闭环 | `DESIGN`、`FLOWS`、`API` | `TestSetupBootstrapAdminQuota` |
| RB-003 | API Key 鉴权 | `API_KEYS`、`ACCOUNTS`、`SECURITY`、`API` | `TestRelayPrecheckRejectsBeforeUpstream` |
| RB-004 | 额度预检 | `BILLING`、`ERRORS`、`RELAY` | `TestRelayPrecheckRejectsBeforeUpstream` |
| RB-005 | 流式边界 | `RELAY`、`ERRORS` | `TestChatCompletionStreamForwardsSSEAndDeductsUsage`、`TestChatCompletionStreamRejectsNonOpenAISSEUpstream` |
| RB-006 | 通道路由 | `RELAY`、`DATA_MODEL` | `TestChannelRoutingConfigResolution` |
| RB-007 | 下游密钥安全 | `SECURITY`、`DATA_MODEL` | `TestChannelExtendedManagement` |
| RB-101 | 上游配置错误 | `ERRORS`、`RELAY` | `TestChatCompletionUpstreamErrorMapping` |
| RB-104 | 账单一致性 | `BILLING`、`OBSERVABILITY` | `TestUserBillingMatchesLogs` |
| RB-105 | 技术用户路由偏好 | `POLICIES`、`API`、`RELAY`、`SECURITY` | route preference 权限测试 |
| RB-201 | Redis 运行模式 | `OPERATIONS`、`SETTINGS` | Redis 缺失/故障注入测试 |
| RB-203 | API Key 泄露 | `API_KEYS`、`SECURITY`、`OBSERVABILITY` | API Key 泄露演练 |
| RB-205 | 支付回调 | `PAYMENTS`、`BILLING`、`SECURITY`、`API` | 支付幂等测试 |

## 8. 实现要求

后续实现每新增一个错误 code、入口协议、APIType、外部 provider、支付 provider、配置分类或账单规则，都必须检查：

- `docs/ERRORS.md` 是否需要新增错误语义。
- `docs/PROTOCOLS.md` 是否需要新增能力等级、字段降级或 SDK 兼容矩阵。
- `docs/OBSERVABILITY.md` 是否需要新增日志字段、指标或告警。
- `docs/SNAPSHOTS.md` 是否需要新增或调整调用事实快照字段。
- `docs/SECURITY.md` 是否需要新增威胁和控制点。
- `docs/RUNBOOKS.md` 是否需要新增或更新排查步骤。
- `docs/TRACEABILITY.md` 是否需要新增验收映射。

商业级默认不是“永不出错”，而是出错时用户知道下一步，管理员知道证据在哪，实现者知道必须补哪条测试。
