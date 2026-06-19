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
| 登录 | `POST /v0/user/login` | 返回 User JWT 和用户摘要，并写入脱敏 `user.login` 审计 | 账号或凭据错误不泄露账号存在性 |
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
模型请求体超过 `relay.max_request_body_bytes` 时会在本地返回 413；OpenAI-compatible code 为 `request_body_too_large`，Anthropic/Gemini 返回各自协议错误 envelope，且不会产生上游调用或扣费。
OpenAI-compatible Images/Audio multipart 缺少必填文件字段时会在本地返回 400 `multipart_file_required`；文件字段超过 `relay.max_multipart_file_bytes` 时会在本地返回 413 `request_file_too_large`，且不会产生上游调用或扣费。
OpenAI-compatible Images/Audio multipart 文件名、扩展名或内容命中基础文件安全策略时会在本地返回 400 `unsafe_multipart_file`，例如路径形态、可执行/脚本类扩展名、图片/音频 API 不匹配的文件扩展名或文件头，或可执行/脚本类内容签名，且不会产生上游调用或扣费。

## 错误码约定

| HTTP 状态 | 场景 |
|-----------|------|
| 400 | 参数错误、请求体无法解析 |
| 401 | 未登录、JWT 过期、API Key 无效 |
| 403 | 已登录但权限不足 |
| 404 | 资源不存在 |
| 409 | 唯一性冲突、状态冲突 |
| 413 | `/v1` 模型请求体超过 `relay.max_request_body_bytes`，或 multipart 单个文件字段超过 `relay.max_multipart_file_bytes`，本地拒绝且不调用上游 |
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
| 400 | `invalid_json`、`invalid_multipart`、`invalid_chat_messages`、`invalid_gemini_embedding_request`、`invalid_image_prompt`、`invalid_image_count`、`multipart_file_required`、`unsafe_multipart_file`、`model_required`、`invalid_routerx_options`、`routerx_hop_exceeded` | `invalid_request_error` / `INVALID_ARGUMENT` | 修正请求参数 |
| 401 | `invalid_api_key`、`expired_api_key` | `authentication_error` / `UNAUTHENTICATED` | 更换或重新创建 API Key |
| 403 | `user_disabled`、`token_forbidden`、`model_not_allowed`、`route_forbidden` | `permission_error` / `PERMISSION_DENIED` | 联系管理员调整权限或通道分组 |
| 404 | `model_not_found`、`unsupported_api`、`resource_not_found` | `not_found_error` / `NOT_FOUND` | 检查模型名、接口路径或资源 ID |
| 413 | `request_body_too_large`、`request_file_too_large` | `invalid_request_error` / `RESOURCE_EXHAUSTED` | 缩小请求体或上传文件后重试 |
| 429 | `insufficient_quota`、`rate_limit_exceeded` | `rate_limit_error` / `RESOURCE_EXHAUSTED` | 充值、降低并发或等待限流窗口 |
| 502 | `no_available_channel`、`unsupported_channel`、`unsupported_api_type`、`unsupported_multipart_channel`、`upstream_request_failed`、`upstream_secret_error`、`upstream_response_too_large`、`upstream_conversion_failed`、`usage_missing` | `upstream_error` / `UNAVAILABLE` | 管理员检查通道、APIType 支持、密钥、响应大小、usage 或上游状态 |
| 503 | `rate_limit_unavailable`、`service_not_initialized` | `server_error` / `UNAVAILABLE` | 等待服务或 Redis 限流依赖恢复 |
| 504 | `upstream_timeout` | `upstream_error` / `DEADLINE_EXCEEDED` | 重试或检查下游耗时 |

错误响应要求：

- 错误 message 面向调用方可理解，但不得泄露下游 API Key、数据库 DSN、支付密钥或内部堆栈。
- 401/403 必须区分认证失败和权限不足；但登录场景可继续使用模糊提示防止账号枚举。
- 余额不足、访问控制不通过、没有可用通道时必须在日志中记录可排障原因。
- 下游原始错误可保存脱敏摘要；对客户端返回时必须转换为当前入口协议兼容格式。
- 选中通道 adapter 不支持当前 APIType 时返回 502 `unsupported_api_type`，不调用上游且不扣费。
- 下游非流式响应超过 `relay.max_response_body_bytes` 时返回 502 `upstream_response_too_large`，不反射完整下游响应体且不扣费。
- OpenAI-compatible Chat 缺少非空数组 `messages` 时返回 400 `invalid_chat_messages`，不调用上游且不扣费。
- Gemini embedContent/batchEmbedContents 缺少 `content`/`requests`、文本 parts 为空、`outputDimensionality` 非正数或同批次维度不一致时返回 400 `invalid_gemini_embedding_request`，不调用上游且不扣费。
- OpenAI-compatible Image Generations 缺少非空字符串 `prompt` 时返回 400 `invalid_image_prompt`，不调用上游且不扣费。
- OpenAI-compatible Image Generations 显式传入的 `n` 不是大于等于 1 的整数时返回 400 `invalid_image_count`，不调用上游且不扣费。
- OpenAI-compatible Images/Audio multipart 缺少必填 `image` 或 `file` 文件字段时返回 400 `multipart_file_required`，不调用上游且不扣费。
- OpenAI-compatible Images/Audio multipart 单个文件字段超过 `relay.max_multipart_file_bytes` 时返回 413 `request_file_too_large`，不调用上游且不扣费。
- OpenAI-compatible Images/Audio multipart 文件名、扩展名或内容命中基础文件安全策略时返回 400 `unsafe_multipart_file`，不调用上游且不扣费。
- `/v1` API Key 鉴权、用户禁用、配额预检查、本地解析错误、未知 `/v1` 路径、`/v1/models/{model}` 的 `model_not_found` 和基础下游错误会按入口协议返回 OpenAI-compatible、Anthropic 或 Gemini 错误外形；本地解析错误语义统一使用 `invalid_json`、`model_required` 等稳定 code，未知 `/v1` 路径在通过 API Key 鉴权后返回 OpenAI-compatible 404 `unsupported_api`。Anthropic/Gemini 基础非流式成功、Anthropic Messages 命中 Anthropic 上游的原生请求字段保真、Gemini generateContent 命中 Gemini 上游的非流式原生字段保真、Anthropic Messages Stream 到 OpenAI-compatible 与 Anthropic 上游的基础 SSE、Gemini streamGenerateContent 到 OpenAI-compatible 与 Gemini 上游的基础 SSE、字段降级和基础下游错误外形已有测试，更深层的 SDK 行为继续按 P1 测试矩阵收敛。

## 公共接口

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/health` | 已注册 | 健康检查 |
| GET | `/ready` | 已实现 | 就绪检查，检查数据库、`schema_migrations.dirty`、外部数据库模式下 Redis 可用性、初始化后的 JWT 配置、关键 auth/relay/rate-limit settings 注册表校验、已启用支付 provider 的必需密钥，以及存在 `enc:v1:` 通道密钥或外部登录 `client_secret` 时的 `ENCRYPTION_KEY` |
| GET | `/metrics` | 基础实现 | Prometheus 文本指标；默认由 `observability.metrics_enabled=false` 关闭，已包含实例、HTTP 请求量/耗时、Relay 日志、Relay 请求数、Relay/上游耗时、Relay 错误维度、token 用量、按模型/供应商/用户组的额度消耗、API Key 鉴权/生命周期/最近使用/额度/轮换/泄露指标、通道可用状态、逐通道错误计数、日志补写 outbox 状态、限流拒绝、计费失败、支付、审计和 DB/Redis/日志库 up 及 DB/Redis 错误计数等基础指标 |
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
| POST | `/v0/admin/user` | 已实现 | 创建普通用户；可提交 email/phone 作为主资料，phone 会同步创建 `phone/local` 登录标识且不保存重复密码哈希；成功后写 `user.create` 管理审计 |
| PUT | `/v0/admin/user/:id` | 已实现 | 编辑普通用户；phone 会同步维护 `phone/local` 登录标识，目标手机号已被占用时整笔拒绝；成功后写 `user.update`；禁用写 `user.disable`；拒绝角色变更写 `user.denied` |
| DELETE | `/v0/admin/user/:id` | 已实现 | 删除普通用户，成功后写 `user.delete` 管理审计 |
| PATCH | `/v0/admin/user/:id/quota` | 已实现 | 调整用户额度并写入 `quota_transactions` 与管理审计，可选 `reason` |
| GET | `/v0/admin/quota-transactions` | 已实现 | 管理员查询额度流水；支持按用户、类型、来源和时间过滤 |

### 用户分组管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/groups` | 已实现 | 用户分组列表，支持 `page`、`page_size` 和 `keyword` |
| POST | `/v0/admin/groups` | 已实现 | 创建用户分组；`ratio <= 0` 时按 `1` 保存，成功后写 `user_group.create` 管理审计 |
| PUT | `/v0/admin/groups/:id` | 已实现 | 更新用户分组名称或展示倍率；名称唯一，显式 `ratio <= 0` 会被拒绝，成功后写 `user_group.update` |
| DELETE | `/v0/admin/groups/:id` | 已实现 | 删除未使用用户分组；`default` 或仍有用户引用时拒绝，成功后写 `user_group.delete` |

`groups.ratio` 当前作为分组元数据和兼容展示字段；成功调用后的实际扣费倍率仍以 `billing.user_group_ratios`、`billing.channel_group_ratios` 和 `billing.user_group_channel_ratios` settings 为权威来源。

额度流水查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int | 页码，默认 1 |
| `page_size` | int | 每页数量，默认 20，最大 100 |
| `user_id` | uint | 管理端按用户过滤；用户端接口会忽略该参数并强制使用当前用户 |
| `type` | string | 流水类型，如 `payment_grant`、`redem_redeem`、`admin_adjust`、`refund_deduct`、`manual_credit`、`manual_debit` |
| `source_type` | string | 来源类型，如 `payment_order`、`payment_event`、`redem_code`、`admin_action`、`refund` |
| `source_id` | string | 来源 ID、本地订单号或事件 ID |
| `start_time` | string | 创建时间下限，支持 RFC3339、`YYYY-MM-DD HH:mm:ss` 或 `YYYY-MM-DD` |
| `end_time` | string | 创建时间上限，支持 RFC3339、`YYYY-MM-DD HH:mm:ss` 或 `YYYY-MM-DD` |

