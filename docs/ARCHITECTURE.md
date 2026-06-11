# RouterX 后端架构设计

## 架构原则

RouterX 后端采用明确的分层结构，避免 Handler 直接处理数据库和下游协议细节。

| 原则 | 要求 |
|------|------|
| Handler 薄层化 | 只负责参数绑定、鉴权上下文读取、调用 Service、返回响应 |
| Service 业务化 | 负责事务、权限、状态流转、额度扣减、通道选择等业务规则 |
| Adapter 隔离厂商差异 | 所有下游模型厂商差异集中在 `internal/relay` |
| Migration 优先 | 运行时以 SQL 版本化迁移为准，GORM 模型用于开发和查询表达 |
| 热路径缓存 | API Key、设置、限流计数、通道快照优先使用 Redis 缓存 |
| 可降级启动 | Redis 初始化失败不阻塞服务启动，但生产环境需要告警 |

## 代码分层

```text
cmd/server
    |
    v
internal/router
    |
    v
internal/middleware
    |
    v
internal/handler
    |
    v
internal/service
    |
    +--> internal/model + GORM
    |
    +--> internal/relay + Adapter Registry
    |
    +--> internal/common
    |
    +--> Redis / DB
```

| 目录 | 职责 |
|------|------|
| `cmd/server` | 程序入口、初始化 DB/Redis、装配依赖、启动 HTTP 服务 |
| `cmd/migrate` | 使用 Atlas GORM Provider 输出模型 schema，用于辅助生成迁移 |
| `internal/database.go` | 解析 `SQL_DSN`、初始化 GORM、执行嵌入式 SQL 迁移 |
| `internal/redis.go` | 初始化 Redis 客户端 |
| `internal/router` | 组织公共、Admin、User、多协议模型转发路由组 |
| `internal/middleware` | Recovery、Logger、CORS、SetupCheck、Auth、RateLimit |
| `internal/handler` | Gin HTTP 控制器 |
| `internal/service` | 核心业务逻辑 |
| `internal/model` | GORM 数据模型 |
| `internal/relay` | 下游厂商适配器和注册表 |
| `internal/dto` | 请求和响应 DTO |
| `internal/common` | 常量、响应结构、密码和 Token 工具函数 |

## 启动流程

当前入口位于 `cmd/server/main.go`。

```text
main
    |
    |-- internal.InitDB()
    |       |-- read SQL_DSN
    |       |-- resolve dialect
    |       |-- gorm.Open(...)
    |       |-- setup connection pool when not SQLite
    |       |-- migrate.Run(SQL_DSN)
    |
    |-- internal.InitRedis()
    |       |-- read REDIS_CONN
    |       |-- ping Redis
    |       |-- failure logs warning, startup continues
    |
    |-- create services
    |-- create handlers
    |-- router.SetupRouter(...)
    |-- r.Run(:SERVER_PORT)
```

启动约束：

- DB 连接或迁移失败必须终止启动。
- Redis 连接失败当前不终止启动，但应在生产环境提升为可观测告警。
- `SERVER_PORT` 未设置时默认 `3000`。
- `SQL_DSN` 未设置时默认 SQLite `data/routerx.db`。

## 路由组织

当前实际路由前缀如下。

| 路由组 | 前缀 | 中间件 | 说明 |
|--------|------|--------|------|
| Health | `/health` | 无 | 健康检查，不依赖初始化状态 |
| Setup | `/v0/setup` | 无 | 初始化状态和首次初始化 |
| Admin | `/v0/admin` | `SetupCheck`、User JWT、Admin Role | 管理端 API |
| User | `/v0/user` | `SetupCheck`、User JWT | 用户控制台 API |
| Relay | `/v1` | `SetupCheck`、API Key Auth | OpenAI、Gemini、Anthropic 入口协议和多上游转发 API |

全局中间件顺序：

```text
Recovery -> Logger -> CORS -> Route Group Middlewares -> Handler
```

目标生产中间件顺序：

```text
Recovery
    -> RequestID
    -> Logger
    -> CORS
    -> SetupCheck
    -> Auth
    -> RateLimit
    -> Idempotency when needed
    -> Handler
```

## 中间件职责

| 中间件 | 当前状态 | 目标职责 |
|--------|----------|----------|
| `Recovery` | 已存在 | 捕获 panic，返回统一错误，记录堆栈 |
| `Logger` | 已存在 | 记录 method/path/status/latency/ip，后续切结构化日志 |
| `CORS` | 已存在 | 当前硬编码 localhost，后续从 settings 读取 |
| `SetupCheck` | 已存在 | 系统未初始化时拦截非 setup/health 请求 |
| `AdminAuthRequired` | 占位 | 校验管理端 Cookie/JWT 和管理员角色 |
| `UserJwtAuthRequired` | 占位 | 校验用户端 JWT，写入 user 上下文 |
| `ApiKeyAuthRequired` | 占位 | 校验 `Authorization: Bearer sk-*`，写入 user/token 上下文 |
| `RateLimit` | 占位 | 基于 Redis 的全局、IP、用户、Token、通道限流 |

## Handler 层设计

Handler 只做 HTTP 协议层工作。

职责：

- 参数绑定和校验。
- 从 Gin context 读取当前用户、Token、请求 ID。
- 调用 Service。
- 将业务错误映射为 HTTP 状态码和统一响应。
- 不直接访问数据库。
- 不直接调用下游模型厂商。

统一错误建议：

