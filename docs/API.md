# RouterX API 设计

## API 分层

RouterX 对外提供三类 API。

API 设计遵循“默认简单、扩展明确”的原则。初始化、账号、通道、API Key、日志和基础模型调用构成开箱路径；支付、多协议高级接口、企业账号等能力作为目标扩展分阶段接入。模型转发 API 必须优先兼容调用方 SDK，不能为了统一管理端响应而破坏 OpenAI、Gemini 或 Anthropic 的错误格式。

Apifox 可导入文档位于 `docs/apifox/openapi.yaml`。该文件只整理当前后端真实注册的 API，目标扩展接口在完成路由和实现前不会写入 OpenAPI paths，避免误导调用方。

开箱路径所需接口必须优先稳定：

```text
/v0/setup/status
/v0/setup/init
/v0/user/login
/v0/admin/channel
/v0/user/token
/v1/models
/v1/chat/completions
/v0/user/log
/v0/user/billing
```

这些接口的权限、状态码、错误响应和敏感信息脱敏规则必须优先于目标扩展接口收口。

调用方接入、SDK 兼容、API Key 使用和迁移体验以 `docs/DEVELOPER_EXPERIENCE.md` 为准。API Key 生命周期、轮换、泄露处理、作用域和高级管理以 `docs/API_KEYS.md` 为准。策略决策、访问控制、限流、分组和路由偏好冲突规则以 `docs/POLICIES.md` 为准。入口协议、APIType、上游厂商和能力等级以 `docs/PROTOCOLS.md` 为准。错误 code、HTTP 状态、协议外形、重试、扣费和日志语义以 `docs/ERRORS.md` 为准。本文只保留接口层最常用的错误格式和状态码说明。

开箱 API 验收矩阵：

| 步骤 | 接口 | 成功证据 | 失败证据 |
|------|------|----------|----------|
| 初始化状态 | `GET /v0/setup/status` | 返回 `initialized=false/true` | DB 不可用时返回明确错误 |
| 首次初始化 | `POST /v0/setup/init` | 创建超级管理员、本地身份和默认 `settings` | 已初始化、弱密码、重复用户名返回冲突或参数错误 |
| 登录 | `POST /v0/user/login` | 返回 User JWT 和用户摘要 | 账号或凭据错误不泄露账号存在性 |
| 创建 API Key | `POST /v0/user/token` | 返回一次性 `sk-` 明文，后续列表不再展示明文 | 用户禁用、额度不足或参数错误时不创建 Key |
| 创建通道 | `POST /v0/admin/channel` | 通道启用，密钥已加密或响应已脱敏 | 密钥缺失、类型不支持、模型配置非法时失败 |
| 模型列表 | `GET /v1/models` | 有效 API Key 返回兼容模型列表 | 无效 Key 返回 401，余额不足返回 429，无通道返回兼容错误 |
| 模型调用 | `POST /v1/chat/completions` | 返回 SDK 兼容响应或 SSE，写日志并扣额度 | 请求非法、无通道、上游失败和余额不足均返回协议兼容错误 |
| 日志账单 | `GET /v0/user/log`、`GET /v0/user/billing` | 用户能看到自己的调用和额度变化 | 普通用户不能查看他人日志 |

| 类型 | 前缀 | 受众 | 响应格式 |
|------|------|------|----------|
| 系统初始化 | `/v0/setup` | 初始化页面 | RouterX 统一响应 |
| 管理端 API | `/v0/admin` | 管理员后台 | RouterX 统一响应 |
| 用户端 API | `/v0/user` | 用户控制台 | RouterX 统一响应 |
| 模型转发 API | `/v1` | OpenAI、Gemini、Anthropic SDK 和外部调用方 | 对应兼容格式响应 |

入口鉴权边界：

| 入口 | 凭据 | 允许 | 禁止 |
|------|------|------|------|
| `/v0/setup/*` | 无，受初始化状态限制 | 查询初始化状态；未初始化时创建第一个超级管理员 | 已初始化后重复初始化 |
| `/v0/user/*` | User JWT，注册/登录除外 | 普通用户管理自己的资料、API Key、日志和账单 | 查看他人数据、调整自身额度或无限 Key |
| `/v0/admin/*` | User JWT + 管理员角色 | 管理用户、通道、日志、看板；超级管理员管理 settings 和管理员账号 | 使用 API Key 调用；普通管理员执行超级管理员操作 |
| `/v1/*` | API Key | 模型调用和模型列表 | 使用 User JWT；修改任何 `/v0` 资源；绕过用户/API Key/通道/额度策略 |
| `/v0/payment/*/webhook` | Provider 签名或 provider 规则 | 处理可信支付回调并幂等入账 | 信任客户端额度、跳过金额和订单校验 |

接口状态说明：

| 状态 | 含义 |
|------|------|
| 已实现 | 当前代码已有可用实现和路由注册 |
| 基础实现 | 当前代码已有最小闭环，但仍需补齐商业级边界或兼容细节 |
| 已注册 | 路由已注册，具体协议能力随 adapter 和 service 阶段完善 |
| 目标扩展 | 产品设计需要，但当前代码尚未形成完整接口闭环 |

## 通用响应

管理端、用户端和初始化接口使用统一响应结构。

成功响应：

```json
{
  "success": true,
  "message": "",
  "data": {}
}
```

失败响应：

```json
{
  "success": false,
  "message": "错误描述",
  "data": null
}
```

模型转发 `/v1/*` 不使用 RouterX 统一响应，应该按路由保持 OpenAI、Gemini 或 Anthropic 兼容响应和错误结构。

## 错误码约定

