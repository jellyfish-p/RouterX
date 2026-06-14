# RouterX 商业级验收门禁

## 目标

本文档定义 RouterX 在不同阶段可以宣称“完成”的最低证据门槛。它不是新的产品设计来源，而是把主设计稿、实现交接、测试设计和能力追踪矩阵收束成可执行的验收闸门。

适用范围：

- 判断 P0 开箱路径是否真正闭环。
- 判断一次实现交付是否可以进入下一阶段。
- 判断文档、代码、测试和运行证据是否互相一致。
- 防止把“已有路由”“基础实现”“目标设计”误写成已经可商用。

不在本文档范围：

- 不设计网页页面。
- 不展开部署教程。
- 不替代 `docs/TESTING.md` 中的具体测试用例。
- 不替代 `docs/TRACEABILITY.md` 中的能力 ID。

## 权威来源

验收时按以下文档分工判断：

| 文档 | 用途 |
|------|------|
| `docs/DESIGN.md` | 判断产品定位、阶段边界和商业级默认体验是否被满足。 |
| `docs/DECISIONS.md` | 判断关键技术取舍、Confirm 项和确认窗口是否已按阶段处理。 |
| `docs/FLOWS.md` | 判断小白开箱、站长管理、技术进阶和运营路径是否可走通。 |
| `docs/PROTOCOLS.md` | 判断入口协议、APIType、上游厂商、能力等级和字段降级是否可承诺。 |
| `docs/POLICIES.md` | 判断访问控制、作用域、分组、限流和路由偏好是否不能越权。 |
| `docs/SNAPSHOTS.md` | 判断一次调用的路由、策略、usage、计费、错误和 settings 事实是否可解释。 |
| `docs/MODULE_BOUNDARIES.md` | 判断实现是否遵守 Handler、Service、Adapter、日志、计费和 settings 的责任边界。 |
| `docs/IMPLEMENTATION.md` | 判断实现顺序、文件落点、禁止事项和交接边界是否被遵守。 |
| `docs/TESTING.md` | 判断测试 fixture、断言和验证命令是否覆盖目标能力。 |
| `docs/TRACEABILITY.md` | 判断每项能力是否有文档来源、落地位置和验收证据。 |
| `docs/RUNBOOKS.md` | 判断失败路径是否有可执行排障动作。 |

## 证据等级

验收证据按可信度排序：

| 等级 | 证据 | 说明 |
|------|------|------|
| E1 | 自动化测试通过 | 优先使用本地 fixture、本地下游桩和隔离数据库。 |
| E2 | 接口响应 + 数据库状态 | 同时证明 HTTP 行为、落库事实和权限边界。 |
| E3 | 日志、审计和指标 | 证明请求链路、错误来源、计费和排障事实可解释。 |
| E4 | 文档一致性扫描 | 证明术语、阶段和旧状态描述没有冲突。 |
| E5 | 手工验证记录 | 只能作为补充，不能替代关键自动化测试。 |

商业级能力不能只靠“接口返回 200”证明。涉及鉴权、计费、安全、路由和协议兼容的能力，至少需要 E1 或 E2。

## P0 开箱验收门

P0 可以宣称完成时，必须同时满足以下门禁。

| 门禁 | 必须证明 | 主要证据 |
|------|----------|----------|
| G0 文档一致 | 主设计稿、路线图、API、数据模型、Relay、计费、测试不互相冲突。 | 旧术语扫描、`git diff --check`、文档链接检查。 |
| G1 空库初始化 | 空库能创建超级管理员、默认 settings 和本地身份。 | `TestSetupBootstrapAdminQuota`、`TestP0BackendFlow`。 |
| G2 就绪状态 | `/ready` 能反映数据库、初始化、JWT 和关键配置状态。 | `TestSettingsRegistryAndReadiness`、ready 接口断言。 |
| G3 账号和权限 | 普通用户、管理员、超级管理员边界正确。 | `TestAdminPrivilegeBoundaries`。 |
| G4 API Key 安全 | API Key 明文只返回一次，数据库保存哈希，禁用/过期/删除立即生效。 | `TestP0BackendFlow`、API Key 列表脱敏断言。 |
| G5 通道安全 | 管理员能创建可用通道，下游密钥加密或脱敏，响应和日志不泄露密钥。 | `TestChannelExtendedManagement`、敏感词扫描。 |
| G6 模型列表 | 有效 API Key 可调用 `/v1/models`，无效或禁用凭据失败。 | `TestP0BackendFlow`、OpenAI-compatible 错误断言。 |
| G7 Chat 非流式 | `/v1/chat/completions` 非流式成功返回兼容响应，写日志并扣费。 | `TestChatCompletionSuccessLogsAndDeductsQuota`。 |
| G8 预检拒绝 | 无效 Key、禁用用户、禁用 API Key、余额不足、禁用通道不调用上游。 | `TestRelayPrecheckRejectsBeforeUpstream`。 |
| G9 错误兼容 | 非法 JSON、缺少 model、未支持流式通道、无通道和下游错误返回稳定 code。 | `TestChatCompletionInvalidRequestDoesNotCallUpstream`、流式通道拒绝测试、`TestChatCompletionUpstreamErrorMapping`。 |
| G10 日志账单一致 | 成功调用的 usage、`quota_used`、用户余额、Key 预算和账单聚合一致。 | `TestUserBillingMatchesLogs`。 |
| G11 开箱不被高级能力阻塞 | 支付、OAuth/OIDC、流式、多协议完整矩阵和高级价格表达式未配置时，不影响 P0。 | P0 测试环境不依赖这些模块。 |

如果 G1-G11 任一项缺失，文档可以说“已有基础”或“目标设计”，不能说 P0 商业级闭环完成。

