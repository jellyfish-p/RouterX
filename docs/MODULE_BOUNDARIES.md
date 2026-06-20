# RouterX 模块责任边界

## 目标

本文档定义 RouterX 后端各模块的责任边界、依赖方向和禁止跨层行为。它面向后续实现者，解决“这段逻辑应该写在哪里、哪个模块不能做什么、完成后应留下什么证据”的问题。

本文不替代 `docs/ARCHITECTURE.md` 的总体分层，也不替代 `docs/IMPLEMENTATION.md` 的工作包顺序。本文只收束模块边界。

## 总原则

- Handler 只处理 HTTP 边界，不直接实现业务规则。
- Middleware 只做横切拦截和上下文注入，不写业务状态流转。
- Service 承载业务规则、事务、状态变更和跨实体协调。
- Model 只表达数据结构、字段约束和持久化形状，不承载请求流程。
- Relay adapter 只隔离上游厂商差异，不判断用户权限、不扣费、不写业务日志。
- 日志、计费、策略、协议和安全事实必须可追踪，不能散落在多个模块各自解释。

依赖方向：

```text
router
  -> middleware
  -> handler
  -> service
  -> model / relay adapter / common
```

允许 Service 之间显式依赖，例如 `RelayService` 依赖 `ChannelService`、`TokenService`、`LogService`、`SettingService`。不允许 Handler 绕过 Service 直接操作数据库完成业务流程。

## 模块职责表

| 模块 | 负责 | 不负责 | 输出证据 |
|------|------|--------|----------|
| `internal/router` | 路由分组、中间件顺序、公共/Admin/User/Relay 入口挂载。 | 业务规则、权限细节、数据库读写。 | 路由表、测试能命中真实路径。 |
| `internal/middleware` | 恢复、日志、初始化拦截、JWT 鉴权、API Key 鉴权、限流上下文。 | 用户 CRUD、通道选择、扣费、协议转换。 | Gin context 中稳定写入 user/token/request_id 或返回明确错误。 |
| `internal/handler` | 参数绑定、路径/header/query/body 读取、调用 Service、返回协议外形。 | 事务、复杂权限判断、通道选择、计费、密钥解密。 | HTTP status、响应结构、协议兼容错误。 |
| `internal/service` | 业务规则、状态流转、事务、跨模块编排、缓存失效、日志账单一致。 | HTTP 细节、上游厂商专有报文细节。 | 数据库事实、日志事实、错误 code、审计摘要。 |
| `internal/model` | GORM 模型、字段、索引、软删除、迁移目标。 | 业务流程、外部请求、权限决策。 | 表结构、迁移、字段含义。 |
| `internal/relay` | APIType、adapter 注册、请求/响应转换、上游 endpoint、usage 提取。 | 用户权限、API Key 校验、额度扣减、用户日志写入。 | 转换后的请求/响应、usage 或转换错误。 |
| `internal/common` | JWT、密码、密钥、统一响应、通用工具。 | 模块业务规则和策略判断。 | 可复用安全工具和响应工具。 |
| `internal/dto` | 请求/响应 DTO、实体到接口响应的映射和脱敏形状。 | 业务决策、数据库事务。 | 稳定 API 外形和脱敏字段。 |
| `internal/migrate` | 嵌入式迁移和 SQL 方言差异。 | 运行时业务逻辑。 | 可重复执行的迁移。 |

## P0 调用链边界

P0 Chat 闭环的责任链：

```text
router/user_router
  -> SetupCheck
  -> ApiKeyAuthRequired
  -> RelayHandler.ChatCompletions
  -> RelayService.Handle
  -> TokenService / ChannelService / relay.Adapter / LogService
```

责任切分：

| 步骤 | 模块 | 边界 |
|------|------|------|
| 路由匹配 | router | 只决定哪个 handler 接收请求。 |
| API Key 鉴权 | middleware + TokenService | 校验 Key、用户和 API Key 状态，写入上下文；不选择通道。 |
| 请求读取 | RelayHandler | 读取 body、识别入口路径和 APIType；不扣费。 |
| 请求解析 | RelayService | 解析 model、stream、基本 JSON；本地失败不调用上游。 |
| 额度预检 | TokenService | 判断用户余额和 Key 预算是否可用；不解释 provider。 |
| 通道候选 | ChannelService | 按模型、状态、优先级、权重、分组、熔断状态筛选。 |
| 协议转换 | relay.Adapter | 把规范化请求转换成上游请求；不读取用户权限。 |
| 上游调用 | relay.Adapter 或 RelayService | 使用通道密钥和 base URL；不能使用用户 API Key。 |
| usage 提取 | relay.Adapter + RelayService | 优先使用下游 usage，缺失时按策略估算或最低计费。 |
| 扣费事务 | TokenService | 条件扣减用户额度并更新 Key 预算计数，避免并发透支。 |
| 日志写入 | LogService | 写 user/token/channel/model/usage/quota/status/error 摘要。 |
| 响应返回 | RelayHandler | 返回入口协议兼容结构。 |

## Handler 边界

Handler 可以做：

- 读取路径、query、header 和 body。
- 调用 DTO bind/validate。
- 从 context 读取 `current_user`、`current_token`、`request_id`。
- 调用对应 Service。
- 将 Service 错误映射为 `/v0` 统一响应或 `/v1` 协议兼容响应。

Handler 不应做：

- 直接 `db.Create`、`db.Save`、`db.Delete` 完成业务状态变更。
- 直接解密通道密钥。
- 直接选择通道、执行扣费或写模型调用日志。
- 在 `/v1` 返回 RouterX 管理端统一响应。
- 在管理端响应里返回 API Key 或下游密钥明文。

## Service 边界

Service 可以做：