额度流水只表达用户余额变化；模型调用消费仍以 `logs.quota_used` 和 `/v0/user/log`、`/v0/admin/log` 为消费事实。

用户列表查询参数：

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
  "phone": "+8613800000000",
  "role": 0,
  "quota": 100000000,
  "group_id": null
}
```

管理员创建或编辑普通用户时，`phone` 会去除首尾空格并同步到该用户的 `phone/local` 登录身份；该身份不写重复密码哈希，手机号密码登录开启后复用用户的 `username/local` 主密码。目标手机号已经属于其他用户时，服务端返回 400，用户资料和 identity 都不会部分落库。

### 充值码管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/redem` | 基础实现 | 充值码列表，支持 `page`、`page_size`、`status`、`keyword`、`batch_no` |
| POST | `/v0/admin/redem` | 基础实现 | 生成随机充值码，或通过 `codes` 导入指定充值码；支持 `batch_no`、`note`、未来 `expired_at`，成功后按码写管理审计；过期时间非法、额度非法、数量超限、重复码等本地拒绝会写 `redem_code.create_denied` |
| PATCH | `/v0/admin/redem/:id/disable` | 基础实现 | 作废未使用充值码；作废后用户不可兑换，成功后写管理审计 |

创建/导入充值码请求：

```json
{
  "quota": 100000000,
  "count": 10,
  "codes": ["OFFLINE-CREDIT-1"],
  "batch_no": "launch-2026",
  "note": "private beta invite",
  "expired_at": 1798761600
}
```

`expired_at` 使用 Unix 秒；为空或 `0` 表示不过期，非空时必须是未来时间。用户兑换已过期充值码会失败，不改变余额，也不会写入额度流水。

当 `codes` 为空时按 `count` 生成随机充值码，`count` 默认 1，最大 100；当 `codes` 非空时导入指定码。

### 支付商品管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/payment/products` | 基础实现 | 支付商品列表，支持 `page`、`page_size`、`keyword`、`enabled` |
| POST | `/v0/admin/payment/products` | 基础实现 | 创建支付商品；金额、币种、额度和 provider 配置均由服务端保存，成功后写管理审计 |
| PUT | `/v0/admin/payment/products/:id` | 基础实现 | 更新支付商品；已创建订单继续使用订单快照，成功后写管理审计 |
| PATCH | `/v0/admin/payment/products/:id/disable` | 基础实现 | 禁用商品；禁用后用户侧不可见且不能创建新订单，成功后写管理审计 |
| PATCH | `/v0/admin/payment/products/:id/enable` | 基础实现 | 启用商品，成功后写管理审计 |
| POST | `/v0/admin/payment/adjustments` | 基础实现 | 支付相关人工补账或扣回；写 `manual_credit`/`manual_debit` 额度流水和 `payment_manual_adjust.*` 管理审计，默认必须填写原因；本地拒绝写 `payment_manual_adjust.denied` |
| POST | `/v0/admin/payment/refunds` | 基础实现 | 管理员确认支付退款后扣回额度，订单置为 `refunded` 或 `partially_refunded`，写 `refund_deduct` 流水和 `payment_refund.manual` 审计；本地拒绝写 `payment_refund.manual_denied` |
| POST | `/v0/admin/payment/refund-requests` | 基础实现 | 向 Stripe 或易支付发起 provider 退款请求，写 `payment_refund_requests`，订单进入 `refund_pending`，最终状态等待可信 webhook 或后续人工收尾确认 |

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

支付人工修正请求：

```json
{
  "user_id": 1001,
  "order_no": "RX202606150001",
  "amount": -100000000,
  "reason": "chargeback correction",
  "idempotency_key": "support-ticket-1234"
}
```

`amount` 为正数时写 `manual_credit`，负数时写 `manual_debit`；`order_no` 可关联原支付订单，`idempotency_key` 用于防止同一人工动作重复改变余额。缺少原因、缺少幂等键、金额为 0、重复幂等键或关联订单/权限校验失败会写 `payment_manual_adjust.denied`，使用稳定 `error_code` 区分原因。

支付人工退款请求：

```json
{
  "order_no": "RX202606150001",
  "refund_quota": 40000000,
  "reason": "customer refund",
  "idempotency_key": "refund-ticket-1234"
}
```

`refund_quota` 必须大于 0 且不能超过订单入账额度；仅支持对 `paid` 订单人工落账退款。全额退款会将订单置为 `refunded`，部分退款会置为 `partially_refunded`；接口会写 `quota_transactions(type=refund_deduct, source_type=refund, source_id=<order_no>)`，并通过 `idempotency_key` 防止重复扣回。缺少订单号、退款额度非法、缺少原因、重复幂等键、订单状态不允许或余额不足会写 `payment_refund.manual_denied`。

Provider 退款请求：

```json
{
  "order_no": "RX202606150001",
  "refund_amount": "5.00",
  "reason": "customer requested partial refund",
  "idempotency_key": "refund-ticket-5678"
}
```

`refund_amount` 为空时按订单全额退款；非空时必须大于 0 且不能超过订单金额。接口需要订单为 `paid` 状态；Stripe 订单必须已保存 `provider_payment_id`，易支付订单需要配置 `payment.epay.pid`、`payment.epay.refund_url` 和 `PAYMENT_EPAY_KEY`。成功后本地订单进入 `refund_pending`，写 `payment_refund_requests` 和 `payment_refund.requested` 审计；本地参数、订单状态或幂等键拒绝会写 `payment_refund.request_denied` 且 `result=denied`，provider 调用失败会写同动作但 `result=failed`。最终退款状态和可选额度扣回仍以可信 provider webhook 或后续人工收尾为准。

### 模型价格管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/model-prices` | 基础实现 | 系统模型价格列表，支持 `page`、`page_size`、`keyword`、`enabled` |
| POST | `/v0/admin/model-prices` | 基础实现 | 创建系统模型价格；`model` 唯一，成功后 `rule_version=1` 并写 `model_price.create` 管理审计 |
| PUT | `/v0/admin/model-prices/:id` | 基础实现 | 更新系统模型价格；每次更新递增 `rule_version` 并写 `model_price.update` 管理审计 |
| PATCH | `/v0/admin/model-prices/:id/disable` | 基础实现 | 禁用系统模型价格；用户侧模型列表回退到 `minimum_usage`，成功后写 `model_price.disable` 管理审计 |
| PATCH | `/v0/admin/model-prices/:id/enable` | 基础实现 | 启用系统模型价格；用户侧模型列表返回版本化价格规则，成功后写 `model_price.enable` 管理审计 |
| GET | `/v0/admin/channel-model-prices` | 基础实现 | 通道模型价格覆盖列表，支持 `page`、`page_size`、`keyword`、`channel_id`、`enabled`、`user_enabled` |
| POST | `/v0/admin/channel-model-prices` | 基础实现 | 创建通道模型价格覆盖；`channel_id + model` 唯一，成功后 `rule_version=1` 并写 `channel_model_price.create` 管理审计 |
| PUT | `/v0/admin/channel-model-prices/:id` | 基础实现 | 更新通道模型价格覆盖；每次更新递增 `rule_version` 并写 `channel_model_price.update` 管理审计 |
| PATCH | `/v0/admin/channel-model-prices/:id/disable` | 基础实现 | 禁用价格覆盖但不改变普通用户可见性；用户侧可回退系统价格，成功后写 `channel_model_price.disable` 管理审计 |
| PATCH | `/v0/admin/channel-model-prices/:id/enable` | 基础实现 | 启用价格覆盖；用户侧优先展示通道级版本化价格规则，成功后写 `channel_model_price.enable` 管理审计 |

创建/更新系统模型价格请求：

```json
{
  "model": "gpt-4o-mini",
  "price_mode": "token",
  "price_expression": "prompt_tokens * prompt_price + completion_tokens * completion_price",
  "variables_json": {
    "prompt_price": 1,
    "completion_price": 2
  },
  "unit_tokens": 1000,
  "enabled": true
}
```

`price_mode` 当前允许 `request`、`token`、`second`、`tiered`。系统模型价格用于 `/v0/user/models` 的 `pricing_ready` 和 `price_rule` 展示；成功调用后如果没有命中通道级覆盖，热路径会执行该表达式，并在 `billing_snapshot` 写入规则来源、表达式、变量、版本和最终扣费。

创建/更新通道模型价格覆盖请求：

```json
{
  "channel_id": 12,
  "model": "gpt-4o-mini",
  "enabled": true,
  "user_enabled": true,
  "price_mode": "token",
  "override_mode": "override",
  "price_expression": "prompt_tokens * prompt_price + completion_tokens * completion_price",
  "variables_json": {
    "prompt_price": 1,
    "completion_price": 2
  },
  "unit_tokens": 1000
}
```

`override_mode` 当前允许 `override` 和 `merge_variables`。`user_enabled=false` 表示该通道下该模型不向普通用户暴露，也不会进入普通用户调用候选；如果没有其他可见通道提供该模型，`/v0/user/models` 不再返回该模型。通道级启用价格覆盖优先于系统模型价格展示，也优先于系统模型价格参与成功调用后的扣费；禁用覆盖后会回退到启用的系统模型价格或 `minimum_usage`。

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
| `error_code` | string | 失败或拒绝 code 过滤，例如 `api_key_quota_edit_forbidden`、`redem_code_expired_at_not_future`、`redem_code_invalid_or_used`、`payment_order_provider_disabled` 或 `payment_order_cancel_not_pending` |
| `start_time` | int64 | 起始 Unix 秒，按 `created_at >= start_time` 过滤 |
| `end_time` | int64 | 结束 Unix 秒，按 `created_at <= end_time` 过滤 |

当前基础实现会记录以下管理动作：

