# RouterX 运维与安全设计

## 运行环境

RouterX 支持三种部署形态。

| 形态 | 数据库 | Redis | 适用场景 |
|------|--------|-------|----------|
| 单机开发 | SQLite | 可选 | 本地开发、演示 |
| 小规模生产 | PostgreSQL/MySQL | 必选 | 单实例或少量实例部署 |
| 高可用生产 | PostgreSQL 主备/托管云数据库 | Redis Sentinel/Cluster | 多实例、负载均衡、较高并发 |

## 环境变量

启动必须项通过环境变量提供。

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `SQL_DSN` | 空 | 数据库连接字符串，空时使用 `sqlite://data/routerx.db` |
| `REDIS_CONN` | `redis://localhost:6379/0` | Redis 连接字符串 |
| `JWT_SECRET` | 空 | JWT 签名密钥；生产和多实例必须显式指定同一个值 |
| `ENCRYPTION_KEY` | 空 | 加密下游 API Key、OAuth/OIDC client secret 的主密钥；生产必须显式指定或由 KMS 提供 |
| `PAYMENT_STRIPE_SECRET_KEY` | 空 | Stripe Secret Key，启用 Stripe 时必填 |
| `PAYMENT_STRIPE_WEBHOOK_SECRET` | 空 | Stripe Webhook 签名密钥，启用 Stripe Webhook 时必填 |
| `PAYMENT_EPAY_KEY` | 空 | 易支付商户签名密钥，启用易支付时必填 |
| `SERVER_PORT` | `3000` | HTTP 服务端口 |

DSN 示例：

```text
postgres://routerx:password@postgres:5432/routerx?sslmode=disable
mysql://routerx:password@tcp(mysql:3306)/routerx?charset=utf8mb4&parseTime=True&loc=Local
sqlite://data/routerx.db
```

Redis 连接示例：

```text
redis://localhost:6379/0
redis://:password@redis:6379/0
rediss://:password@redis.example.com:6379/0
```

注意：

- 生产建议使用 PostgreSQL。
- SQLite 适合单机，不适合多实例写入。
- Windows 绝对路径 SQLite DSN 在迁移 URL 转换上需要额外测试，生产建议使用相对路径或类 Unix 容器路径。
- `JWT_SECRET` 不能由每个实例各自随机生成；多实例必须使用同一个环境变量值或同一个已写入数据库的 `jwt.secret`。
- `ENCRYPTION_KEY` 不能随机丢失或随实例变化，否则已加密的下游密钥和 OAuth/OIDC client secret 无法解密。

## settings 配置

运行时配置保存在 `settings` 表，首次初始化写入默认值。

推荐配置项：

| key | 默认 | 说明 |
|-----|------|------|
| `server.mode` | `release` | Gin 运行模式 |
| `jwt.secret` | `JWT_SECRET` 或初始化生成 | JWT 签名密钥；环境变量优先，生产和多实例必须保持一致 |
| `jwt.user_expire_hours` | `168` | User JWT 过期小时数，管理端和用户端共用 |
| `cors.allowed_origins` | `[]` | CORS 白名单 |
| `cors.allow_credentials` | `true` | 是否允许 Cookie |
| `rate_limit.enabled` | `true` | 是否启用限流 |
| `rate_limit.global_per_min` | `1000` | 全局每分钟限制 |
| `rate_limit.per_token_per_min` | `60` | Token 每分钟限制 |
| `rate_limit.per_ip_per_min` | `30` | IP 每分钟限制 |
| `relay.timeout` | `120` | 下游超时秒数 |
| `relay.retry_count` | `2` | 重试次数 |
| `relay.error_auto_ban` | `true` | 自动熔断 |
| `relay.error_ban_threshold` | `10` | 熔断阈值 |
| `billing.default_ratio` | `1.0` | 默认计费倍率 |
| `log.request_body_enabled` | `false` | 是否记录请求体 |
| `log.response_body_enabled` | `false` | 是否记录响应体 |
| `log.body_max_bytes` | `4096` | 请求/响应日志最大字节数 |