## P0 不可接受状态

出现以下任一情况时，不能通过 P0 验收：

- 成功响应没有写调用日志。
- 扣费成功但日志失败，或日志成功但扣费失败且无补偿策略。
- 余额不足、权限不足或请求非法时仍调用上游。
- API Key、下游密钥、数据库 DSN、支付密钥或内部堆栈进入响应或日志。
- `/v1` 错误返回 RouterX 管理端 `{success,data,message}` 包装。
- 不支持的流式入口或通道被静默转发或静默忽略。
- 通道选择依赖不可解释的随机组合，例如把 `upstreams` 与外层 `api_keys`、`base_urls` 任意交叉。
- 文档把已注册高级路由写成完整兼容。

## P1 验收门

P1 可以宣称完成时，必须在 P0 全部通过后再满足：

| 门禁 | 必须证明 | 主要证据 |
|------|----------|----------|
| G12 流式 | SSE/chunk 不缓存完整响应，客户端断开取消下游，流式 usage 可结算或估算。 | OpenAI Chat 基础 SSE 测试、流式集成测试、断开取消测试。 |
| G13 多入口协议 | OpenAI、Anthropic、Gemini 基础入口成功和错误外形分别兼容。 | 基础非流式测试、协议矩阵测试、SDK 行为断言。 |
| G14 多上游转换 | 主要上游 provider 的请求、响应、usage 和错误映射符合能力等级。 | `docs/PROTOCOLS.md` 对应矩阵测试。 |
| G15 路由偏好 | `routerx.route` 合法、非法、越权和无候选路径都有稳定行为。 | 路由策略测试、调用事实快照。 |
| G16 访问控制 | API Key scope、用户分组、通道分组、来源 IP、方法路径、日预算和模型/APIType 权限只能收窄，不能放大权限。 | `TestAPIKeyModelScopeRestrictsRelayBeforeUpstream`、`TestAPIKeyAPIScopeRestrictsRelayBeforeUpstream`、`TestAPIKeyChannelGroupScopeFiltersRelayCandidates`、`TestAPIKeyIPScopeRejectsBeforeRelay`、`TestAPIKeyMethodScopeRejectsBeforeRelay`、`TestAPIKeyDailyQuotaScopeRejectsAfterDailyBudgetUsed`、访问允许/拒绝测试。 |
| G17 可靠性 | 非流式安全重试、熔断、限流和半开恢复可解释。 | 故障注入测试、指标和 Runbook。 |
| G18 计费规则 | 价格表达式、倍率、规则快照和历史账单解释一致。 | 计费事实链和调用事实快照测试。 |
| G19 运行模式 | SQLite 单镜像可无 Redis；外部数据库或集群模式必须 Redis 可用。 | readiness、启动模式和 Redis 故障测试。 |
| G20 热路径缓存和日志库 | 通道候选缓存可失效，独立日志库不破坏账单最小事实。 | 缓存版本测试、`LOG_SQL_DSN` 降级测试。 |

P1 新增任何协议、APIType 或 provider 时，必须先更新 `docs/PROTOCOLS.md`，再更新 API、Relay、错误、测试、Runbook 和追踪矩阵。

## P2 验收门

P2 可以宣称完成时，必须证明生产和企业增强不破坏 P0/P1：

| 门禁 | 必须证明 | 主要证据 |
|------|----------|----------|
| G19 企业身份安全 | OAuth/OIDC 不因 email 自动接管账号，身份绑定和恢复可审计。 | 企业身份绑定和恢复测试。 |
| G20 管理审计 | settings、通道、用户、额度、价格和支付关键操作可追溯。 | 审计日志测试。 |
| G21 支付幂等 | Stripe/易支付/充值码/人工补账不会重复入账，金额和签名可信。 | 支付 provider fixture、幂等测试。 |
| G22 密钥轮换 | API Key、下游密钥、`ENCRYPTION_KEY` 或 KMS 轮换有兼容和恢复策略。 | 轮换测试、脱敏扫描。 |
| G23 生产观测 | Request ID、结构化日志、Prometheus 指标、告警和生产 `/ready` 可用。 | readiness/metrics 测试、指标字段断言。 |
| G24 高级 API | Responses、Images、Audio、Moderations 等按能力矩阵打开，不影响基础 Chat。 | 高级 API 安全和协议测试。 |

## 交付前检查

每次实现或文档交付前，至少执行：

```text
go test ./...
git diff --check
rg -n "<legacy-or-placeholder-pattern>" README.md docs
rg -n "<legacy-routing-term>" README.md docs
```

检查规则：

- 第一条搜索用于查旧配置名、旧字段名、旧 API Key 明文字段说法、空泛填充描述和不确定状态描述；应无命中，除非命中内容是刻意保留的代码事实说明。
- 第二条搜索用于确认模型路由实体没有使用旧称；该旧称只能出现在支付语境或术语表解释中。
- `git diff --check` 允许 Windows 换行提示，但不能有空白错误。
- 测试失败时不能宣称验收通过。

## 验收结论格式

交付说明应包含：

| 项目 | 要求 |
|------|------|
| 本次阶段 | P0、P1、P2 或文档一致性。 |
| 完成能力 | 使用 `docs/TRACEABILITY.md` 中的能力 ID 或工作包名称。 |
| 证据 | 测试命令、关键接口、数据库事实、日志或扫描结果。 |
| 未完成项 | 仍属于目标设计、下一阶段或需用户确认的技术决策。 |
| 风险 | 可能影响上线、账单、安全、协议兼容或观测的残余风险。 |

不能只写“已完成”“已优化”“已商业级”。商业级结论必须能追溯到具体证据。