| 动作 | 触发接口 |
|------|----------|
| `payment_product.create` | `POST /v0/admin/payment/products` |
| `payment_product.update` | `PUT /v0/admin/payment/products/:id` |
| `payment_product.disable` | `PATCH /v0/admin/payment/products/:id/disable` |
| `payment_product.enable` | `PATCH /v0/admin/payment/products/:id/enable` |
| `model_price.create` | `POST /v0/admin/model-prices` |
| `model_price.update` | `PUT /v0/admin/model-prices/:id` |
| `model_price.disable` | `PATCH /v0/admin/model-prices/:id/disable` |
| `model_price.enable` | `PATCH /v0/admin/model-prices/:id/enable` |
| `channel_model_price.create` | `POST /v0/admin/channel-model-prices` |
| `channel_model_price.update` | `PUT /v0/admin/channel-model-prices/:id` |
| `channel_model_price.disable` | `PATCH /v0/admin/channel-model-prices/:id/disable` |
| `channel_model_price.enable` | `PATCH /v0/admin/channel-model-prices/:id/enable` |
| `payment_order.create` | `POST /v0/user/payment/orders` |
| `payment_order.create_denied` | `POST /v0/user/payment/orders` 本地拒绝创建订单或 provider checkout 发起失败 |
| `payment_order.cancel` | `POST /v0/user/payment/orders/:order_no/cancel` |
| `payment_order.cancel_denied` | `POST /v0/user/payment/orders/:order_no/cancel` 拒绝取消非 `pending`、不存在或不属于当前用户的订单 |
| `payment_webhook.processed` | `POST /v0/payment/stripe/webhook`、`POST /v0/payment/epay/notify` |
| `payment_webhook.failed` | Stripe `checkout.session.async_payment_failed` 或易支付明确失败通知将 pending 订单置为 `failed` |
| `payment_order.paid` | 支付 provider 成功回调入账 |
| `payment_refund.requested` | `POST /v0/admin/payment/refund-requests` 向 Stripe 或易支付发起退款请求 |
| `payment_refund.request_denied` | `POST /v0/admin/payment/refund-requests` 本地拒绝或 provider 发起失败 |
| `payment_refund.processed` | `POST /v0/payment/stripe/webhook` 处理全额或部分退款事件 |
| `payment_refund.deducted` | Stripe 全额或部分退款按 settings 自动扣回额度 |
| `payment_refund.manual` | `POST /v0/admin/payment/refunds` 管理员人工确认退款并扣回额度 |
| `payment_refund.manual_denied` | `POST /v0/admin/payment/refunds` 本地拒绝人工退款落账 |
| `payment_dispute.created` | `POST /v0/payment/stripe/webhook` 处理 Stripe 争议/拒付事件，可按 settings 禁用 API Key |
| `payment_dispute.updated` | `POST /v0/payment/stripe/webhook` 处理 Stripe 争议更新事件 |
| `payment_dispute.closed` | `POST /v0/payment/stripe/webhook` 处理 Stripe 争议关闭事件 |
| `payment_dispute.funds_changed` | `POST /v0/payment/stripe/webhook` 处理 Stripe 争议资金扣回或返还事件 |
| `payment_manual_adjust.credit` | `POST /v0/admin/payment/adjustments` 人工补账 |
| `payment_manual_adjust.debit` | `POST /v0/admin/payment/adjustments` 人工扣回 |
| `payment_manual_adjust.denied` | `POST /v0/admin/payment/adjustments` 本地拒绝人工补账或扣回 |
| `api_key.created` | `POST /v0/user/token` |
| `api_key.updated` | `PUT /v0/user/token/:id` 编辑名称或过期时间 |
| `api_key.disabled` | `PUT /v0/user/token/:id` 将 Key 状态改为禁用，或 `POST /v0/user/token/:id/disable` |
| `api_key.deleted` | `DELETE /v0/user/token/:id` |
| `api_key.rotated` | `POST /v0/user/token/:id/rotate` |
| `api_key.leak_reported` | `POST /v0/user/token/:id/report-leak` |
| `api_key.scope_updated` | `PUT /v0/user/token/:id/scope` |
| `api_key.batch_disabled` | `POST /v0/admin/token/batch-disable` |
| `api_key.batch_expired` | `POST /v0/admin/token/batch-expire` |
| `api_key.batch_disable_denied` | `POST /v0/admin/token/batch-disable` 缺少 `token_ids` 和 `user_id` 被拒绝 |
| `api_key.batch_expire_denied` | `POST /v0/admin/token/batch-expire` 缺少 `token_ids` 和 `user_id` 被拒绝 |
| `api_key.quota_limit_denied` | 用户端尝试通过 `PUT /v0/user/token/:id` 修改额度或无限标记被拒绝 |
| `setting.create` | `PUT /v0/admin/setting` 新增 key |
| `setting.update` | `PUT /v0/admin/setting` 修改已有 key |
| `setting.denied` | `PUT /v0/admin/setting` 参数校验失败或高风险配置被拒绝 |
| `user.create` | `POST /v0/admin/user` |
| `user.update` | `PUT /v0/admin/user/:id` |
| `user.disable` | `PUT /v0/admin/user/:id` 将用户状态改为禁用 |
| `user.delete` | `DELETE /v0/admin/user/:id` |
| `user.denied` | 用户管理接口拒绝角色变更 |
| `user.quota_update` | `PATCH /v0/admin/user/:id/quota` |
| `user.self_cancel` | `DELETE /v0/user/self` |
| `user.self_cancel_denied` | `DELETE /v0/user/self` 缺少或未通过本地密码二次确认 |
| `user.recover` | `POST /v0/user/register`、`POST /v0/user/oauth/:provider/register` 或 `POST /v0/user/oidc/:provider/register` 命中已注销保留身份并恢复 |
| `user.identity_unbound` | `DELETE /v0/user/identities/:id` 用户解绑非主登录身份 |
| `user_group.create` | `POST /v0/admin/groups` |
| `user_group.update` | `PUT /v0/admin/groups/:id` |
| `user_group.delete` | `DELETE /v0/admin/groups/:id` |
| `redem_code.create` | `POST /v0/admin/redem` |
| `redem_code.create_denied` | `POST /v0/admin/redem` 本地拒绝生成或导入充值码 |
| `redem_code.disable` | `PATCH /v0/admin/redem/:id/disable` |
| `redem_code.redeem` | `POST /v0/user/redem` 成功兑换充值码 |
| `redem_code.redeem_denied` | `POST /v0/user/redem` 兑换码已用、作废、过期、不存在或参数无效 |
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
| `security.secret_rotate` | `POST /v0/admin/security/rotate-secrets` 成功轮换数据库密文 |
| `security.secret_rotate_denied` | `POST /v0/admin/security/rotate-secrets` 旧主密钥错误、当前主密钥缺失或参数无效 |
| `log.clear` | `DELETE /v0/admin/log` |
| `log.export` | `GET /v0/admin/log/export` |

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
| GET | `/v0/admin/channel/:id/models` | 基础实现 | 从单上游 URL 拉取模型列表；Azure OpenAI 返回 deployment id；成功或失败写 `channel.fetch_models` 管理审计 |

通道列表和创建响应会返回脱敏通道信息，并额外包含显式健康摘要：`health_status=healthy|disabled|tripped|probing`、`health_reason=ok|manual_status|error_count_threshold|cooldown_elapsed` 和 `cooldown_remaining_seconds`。`status` 仍表示管理员手工启停状态；`health_status` 由 `status`、`error_count`、`relay.error_auto_ban`、`relay.error_ban_threshold` 和 `relay.error_ban_cooldown_seconds` 计算得出，不额外持久化。

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
| GET | `/v0/admin/log` | 已实现 | 调用日志列表；配置 `LOG_SQL_DSN` 时读取独立日志库 |
| GET | `/v0/admin/log/export` | 基础实现 | 按日志查询条件导出脱敏 CSV；配置 `LOG_SQL_DSN` 时读取独立日志库，查询失败回退主库；成功后写 `log.export` 管理审计 |
| DELETE | `/v0/admin/log` | 基础实现 | 按 `before` 清理日志；配置 `LOG_SQL_DSN` 时清理独立日志库；成功后写 `log.clear` 管理审计 |
| GET | `/v0/admin/dashboard` | 基础实现 | 仪表盘统计；基础用户/通道/API Key 数来自主库，今日调用和额度在配置 `LOG_SQL_DSN` 时来自日志库；同时返回 `ready`、`ready_status` 和数据库、迁移、Redis、日志库、settings 依赖状态，便于控制台解释当前可用性 |
| GET | `/v0/admin/setting` | 已实现 | 获取系统设置，仅超级管理员；敏感值脱敏，外部登录 `client_secret` 不返回明文 |
| PUT | `/v0/admin/setting` | 已实现 | 批量更新系统设置，仅超级管理员；`oauth.*.client_secret` 和 `oidc.*.client_secret` 在配置 `ENCRYPTION_KEY` 时加密落库，成功后按 key 写管理审计 |
| POST | `/v0/admin/security/rotate-secrets` | 基础实现 | 仅超级管理员；使用请求中的 `previous_encryption_key` 解密现有通道 `api_key`、`api_keys`、`upstreams.api_key` 以及外部登录 `oauth.*.client_secret`/`oidc.*.client_secret`，再用当前 `ENCRYPTION_KEY` 重新加密；整批事务失败回滚，响应和审计只返回扫描/轮换计数 |

看板响应示例：

```json
{
  "success": true,
  "data": {
    "user_count": 1,
    "channel_count": 1,
    "token_count": 1,
    "today_call_count": 0,
    "today_quota_used": 0,
    "active_channel_count": 1,
    "ready": true,
    "ready_status": "ready",
    "dependencies": {
      "database": "up",
      "migration": "ok",
      "redis": "not_required",
      "log_db": "main_database",
      "setting": "ok"
    }
  },
  "message": ""
}
```

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
| `error_code` | string | 稳定错误 code 过滤，例如 `upstream_400`、`rate_limit_exceeded` |
| `error_source` | string | 失败来源过滤，例如 `request`、`quota`、`route`、`channel`、`upstream`、`billing` |
| `upstream_status` | int | 上游 HTTP 状态过滤，例如 `400`、`429`、`500` |
| `start_time` | string | 开始时间 |
| `end_time` | string | 结束时间 |