配置缓存：

- 启动时加载 settings 到 Redis Hash。
- 读取配置时优先查 Redis。
- 修改配置后写 DB 并刷新 Redis。
- Redis 不可用时退回 DB，但需要打告警日志。

## Docker 部署建议

示例：

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: routerx
      POSTGRES_USER: routerx
      POSTGRES_PASSWORD: routerx_pwd
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U routerx"]
      interval: 10s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    command: ["redis-server", "--appendonly", "yes"]
    volumes:
      - redisdata:/data

  routerx:
    build: .
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_started
    ports:
      - "3000:3000"
    environment:
      SQL_DSN: postgres://routerx:routerx_pwd@postgres:5432/routerx?sslmode=disable
      REDIS_CONN: redis://redis:6379/0
      JWT_SECRET: ${JWT_SECRET:?required}
      ENCRYPTION_KEY: ${ENCRYPTION_KEY:?required}
      SERVER_PORT: "3000"

volumes:
  pgdata:
  redisdata:
```

## 数据库迁移

迁移方式：

- SQL 迁移文件随二进制嵌入。
- 服务启动时自动执行待应用迁移。
- 每个方言维护独立 SQL 文件。

发布流程建议：

```text
1. 备份数据库
2. 在 staging 环境执行新版本启动迁移
3. 校验 schema 和关键 API
4. 低峰发布生产
5. 首个实例完成迁移后再滚动其他实例
6. 观察错误率、启动日志和数据库状态
```

多实例注意事项：

- golang-migrate 会记录 schema 版本，但多实例同时启动仍可能争抢迁移锁。
- 生产建议将迁移作为发布前置 Job 执行，业务实例只在迁移完成后启动。
- 如果保留自动迁移，滚动发布时先启动一个实例完成迁移，再扩容其他实例。

回滚策略：

- 应用版本回滚不一定等于数据库回滚。
- 包含数据降级的 down 迁移需要谨慎执行。
- `002_user_identities` down 会丢失 OAuth/OIDC/phone 等非旧结构身份，不建议生产回滚。

## 日志设计

日志类型：

| 类型 | 位置 | 说明 |
|------|------|------|
| HTTP 访问日志 | 应用日志 | method、path、status、latency、ip、request_id |
| 模型调用日志 | `logs` 表 | user、token、channel、model、usage、quota、错误 |
| 管理审计日志 | 建议新增表 | 管理员操作、目标资源、变更前后摘要 |
| 系统错误日志 | 应用日志 | panic、DB、Redis、下游错误 |

结构化日志字段建议：

| 字段 | 说明 |
|------|------|
| `request_id` | 请求 ID |
| `trace_id` | 链路追踪 ID |
| `method` | HTTP method |
| `path` | 请求路径 |
| `status` | HTTP 状态 |
| `latency_ms` | 耗时 |
| `user_id` | 用户 ID |
| `token_id` | Token ID |
| `channel_id` | 通道 ID |
| `model` | 模型 |
| `error` | 错误摘要 |

脱敏规则：

- `Authorization` 只保留前缀和后 4 位。
- Cookie 不记录。
- 下游 API Key 不记录。
- 请求和响应体默认不记录。
- 开启请求体日志时按 `log.body_max_bytes` 截断。

## 指标设计

建议暴露 Prometheus `/metrics`。

核心指标：

| 指标 | 类型 | 说明 |
|------|------|------|
| `routerx_http_requests_total` | counter | HTTP 请求数 |
| `routerx_http_request_duration_seconds` | histogram | HTTP 请求耗时 |
| `routerx_relay_requests_total` | counter | 模型转发请求数 |
| `routerx_relay_errors_total` | counter | 转发错误数 |
| `routerx_relay_duration_seconds` | histogram | 下游调用耗时 |
| `routerx_tokens_used_total` | counter | token 用量 |
| `routerx_quota_used_total` | counter | 额度消耗 |
| `routerx_channel_available` | gauge | 通道可用状态 |
| `routerx_channel_error_count` | gauge | 通道连续错误数 |
| `routerx_redis_errors_total` | counter | Redis 错误数 |
| `routerx_db_errors_total` | counter | DB 错误数 |

## 健康检查

当前：

- `GET /health` 返回 `{"status":"healthy"}`。

目标拆分：

| 路径 | 用途 | 检查项 |
|------|------|--------|
| `/health` | 存活检查 | 进程是否存活 |
| `/ready` | 就绪检查 | DB、迁移状态、必要配置 |
| `/metrics` | 指标 | Prometheus metrics |

Redis 失败处理：

- `/health` 可以仍然健康。
- `/ready` 在生产模式下应返回不就绪，避免流量进入功能受限实例。

## 安全基线

### 密钥管理

必须保护的密钥：

- `jwt.secret`
- 下游 `channels.api_key`
- OAuth/OIDC client secret
- Stripe Secret Key 和 Webhook Secret
- 易支付商户签名密钥
- API Key 明文
- 数据库密码

要求：

- `jwt.secret` 初始化后不可为空。
- `jwt.secret` 必须支持由 `JWT_SECRET` 环境变量指定；生产和多实例必须显式配置，禁止各实例启动时各自随机生成。
- 下游 API Key、OAuth/OIDC client secret 应加密存储，主密钥来自 `ENCRYPTION_KEY` 环境变量或 KMS。
- 支付密钥应通过环境变量、KMS 或加密配置提供，禁止写入前端响应、日志或明文配置文件。
- API Key 目标设计只保存哈希。
- 管理端任何响应不得返回完整下游 API Key。

### HTTP 安全

建议：

- 生产启用 HTTPS。
- 管理端和用户端共用 User JWT，前端需要采用统一安全存储策略。
- CORS 白名单从 settings 读取，生产禁止 `*` 配合 credentials。
- 限制请求体大小。
- 对登录、注册、验证码、OAuth callback 添加限流。

### 数据安全

建议：

- 密码使用 bcrypt，成本值可配置。
- 日志默认不存完整提示词和响应。
- 支持按用户删除或匿名化日志中的敏感内容。
- 管理端导出数据需要审计。

## 故障处理

| 故障 | 处理 |
|------|------|
| DB 不可用 | 服务启动失败；运行中返回 500/503，触发告警 |
| Redis 不可用 | 缓存和限流降级；生产应告警，关键限流可 fail-closed |
| 下游 401/403 | 标记通道配置错误，不重试，通知管理员 |
| 下游 429 | 可切换其他通道；增加通道错误统计 |
| 下游 5xx | 非流式可重试其他通道；流式已输出则结束并记录错误 |
| 通道全部不可用 | 返回 502，记录失败日志 |
| 余额不足 | 返回 429，不调用下游 |
| 迁移失败 | 启动失败，保留 dirty 状态，人工介入修复 |

## 备份和恢复

数据库：

- 生产 PostgreSQL 每日至少一次全量备份。
- 重要发布前手动快照。
- 开启 WAL 或云数据库 PITR。

Redis：

- Redis 中主要是缓存和短期状态，可不作为强一致持久源。
- 如果使用 Redis 存 Admin Session，重启会导致重新登录，需要可接受。

密钥：

- 加密通道 API Key 和 OAuth/OIDC client secret 的主密钥必须独立备份。
- 丢失主密钥会导致已存下游密钥不可恢复。

## 发布检查清单

- `SQL_DSN` 指向正确环境。
- 生产 `JWT_SECRET` 已显式配置，且所有实例一致。
- 生产 `ENCRYPTION_KEY` 已显式配置或 KMS 已可用，且所有实例能解密同一批密文。
- CORS 白名单正确。
- Redis 可用。
- 数据库已备份。
- 待执行迁移已在 staging 验证。
- 下游通道 API Key 未出现在日志中。
- `/health` 和 `/ready` 正常。
- 管理员账号至少一个可用。
- 回滚方案明确。
