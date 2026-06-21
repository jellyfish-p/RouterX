# RouterX

RouterX 是一个开源的 AI 模型网关，用于统一管理多个大模型厂商的 API 访问。项目提供 OpenAI-compatible、Anthropic-compatible 和 Gemini-compatible 模型入口，配套用户与管理员体系、API Key 管理、通道管理、额度控制、调用日志、审计、观测和支付/充值基础闭环。

当前仓库包含 Go 后端和 Nuxt 前端。后端负责鉴权、路由、数据库迁移和模型转发；前端提供初始化、登录、用户控制台和管理后台页面。

## 功能概览

- OpenAI-compatible、Anthropic-compatible 和 Gemini-compatible `/v1` 入口，覆盖 Chat、Completions、Responses、Embeddings、Images、Audio、Moderations、模型列表和基础流式链路。
- 支持 OpenAI / OpenAI-compatible、Azure OpenAI、Claude/Anthropic、Gemini、Qwen、DeepSeek、xAI 和 RouterX-compatible 上游通道，按 `docs/PROTOCOLS.md` 记录能力等级和字段降级边界。
- 用户注册、登录、JWT 会话和管理员角色控制。
- 首次初始化超级管理员和默认系统设置。
- 用户 API Key 创建、列表、编辑、删除、轮换、泄露上报、scope 收窄、用量统计和风险视图。
- 管理员用户、管理员账号、用户分组、通道、日志、设置、看板、模型价格、支付商品、充值码、支付订单、退款请求和审计接口。
- PostgreSQL、MySQL、SQLite 多数据库支持，启动时自动执行嵌入式迁移。
- 单镜像 SQLite 模式不需要 Redis；外部数据库或集群模式必须配置 Redis，用于缓存、限流和跨实例一致性。
- Nuxt 管理/用户前端，开发环境下自动代理 `/v0`、`/v1` 和 `/health` 到后端。

## 技术栈

| 模块 | 技术 |
|------|------|
| 后端 | Go 1.26.4, Gin, GORM |
| 数据库 | PostgreSQL, MySQL, SQLite |
| 迁移 | golang-migrate, Atlas GORM Provider |
| 缓存/限流 | Redis |
| 前端 | Nuxt 4, Vue 3, Nuxt UI, Pinia |

## 项目结构

```text
.
├── cmd/
│   ├── server/          # 后端服务入口
│   └── migrate/         # Atlas/GORM schema 与迁移命令
├── docs/                # 架构、API、数据模型、运维、计费等设计文档
├── frontend/            # Nuxt 前端
├── internal/
│   ├── common/          # 通用响应、JWT、密码、密钥工具
│   ├── dto/             # 请求/响应 DTO
│   ├── handler/         # HTTP 控制器
│   ├── middleware/      # 鉴权、限流、恢复、日志等中间件
│   ├── migrate/         # 嵌入式 SQL 迁移
│   ├── model/           # GORM 数据模型
│   ├── relay/           # 上游模型厂商适配器
│   ├── router/          # 路由注册
│   └── service/         # 业务逻辑
├── atlas.hcl
├── go.mod
└── go.sum
```

## 快速开始

### 1. 启动后端

最小启动不需要外部数据库，默认使用 SQLite 文件 `data/routerx.db`：

```bash
go run ./cmd/server
```

直接启动 Docker 镜像时也属于 SQLite 单机模式；如果没有配置 `SQL_DSN`，Redis 可以不配置。该模式适合开箱验证和单实例小规模使用，不适合集群。

服务默认监听：

```text
http://localhost:3000
```

健康检查：

```bash
curl http://localhost:3000/health
curl http://localhost:3000/ready
```

### 2. 初始化系统

首次启动后创建超级管理员：

```bash
curl -X POST http://localhost:3000/v0/setup/init \
  -H "Content-Type: application/json" \
  -d '{
    "username": "admin",
    "password": "password123",
    "display_name": "Administrator",
    "email": "admin@example.com"
  }'
```

查询初始化状态：

```bash
curl http://localhost:3000/v0/setup/status
```

### 3. 启动前端

前端只使用 Bun 作为包管理器和脚本运行器。进入前端目录安装依赖并启动开发服务：

```bash
cd frontend
bun install
bun run dev
```

前端默认运行在 `http://localhost:5173`，并会将 `/v0`、`/v1` 和 `/health` 代理到 `http://localhost:3000`。

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `SERVER_PORT` | `3000` | HTTP 服务端口 |
| `SQL_DSN` | 空 | 数据库连接字符串；为空时使用 SQLite `data/routerx.db` |
| `LOG_SQL_DSN` | 空 | 独立日志数据库连接字符串；为空时日志写入主数据库 |
| `REDIS_CONN` | 空 | Redis 连接字符串；SQLite 单镜像模式可为空，外部数据库或集群模式必填 |
| `JWT_SECRET` | 空 | JWT 签名密钥；生产和多实例部署必须显式指定同一个值 |
| `ENCRYPTION_KEY` | 空 | 下游 API Key、OAuth/OIDC client secret 和支付 provider 密钥的加密主密钥；生产必须固定配置 |

Stripe 和易支付配置通过超级管理员系统设置写入数据库：`payment.stripe.secret_key`、`payment.stripe.webhook_secret` 和 `payment.epay.key` 会加密落库，集群节点共享同一份 settings。