用户账单统计 `GET /v0/user/billing` 使用同样的 `start_time` / `end_time` 时间窗口，并支持 `token_id` 只聚合当前用户指定 API Key 的成功调用；该过滤不会跨用户读取其他 Key 的日志。

日志响应字段至少包含 `user_id`、`token_id`、`channel_id`、`model`、usage、`usage_source`、`quota_used`、`status`、`request_id`、`error_code`、`error_source`、`upstream_status`、`request_snapshot`、`policy_snapshot`、`route_snapshot`、`billing_snapshot`、`error_msg`、`ip` 和 `created_at`。`content` / `response` 默认为空；只有显式开启 body 日志且配置正数上限时，才会保存截断和脱敏后的非流式请求/响应片段。`usage_source` 当前会记录 `upstream` 或 `minimum`；当 `billing.usage_missing_strategy=reject` 且上游成功响应缺少 usage 时，会写失败日志 `usage_missing`、`error_source=billing` 且不扣费；`request_id` 用于关联 HTTP 访问日志和审计日志；`error_code` 成功调用为空，失败调用按 `docs/ERRORS.md` 使用稳定 code；`error_source` 和 `upstream_status` 用于排查失败来源；`request_snapshot`、`policy_snapshot`、`route_snapshot` 和 `billing_snapshot` 当前是脱敏 JSON 字符串，分别记录基础请求事实、基础策略事实、基础路由选择事实和基础计费事实，其中请求快照包含入口协议、API 类型、请求模型和 stream 标记，策略快照包含成功 allow、额度预检、基础 scope allow、API Key scope 拒绝、基础余额预检拒绝、用户分组 x 通道分组访问控制拒绝、无可用候选 `no_available_channel` 拒绝和 Redis 全局/IP/Token/User/Model/Channel 限流拒绝摘要；限流拒绝会额外写 `rate_limit_snapshot`，包含命中的维度、分钟窗口、阈值、当前计数、剩余量和拒绝决策；因 `health_blocked` 熔断造成无可用候选时会额外写 `breaker_snapshot`，包含阈值、冷却窗口、被挡通道、错误计数和冷却剩余时间；路由快照包含候选过滤、模型重写和非流式重试摘要，计费快照包含价格表达式或 P0 回退表达式、规则 ID/版本、倍率快照、Key 预算和用户余额前后摘要；扣费失败日志会保持 `quota_used=0`，同时在计费快照中记录 `billing_status=failed`、`attempted_quota_used` 和 `deduction_error_code`。配置 `LOG_SQL_DSN` 时，`LogService` 会先在主库事务内保存可恢复结算事实并创建 `log_replication_outboxes` 补写项，再写入独立日志库；日志查询和清理优先使用独立日志库，日志库查询失败时回退读取主库事实，独立日志库运行期写入失败不应抹掉主库事实，后台 worker 会在恢复后补写 pending outbox。

日志导出使用与列表相同的过滤参数，并额外支持 `limit`，默认 `1000`、最大 `10000`。CSV 只包含 `id`、`user_id`、`token_id`、`channel_id`、`model`、usage、`usage_source`、`quota_used`、`status`、`error_code`、`error_source`、`upstream_status`、`request_id` 和 `created_at`。导出内容不包含请求体、响应体、IP、错误原文、request/policy/route/billing snapshot、API Key、上游密钥或支付密钥；成功导出写 `log.export` 管理审计，摘要记录过滤条件、规范化后的 `limit` 和 `exported_count`。

删除日志目标要求：

- 必须支持时间范围。
- 默认拒绝无条件全表清理。
- 成功后写 `log.clear` 管理审计，摘要保存本次清理的 `before` 截止时间。

## 用户端 API

前缀：`/v0/user`。

鉴权：

- 注册和登录不需要 User JWT，但需要系统已初始化。
- 用户名密码登录是当前本地登录基线；email/phone 密码登录只对已有本地身份生效，并分别受 `auth.login.email_password.enabled` 与 `auth.login.phone_password.enabled` 控制，默认关闭；本地 email/phone 身份复用同一用户的 `username/local` 主密码，不要求各自保存独立密码哈希。公开登录、注册、验证码生成、OAuth/OIDC 登录和回调入口会复用 Redis 分钟级 `rate_limit.global_per_min` 与 `rate_limit.per_ip_per_min` 限流，命中返回 429；外部数据库或集群模式下 Redis 限流依赖不可用时 fail-closed 返回 503。`POST /v0/user/login/code` 当前可生成 Redis-backed 登录验证码挑战，统一登录接口已经识别 `credential_type=password|code`；验证码登录支持 Redis 中的短期验证码记录校验和一次性消费，Redis 缺失或不可用时 fail-closed，不会回退为密码登录。
- 自部署商业级默认关闭公开自助注册；`POST /v0/user/register/captcha` 当前可生成 Redis-backed 注册图片验证码，`POST /v0/user/register` 支持 `register_method=username/email/phone`，分别需要 `auth.register.enabled=true`、对应注册方法开关为 true。`auth.register.captcha.required=true` 时必须提交 Redis 中存在且匹配的注册验证码。
- 管理员创建用户不受自助注册开关影响。
- 个人信息、日志和账单需要 User JWT。

### 认证和个人信息

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| POST | `/v0/user/register/captcha` | 已实现 | 生成注册图片验证码；写入 `auth:register_captcha:<captcha_id>` Redis 记录并返回可展示的 SVG、验证码 ID 和 TTL，Redis 不可用时 fail-closed |
| POST | `/v0/user/register` | 已实现 | 统一自助注册入口，支持 `register_method=username/email/phone`；所有方法仍要求用户名和密码，可创建或恢复补齐 email/phone 本地登录标识但不保存重复密码哈希；命中已注销同名、同邮箱或同手机号账号时恢复原账号 |
| POST | `/v0/user/login/code` | 已实现 | 生成邮箱或手机号登录验证码挑战；写入 `auth:login_code:<captcha_id>` Redis 记录，返回验证码 ID、投递方式和 TTL；显式开启 `auth.captcha.debug_response.enabled=true` 时额外返回自部署调试用 `debug_code`，Redis 不可用时返回 503 |
| POST | `/v0/user/login` | 已实现 | 用户统一登录；用户名密码始终可用，email/phone 密码登录受 settings 控制并复用 `username/local` 主密码；`credential_type=code` 使用 Redis 验证码记录，成功后一次性消费且不会误走密码登录；成功登录写 `user.login` 管理审计，摘要不包含密码或 JWT |
| GET | `/v0/user/oauth/:provider/login` | 基础实现 | OAuth 授权跳转；检查 `auth.login.oauth.enabled` 和 `oauth.{provider}.enabled`，生成一次性 state Cookie 后跳转 provider 授权地址 |
| GET | `/v0/user/oauth/:provider/callback` | 基础实现 | OAuth 回调；校验 state Cookie，使用 provider token/userinfo 接口解析稳定 id/sub，只允许已绑定 `oauth/provider/identifier` 身份登录并写 `user.login` 审计；相同 email 不自动绑定 |
| GET | `/v0/user/oauth/:provider/bind` | 基础实现 | 登录用户发起 OAuth identity 绑定；写入 state Cookie 和签名 bind Cookie 后跳转 provider，bind Cookie 只用于证明本次绑定由当前用户发起 |
| GET | `/v0/user/oauth/:provider/bind/callback` | 基础实现 | OAuth 绑定回调；校验 state 与签名 bind Cookie 后创建或刷新 `oauth/provider/identifier` 身份；同一第三方 subject 已绑定其他用户时返回 409 |
| GET | `/v0/user/oidc/:provider/login` | 基础实现 | OIDC 授权跳转；检查 `auth.login.oidc.enabled` 和 `oidc.{provider}.enabled`，读取 Discovery 后生成 state/nonce Cookie 并跳转 authorization endpoint |
| GET | `/v0/user/oidc/:provider/callback` | 基础实现 | OIDC 回调；校验 state/nonce、RS256 ID Token 签名、iss、aud、exp 和 sub，只允许已绑定 `oidc/provider/sub` 身份登录并写 `user.login` 审计 |
| GET | `/v0/user/oidc/:provider/bind` | 基础实现 | 登录用户发起 OIDC identity 绑定；写入 state、nonce 和签名 bind Cookie 后跳转 authorization endpoint |
| GET | `/v0/user/oidc/:provider/bind/callback` | 基础实现 | OIDC 绑定回调；校验 state/nonce、签名 bind Cookie 和 ID Token 后创建或刷新 `oidc/provider/sub` 身份；同一 subject 已绑定其他用户时返回 409 |
| GET | `/v0/user/identities` | 已实现 | 当前用户身份列表；只返回身份元数据，不返回密码哈希、JWT 或 API Key |
| DELETE | `/v0/user/identities/:id` | 已实现 | 当前用户解绑非 `username/local` 主身份；软删除目标 identity，解绑后该身份不能继续登录，并写 `user.identity_unbound` 审计 |
| GET | `/v0/user/self` | 已实现 | 获取个人信息 |
| PUT | `/v0/user/self` | 已实现 | 修改个人信息；提交 email 或 phone 时会规范化并同步同用户 `email/local` 或 `phone/local` 登录标识，目标身份已被其他账号占用时整笔拒绝 |
| DELETE | `/v0/user/self` | 已实现 | 注销当前普通用户账号；请求体必须提供 `password` 做二次确认，成功后禁用登录和 API Key，清空展示名、主邮箱和主手机号，保留账号、身份、日志、额度与历史事实，并写 `user.self_cancel` 审计；缺少或错误密码会写 `user.self_cancel_denied` 拒绝审计 |
| POST | `/v0/user/self/password` | 已实现 | 修改当前用户本地密码；只更新 `username/local` 主身份密码哈希，成功后写 `user.password_changed` 审计，摘要不包含旧密码、新密码或 JWT |

注册目标请求：

先通过注册验证码接口取得一次性验证码：

```http
POST /v0/user/register/captcha
```