| 错误类型 | HTTP 状态 | 示例 |
|----------|-----------|------|
| 参数错误 | 400 | 缺少 `model`、非法分页参数 |
| 未认证 | 401 | 未登录、API Key 无效 |
| 无权限 | 403 | 非超级管理员创建超级管理员 |
| 不存在 | 404 | 用户、通道、Token 不存在 |
| 状态冲突 | 409 | 用户名身份已存在、通道已禁用 |
| 余额不足 | 429 | 用户或 Token 额度不足 |
| 下游失败 | 502 | 厂商 API 返回不可恢复错误 |
| 超时 | 504 | 下游请求超时 |
| 内部错误 | 500 | 未预期错误 |

## Service 层设计

| Service | 目标职责 |
|---------|----------|
| `SetupService` | 首次初始化、超级管理员创建、默认 settings 写入 |
| `AuthService` | 注册、登录、改密、JWT/Session 创建和校验 |
| `UserService` | 用户 CRUD、个人信息、状态、分组、额度调整 |
| `AdminService` | 管理员账号管理和权限链校验 |
| `TokenService` | API Key 创建、校验、缓存、过期、扣费 |
| `ChannelService` | 通道 CRUD、模型匹配、通道选择、健康状态维护 |
| `RelayService` | 转发编排、请求转换、下游调用、响应转换、计费日志闭环 |
| `LogService` | 调用日志写入、查询、统计、清理 |
| `SettingService` | settings 读写、默认值、Redis 缓存、动态配置刷新 |

事务边界建议：

| 场景 | 事务内容 |
|------|----------|
| 首次初始化 | 创建超级管理员、创建本地身份、写默认 settings |
| 用户注册 | 创建 user、创建 identity、可选写默认分组和额度 |
| 兑换充值码 | 锁定充值码、增加用户额度、写使用时间 |
| 模型调用扣费 | 扣 Token 或用户额度、写 Log、更新通道状态 |
| 管理员删除用户 | 软删除用户、级联删除或禁用 API Key |

## 初始化流程

```text
GET /v0/setup/status
    -> SetupService.GetInitStatus
    -> SELECT COUNT(*) FROM users WHERE role >= RoleAdmin
    -> initialized = count > 0

POST /v0/setup/init
    -> 校验系统未初始化
    -> 创建 super admin user
    -> 创建 user_identities(username/local)
    -> 写入默认 settings
    -> 优先写入 JWT_SECRET；未配置时初始化生成一次 jwt.secret
    -> 提交事务
```

当前注意点：

- `SetupHandler.Init` 仍是 TODO，占位返回 `not implemented`。
- 初始化完成条件由管理员用户数量决定，不需要独立 init 表。
- 初始化接口必须具备幂等保护和并发保护，避免并发创建多个超级管理员。
- 生产和多实例必须通过 `JWT_SECRET` 指定一致的 JWT 签名密钥，不能由每个实例独立随机生成。

## 认证上下文

建议在鉴权成功后向 Gin context 写入统一键。

| Key | 类型 | 来源 |
|-----|------|------|
| `current_user` | `*model.User` | User JWT/API Key 鉴权 |
| `current_token` | `*model.Token` | API Key 鉴权 |
| `request_id` | `string` | RequestID 中间件 |
| `auth_method` | `string` | `user_jwt`、`api_key` |

## 依赖装配

当前使用手工依赖注入，适合项目早期。

目标约束：

- Service 之间可以显式依赖，例如 `RelayService` 依赖 `ChannelService`、`TokenService`、`LogService`、`SettingService`。
- Handler 只依赖对应 Service，不跨层调用。
- 不引入全局 Service 单例，除 `internal.DB`、`internal.RDB` 这类基础连接外，业务对象由入口装配。

## 缓存策略

| 数据 | Redis Key 示例 | 失效策略 |
|------|----------------|----------|
| API Key 校验 | `token:{sha256(key)}` | Token 更新、禁用、删除时删除缓存 |
| settings | `settings` hash | 设置变更后刷新 hash |
| 限流计数 | `rl:token:{id}:{minute}` | TTL 按窗口自动过期 |
| 通道快照 | `channels:model:{model}` | 通道更新后删除或短 TTL |
| Admin Session | `session:admin:{id}` | 登录生成，退出删除，到期过期 |
| OAuth State | `oauth:state:{state}` | 短 TTL，一次性消费 |

## 并发与一致性

模型转发热路径存在额度扣减并发问题，目标实现需要保证不会透支。

建议策略：

- 非流式请求在收到 usage 后扣费，扣费使用数据库原子更新条件。
- 流式请求可先做额度预检，结束后按实际 usage 扣费。
- 如果下游不返回 usage，则使用 tokenizer 估算或按配置规则兜底。
- Token 额度和用户额度扣减必须在同一事务中完成。
- 对 `remain_quota >= cost` 使用条件更新，更新行数为 0 时返回余额不足。

## 当前实现差距

| 区域 | 差距 |
|------|------|
| Setup | 初始化创建超级管理员和 settings 未实现 |
| Auth | 管理端、用户端、API Key 鉴权均未接入真实校验 |
| Relay | Handler、Service、Adapter 均为占位实现 |
| Settings | 默认值存在，但写入、缓存、动态读取未实现 |
| RateLimit | 中间件未注册，限流逻辑未实现 |
| Token 管理 | API Key CRUD 路由尚未注册 |
| RedemCode | 模型存在，充值码 API 尚未注册 |
| Observability | 结构化日志、指标、追踪尚未实现 |