| HTTP 状态 | 场景 |
|-----------|------|
| 400 | 参数错误、请求体无法解析 |
| 401 | 未登录、JWT 过期、API Key 无效 |
| 403 | 已登录但权限不足 |
| 404 | 资源不存在 |
| 409 | 唯一性冲突、状态冲突 |
| 429 | 限流或余额不足 |
| 500 | 内部错误 |
| 502 | 下游厂商错误 |
| 504 | 下游厂商超时 |

## 模型转发错误格式

`/v1/*` 错误不能返回 RouterX 管理端统一响应，必须按入口协议返回兼容错误。内部错误可以统一归类，但外部结构由入口协议决定。

OpenAI-compatible 错误示例：

```json
{
  "error": {
    "message": "no available upstream channel",
    "type": "upstream_error",
    "code": "no_available_channel"
  }
}
```

Anthropic-compatible 错误示例：

```json
{
  "type": "error",
  "error": {
    "type": "upstream_error",
    "message": "no available upstream channel"
  }
}
```

Gemini-compatible 错误示例：

```json
{
  "error": {
    "code": 502,
    "message": "no available upstream channel",
    "status": "UNAVAILABLE"
  }
}
```

错误分类：

| HTTP 状态 | 内部 code 示例 | type/status 示例 | 调用方含义 |
|-----------|----------------|------------------|------------|
| 400 | `invalid_json`、`invalid_multipart`、`model_required`、`invalid_routerx_options`、`unsupported_api` | `invalid_request_error` / `INVALID_ARGUMENT` | 修正请求参数或换用已支持接口 |
| 401 | `invalid_api_key`、`expired_api_key` | `authentication_error` / `UNAUTHENTICATED` | 更换或重新创建 API Key |
| 403 | `user_disabled`、`token_forbidden`、`model_not_allowed`、`route_forbidden` | `permission_error` / `PERMISSION_DENIED` | 联系管理员调整权限或通道分组 |
| 404 | `model_not_found`、`resource_not_found` | `not_found_error` / `NOT_FOUND` | 检查模型名或资源 ID |
| 429 | `insufficient_quota`、`rate_limit_exceeded` | `rate_limit_error` / `RESOURCE_EXHAUSTED` | 充值、降低并发或等待限流窗口 |
| 502 | `no_available_channel`、`unsupported_channel`、`unsupported_multipart_channel`、`upstream_request_failed`、`upstream_secret_error`、`upstream_conversion_failed` | `upstream_error` / `UNAVAILABLE` | 管理员检查通道、密钥或上游状态 |
| 504 | `upstream_timeout` | `upstream_error` / `DEADLINE_EXCEEDED` | 重试或检查下游耗时 |

错误响应要求：

- 错误 message 面向调用方可理解，但不得泄露下游 API Key、数据库 DSN、支付密钥或内部堆栈。
- 401/403 必须区分认证失败和权限不足；但登录场景可继续使用模糊提示防止账号枚举。
- 余额不足、访问控制不通过、没有可用通道时必须在日志中记录可排障原因。
- 下游原始错误可保存脱敏摘要；对客户端返回时必须转换为当前入口协议兼容格式。
- `/v1` API Key 鉴权、用户禁用、配额预检查和基础下游错误会按入口协议返回 OpenAI-compatible、Anthropic 或 Gemini 错误外形；Anthropic/Gemini 基础非流式成功、Anthropic Messages Stream、Gemini streamGenerateContent 基础 SSE、字段降级和基础下游错误外形已有测试，更深层的原生字段保真和 SDK 行为继续按 P1 测试矩阵收敛。