返回 `captcha_id`、`captcha_image_svg` 和 `ttl_seconds`；服务端在 Redis 写入 `auth:register_captcha:<captcha_id>`，验证码答案显示在 SVG 中，后续注册请求提交同一个 `captcha_id` 和用户读到的 `captcha_code`。该基础实现用于自部署前端显示图片验证码；邮箱/手机号归属验证仍按 `docs/ACCOUNTS.md` 后续阶段扩展。

```json
{
  "username": "alice",
  "password": "password",
  "display_name": "Alice",
  "email": "alice@example.com",
  "phone": "+8613800000000",
  "register_method": "email",
  "captcha_id": "captcha-id",
  "captcha_code": "123456"
}
```

`register_method` 省略时按 `username` 处理；`email` 方法必须填写 email 并开启 `auth.register.email.enabled`，`phone` 方法必须填写 phone 并开启 `auth.register.phone.enabled`。注册策略拒绝返回 403，例如公开注册关闭、对应注册方法关闭，或 `auth.register.captcha.required=true` 但未提供有效验证码。启用注册验证码时，服务端读取 `auth:register_captcha:<captcha_id>` Redis 记录并比对 `SHA256(captcha_code)`，验证码正确后删除 Redis key；验证码缺失、过期、错误、超过尝试次数或 Redis 不可用都会拒绝注册。新账号注册成功时会应用 `auth.register.default_quota` 和可解析的 `auth.register.default_group_id`。如果请求提供 `email` 或 `phone`，服务端会规范化并创建对应 `email/local` 或 `phone/local` identity 作为可选登录标识，但密码哈希只保存到 `username/local` 主身份。若同名 `username/local`、同邮箱 `email/local` 或同手机号 `phone/local` identity 命中已注销的普通用户账号，当前接口会恢复原 `users.id`，把账号状态改回启用，更新本地密码身份和展示名；恢复请求附带未被其他账号占用的 `email` 或 `phone` 时，会补齐同用户的本地登录标识。恢复保留原额度、分组、日志和历史流水，不会自动启用旧 API Key，并写入 `user.recover` 审计。

自助修改个人信息使用 `PUT /v0/user/self`。当前实现允许更新展示名、email 和 phone；email 会先规范化为小写去空格，phone 会去除首尾空格，再分别和同用户 `email/local` 或 `phone/local` identity 在同一事务中保持一致。这些 identity 不保存重复密码哈希，邮箱或手机号密码登录开启后仍复用同用户 `username/local` 主密码。目标 email 或 phone 如果已绑定其他用户身份，接口返回 400，用户资料和 identity 都不会部分落库。`GET /v0/user/self`、登录和注册响应中的 `UserBrief` 会返回当前主邮箱和主手机号。

自助修改密码使用 `POST /v0/user/self/password`。当前实现要求旧密码和不少于 6 位的新密码，只更新 `username/local` 主身份上的 `password_hash`；修改后旧密码不能继续登录，新密码立即生效。成功后写入 `user.password_changed` 审计，审计摘要只记录用户元数据和密码已变更事实，不保存旧密码、新密码或 JWT。

自助注销使用 `DELETE /v0/user/self`，仅允许当前普通用户操作自己的账号，请求体必须提交当前本地密码进行二次确认。当前实现复用 `users.status=disabled` 表达注销态，并在同一事务中禁用该用户所有已启用 API Key，同时清空 `users.display_name`、`users.email` 和 `users.phone`。服务端不会删除 `users`、`user_identities`、`tokens`、`logs` 或额度历史；`username/local`、`email/local`、`phone/local`、OAuth 和 OIDC identity 继续保留用于去重和账号恢复。缺少密码或密码错误时不会修改账号和 API Key 状态，并写入 `user.self_cancel_denied` 拒绝审计，`error_code` 分别为 `self_cancel_password_required` 或 `self_cancel_password_invalid`，审计摘要不保存密码。注销后用户名密码登录返回统一认证失败，同名、同邮箱、同手机号注册、同一 OAuth identity 补齐注册或同一 OIDC subject 补齐注册会走上述恢复流程。

登录目标请求：

验证码登录前可先生成一次性登录验证码：

```http
POST /v0/user/login/code
Content-Type: application/json

{
  "account": "alice@example.com"
}
```

服务端会识别邮箱或手机号、检查 `auth.login.email_code.enabled` 或 `auth.login.phone_code.enabled`、确认本地身份和主密码存在，然后写入 `auth:login_code:<captcha_id>`。当前基础实现尚未接入真实邮件/短信网关；只有显式开启 `auth.captcha.debug_response.enabled=true` 时，响应才会返回 `debug_code` 供自部署前端和 Apifox 闭环调试。生产环境应保持该开关关闭，并在接入真实投递后只返回投递状态，不把验证码明文暴露给终端用户。

```json
{
  "account": "alice@example.com",
  "credential_type": "password",
  "password": "password"
}
```

`credential_type` 省略时按 `password` 处理，`account` 可为用户名、邮箱或手机号，旧客户端仍可用 `username` 字段。邮箱/手机号密码登录需要对应开关开启，并复用同一用户的 `username/local` 主密码。提交 `credential_type=code` 时必须提供 `account`、`captcha_id` 和 `captcha_code`；服务端会先检查邮箱或手机号验证码登录开关，再读取 `auth:login_code:<captcha_id>` Redis 记录校验 method、account 和 `SHA256(captcha_code)`。验证码正确时删除 Redis key 并签发 JWT；验证码缺失、过期、错误或超过尝试次数返回 401；账号类型不支持、登录开关关闭或 Redis 校验器不可用返回 403。即使请求体同时带有正确密码，验证码登录也不会回退到密码登录。

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

OAuth 基础登录当前用于第三方身份登录、首次补齐注册、注销账号恢复和绑定闭环。`GET /v0/user/oauth/:provider/login` 会读取 `oauth.{provider}.auth_url`、`client_id`、`client_secret`、`token_url`、`userinfo_url` 和 `scopes` 等 settings，生成 state 并写入 HttpOnly Cookie 后返回 302；`oauth.*.client_secret` 通过设置服务透明解密使用，管理响应和审计只显示脱敏值。`GET /v0/user/oauth/:provider/callback` 要求回调 state 与 Cookie 匹配，再用 code 换 token、拉取 userinfo，并以 userinfo 的稳定 `id` 或 `sub` 查询 `user_identities(method=oauth, provider, identifier)`。已绑定且用户启用时直接返回 User JWT；未绑定或已绑定到注销保留普通用户时，不会按 email 自动绑定或接管其他已有账号。只有当 `auth.register.enabled=true`、`auth.register.username.enabled=true`、`auth.register.oauth.enabled=true`、`oauth.{provider}.register_enabled=true` 时，回调才返回 `registration_required=true` 和短期 `registration_ticket`；验证码不在回调阶段提交。`POST /v0/user/oauth/:provider/register` 使用该票据补齐用户名和密码，且在 `auth.register.captcha.required=true` 时必须提交 `captcha_id`/`captcha_code`，服务端会校验并一次性消费 `auth:register_captcha:<captcha_id>`：全新 subject 会创建本地有密码账号、可选 `email/local` 和 `oauth/provider/identifier`；命中已注销普通用户的同一 OAuth identity 时恢复原 `users.id`、更新本地密码和展示名、刷新该 OAuth identity 最近使用时间，不创建第二个账号且不自动启用旧 API Key。新绑定写 `user.identity_bound` 与 `user.login`，恢复写 `user.recover` 与 `user.login`。

`POST /v0/user/oauth/:provider/register` 使用 `registration_ticket`、`username`、`password`、可选 `display_name`，以及开启注册验证码时必填的 `captcha_id`、`captcha_code` 完成 OAuth 首次注册。服务端会重新校验票据签名、过期时间、provider 注册开关、用户名/邮箱去重、密码要求和注册验证码，在一个事务里创建本地有密码账号、`username/local`、可选 `email/local` 和 `oauth/provider/identifier` identity；成功后返回 User JWT 并写 `user.identity_bound`、`user.login` 审计。票据无效或过期返回 400，注册开关关闭或验证码缺失/过期/错误/不可用返回 403，provider subject 已被绑定返回 409。

OAuth 绑定由已登录用户通过 `GET /v0/user/oauth/:provider/bind` 发起。该接口需要 User JWT，会同时写入 state Cookie 和签名 bind Cookie；bind Cookie 用 `jwt.secret` 对 provider、state 和 user_id 签名，只用于回调时确认本次绑定由哪个本地用户发起。`GET /v0/user/oauth/:provider/bind/callback` 校验 state 和签名后，仍只使用 userinfo 的稳定 `id` 或 `sub` 创建 `user_identities(method=oauth, provider, identifier)`，不会按 email 自动绑定；同一 provider subject 已属于其他用户时返回 409。更完整的 provider 错误恢复和风控流程仍按 `docs/ACCOUNTS.md` 后续扩展。

用户身份管理通过 `GET /v0/user/identities` 和 `DELETE /v0/user/identities/:id` 暴露。列表接口只返回当前用户未解绑的身份元数据；解绑接口只允许删除当前用户名下的非 `username/local` 主身份，采用软删除保留历史事实，成功后写入 `user.identity_unbound` 审计。解绑后的 OAuth/OIDC identity 不再可用于登录。

