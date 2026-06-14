# RouterX 运维与安全设计

## 运行环境

RouterX 支持两类基础运行方式，并可在第二类上扩展高可用。

| 形态 | 数据库 | Redis | 适用场景 |
|------|--------|-------|----------|
| 单镜像 SQLite | SQLite | 不需要 | 直接启动 Docker 镜像、本地开发、演示、小白开箱 |
| 数据库 + Redis | PostgreSQL/MySQL | 必选 | 单实例长期运行、生产部署 |
| 高可用生产 | PostgreSQL 主备/托管云数据库 | Redis Sentinel/Cluster | 多实例、负载均衡、较高并发 |

不允许的形态：配置了 PostgreSQL/MySQL 等外部数据库，但没有配置 Redis。该形态会让 API Key 缓存、settings 刷新、限流、通道候选缓存和集群一致性不可预测，应在启动或 `/ready` 中明确失败。

## 环境变量

启动必须项通过环境变量提供。

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `SQL_DSN` | 空 | 数据库连接字符串，空时使用 `sqlite://data/routerx.db` |
| `LOG_SQL_DSN` | 空 | 独立日志数据库连接字符串；为空时日志写入主数据库 |
| `REDIS_CONN` | 空 | Redis 连接字符串；SQLite 单镜像模式可为空，外部数据库模式必填 |
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

- 直接启动单个 RouterX 镜像且不设置 `SQL_DSN` 时，默认 SQLite 模式必须能正常运行，不要求 Redis。
- 生产建议使用 PostgreSQL/MySQL + Redis。
- SQLite 适合单机，不适合多实例写入。
- 只配置外部数据库但不配置 Redis 是非法运行形态。
- Windows 绝对路径 SQLite DSN 在迁移 URL 转换上需要额外测试，生产建议使用相对路径或类 Unix 容器路径。
- `JWT_SECRET` 不能由每个实例各自随机生成；多实例必须使用同一个环境变量值或同一个已写入数据库的 `jwt.secret`。
- `ENCRYPTION_KEY` 不能随机丢失或随实例变化，否则已加密的下游密钥和 OAuth/OIDC client secret 无法解密。

环境模式建议：

| 模式 | 允许行为 | 不允许行为 |
|------|----------|------------|
| 开发/演示 | 使用 SQLite、进程内缓存、缺少 `ENCRYPTION_KEY` 时以警告方式保存新密钥 | 把演示数据当生产数据长期保存 |
| 小规模生产 | 必须显式配置 `JWT_SECRET`、`ENCRYPTION_KEY`、PostgreSQL/MySQL 和 Redis | 每个实例随机生成 JWT 或加密主密钥；只配数据库不配 Redis |
| 高可用生产 | 多实例共享同一批启动密钥，迁移由发布 Job 控制，Redis/DB 有监控告警 | 自动迁移争抢、密钥缺失仍接收生产流量 |

## settings 配置

运行时配置保存在 `settings` 表，首次初始化写入默认值。

配置边界：

- `settings` 是运行时配置的权威来源。
- 环境变量只承载启动必须项、跨实例必须一致的密钥和外部服务密钥。
- 计费倍率、访问控制、限流、日志和 Relay 默认值应通过 `settings` 管理；策略语义以 `docs/POLICIES.md` 为准，协议和 provider 能力等级以 `docs/PROTOCOLS.md` 为准。
- 敏感密钥可以来自环境变量、KMS 或加密配置，但不得写入前端响应或日志明文。

完整 settings 注册表以 `docs/SETTINGS.md` 为准。这里保留运维视角最常用的当前默认配置：

| key | 默认 | 说明 |
|-----|------|------|
| `server.port` | `3000` | 服务端口；生产通常由 `SERVER_PORT` 或进程管理配置控制 |
| `server.mode` | `release` | Gin 运行模式 |
| `jwt.secret` | `JWT_SECRET` 或初始化生成 | JWT 签名密钥；环境变量优先，生产和多实例必须保持一致 |
| `jwt.admin_expire_hours` | `24` | 管理员 JWT 过期小时数 |
| `jwt.user_expire_hours` | `168` | User JWT 过期小时数，管理端和用户端共用 |
| `rate_limit.enabled` | `true` | 是否启用限流 |
| `rate_limit.global_per_min` | `1000` | 全局每分钟限制；`0` 表示关闭该维度 |
| `rate_limit.per_token_per_min` | `60` | Token 每分钟限制；`0` 表示关闭该维度 |
| `rate_limit.per_ip_per_min` | `30` | IP 每分钟限制；`0` 表示关闭该维度 |
| `relay.timeout` | `120` | 下游超时秒数 |
| `relay.retry_count` | `0` | 默认不自动重试；大于 0 时非流式仅对可安全重试错误换候选 |
| `relay.error_auto_ban` | `true` | 是否按 `error_count` 自动排除故障通道 |
| `relay.error_ban_threshold` | `10` | 自动排除通道的连续错误阈值 |
| `relay.log_body_max_bytes` | `0` | Relay 请求/响应 body 日志上限，`0` 表示不记录 |
| `billing.default_ratio` | `1.0` | 默认计费倍率 |
| `log.body_max_bytes` | `0` | 通用日志 body 上限，`0` 表示不记录 |