- 使用事务和条件更新维护一致性。
- 调用其他 Service 完成明确业务编排。
- 读取 settings typed accessor。
- 处理缓存读写和失效。
- 写业务日志、审计摘要和错误 code。

Service 不应做：

- 依赖 Gin `Context` 作为核心输入；应使用明确参数或业务上下文。
- 返回未脱敏的密钥给 Handler。
- 把 provider-specific 报文处理散落到业务 Service；这类逻辑应进入 adapter 或 translator。
- 在没有日志或账单证据的情况下静默修改额度。

## Relay 边界

RelayService 负责：

- 编排一次模型调用。
- 应用策略和通道路由结果。
- 调用 adapter 做请求/响应转换。
- 串联 usage、扣费和日志。
- 保证失败路径可解释。

RelayService 不负责：

- User JWT 登录、管理员权限和普通用户 CRUD。
- 支付订单、充值码、退款和人工补账。
- OAuth/OIDC 身份绑定。
- 直接保存上游厂商密钥明文。

Adapter 负责：

- 根据 `APIType` 构造上游 endpoint。
- 转换请求体、header、query 和响应体。
- 提取或转换 usage。
- 转换流式 chunk。
- 获取模型列表。

Adapter 不负责：

- 判断 API Key 是否允许访问模型。
- 筛选通道或应用 `routerx.route` 越权规则。
- 扣减用户余额和 Key 预算。
- 写 `logs` 或管理审计。
- 读取用户余额。

## 通道和策略边界

ChannelService 负责通道实体和候选通道选择的可解释规则：

- 通道 CRUD、启用/禁用、软删除。
- 密钥加密、脱敏摘要、上游解析。
- 模型匹配、模型重写、priority、weight、错误计数。
- `upstreams` 优先于外层 `api_keys` 和 `base_urls`。

策略规则以 `docs/POLICIES.md` 为准。实现时建议把“不可绕过过滤”和“用户偏好收窄”分开：

```text
system filters
  -> access policy
  -> routerx.route narrowing
  -> priority/weight selection
```

禁止：

- 让 `routerx.route` 启用被禁用通道。
- 让用户请求覆盖上游鉴权 header。
- 在未知 provider 或 adapter 缺失时静默降级到任意通道。

## 计费和日志边界

TokenService 负责额度扣减：

- 有限 API Key 调用成功时扣 `users.quota`，并消耗 Key 剩余预算或增加 Key 累计已用。
- 无限 API Key 调用成功时只扣 `users.quota`。
- 使用条件更新或事务避免并发透支。
- 扣费失败返回稳定错误，不静默继续。

LogService 负责事实记录：

- 成功日志记录 usage、quota、user、token、channel、model。
- 失败日志记录 error_code、source、是否调用上游、是否扣费。
- 请求/响应 body 默认不记录，开启时截断和脱敏。

Billing 规则负责解释价格和倍率，不直接替代日志事实。账单聚合必须来自 `logs` 或明确的额度流水表，不能由当前价格规则重新解释历史调用。

禁止：

- 有限 API Key 调用时只扣 Key 预算但不扣用户余额，或只扣用户余额但不更新 Key 预算。
- 失败且无有效 usage 时默认扣费。
- 扣费成功但日志完全缺失且没有补偿记录。
- 账单接口绕过日志事实重新计算历史价格。

## Settings 边界

SettingService 负责：

- 注册表默认值、类型、敏感级别、校验、生效方式。
- 初始化默认 settings。
- 管理端批量读写和脱敏。
- 缓存刷新和 typed accessor。

环境变量负责：

- `SQL_DSN`、`LOG_SQL_DSN`、`REDIS_CONN`。
- `JWT_SECRET`、`ENCRYPTION_KEY`。
- 支付 provider 密钥和其他启动期 secret。

禁止：

- 把环境变量当成普通运行时业务配置散落读取。
- settings 类型错误时静默解析为 0、false 或空字符串。
- 在响应、日志或审计里返回敏感 settings 原文。

## DTO 和脱敏边界

DTO 层负责输出形状和脱敏，不负责业务决策。

脱敏要求：

- API Key 明文只在创建响应中出现一次。
- API Key 列表只返回前后缀、哈希摘要或安全 display 值。
- 下游密钥只返回脱敏摘要。
- settings 敏感值只返回脱敏标记和是否已配置。
- 错误响应不包含 DSN、密钥、Cookie、支付密钥或堆栈。

## 测试边界

接口级验收：

- 使用真实路由、中间件、Handler 和 Service 装配。
- 使用本地下游桩替代真实模型厂商。
- 同时断言 HTTP 响应、数据库事实和敏感信息不泄露。

Service 单元测试可以覆盖：

- 额度扣减事务。
- 通道选择和 `upstreams` 优先级。
- settings 类型校验。
- adapter 请求/响应转换。

Service 单元测试不能替代：

- `/v1` 协议兼容响应。
- `/v0` 权限边界。
- API Key 鉴权链路。
- 日志账单一致性。

## 新增模块准入清单

新增模块或明显扩展现有模块时，必须回答：

| 问题 | 要求 |
|------|------|
| 模块负责什么 | 用一句话定义核心责任。 |
| 模块不负责什么 | 明确至少三条禁止跨界行为。 |
| 上游依赖 | 说明依赖哪些 Service、Model、Adapter 或外部系统。 |
| 输出证据 | 说明会写哪些数据库事实、日志、指标或响应。 |
| 失败语义 | 说明返回哪些错误 code，是否调用上游，是否扣费。 |
| 测试方式 | 说明接口级测试、Service 测试或本地桩测试。 |
| 文档同步 | 检查 `ARCHITECTURE`、`IMPLEMENTATION`、`TRACEABILITY`、`TESTING` 和相关专题文档。 |