OIDC 基础登录当前用于已绑定企业身份登录、首次补齐注册、注销账号恢复和绑定闭环。`GET /v0/user/oidc/:provider/login` 会读取 `oidc.{provider}.issuer/client_id/client_secret/scopes`，通过 issuer Discovery 获取 `authorization_endpoint`、`token_endpoint` 和 `jwks_uri`，生成 state 与 nonce 后返回 302；`oidc.*.client_secret` 通过设置服务透明解密使用，管理响应和审计只显示脱敏值。`GET /v0/user/oidc/:provider/callback` 要求 state 与 Cookie 匹配，并校验 ID Token 的 RS256 签名、`iss`、`aud`、`exp`、`nonce` 和 `sub`，只用 `sub` 查询 `user_identities(method=oidc, provider, identifier)`。已绑定且用户启用时直接返回 User JWT；未绑定或已绑定到注销保留普通用户时，不会按 email 自动绑定或接管其他已有账号。只有当 `auth.register.enabled=true`、`auth.register.username.enabled=true`、`auth.register.oidc.enabled=true`、`oidc.{provider}.register_enabled=true` 时，回调才返回 `registration_required=true` 和短期 `registration_ticket`；验证码不在回调阶段提交。`POST /v0/user/oidc/:provider/register` 使用该票据补齐用户名和密码，且在 `auth.register.captcha.required=true` 时必须提交 `captcha_id`/`captcha_code`，服务端会校验并一次性消费 `auth:register_captcha:<captcha_id>`：全新 subject 会创建本地有密码账号、`username/local`、可选 `email/local` 和 `oidc/provider/sub` identity；命中已注销普通用户的同一 OIDC subject 时恢复原 `users.id`、更新本地密码和展示名、刷新 OIDC identity 最近使用时间，不创建第二个账号且不自动启用旧 API Key。新绑定写 `user.identity_bound` 与 `user.login`，恢复写 `user.recover` 与 `user.login`。已登录用户可通过 `GET /v0/user/oidc/:provider/bind` 发起绑定；`GET /v0/user/oidc/:provider/bind/callback` 校验 state、nonce、签名 bind Cookie 和 ID Token 后创建或刷新当前用户的 `oidc/provider/sub` 身份。同一 subject 已属于其他用户时返回 409。更完整 claim 映射仍按 `docs/ACCOUNTS.md` 后续扩展。

### API Key

API Key 用于 `/v1/*` 模型转发鉴权。
完整生命周期、轮换、泄露处置、作用域、缓存和审计契约以 `docs/API_KEYS.md` 为准；本节只列当前接口和接口层边界。

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/user/token` | 已实现 | 当前用户 API Key 列表 |
| POST | `/v0/user/token` | 已实现 | 创建 API Key，明文只返回一次；可写入 `metadata.environment/team/app/tags/external_id/note/principal_type/principal_id/principal_name` 非安全元数据；成功后写 `api_key.created` 审计 |
| PUT | `/v0/user/token/:id` | 已实现 | 编辑 API Key 名称、状态、过期时间和元数据；普通编辑写 `api_key.updated`，禁用写 `api_key.disabled`，额度/无限标记编辑拒绝写 `api_key.quota_limit_denied` |
| DELETE | `/v0/user/token/:id` | 已实现 | 删除 API Key，成功后写 `api_key.deleted` 审计 |
| POST | `/v0/user/token/:id/disable` | 已实现 | 禁用自己的 API Key，可记录禁用原因，成功后写 `api_key.disabled` 审计 |
| POST | `/v0/user/token/:id/rotate` | 已实现 | 创建替换 Key、返回新明文一次、写入 `rotated_from_id` 并禁用旧 Key，成功后写 `api_key.rotated` 审计 |
| POST | `/v0/user/token/:id/report-leak` | 已实现 | 上报泄露并立即禁用 Key，返回替换建议，成功后写 `api_key.leak_reported` 审计并创建管理员告警 |
| PUT | `/v0/user/token/:id/scope` | 基础实现 | 更新自己的 Key 收窄 scope，当前支持 `allow_models`、`api_types`、`channel_groups`、`entry_protocols`、`ip_cidrs`、`methods`、`daily_quota`、`monthly_quota`、`max_concurrency`、`rpm` 和 `tpm`；成功后写 `api_key.scope_updated` 审计 |
| GET | `/v0/user/token/:id/usage` | 已实现 | 返回该 Key 的调用数、成功/失败数、额度消耗、总 tokens 和最近调用摘要 |
| GET | `/v0/user/token/:id/leak-window` | 基础实现 | 当前用户查询单 Key 最近窗口调用摘要；`window_hours` 默认 24、最大 720，返回模型、错误 code 和来源 IP 哈希计数，不返回明文 Key 或原始 IP |
| GET | `/v0/user/token/:id/events` | 基础实现 | 当前用户查询单 Key 最近错误/限流事件聚合；`window_hours` 默认 24、最大 720，返回错误 code、错误来源、上游状态、限流维度和模型计数，不返回原始 IP、错误正文、明文 Key 或哈希 |
| GET | `/v0/admin/token` | 已实现 | 管理员跨用户查询脱敏 API Key 摘要，可按 `user_id`、`status`、`environment`、`team`、`app`、`tag`、`principal_type` 和 `principal_id` 过滤 |
| GET | `/v0/admin/token/export` | 基础实现 | 管理员按同样过滤条件导出脱敏 API Key CSV 摘要，包含元数据和服务账号主体列；`limit` 默认 1000、最大 10000，成功后写 `api_key.export` 审计 |
| GET | `/v0/admin/token/risk` | 基础实现 | 管理员查看异常 API Key 风险视图，支持 `user_id`、`window_hours`、`min_error_count` 和 `low_quota_below` 过滤；泄露风险会返回基础轮换建议，不返回明文 Key 或哈希 |
| GET | `/v0/admin/token/:id/leak-window` | 基础实现 | 管理员跨用户查询单 Key 泄露窗口摘要；输出字段与用户侧一致，用于泄露处置和工单排障 |
| GET | `/v0/admin/token/:id/events` | 基础实现 | 管理员跨用户查询单 Key 错误/限流事件聚合；输出字段与用户侧一致，用于排障和风控复核 |
| POST | `/v0/admin/token/batch-disable` | 已实现 | 管理员按 `token_ids` 或 `user_id` 批量禁用 Key，必须提供筛选条件；成功后写 `api_key.batch_disabled` 审计，缺少筛选条件时返回 400 并写 `api_key.batch_disable_denied` |
| POST | `/v0/admin/token/batch-expire` | 已实现 | 管理员按 `token_ids` 或 `user_id` 立即过期 Key，必须提供筛选条件；成功后写 `api_key.batch_expired` 审计，缺少筛选条件时返回 400 并写 `api_key.batch_expire_denied` |
| GET | `/v0/admin/alerts` | 基础实现 | 管理员查询主动告警收件箱，可按 `type`、`severity`、`status`、资源、用户和 API Key 过滤；当前泄露上报会创建 `api_key.leak_reported` critical 告警 |
| GET | `/v0/admin/alerts/deliveries` | 基础实现 | 管理员查询告警外部投递 outbox，可按 `alert_id`、`target` 和 `status` 过滤；当前 target 支持 `webhook`、`email`、`im` |
| POST | `/v0/admin/alerts/deliveries/replay` | 基础实现 | 手动重放到期的 pending 告警投递，可选 `target=webhook/email/im`；`limit` 默认 20、最大 100，成功返回本次投递成功条数 |
| POST | `/v0/admin/alerts/:id/ack` | 基础实现 | 管理员确认告警，写入确认时间和确认人；重复确认保持幂等 |

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

`allow_models` 为空或 scope 为空时继承用户和系统策略；非空时只允许列表内模型，拒绝返回 `model_not_allowed` 且不调用上游。`api_types` 为空时不按接口能力额外收窄；非空时只允许列出的 APIType，未命中返回 `token_forbidden` 且不调用上游。`channel_groups` 为空时不按通道分组额外收窄；非空时只允许候选通道落在列表内，未命中返回 `route_forbidden`。`entry_protocols` 为空时不按入口协议额外收窄；非空时只允许 `openai`、`anthropic`、`gemini` 或 `*` 命中，未命中返回当前入口协议兼容的 `token_forbidden` 且不调用上游；`gemini` 覆盖 generateContent、streamGenerateContent、countTokens、embedContent 和 batchEmbedContents。`ip_cidrs` 为空时不限制来源 IP；非空时只允许命中的单 IP 或 CIDR，未命中返回 `token_forbidden`。`methods` 为空时不按路径额外收窄；非空时只允许 `METHOD path` 命中，未命中返回 `token_forbidden`。`daily_quota` 为空时不设日预算，`monthly_quota` 为空时不设月预算；非空时分别按当天或当月成功日志已消耗额度拦截，到达上限返回 `insufficient_quota`。`max_concurrency` 为空时不设单 Key 并发上限；非空时限制同一 Key 同时在途请求数，达到上限返回 `rate_limit_exceeded`。`rpm` 为空时不设单 Key 每分钟请求上限，`tpm` 为空时不设单 Key 每分钟模型 token 上限；非空时达到上限返回 `rate_limit_exceeded`。

### 用量和账单

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/user/log` | 已实现 | 当前用户调用日志 |
| GET | `/v0/user/billing` | 基础实现 | 当前用户账单统计 |
| GET | `/v0/user/quota-transactions` | 已实现 | 当前用户额度流水；支持按类型、来源和时间过滤，`user_id` 参数会被忽略 |
| POST | `/v0/user/redem` | 基础实现 | 使用未兑换且未过期的充值码给当前用户增加额度，并写入 `quota_transactions` 幂等流水与 `redem_code.redeem` 管理审计；已用、作废、过期、不存在或参数无效会写 `redem_code.redeem_denied`，稳定 `error_code` 包含 `redem_code_invalid_or_used`、`redem_code_expired` 或 `redem_code_required` |
| GET | `/v0/user/models` | 基础实现 | 当前启用通道暴露且未被 `channel_model_prices.user_enabled=false` 隐藏的可用模型列表；普通用户调用也会过滤这些隐藏通道；通道级价格优先返回 `channel_model_price:<price_mode>:v<rule_version>`，否则命中启用 `model_prices` 时返回 `model_price:<price_mode>:v<rule_version>`，再否则返回 `minimum_usage` |

### 支付接口

支付接口用于用户在线购买额度。支付 provider、充值码、退款、人工补账和额度流水契约以 `docs/PAYMENTS.md` 为准；本文只定义接口外形和鉴权边界。当前用户侧基础实现已支持商品列表、创建本地 `pending` 订单、取消未支付订单、订单列表和详情；Stripe secret 与绝对 `return_url` 齐全时会创建真实 Checkout Session，配置不足时保留本地安全占位链接；Stripe webhook 已支持原始 body 签名、Checkout Session 成功事件、异步支付失败事件、金额/币种/metadata 校验、幂等入账和基础审计，以及全额/部分退款事件、退款审计、可选自动扣回、争议生命周期记录和可选 API Key 禁用；易支付异步通知已支持 MD5 签名、金额校验、成功/明确失败状态处理、幂等入账和基础审计，同步返回页仅展示本地订单状态；管理端已支持支付相关人工补账/扣回、人工退款落账以及 Stripe/易支付 provider 退款请求并写流水与审计。更多 provider 自动发起退款流程仍属于后续能力。