配置缓存：

- 启动时加载 settings 到 Redis Hash。
- 读取配置时优先查 Redis。
- 修改配置后写 DB 并刷新 Redis。
- SQLite 单镜像模式下 Redis 不可用时可退回 DB 或进程内缓存，并打告警日志。
- 外部数据库或集群模式下 Redis 不可用时应不就绪，不应长期依赖各实例本地缓存。

配置变更流程：

1. 新增配置 key 时同时写清分类、默认值、类型、校验器、敏感级别和生效方式。
2. 初始化或迁移只补缺失 key，不覆盖已有管理员配置。
3. 管理端修改配置时先做类型和业务校验，校验失败不得写入 DB。
4. 写入成功后刷新 Redis 缓存；缓存刷新失败时返回可感知错误或进入明确降级状态。
5. 关键配置变更写入管理审计日志，审计内容只保存变更摘要和脱敏值。
6. 需要重启的配置必须在响应中标记 `restart_required=true`，不能让用户误以为已经热生效。

配置审计建议字段：

| 字段 | 说明 |
|------|------|
| `actor_user_id` | 操作管理员 |
| `key` | settings key |
| `category` | 配置分类 |
| `old_value_summary` | 旧值摘要，敏感值脱敏 |
| `new_value_summary` | 新值摘要，敏感值脱敏 |
| `change_reason` | 可选变更原因 |
| `request_id` | 关联 HTTP 请求 |
| `created_at` | 变更时间 |

## Docker 部署建议

直接启动单镜像：

```text
docker run -p 3000:3000 routerx
```

该模式不配置 `SQL_DSN`，默认使用容器内 SQLite 路径；生产长期使用时应挂载数据目录。该模式不要求 Redis，不支持多实例共享写入，也不承诺集群缓存一致性。

数据库 + Redis 示例：

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

日志字段、审计动作、指标目录、告警、保留和脱敏契约以 `docs/OBSERVABILITY.md` 为准。本文保留运维侧摘要。

日志数据库：

- `LOG_SQL_DSN` 为空时，模型调用日志写入主业务数据库。
- `LOG_SQL_DSN` 非空时，模型调用日志、诊断快照和可清理历史日志可以写入独立日志数据库。
- 独立日志数据库适合单独备份、冷热分层、按周期归档和清理。
- 扣费事务、用户余额变化和 Key 预算消耗的最小结算事实必须保留在主业务数据库或主库 outbox 中，不能只依赖日志数据库。
- 日志数据库不可用时，系统应能进入明确降级状态：要么拒绝产生不可解释账单，要么先写主库 outbox 并异步补写日志。

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
- 开启 body 日志时按 `log.body_max_bytes` 或 `relay.log_body_max_bytes` 截断。

## 指标设计

完整指标目录、标签控制和告警建议以 `docs/OBSERVABILITY.md` 为准。

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

当前 `/ready` 已检查数据库连通性，以及初始化后 `jwt.secret` 是否可用。目标生产就绪检查应继续补充：

- 迁移状态不是 dirty。
- 生产模式下 `JWT_SECRET` 或数据库 `jwt.secret` 可用且跨实例一致。
- 生产模式下 `ENCRYPTION_KEY` 或 KMS 可用，且能解密已有 `enc:v1:` 密文。
- Redis 策略符合当前模式：SQLite 单镜像可无 Redis；外部数据库和集群模式必须 Redis 可用。
- 必要 settings 已加载，关键配置值格式合法。

Redis 失败处理：

- `/health` 可以仍然健康。
- `/ready` 在外部数据库、生产或集群模式下应返回不就绪，避免流量进入功能受限实例。
- 如果当前是 SQLite 单镜像模式，Redis 不可用不影响 `/ready`，但相关缓存和限流只能使用进程内策略。

## 安全基线