数据库 DSN 示例：

```text
postgres://routerx:password@localhost:5432/routerx?sslmode=disable
mysql://routerx:password@tcp(localhost:3306)/routerx?charset=utf8mb4&parseTime=True&loc=Local
sqlite://data/routerx.db
```

Redis DSN 示例：

```text
redis://localhost:6379/0
redis://:password@localhost:6379/0
rediss://:password@redis.example.com:6379/0
```

生产环境建议至少配置：

```bash
export SQL_DSN="postgres://routerx:password@localhost:5432/routerx?sslmode=disable"
export REDIS_CONN="redis://localhost:6379/0"
export JWT_SECRET="change-me-to-a-long-random-secret"
export ENCRYPTION_KEY="change-me-to-a-32-byte-secret"
export SERVER_PORT="3000"
```

如果需要把高流量调用日志放到独立数据库，可以额外配置：

```bash
export LOG_SQL_DSN="postgres://routerx_logs:password@localhost:5432/routerx_logs?sslmode=disable"
```

运行模式约束：

- 不设置 `SQL_DSN` 时使用 SQLite 单镜像模式，Redis 可省略。
- 设置 PostgreSQL/MySQL 等外部 `SQL_DSN` 时必须同时设置可用 `REDIS_CONN`。
- 不支持只配置外部数据库但不配置 Redis 的运行形态。

## 常用命令

```bash
# 运行后端
go run ./cmd/server

# 手动执行数据库迁移
go run ./cmd/migrate/main.go up

# 输出 Atlas/GORM schema
go run ./cmd/migrate/main.go

# 运行后端测试
go test ./...

# 前端开发
cd frontend && bun run dev

# 前端构建
cd frontend && bun run build

# 前端类型检查
cd frontend && bun run typecheck
```

## API 入口

| 入口 | 说明 |
|------|------|
| `GET /health` | 进程健康检查 |
| `GET /ready` | 数据库和 JWT 配置就绪检查 |
| `/v0/setup/*` | 首次初始化 |
| `/v0/user/*` | 用户注册、登录、个人资料、API Key、日志和账单 |
| `/v0/admin/*` | 管理员用户、通道、日志、设置和看板 |
| `/v1/*` | OpenAI-compatible 模型转发入口 |

模型转发请求使用 API Key：

```http
Authorization: Bearer sk-xxxxxxxx
```

示例：

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      { "role": "user", "content": "hello" }
    ]
  }'
```

## 数据库迁移

迁移 SQL 位于：

```text
internal/migrate/postgres
internal/migrate/mysql
internal/migrate/sqlite
```

后端启动时会根据 `SQL_DSN` 自动识别数据库类型并执行迁移。也可以手动运行：

```bash
go run ./cmd/migrate/main.go up
```

如果需要基于 GORM 模型生成 Atlas schema：

```bash
go run ./cmd/migrate/main.go > schema.hcl
```

## 开发状态

RouterX 后端当前已按 `docs/ACCEPTANCE.md` 和 `docs/TRACEABILITY.md` 收敛到可验证的 P0 开箱闭环，并覆盖多协议入口、主要上游适配、基础流式、访问控制、限流熔断、独立日志库、观测审计、模型价格管理、支付/充值/退款和高级 API 的基础链路。仍保留为后续增强的长尾项以追踪矩阵为准，例如更完整 SDK 行为矩阵、更多 provider 自动退款流程、KMS provider、冷热归档和更细粒度的生产观测。

## 文档索引

- [商业级设计总稿](docs/DESIGN.md)
- [技术决策记录](docs/DECISIONS.md)
- [术语表](docs/GLOSSARY.md)
- [用户路径契约](docs/FLOWS.md)
- [控制台能力契约](docs/CONSOLE.md)
- [开发者体验契约](docs/DEVELOPER_EXPERIENCE.md)
- [API Key 管理契约](docs/API_KEYS.md)
- [策略与访问控制契约](docs/POLICIES.md)
- [协议兼容与能力矩阵](docs/PROTOCOLS.md)
- [能力到验收追踪矩阵](docs/TRACEABILITY.md)
- [商业级验收门禁](docs/ACCEPTANCE.md)
- [调用事实快照契约](docs/SNAPSHOTS.md)
- [安全威胁模型](docs/SECURITY.md)
- [错误代码与失败语义](docs/ERRORS.md)
- [观测与审计设计](docs/OBSERVABILITY.md)
- [故障处理 Runbooks](docs/RUNBOOKS.md)
- [架构设计](docs/ARCHITECTURE.md)
- [模块责任边界](docs/MODULE_BOUNDARIES.md)
- [API 设计](docs/API.md)
- [Apifox/OpenAPI 导入文档](docs/apifox/openapi.yaml)
- [数据模型](docs/DATA_MODEL.md)
- [模型转发设计](docs/RELAY.md)
- [账号系统设计](docs/ACCOUNTS.md)
- [计费与额度设计](docs/BILLING.md)
- [支付与充值插件契约](docs/PAYMENTS.md)
- [settings 注册表](docs/SETTINGS.md)
- [实现交接清单](docs/IMPLEMENTATION.md)
- [运维与安全设计](docs/OPERATIONS.md)
- [测试设计](docs/TESTING.md)
- [实施路线图](docs/ROADMAP.md)

## 许可证

本项目使用 [MIT License](LICENSE)。