## 公共接口

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/health` | 已注册 | 健康检查 |
| GET | `/ready` | 已实现 | 就绪检查，检查数据库、初始化后的 JWT 配置、关键 settings，以及已启用支付 provider 的必需密钥 |
| GET | `/metrics` | 基础实现 | Prometheus 文本指标；默认由 `observability.metrics_enabled=false` 关闭，已包含实例、Relay 日志、支付和 DB/Redis 基础指标 |
| GET | `/v0/setup/status` | 已实现 | 查询系统是否初始化 |
| POST | `/v0/setup/init` | 已实现 | 首次初始化超级管理员和默认设置 |

### `GET /v0/setup/status`

响应：

```json
{
  "success": true,
  "data": {
    "initialized": true
  },
  "message": ""
}
```

### `POST /v0/setup/init`

目标请求：

```json
{
  "username": "admin",
  "password": "password",
  "display_name": "Administrator",
  "email": "admin@example.com"
}
```

目标行为：

- 仅当系统未初始化时可调用。
- 创建 `role=2` 的超级管理员用户。
- 创建 `user_identities(method=username, provider=local)` 本地登录身份。
- 写入默认 settings。
- 优先使用 `JWT_SECRET` 写入 `jwt.secret`；未配置时仅允许初始化流程生成一次并写入数据库。

## 管理端 API

前缀：`/v0/admin`。

鉴权：

- `/v0/admin/*` 使用与 `/v0/user` 相同的 User JWT。
- 管理后台通过 `/v0/user/login` 获取 JWT，不提供独立的管理端登录和登出接口。
- 管理员接口需要用户具备管理员角色，超级管理员接口需要 `RoleSuper`。

### 用户管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/user` | 已实现 | 用户列表 |
| POST | `/v0/admin/user` | 已实现 | 创建普通用户，成功后写 `user.create` 管理审计 |
| PUT | `/v0/admin/user/:id` | 已实现 | 编辑普通用户，成功后写 `user.update`；禁用写 `user.disable`；拒绝角色变更写 `user.denied` |
| DELETE | `/v0/admin/user/:id` | 已实现 | 删除普通用户，成功后写 `user.delete` 管理审计 |
| PATCH | `/v0/admin/user/:id/quota` | 已实现 | 调整用户额度并写入 `quota_transactions` 与管理审计，可选 `reason` |

列表查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int | 页码，默认 1 |
| `page_size` | int | 每页数量，默认 20，最大 100 |
| `keyword` | string | 用户名、显示名、邮箱、手机号搜索 |
| `role` | int | 用户角色过滤 |
| `status` | int | 状态过滤 |
| `group_id` | uint | 分组过滤 |

创建用户目标请求：

```json
{
  "username": "alice",
  "password": "password",
  "display_name": "Alice",
  "email": "alice@example.com",
  "role": 0,
  "quota": 100000000,
  "group_id": null
}
```

### 充值码管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/redem` | 基础实现 | 充值码列表，支持 `page`、`page_size`、`status`、`keyword` |
| POST | `/v0/admin/redem` | 基础实现 | 生成随机充值码，或通过 `codes` 导入指定充值码，成功后按码写管理审计 |
| PATCH | `/v0/admin/redem/:id/disable` | 基础实现 | 作废未使用充值码；作废后用户不可兑换，成功后写管理审计 |

创建/导入充值码请求：

```json
{
  "quota": 100000000,
  "count": 10,
  "codes": ["OFFLINE-CREDIT-1"]
}
```

当 `codes` 为空时按 `count` 生成随机充值码，`count` 默认 1，最大 100；当 `codes` 非空时导入指定码。

### 支付商品管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/payment/products` | 基础实现 | 支付商品列表，支持 `page`、`page_size`、`keyword`、`enabled` |
| POST | `/v0/admin/payment/products` | 基础实现 | 创建支付商品；金额、币种、额度和 provider 配置均由服务端保存，成功后写管理审计 |
| PUT | `/v0/admin/payment/products/:id` | 基础实现 | 更新支付商品；已创建订单继续使用订单快照，成功后写管理审计 |
| PATCH | `/v0/admin/payment/products/:id/disable` | 基础实现 | 禁用商品；禁用后用户侧不可见且不能创建新订单，成功后写管理审计 |
| PATCH | `/v0/admin/payment/products/:id/enable` | 基础实现 | 启用商品，成功后写管理审计 |

创建/更新支付商品请求：

```json
{
  "product_id": "quota_100",
  "name": "100 credits",
  "amount": "9.99",
  "currency": "usd",
  "quota": 100000000,
  "bonus_quota": 0,
  "enabled": true,
  "provider_config_json": {
    "stripe_price_id": "price_123"
  }
}
```

### 管理审计

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/audit` | 基础实现 | 超级管理员查询管理审计日志，支持按操作者、动作和资源过滤 |

查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int | 页码，默认 1 |
| `page_size` | int | 每页数量，默认 20，最大 100 |
| `action` | string | 动作过滤，例如 `payment_product.create` 或 `channel.update` |
| `resource_type` | string | 资源类型过滤，例如 `payment_product` 或 `channel` |
| `resource_id` | string | 资源 ID 过滤 |
| `actor_user_id` | uint | 操作人 ID 过滤 |
| `result` | string | 结果过滤，例如 `success`、`failed`、`denied` |
| `error_code` | string | 失败或拒绝 code 过滤，例如 `api_key_quota_edit_forbidden` |
| `start_time` | int64 | 起始 Unix 秒，按 `created_at >= start_time` 过滤 |
| `end_time` | int64 | 结束 Unix 秒，按 `created_at <= end_time` 过滤 |

当前基础实现会记录以下管理动作：

| 动作 | 触发接口 |
|------|----------|
| `payment_product.create` | `POST /v0/admin/payment/products` |
| `payment_product.update` | `PUT /v0/admin/payment/products/:id` |
| `payment_product.disable` | `PATCH /v0/admin/payment/products/:id/disable` |
| `payment_product.enable` | `PATCH /v0/admin/payment/products/:id/enable` |
| `payment_order.create` | `POST /v0/user/payment/orders` |
| `api_key.created` | `POST /v0/user/token` |
| `api_key.updated` | `PUT /v0/user/token/:id` 编辑名称或过期时间 |
| `api_key.disabled` | `PUT /v0/user/token/:id` 将 Key 状态改为禁用，或 `POST /v0/user/token/:id/disable` |
| `api_key.deleted` | `DELETE /v0/user/token/:id` |
| `api_key.rotated` | `POST /v0/user/token/:id/rotate` |
| `api_key.leak_reported` | `POST /v0/user/token/:id/report-leak` |
| `api_key.scope_updated` | `PUT /v0/user/token/:id/scope` |
| `api_key.batch_disabled` | `POST /v0/admin/token/batch-disable` |
| `api_key.batch_expired` | `POST /v0/admin/token/batch-expire` |
| `api_key.quota_limit_denied` | 用户端尝试通过 `PUT /v0/user/token/:id` 修改额度或无限标记被拒绝 |
| `setting.create` | `PUT /v0/admin/setting` 新增 key |
| `setting.update` | `PUT /v0/admin/setting` 修改已有 key |
| `user.create` | `POST /v0/admin/user` |
| `user.update` | `PUT /v0/admin/user/:id` |
| `user.disable` | `PUT /v0/admin/user/:id` 将用户状态改为禁用 |
| `user.delete` | `DELETE /v0/admin/user/:id` |
| `user.denied` | 用户管理接口拒绝角色变更 |
| `user.quota_update` | `PATCH /v0/admin/user/:id/quota` |
| `redem_code.create` | `POST /v0/admin/redem` |
| `redem_code.disable` | `PATCH /v0/admin/redem/:id/disable` |
| `redem_code.redeem` | `POST /v0/user/redem` 成功兑换充值码 |
| `admin.create` | `POST /v0/admin/admin` |
| `admin.update` | `PUT /v0/admin/admin/:id` |
| `admin.disable` | `PUT /v0/admin/admin/:id` 将管理员状态改为禁用 |
| `admin.delete` | `DELETE /v0/admin/admin/:id` |
| `admin.denied` | 普通管理员访问超级管理员管理接口被拒绝 |
| `channel.create` | `POST /v0/admin/channel` |
| `channel.update` | `PUT /v0/admin/channel/:id` |
| `channel.delete` | `DELETE /v0/admin/channel/:id` |
| `channel.disable` | `PATCH /v0/admin/channel/:id/disable` |
| `channel.enable` | `PATCH /v0/admin/channel/:id/enable` |
| `channel.test` | `POST /v0/admin/channel/:id/test` |
| `channel.fetch_models` | `GET /v0/admin/channel/:id/models` |
| `log.clear` | `DELETE /v0/admin/log` |

审计摘要只保存脱敏后的变更摘要，不保存完整请求体、支付密钥、JWT secret、API Key 或 provider secret。

### 管理员管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/admin` | 已实现 | 管理员列表，管理员及以上可查看 |
| POST | `/v0/admin/admin` | 已实现 | 创建管理员，仅超级管理员；成功后写 `admin.create`，越权拒绝写 `admin.denied` |
| PUT | `/v0/admin/admin/:id` | 已实现 | 编辑管理员，仅超级管理员；成功后写 `admin.update`，禁用写 `admin.disable` |
| DELETE | `/v0/admin/admin/:id` | 已实现 | 删除管理员，仅超级管理员；成功后写 `admin.delete` |

权限规则：

- 只有超级管理员可以管理管理员账号。
- 普通管理员不能创建超级管理员。
- 不能删除自己。
- 不能删除最后一个超级管理员。

### 通道管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/channel` | 已实现 | 通道列表 |
| POST | `/v0/admin/channel` | 已实现 | 创建通道，成功后写 `channel.create` 管理审计 |
| PUT | `/v0/admin/channel/:id` | 已实现 | 编辑通道，成功后写 `channel.update` 管理审计 |
| DELETE | `/v0/admin/channel/:id` | 已实现 | 删除通道，成功后写 `channel.delete` 管理审计 |
| PATCH | `/v0/admin/channel/:id/disable` | 已实现 | 禁用通道，成功后写 `channel.disable` 管理审计 |
| PATCH | `/v0/admin/channel/:id/enable` | 已实现 | 启用通道，成功后写 `channel.enable` 管理审计 |
| POST | `/v0/admin/channel/:id/test` | 基础实现 | 测试通道连通性，并写 `channel.test` 管理审计 |
| GET | `/v0/admin/channel/:id/models` | 基础实现 | 从上游拉取模型列表，并写 `channel.fetch_models` 管理审计 |

创建通道目标请求：

```json
{
  "type": 1,
  "name": "openai-main",
  "models": "gpt-4o,gpt-4o-mini",
  "base_url": "https://api.openai.com",
  "api_key": "sk-...",
  "base_urls": ["https://api.openai.com"],
  "api_keys": ["sk-..."],
  "key_selection_mode": "round_robin",
  "model_rewrites": {
    "gpt-4o-mini": "upstream-model"
  },
  "group": "default",
  "upstream_options": {},
  "priority": 100,
  "weight": 10,
  "status": 1
}
```

通道测试目标响应：

```json
{
  "success": true,
  "data": {
    "ok": true,
    "latency_ms": 320,
    "models": ["gpt-4o", "gpt-4o-mini"]
  },
  "message": ""
}
```

### 日志、统计和设置

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/log` | 已实现 | 调用日志列表 |
| DELETE | `/v0/admin/log` | 基础实现 | 按 `before` 清理日志，成功后写 `log.clear` 管理审计 |
| GET | `/v0/admin/dashboard` | 基础实现 | 仪表盘统计 |
| GET | `/v0/admin/setting` | 已实现 | 获取系统设置，仅超级管理员 |
| PUT | `/v0/admin/setting` | 已实现 | 批量更新系统设置，仅超级管理员，成功后按 key 写管理审计 |

日志查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int | 页码 |
| `page_size` | int | 每页数量 |
| `user_id` | uint | 用户过滤 |
| `token_id` | uint | API Key 过滤 |
| `channel_id` | uint | 通道过滤 |
| `model` | string | 模型过滤 |
| `status` | int | 状态过滤 |
| `start_at` | string | 开始时间 |
| `end_at` | string | 结束时间 |

日志响应字段至少包含 `user_id`、`token_id`、`channel_id`、`model`、usage、`quota_used`、`status`、`request_id`、`error_code`、`error_msg`、`ip` 和 `created_at`。`request_id` 用于关联 HTTP 访问日志和审计日志；`error_code` 成功调用为空，失败调用按 `docs/ERRORS.md` 使用稳定 code。

删除日志目标要求：

- 必须支持时间范围。
- 默认拒绝无条件全表清理。
- 成功后写 `log.clear` 管理审计，摘要保存本次清理的 `before` 截止时间。

## 用户端 API

前缀：`/v0/user`。

鉴权：

- 注册和登录不需要 User JWT，但需要系统已初始化。
- 自部署商业级目标默认关闭公开自助注册；`POST /v0/user/register` 只有在注册开关开启时可用。
- 管理员创建用户不受自助注册开关影响。
- 个人信息、日志和账单需要 User JWT。

### 认证和个人信息

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| POST | `/v0/user/register` | 基础实现 | 用户名密码注册 |
| POST | `/v0/user/login` | 已实现 | 用户统一登录 |
| GET | `/v0/user/self` | 已实现 | 获取个人信息 |
| PUT | `/v0/user/self` | 已实现 | 修改个人信息 |
| POST | `/v0/user/self/password` | 已实现 | 修改密码 |

注册目标请求：

```json
{
  "username": "alice",
  "password": "password",
  "display_name": "Alice",
  "email": "alice@example.com"
}
```

登录目标响应：

```json
{
  "success": true,
  "data": {
    "token": "jwt...",
    "user": {
      "id": 10,
      "username": "alice",
      "display_name": "Alice",
      "email": "alice@example.com",
      "role": 0,
      "quota": 100000000,
      "status": 1
    }
  },
  "message": ""
}
```

### API Key

API Key 用于 `/v1/*` 模型转发鉴权。
完整生命周期、轮换、泄露处置、作用域、缓存和审计契约以 `docs/API_KEYS.md` 为准；本节只列当前接口和接口层边界。

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/user/token` | 已实现 | 当前用户 API Key 列表 |
| POST | `/v0/user/token` | 已实现 | 创建 API Key，明文只返回一次，成功后写 `api_key.created` 审计 |
| PUT | `/v0/user/token/:id` | 已实现 | 编辑 API Key 名称、状态、过期时间；普通编辑写 `api_key.updated`，禁用写 `api_key.disabled`，额度/无限标记编辑拒绝写 `api_key.quota_limit_denied` |
| DELETE | `/v0/user/token/:id` | 已实现 | 删除 API Key，成功后写 `api_key.deleted` 审计 |
| POST | `/v0/user/token/:id/disable` | 已实现 | 禁用自己的 API Key，可记录禁用原因，成功后写 `api_key.disabled` 审计 |
| POST | `/v0/user/token/:id/rotate` | 已实现 | 创建替换 Key、返回新明文一次、写入 `rotated_from_id` 并禁用旧 Key，成功后写 `api_key.rotated` 审计 |
| POST | `/v0/user/token/:id/report-leak` | 已实现 | 上报泄露并立即禁用 Key，返回替换建议，成功后写 `api_key.leak_reported` 审计 |
| PUT | `/v0/user/token/:id/scope` | 基础实现 | 更新自己的 Key 收窄 scope，当前支持 `allow_models`、`api_types`、`channel_groups`、`entry_protocols`、`ip_cidrs`、`methods`、`daily_quota`、`monthly_quota`、`max_concurrency`、`rpm` 和 `tpm`；成功后写 `api_key.scope_updated` 审计 |
| GET | `/v0/user/token/:id/usage` | 已实现 | 返回该 Key 的调用数、成功/失败数、额度消耗、总 tokens 和最近调用摘要 |
| GET | `/v0/admin/token` | 已实现 | 管理员跨用户查询脱敏 API Key 摘要，可按 `user_id` 和 `status` 过滤 |
| POST | `/v0/admin/token/batch-disable` | 已实现 | 管理员按 `token_ids` 或 `user_id` 批量禁用 Key，必须提供筛选条件，成功后写 `api_key.batch_disabled` 审计 |
| POST | `/v0/admin/token/batch-expire` | 已实现 | 管理员按 `token_ids` 或 `user_id` 立即过期 Key，必须提供筛选条件，成功后写 `api_key.batch_expired` 审计 |

用户端 API Key 不允许直接编辑最大消耗额度和无限额度标记，避免普通用户绕过预算策略；拒绝记录会写入管理审计，审计摘要不包含完整 API Key 明文或哈希。当前 scope 请求格式：

```json
{
  "allow_models": ["gpt-4o-mini", "gpt-4o"],
  "api_types": ["openai.chat", "openai.embeddings"],
  "channel_groups": ["default", "cheap"],
  "entry_protocols": ["openai", "anthropic"],
  "ip_cidrs": ["203.0.113.10", "198.51.100.0/24"],
  "methods": ["POST /v1/chat/completions", "GET /v1/models"],
  "daily_quota": 100000,
  "monthly_quota": 3000000,
  "max_concurrency": 2,
  "rpm": 60,
  "tpm": 100000
}
```

`allow_models` 为空或 scope 为空时继承用户和系统策略；非空时只允许列表内模型，拒绝返回 `model_not_allowed` 且不调用上游。`api_types` 为空时不按接口能力额外收窄；非空时只允许列出的 APIType，未命中返回 `token_forbidden` 且不调用上游。`channel_groups` 为空时不按通道分组额外收窄；非空时只允许候选通道落在列表内，未命中返回 `route_forbidden`。`entry_protocols` 为空时不按入口协议额外收窄；非空时只允许 `openai`、`anthropic`、`gemini` 或 `*` 命中，未命中返回当前入口协议兼容的 `token_forbidden` 且不调用上游。`ip_cidrs` 为空时不限制来源 IP；非空时只允许命中的单 IP 或 CIDR，未命中返回 `token_forbidden`。`methods` 为空时不按路径额外收窄；非空时只允许 `METHOD path` 命中，未命中返回 `token_forbidden`。`daily_quota` 为空时不设日预算，`monthly_quota` 为空时不设月预算；非空时分别按当天或当月成功日志已消耗额度拦截，到达上限返回 `insufficient_quota`。`max_concurrency` 为空时不设单 Key 并发上限；非空时限制同一 Key 同时在途请求数，达到上限返回 `rate_limit_exceeded`。`rpm` 为空时不设单 Key 每分钟请求上限，`tpm` 为空时不设单 Key 每分钟模型 token 上限；非空时达到上限返回 `rate_limit_exceeded`。

### 用量和账单

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/user/log` | 已实现 | 当前用户调用日志 |
| GET | `/v0/user/billing` | 基础实现 | 当前用户账单统计 |
| POST | `/v0/user/redem` | 基础实现 | 使用未兑换充值码给当前用户增加额度，并写入 `quota_transactions` 幂等流水与 `redem_code.redeem` 管理审计 |
| GET | `/v0/user/models` | 基础实现 | 当前启用通道暴露的可用模型列表；价格表未接入时 `pricing_ready=false` |

### 支付接口

支付接口用于用户在线购买额度。支付 provider、充值码、退款、人工补账和额度流水契约以 `docs/PAYMENTS.md` 为准；本文只定义接口外形和鉴权边界。当前用户侧基础实现已支持商品列表、创建本地 `pending` 订单、订单列表和详情；Stripe webhook 已支持原始 body 签名、Checkout Session 成功事件、金额/币种/metadata 校验、幂等入账，以及全额退款事件和可选自动扣回；易支付异步通知已支持 MD5 签名、金额校验和幂等入账，同步返回页仅展示本地订单状态。真实 Stripe Checkout Session 创建、部分退款、争议和完整审计仍属于后续能力。

用户鉴权接口：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v0/user/payment/products` | 获取可购买的充值商品 |
| POST | `/v0/user/payment/orders` | 创建本地 `pending` 支付订单并写 `payment_order.create` 管理审计；provider 必须已在 settings 启用，易支付配置齐全时返回签名收银台 URL，否则返回安全 checkout 占位链接；`expires_at` 来自 `payment.order_expire_minutes` |
| GET | `/v0/user/payment/orders` | 查询当前用户支付订单列表 |
| GET | `/v0/user/payment/orders/:order_no` | 查询当前用户支付订单详情 |

Provider 回调接口：

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/v0/payment/stripe/webhook` | Stripe 签名 | 基础实现；Stripe Checkout webhook，成功时幂等入账并返回纯文本 `success` |
| POST | `/v0/payment/epay/notify` | 易支付签名 | 基础实现；易支付异步通知，成功时幂等入账并返回纯文本 `success` |
| GET | `/v0/payment/epay/return` | 无，仅读状态 | 基础实现；易支付同步返回页，只读取本地订单状态，不入账 |

创建支付订单请求：

```json
{
  "provider": "stripe",
  "product_id": "quota_100",
  "pay_type": "card",
  "return_url": "https://app.example.com/billing/result"
}
```

字段说明：

| 字段 | 说明 |
|------|------|
| `provider` | 支付渠道，支持 `stripe`、`epay` |
| `product_id` | 充值商品 ID，服务端据此确定金额、货币和额度 |
| `pay_type` | 易支付支付方式，如 `alipay`、`wxpay`、`qqpay`；Stripe 可忽略或使用 `card` |
| `return_url` | 支付完成后的前端跳转地址，服务端需校验白名单 |

创建支付订单响应：

```json
{
  "success": true,
  "data": {
    "order_no": "pay_20260611123456789",
    "provider": "stripe",
    "status": "pending",
    "amount": "9.99",
    "currency": "usd",
    "quota": 1000000000,
    "checkout_url": "https://checkout.stripe.com/c/...",
    "expires_at": "2026-06-11T16:00:00Z"
  },
  "message": ""
}
```

易支付订单响应可以返回 `pay_url` 或自动提交表单参数：

```json
{
  "success": true,
  "data": {
    "order_no": "pay_20260611123456789",
    "provider": "epay",
    "status": "pending",
    "amount": "50.00",
    "currency": "cny",
    "quota": 5000000000,
    "pay_url": "https://pay.example.com/submit.php?...",
    "method": "GET"
  },
  "message": ""
}
```

订单状态：

| 状态 | 说明 |
|------|------|
| `pending` | 已创建，等待支付 |
| `paid` | 支付成功，额度已入账 |
| `failed` | 支付失败或通知校验失败 |
| `closed` | 超时关闭或用户取消 |
| `refunded` | 已退款，是否扣回额度由后续策略决定 |

Stripe Webhook 要求：

- 使用原始 body 和 `Stripe-Signature` 校验签名。
- 只在 `checkout.session.completed` 或可信成功事件后入账。
- 校验 `metadata.order_no`、金额、货币和订单状态。
- 同一个 Stripe event id 必须幂等处理。
- `charge.refunded` 全额退款事件会把订单置为 `refunded`；开启 `payment.refund.auto_deduct=true` 时按策略扣回额度并写 `refund_deduct` 流水。

易支付通知要求：

- 校验易支付签名，排除 `sign`、`sign_type` 和空值字段后按网关规则生成签名。
- 校验 `pid`、`out_trade_no`、`money` 和成功状态。
- 入账只依赖异步通知；同步返回页只展示本地订单状态。
- 通知处理成功后返回网关要求的纯文本，例如 `success`。
- 当前实现从 `PAYMENT_EPAY_KEY` 读取签名密钥，金额匹配且订单为 `pending` 时才把订单置为 `paid`、写 `payment_events`、写 `quota_transactions` 并增加用户额度；重复通知不会重复入账。

安全要求：

- 客户端不能提交要增加的 `quota`，入账额度只能来自服务端商品配置。
- 支付成功更新订单和增加 `users.quota` 必须在同一事务中完成。
- 支付密钥、Stripe secret、Stripe webhook secret、易支付商户 key 不得返回给前端或写入日志明文。
- 回调接口不使用用户 JWT，但必须通过 provider 签名、订单金额和订单状态校验。

## 模型转发 API

前缀：`/v1`。

协议兼容矩阵、当前等级、路径冲突、流式阶段和新增 provider 准入清单见 `docs/PROTOCOLS.md`。本文只列接口外形、鉴权边界和常用错误示例。

鉴权：

```http
Authorization: Bearer sk-xxxxxxxx
```

`/v1` 需要兼容 OpenAI、Gemini、Anthropic 三类入口请求格式，并根据通道配置转发到 OpenAI、Anthropic、Gemini、xAI、Azure OpenAI、Qwen、DeepSeek、OpenAI-Compatible、RouterX-Compatible 等上游。以下仅列目标接口表。

模型转发接口不使用 RouterX 统一响应。入口协议为 OpenAI 时返回 OpenAI 兼容响应，入口协议为 Gemini 时返回 Gemini 兼容响应，入口协议为 Anthropic 时返回 Anthropic 兼容响应。

### OpenAI 格式

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/responses` | 基础实现，OpenAI-compatible Responses JSON 透传和 usage 映射 |
| POST | `/v1/chat/completions` | 基础实现，Chat Completions；支持非流式和 OpenAI-compatible SSE 流式 |
| POST | `/v1/completions` | 基础实现，Legacy Completions；支持非流式和 OpenAI-compatible SSE 流式 |
| POST | `/v1/embeddings` | 基础实现，OpenAI-compatible Embeddings JSON 透传、`routerx` 剥离和 usage 扣费 |
| POST | `/v1/images/generations` | 基础实现，OpenAI-compatible 图像生成 JSON 透传；无 usage 时按 P0 最低计费 |
| POST | `/v1/images/edits` | 基础实现，OpenAI-compatible multipart 图像表单透传、`routerx` 表单字段剥离和路由偏好；无 usage 时按 P0 最低计费 |
| POST | `/v1/images/variations` | 基础实现，OpenAI-compatible multipart 图像表单透传、`routerx` 表单字段剥离和路由偏好；无 usage 时按 P0 最低计费 |
| POST | `/v1/audio/transcriptions` | 基础实现，OpenAI-compatible multipart 音频表单透传、`routerx` 表单字段剥离和路由偏好；无 usage 时按 P0 最低计费 |
| POST | `/v1/audio/translations` | 基础实现，OpenAI-compatible multipart 音频表单透传、`routerx` 表单字段剥离和路由偏好；无 usage 时按 P0 最低计费 |
| POST | `/v1/audio/speech` | 基础实现，OpenAI-compatible 文本转语音 JSON 透传，二进制音频响应透传；无 usage 时按 P0 最低计费 |
| GET | `/v1/models` | 基础实现，模型列表 |
| GET | `/v1/models/:model` | 基础实现，模型详情 |
| POST | `/v1/moderations` | 基础实现，OpenAI-compatible Moderations JSON 透传；无 usage 时按 P0 最低计费 |

#### Chat Completions 契约

当前优先保证 OpenAI-compatible Chat 非流式闭环，并已支持 OpenAI-compatible SSE 流式的基础转发。目标请求最小格式：

```json
{
  "model": "gpt-test",
  "messages": [
    { "role": "user", "content": "hello" }
  ],
  "stream": false
}
```

成功要求：

- 返回 OpenAI-compatible Chat Completions 响应。
- `stream=true` 且命中 OpenAI SSE 形态通道时，返回 `text/event-stream` 并逐行转发 `data:` chunk。
- 保留下游返回的 `usage`，或按 P0 最低规则估算。
- 写入 `logs`，包含 user、token、channel、model、prompt/completion/total tokens、`quota_used` 和 status。
- 扣减 API Key 或用户额度，扣费失败时返回 429 并写失败日志。

P0 明确失败：

| 场景 | HTTP | code |
|------|------|------|
| 非法 JSON | 400 | `invalid_json` |
| 缺少 `model` | 400 | `model_required` |
| API Key scope 不允许该模型 | 403 | `model_not_allowed` |
| API Key scope 不允许该 APIType | 403 | `token_forbidden` |
| API Key scope 不允许该通道分组 | 403 | `route_forbidden` |
| 用户分组不允许该通道分组 | 403 | `route_forbidden` |
| API Key scope 不允许该来源 IP | 403 | `token_forbidden` |
| API Key scope 不允许该方法路径 | 403 | `token_forbidden` |
| API Key scope 达到日/月预算 | 429 | `insufficient_quota` |
| API Key scope 达到并发上限 | 429 | `rate_limit_exceeded` |
| API Key scope 达到 RPM/TPM 上限 | 429 | `rate_limit_exceeded` |
| `stream=true` 但选中通道不是 OpenAI SSE 形态 | 502 | `unsupported_stream_channel` |
| `routerx` 结构非法 | 400 | `invalid_routerx_options` |
| `routerx.route` 字段类型非法 | 400 | `invalid_routerx_route` |
| 无可用通道 | 502 | `no_available_channel` |
| `routerx.route` 筛选后无可用通道 | 502 | `no_available_channel` |
| 下游密钥不可解密或缺失 | 502 | `upstream_secret_error` |
| 余额不足 | 429 | `insufficient_quota` |

### Gemini 格式

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v1/models` | 基础实现，模型列表 |
| GET | `/v1/models/{model}` | 基础实现，模型详情 |
| POST | `/v1/models/{model}:generateContent` | 基础实现，内容生成；当前转 OpenAI-compatible Chat，非文本 parts 降级为 compact JSON 文本 |
| POST | `/v1/models/{model}:streamGenerateContent` | 基础实现，流式内容生成；当前将 Gemini 请求转 OpenAI-compatible Chat SSE，再输出 Gemini SSE 事件 |
| POST | `/v1/models/{model}:countTokens` | 基础实现，Token 计数 |
| POST | `/v1/models/{model}:embedContent` | 基础实现，Gemini embedContent 当前转 OpenAI-compatible Embeddings 上游并返回 Gemini embedding 外形 |
| POST | `/v1/models/{model}:batchEmbedContents` | 基础实现，Gemini batchEmbedContents 当前转 OpenAI-compatible Embeddings 批量 input 并返回 Gemini embeddings 外形 |

### Anthropic 格式

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/messages` | 基础实现，Messages；当前转 OpenAI-compatible Chat，非文本 content blocks 降级为 compact JSON 文本；`stream=true` 输出 Anthropic SSE 事件 |
| POST | `/v1/messages/count_tokens` | 基础实现，Token 计数 |
| GET | `/v1/models` | 基础实现，模型列表 |
| GET | `/v1/models/:model` | 基础实现，模型详情 |

### 额外参数

JSON 请求可以使用保留字段 `routerx` 传递 RouterX 路由偏好和 provider-specific 参数。该字段不会透传给真实厂商，除非上游通道也是 RouterX-Compatible。

```json
{
  "model": "gpt-4o-mini",
  "messages": [{ "role": "user", "content": "hi" }],
  "routerx": {
    "route": {
      "channel_group": "premium",
      "upstream_provider": "xai"
    },
    "upstream": {
      "headers": {},
      "query": {},
      "body": {}
    },
    "provider": {
      "openai": {},
      "anthropic": {},
      "gemini": {},
      "xai": {}
    }
  }
}
```

规则：

- 策略决策顺序、访问控制、限流、分组和冲突规则以 `docs/POLICIES.md` 为准。
- `routerx.route` 用于路由偏好，不参与模型原生请求。
- `routerx.route` 只能收窄管理员策略允许的候选通道，不能启用已禁用通道、绕过额度、绕过通道分组访问控制或强制使用无权限 provider。
- `routerx.upstream` 用于补充上游 header、query 和 body 参数，但敏感鉴权 header 必须来自通道配置，不能由用户请求覆盖。
- `routerx.provider.<provider>` 仅在选中对应上游 provider 时生效。
- multipart 或非 JSON 请求当前可通过 `routerx` 表单字段传递 JSON 字符串；`X-RouterX-Options` header 是后续扩展目标。
- 对 `GET /v1/models` 这类无 JSON body 的冲突路径，当前可使用 `?format=gemini` 或 `?format=anthropic`，并可通过 `anthropic-version` header 识别 Anthropic 格式；目标设计可扩展 `?routerx_protocol=` 或 `X-RouterX-Protocol`。

路由偏好处理：

| 场景 | 目标行为 |
|------|----------|
| 偏好合法且候选通道可用 | 进入正常通道选择，并在日志中记录偏好被接受 |
| 偏好字段未知但不影响安全 | 忽略未知字段，在日志中记录被忽略 |
| 偏好格式非法 | 返回当前入口协议兼容的 400 错误 |
| 偏好要求无权限通道或 provider | 返回当前入口协议兼容的 403 错误 |
| 偏好合法但筛选后无可用通道 | 返回当前入口协议兼容的无可用通道错误 |

安全边界：

- 客户端不能通过 `routerx.upstream.headers` 覆盖 `Authorization`、`Cookie`、`Set-Cookie`、`X-Api-Key`、`api-key` 等敏感鉴权字段。
- 客户端不能通过 `routerx.upstream.body` 覆盖 RouterX 已经完成安全决策的内部字段。
- RouterX-Compatible 上游可以继续接收 `routerx` 扩展，但真实厂商上游必须在请求发出前移除该私有字段。
- 所有路由偏好、是否命中、是否被拒绝和最终通道应进入日志或后续路由决策快照。

### 多层 RouterX

当上游通道也是 RouterX 时，需要保持兼容：

- 允许保留 `routerx` 扩展字段继续转发。
- 每层递增 `X-RouterX-Hop`，超过最大跳数时拒绝，避免循环。
- 每层透传或生成 `X-Request-Id`，便于跨层追踪。
- 转发到真实厂商前必须移除 `routerx` 私有字段和 `X-RouterX-*` 内部 header。

## API Key 生命周期

本节是接口层摘要。更完整的产品语义、数据字段、轮换、泄露处置、作用域和阶段验收见 `docs/API_KEYS.md`。

目标流程：

```text
用户登录 User JWT
    -> POST /v0/user/token
    -> 生成 sk- API Key
    -> 明文只返回一次
    -> DB 保存 SHA256 哈希
    -> Redis 缓存校验结果
    -> 调用 /v1/* 时通过 Authorization Bearer 使用
```

API Key 校验规则：

- 格式必须以 `sk-` 开头。
- API Key 不存在、禁用、软删除、过期均返回 401；数据库兼容早期明文存量，验证成功后迁移为 SHA256 哈希。
- 所属用户禁用或软删除返回 403。
- 额度不足返回 429。
- 鉴权成功后写入 `current_user` 和 `current_token` 上下文。

额度规则：

- 创建带最大消耗额度的 API Key 时，不扣减或冻结用户余额；该额度只是 Key 的预算上限。
- 有限额度 API Key 调用成功后同时扣减用户余额，并消耗 Key 剩余预算或累计已用额度。
- `unlimited=true` 或 `remain_quota=-1` 的 API Key 调用成功后只扣减用户额度。
- 普通用户不能通过编辑 API Key 接口调整最大消耗额度或无限标记，预算调整只能由管理员或后续策略流程完成。

## 分页规范

请求参数：

| 参数 | 默认 | 约束 |
|------|------|------|
| `page` | 1 | `>= 1` |
| `page_size` | 20 | `1 <= page_size <= 100` |

响应：

```json
{
  "success": true,
  "data": {
    "total": 100,
    "page": 1,
    "page_size": 20,
    "data": []
  },
  "message": ""
}
```

## 兼容性要求

- `/v1` 路由不应返回 RouterX 的 `{success,data,message}` 包装。
- `/v1` 错误应尽量兼容当前路由对应格式的错误对象。
- 能力状态以 `docs/PROTOCOLS.md` 为准；已注册路由不能在产品文案中写成完整协议兼容。
- 对未知兼容格式字段默认透传，不应无故拒绝。
- 对不支持的接口返回明确的 `404` 或当前格式兼容的 `unsupported_api` 错误。
- 管理端和用户端 API 使用 `/v0` 版本前缀，后续破坏性变更使用 `/v1/admin` 或 `/v1/user`，不与模型 API 混淆。