安全威胁模型、信任边界和默认控制点以 `docs/SECURITY.md` 为准。本文保留运维侧需要落地的密钥、HTTP、数据、故障和发布检查要求。

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
- 当前加密格式使用 `enc:v1:` 前缀；未配置 `ENCRYPTION_KEY` 时新密钥可能以兼容方式保存，生产必须通过发布检查和 `/ready` 目标检查阻止这种状态接收流量。
- 支付密钥应通过环境变量、KMS 或加密配置提供，禁止写入前端响应、日志或明文配置文件。
- API Key 明文只返回一次，数据库保存 SHA256 哈希；后续重点是存量明文迁移兜底、缓存失效和审计。
- 管理端任何响应不得返回完整下游 API Key。

密钥生命周期：

| 密钥 | 创建 | 轮换 | 丢失影响 |
|------|------|------|----------|
| `jwt.secret` | 初始化或 `JWT_SECRET` 注入 | 支持双签或短窗口强制重新登录 | 旧 JWT 失效，跨实例登录异常 |
| `ENCRYPTION_KEY` | 环境变量或 KMS 注入 | 目标支持逐条解密后重加密，更新 `enc:v*` 版本 | 已加密下游密钥不可解密 |
| API Key 明文 | 用户创建时生成一次 | 用户重新创建或禁用旧 key | 明文不可恢复，只能重建 |
| 下游 API Key | 管理员配置 | 新旧通道或同通道多 key 灰度切换 | 影响对应通道调用 |
| 支付密钥 | 支付 provider 后台生成 | 按 provider 规则双密钥或短暂停机切换 | 回调校验失败，不能入账 |

### HTTP 安全

建议：

- 生产启用 HTTPS。
- 管理端和用户端共用 User JWT，前端需要采用统一安全存储策略。
- 限制请求体大小。
- 对登录、注册、验证码、OAuth callback 添加限流。

### 数据安全

建议：

- 密码使用 bcrypt，成本值可配置。
- 日志默认不存完整提示词和响应。
- 支持按用户删除或匿名化日志中的敏感内容。
- 管理端导出数据需要审计。

## 故障处理

错误 code、HTTP 状态、重试和扣费语义以 `docs/ERRORS.md` 为准；协议兼容、APIType、流式阶段和 provider 能力等级以 `docs/PROTOCOLS.md` 为准；具体可执行排查路径以 `docs/RUNBOOKS.md` 为准。本文保留运维排障摘要。

| 故障 | 处理 |
|------|------|
| DB 不可用 | 服务启动失败；运行中返回 500/503，触发告警 |
| Redis 不可用 | SQLite 单镜像可降级；外部数据库或集群模式应不就绪，关键限流 fail-closed |
| 下游 401/403 | 标记通道配置错误，不重试，通知管理员 |
| 下游 429 | 可切换其他通道；增加通道错误统计 |
| 下游 5xx | 非流式可重试其他通道；流式已输出则结束并记录错误 |
| 通道全部不可用 | 返回 502，记录失败日志 |
| 余额不足 | 返回 429，不调用下游 |
| 迁移失败 | 启动失败，保留 dirty 状态，人工介入修复 |

开箱故障定位顺序：

1. 看 `/ready`：确认 DB、初始化状态、JWT 和关键 settings 可用。
2. 看用户和 API Key：确认用户启用、Token 未禁用或过期、额度足够。
3. 看通道和协议矩阵：确认通道启用、模型匹配、provider adapter 存在、能力等级支持该 APIType、密钥可解密。
4. 看下游桩或真实上游：区分通道配置错误、上游 401/403、上游 429/5xx 和超时。
5. 看日志账单：确认失败是否应扣费、成功是否写入 usage 和 `quota_used`。
6. 看脱敏：确认错误响应、调用日志和应用日志不包含 API Key、下游密钥或 DSN。

降级原则：

- 开箱路径优先保持清晰失败，不为了继续服务而隐藏关键错误。
- Redis 不可用时，SQLite 单镜像的缓存和非关键限流可降级；外部数据库或集群模式不应继续接收流量。
- 单个通道不可用时可以重试其他候选通道；所有通道不可用时返回协议兼容的上游错误。
- 计费规则不可用或无法解释时，不能继续产生不可追溯账单。
- 支付回调校验失败时只能记录事件和拒绝入账，不能为了用户体验提前加额度。

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
- SQLite 单镜像模式确认无需 Redis；外部数据库或集群模式确认 Redis 可用。
- 数据库已备份。
- 待执行迁移已在 staging 验证。
- 下游通道 API Key 未出现在日志中。
- `/health` 和 `/ready` 正常。
- 管理员账号至少一个可用。
- 回滚方案明确。