用户鉴权接口：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v0/user/payment/products` | 获取可购买的充值商品 |
| POST | `/v0/user/payment/orders` | 创建本地 `pending` 支付订单并写 `payment_order.create` 管理审计；provider 必须已在 settings 启用，Stripe secret + 绝对 `return_url` 齐全时创建 Stripe Checkout Session，易支付配置齐全时返回签名收银台 URL，否则返回安全 checkout 占位链接；本地参数、provider 未启用、商品不可用或 provider checkout 发起失败会写 `payment_order.create_denied`，稳定 `error_code` 包含 `payment_order_provider_disabled`、`payment_order_product_unavailable` 或 `payment_order_provider_checkout_failed`；`expires_at` 来自 `payment.order_expire_minutes` |
| GET | `/v0/user/payment/orders` | 查询当前用户支付订单列表 |
| GET | `/v0/user/payment/orders/:order_no` | 查询当前用户支付订单详情 |
| POST | `/v0/user/payment/orders/:order_no/cancel` | 取消当前用户自己的 `pending` 订单，置为 `closed` 并写 `payment_order.cancel` 审计；已 `closed` 订单幂等返回，已支付/退款中/已退款订单拒绝取消并写 `payment_order.cancel_denied`，稳定 `error_code` 包含 `payment_order_cancel_not_pending` 或 `payment_order_cancel_not_found`，不会入账 |

Provider 回调接口：

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/v0/payment/stripe/webhook` | Stripe 签名 | 基础实现；Stripe Checkout webhook，成功时幂等入账并写 `payment_webhook.processed`/`payment_order.paid` 审计，`checkout.session.async_payment_failed` 会将 pending 订单置为 `failed` 并写 `payment_webhook.failed`，全额或部分退款时写 `payment_refund.*` 审计，争议 created/updated/closed/funds_* 事件会更新 `payment_disputes` 并写 `payment_dispute.*` 审计，created 可按 settings 禁用 API Key，返回纯文本 `success` |
| POST | `/v0/payment/epay/notify` | 易支付签名 | 基础实现；易支付异步通知，成功时幂等入账并写 `payment_webhook.processed`/`payment_order.paid` 审计，明确失败状态会将 pending 订单置为 `failed` 并写 `payment_webhook.failed`，返回纯文本 `success` |
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
| `return_url` | 支付完成后的前端跳转地址；Stripe 创建真实 Checkout Session 时必须是绝对 URL，入账仍只依赖 provider webhook |

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
| `failed` | provider 明确支付失败；签名、金额或订单快照校验失败只拒绝入账，不直接改订单 |
| `closed` | 超时关闭或用户取消 |
| `refunded` | 已全额退款，是否扣回额度由退款策略决定 |
| `partially_refunded` | 已部分退款，自动扣回开启时按退款金额比例扣回额度 |

Stripe Webhook 要求：

- 使用原始 body 和 `Stripe-Signature` 校验签名。
- 只在 `checkout.session.completed` 或可信成功事件后入账。
- 校验 `metadata.order_no`、金额、货币和订单状态。
- 同一个 Stripe event id 必须幂等处理。
- `charge.refunded` 全额退款事件会把订单置为 `refunded`；部分退款事件会把订单置为 `partially_refunded`。开启 `payment.refund.auto_deduct=true` 时按全额或比例策略扣回额度并写 `refund_deduct` 流水。
- `charge.dispute.created`、`charge.dispute.updated`、`charge.dispute.closed`、`charge.dispute.funds_withdrawn` 和 `charge.dispute.funds_reinstated` 会更新 `payment_disputes` 并写 `payment_dispute.*` 审计；开启 `payment.dispute.auto_disable_tokens=true` 时，created 事件会禁用该用户已启用的 API Key，`revoked_reason=payment_dispute`，不直接修改额度或订单状态。

易支付通知要求：

- 校验易支付签名，排除 `sign`、`sign_type` 和空值字段后按网关规则生成签名。
- 校验 `pid`、`out_trade_no`、`money` 和状态；成功状态入账，明确失败状态只把 pending 订单置为 `failed`。
- 入账只依赖异步通知；同步返回页只展示本地订单状态。
- 通知处理成功后返回网关要求的纯文本，例如 `success`。
- 当前实现从 `PAYMENT_EPAY_KEY` 读取签名密钥，金额匹配且订单为 `pending` 时才把订单置为 `paid`、写 `payment_events`、写 `quota_transactions` 并增加用户额度；明确失败状态会把 pending 订单置为 `failed`，写 `payment_webhook.failed` 审计且不增加额度；重复通知不会重复入账。

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
| POST | `/v1/responses` | 基础实现，OpenAI-compatible Responses JSON 或 SSE 透传；Azure OpenAI 通道转发到 `/openai/v1/responses?api-version=preview`；Claude/Anthropic 通道支持基础非流式 Responses 转 Messages，Gemini 通道支持基础非流式 Responses 转 `generateContent`，并将对应上游 usage 映射回 Responses usage；usage 映射和扣费 |
| POST | `/v1/chat/completions` | 基础实现，Chat Completions；支持非流式和 OpenAI-compatible SSE 流式 |
| POST | `/v1/completions` | 基础实现，Legacy Completions；支持非流式、Azure deployment 路径转发和 OpenAI-compatible SSE 流式 |
| POST | `/v1/embeddings` | 基础实现，OpenAI-compatible Embeddings JSON 透传、Azure deployment 路径转发、`routerx` 剥离、usage 扣费，以及上游前 `input` schema 和 2048 批量边界校验 |
| POST | `/v1/images/generations` | 基础实现，OpenAI-compatible 图像生成 JSON 透传；Azure OpenAI 通道转发到 `/openai/v1/images/generations?api-version=preview`，保留 `model` 作为 deployment 名；缺少非空字符串 `prompt` 时返回 `invalid_image_prompt` 且不上游、不扣费；显式 `n` 必须为大于等于 1 的整数，非法返回 `invalid_image_count` 且不上游、不扣费；`size` 支持缺省、空字符串、`auto` 或 `WIDTHxHEIGHT`，本地校验单边不超过 4096 且总像素不超过 4194304，非法返回 `invalid_image_size` 且不上游、不扣费；无 usage 时按 P0 最低计费 |
| POST | `/v1/images/edits` | 基础实现，OpenAI-compatible multipart 图像表单透传，Azure OpenAI 可转发到 `/openai/v1/images/edits?api-version=preview`；`routerx` 表单字段剥离和路由偏好；缺少 `image` 文件字段返回 `multipart_file_required` 且不上游、不扣费；`size` 支持缺省、空字符串、`auto` 或 `WIDTHxHEIGHT`，本地校验单边不超过 4096 且总像素不超过 4194304，非法返回 `invalid_image_size` 且不上游、不扣费；单文件字段受 `relay.max_multipart_file_bytes` 限制，路径形态、危险扩展名、非图片扩展名、非图片文件头或可执行/脚本内容签名会本地拒绝；无 usage 时按 P0 最低计费 |
| POST | `/v1/images/variations` | 基础实现，OpenAI-compatible multipart 图像表单透传，Azure OpenAI 可转发到 `/openai/v1/images/variations?api-version=preview`；`routerx` 表单字段剥离和路由偏好；缺少 `image` 文件字段返回 `multipart_file_required` 且不上游、不扣费；`size` 支持缺省、空字符串、`auto` 或 `WIDTHxHEIGHT`，本地校验单边不超过 4096 且总像素不超过 4194304，非法返回 `invalid_image_size` 且不上游、不扣费；单文件字段受 `relay.max_multipart_file_bytes` 限制，路径形态、危险扩展名、非图片扩展名、非图片文件头或可执行/脚本内容签名会本地拒绝；无 usage 时按 P0 最低计费 |
| POST | `/v1/audio/transcriptions` | 基础实现，OpenAI-compatible multipart 音频表单透传；Azure OpenAI 通道转发到 `/openai/v1/audio/transcriptions?api-version=preview`；`routerx` 表单字段剥离和路由偏好；缺少 `file` 文件字段返回 `multipart_file_required` 且不上游、不扣费；`response_format` 支持缺省、空字符串或 `json`、`text`、`srt`、`verbose_json`、`vtt`，非法返回 `invalid_audio_response_format` 且不上游、不扣费；单文件字段受 `relay.max_multipart_file_bytes` 限制，路径形态、危险扩展名、非音频扩展名、非音频文件头或可执行/脚本内容签名会本地拒绝；无 usage 时按 P0 最低计费 |
| POST | `/v1/audio/translations` | 基础实现，OpenAI-compatible multipart 音频表单透传；Azure OpenAI 通道转发到 `/openai/v1/audio/translations?api-version=preview`；`routerx` 表单字段剥离和路由偏好；缺少 `file` 文件字段返回 `multipart_file_required` 且不上游、不扣费；`response_format` 支持缺省、空字符串或 `json`、`text`、`srt`、`verbose_json`、`vtt`，非法返回 `invalid_audio_response_format` 且不上游、不扣费；单文件字段受 `relay.max_multipart_file_bytes` 限制，路径形态、危险扩展名、非音频扩展名、非音频文件头或可执行/脚本内容签名会本地拒绝；无 usage 时按 P0 最低计费 |
| POST | `/v1/audio/speech` | 基础实现，OpenAI-compatible 文本转语音 JSON 透传，Azure OpenAI 通道转发到 `/openai/v1/audio/speech?api-version=preview`，二进制音频响应透传；本地要求 `input` 为 1-4096 字符字符串、`voice` 为非空字符串，`response_format` 支持缺省、空字符串或 `mp3`、`opus`、`aac`、`flac`、`wav`、`pcm`，非法请求返回 `invalid_audio_speech_input`、`invalid_audio_speech_voice` 或 `invalid_audio_response_format` 且不上游、不扣费；无 usage 时按 P0 最低计费 |
| GET | `/v1/models` | 基础实现，模型列表；Gemini 外形会在 `supportedGenerationMethods` 声明 `generateContent`、`streamGenerateContent`、`countTokens`、`embedContent` 和 `batchEmbedContents` |
| GET | `/v1/models/:model` | 基础实现，模型详情；支持 `format`、`routerx_protocol` 或 `X-RouterX-Protocol` 选择 OpenAI、Gemini 或 Anthropic 外形 |
| POST | `/v1/moderations` | 基础实现，OpenAI-compatible Moderations JSON 透传；本地要求 `input` 为非空字符串或非空字符串数组，非法返回 `invalid_moderation_input` 且不上游、不扣费；未支持该 APIType 的上游 adapter 返回 `unsupported_api_type`；无 usage 时按 P0 最低计费 |

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
- 请求体必须包含非空数组 `messages`；RouterX 仅在本地校验数组存在性，消息内容结构继续由适配器和上游处理。
- `stream=true` 且命中 OpenAI SSE 形态通道时，返回 `text/event-stream` 并逐行转发 `data:` chunk。
- 保留下游返回的 `usage`，或按 P0 最低规则估算。
- 写入 `logs`，包含 user、token、channel、model、prompt/completion/total tokens、`quota_used` 和 status。
- 扣减 API Key 或用户额度，扣费失败时返回 429 并写失败日志；失败日志 `quota_used=0`，`billing_snapshot` 记录本次试算额度和扣减失败原因。

P0 明确失败：

| 场景 | HTTP | code |
|------|------|------|
| 非法 JSON | 400 | `invalid_json` |
| 缺少 `model` | 400 | `model_required` |
| 缺少或非法 `messages` | 400 | `invalid_chat_messages` |
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
| 选中通道 adapter 不支持该 APIType | 502 | `unsupported_api_type` |
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
| GET | `/v1/models/{model}` | 基础实现，模型详情；支持协议选择器返回 Gemini 或 Anthropic 外形 |
| POST | `/v1/models/{model}:generateContent` | 基础实现，内容生成；命中 OpenAI-compatible 上游时转 OpenAI Chat，非文本 parts 降级为 compact JSON 文本，`generationConfig.maxOutputTokens/temperature/topP/stopSequences` 会映射，其他有值子字段会进入 `request_snapshot.adapter_degradations`；命中 Gemini 上游时会以原生 Gemini body 发送 `contents/systemInstruction/generationConfig/safetySettings/tools/toolConfig/cachedContent`，且这些已保真字段不会被成功日志误记为 dropped |
| POST | `/v1/models/{model}:streamGenerateContent` | 基础实现，流式内容生成；命中 OpenAI-compatible 上游时将 Gemini 请求转 Chat SSE 再输出 Gemini SSE 事件，命中 Gemini 上游时原生调用 `:streamGenerateContent` 并透传 Gemini SSE，同时从 `usageMetadata` 提取 usage 扣费 |
| POST | `/v1/models/{model}:countTokens` | 基础实现，本地近似 Token 计数；优先统计 `contents[].parts[]`、`systemInstruction.parts[]` 或 `generateContentRequest` 内的文本内容，`generateContentRequest` 存在时忽略顶层 `contents` |
| POST | `/v1/models/{model}:embedContent` | 基础实现，Gemini embedContent 命中 OpenAI-compatible 上游时转 Embeddings 并返回 Gemini embedding 外形；命中 Gemini 上游时原生调用 `:embedContent`，保留 `content/taskType/title/outputDimensionality` 并从 `usageMetadata` 扣费；请求结构非法时返回 `invalid_gemini_embedding_request` 且不上游、不扣费 |
| POST | `/v1/models/{model}:batchEmbedContents` | 基础实现，Gemini batchEmbedContents 命中 OpenAI-compatible 上游时转 Embeddings 批量 input 并返回 Gemini embeddings 外形，`outputDimensionality` 映射为 OpenAI `dimensions` 且同批次已填写的值必须一致，`taskType/title` 会进入 `request_snapshot.adapter_degradations`；命中 Gemini 上游时原生调用 `:batchEmbedContents`，保留 `requests[].content/taskType/title/outputDimensionality`，从 `usageMetadata` 扣费；OpenAI-compatible 上游返回 embedding 数量必须和请求数一致；请求结构非法时返回 `invalid_gemini_embedding_request` 且不上游、不扣费 |

### Anthropic 格式

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/messages` | 基础实现，Messages；命中 Anthropic 上游时会保留原生 `system/messages` content blocks、`tools/tool_choice`、`thinking`、`metadata` 和 `stop_sequences`；命中 OpenAI-compatible 上游时非文本 content blocks 会降级为 compact JSON 文本并记录摘要；`stream=true` 命中 OpenAI-compatible 上游时会转换为 Anthropic SSE，命中 Anthropic 上游时会透传原生 Anthropic SSE 并提取 usage |
| POST | `/v1/messages/count_tokens` | 基础实现，本地近似 Token 计数；优先统计 `system` 和 `messages[].content` 的文本内容，不把 JSON 字段名当作 token；非法 JSON 返回 Anthropic 兼容 `invalid_json` |
| GET | `/v1/models` | 基础实现，模型列表 |
| GET | `/v1/models/:model` | 基础实现，模型详情；支持协议选择器返回 Anthropic 或 Gemini 外形 |

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
- `routerx.upstream` 用于安全补充上游 header、query 和 JSON body 参数；当前实现会把允许的 header/query 加到真实上游请求，并把 `routerx.upstream.body` 中不存在于原请求的字段合并进 JSON 请求体。
- 敏感鉴权 header/query 必须来自通道配置，不能由用户请求覆盖；已有请求字段、`model`、`routerx` 和 `stream` 不会被 `routerx.upstream.body` 改写。
- `routerx.provider.<provider>` 仅在选中对应上游 provider 时生效；当前基础实现会把匹配 provider 下的 JSON 字段作为 body 缺省补充，优先级高于通用 `routerx.upstream.body`，但仍不能覆盖调用方原请求字段、`model`、`routerx` 或 `stream`。
- OpenAI-compatible Chat 命中 Gemini 上游时，`routerx.provider.gemini.safetySettings` 会映射到 Gemini 原生 `safetySettings`；其他未显式支持的 Gemini provider 字段不会透传到真实厂商请求。Anthropic `messages` 原生入口命中 Anthropic 上游时，会保留 `system/messages/max_tokens/metadata/stop_sequences/stream/temperature/top_p/top_k/tools/tool_choice/thinking` 等白名单字段到真实上游请求体。Gemini `generateContent` 原生入口命中 Gemini 上游时，会保留 `contents/systemInstruction/generationConfig/safetySettings/tools/toolConfig/cachedContent` 到真实上游请求体。
- multipart 或非 JSON 请求当前可通过 `routerx` 表单字段或 `X-RouterX-Options` header 传递 JSON 字符串；body/form 中的 `routerx` 优先于 header，multipart 当前只应用路由偏好和安全 header/query 补充，不重写文件表单 body。
- 对 `GET /v1/models` 这类无 JSON body 的冲突路径，当前按 `format`、`routerx_protocol`、`X-RouterX-Protocol`、`anthropic-version`、OpenAI 默认值的顺序选择返回外形；`format` 继续保持最高优先级以兼容已有调用方。

路由偏好处理：

| 场景 | 目标行为 |
|------|----------|
| 偏好合法且候选通道可用 | 进入正常通道选择，并在日志中记录偏好被接受 |
| 偏好字段未知但不影响安全 | 忽略未知字段，在日志中记录被忽略 |
| 偏好格式非法 | 返回当前入口协议兼容的 400 错误 |
| 偏好要求无权限通道或 provider | 返回当前入口协议兼容的 403 错误 |
| 偏好合法但筛选后无可用通道 | 返回当前入口协议兼容的无可用通道错误 |

安全边界：

- 客户端不能通过 `routerx.upstream.headers` 覆盖 `Authorization`、`Cookie`、`Set-Cookie`、`X-Api-Key`、`api-key`、`Content-Type` 或 `X-RouterX-*` 等敏感/内部 header，也不能通过 query 覆盖常见 API key 参数。
- 客户端不能通过 `routerx.upstream.body` 覆盖 RouterX 已经完成安全决策的内部字段；当前 `model`、`routerx`、`stream` 和原请求已存在字段都会保持原值。
- RouterX-Compatible 上游可以继续接收 `routerx` 扩展，但真实厂商上游必须在请求发出前移除该私有字段。
- 所有路由偏好、是否命中、是否被拒绝和最终通道应进入日志或后续路由决策快照。

### 多层 RouterX

当上游通道也是 RouterX 时，需要保持兼容：

- 允许保留 `routerx` 扩展字段继续转发。
- 当前实现会在选中 RouterX-Compatible 上游时保留 `routerx` 扩展字段、递增 `X-RouterX-Hop`，并把 `X-RouterX-Chain` 追加当前 `routerx` 节点后转发；最大跳数由 `relay.routerx_max_hops` 配置，默认 `3`，达到或超过上限时返回 `routerx_hop_exceeded` 且不调用上游。
- 每层透传或生成请求 ID，默认使用 `X-Request-Id`；部署方可通过 `observability.request_id_header` 改为其他合法 HTTP header 名，便于跨层追踪。
- 调用方携带合法 W3C `traceparent` 时，每层会通过 `Traceparent` 响应头回显，并在 `/v1` 真实上游请求中继续透传；合法 `tracestate` 会随同通过 `Tracestate` 回显和透传。非法 trace context 会被忽略，不进入日志或上游请求。
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
- 对不支持的接口返回明确的 `404` 或当前格式兼容的 `unsupported_api` 错误；未知 `/v1` 路径仍必须先经过初始化、API Key 和限流中间件。
- 管理端和用户端 API 使用 `/v0` 版本前缀，后续破坏性变更使用 `/v1/admin` 或 `/v1/user`，不与模型 API 混淆。
