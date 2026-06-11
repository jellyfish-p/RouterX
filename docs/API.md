# RouterX API 设计

## API 分层

RouterX 对外提供三类 API。

| 类型 | 前缀 | 受众 | 响应格式 |
|------|------|------|----------|
| 系统初始化 | `/v0/setup` | 初始化页面 | RouterX 统一响应 |
| 管理端 API | `/v0/admin` | 管理员后台 | RouterX 统一响应 |
| 用户端 API | `/v0/user` | 用户控制台 | RouterX 统一响应 |
| 模型转发 API | `/v1` | OpenAI、Gemini、Anthropic SDK 和外部调用方 | 对应兼容格式响应 |

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

## 公共接口

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/health` | 已注册 | 健康检查 |
| GET | `/v0/setup/status` | 已实现 | 查询系统是否初始化 |
| POST | `/v0/setup/init` | 占位 | 首次初始化超级管理员和默认设置 |

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
| GET | `/v0/admin/user` | 占位 | 用户列表 |
| POST | `/v0/admin/user` | 占位 | 创建用户 |
| PUT | `/v0/admin/user/:id` | 占位 | 编辑用户 |
| DELETE | `/v0/admin/user/:id` | 占位 | 删除用户 |
| PATCH | `/v0/admin/user/:id/quota` | 占位 | 调整用户额度 |

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

### 管理员管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/admin` | 占位 | 管理员列表 |
| POST | `/v0/admin/admin` | 占位 | 创建管理员 |
| PUT | `/v0/admin/admin/:id` | 占位 | 编辑管理员 |
| DELETE | `/v0/admin/admin/:id` | 占位 | 删除管理员 |

权限规则：

- 只有超级管理员可以管理管理员账号。
- 普通管理员不能创建超级管理员。
- 不能删除自己。
- 不能删除最后一个超级管理员。

### 通道管理

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/admin/channel` | 占位 | 通道列表 |
| POST | `/v0/admin/channel` | 占位 | 创建通道 |
| PUT | `/v0/admin/channel/:id` | 占位 | 编辑通道 |
| DELETE | `/v0/admin/channel/:id` | 占位 | 删除通道 |
| POST | `/v0/admin/channel/:id/test` | 占位 | 测试通道连通性 |

创建通道目标请求：

```json
{
  "type": 1,
  "name": "openai-main",
  "models": "gpt-4o,gpt-4o-mini",
  "base_url": "https://api.openai.com",
  "api_key": "sk-...",
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
| GET | `/v0/admin/log` | 占位 | 调用日志列表 |
| DELETE | `/v0/admin/log` | 占位 | 清理日志 |
| GET | `/v0/admin/dashboard` | 占位 | 仪表盘统计 |
| GET | `/v0/admin/setting` | 占位 | 获取系统设置 |
| PUT | `/v0/admin/setting` | 占位 | 批量更新系统设置 |

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

删除日志目标要求：

- 必须支持时间范围。
- 默认拒绝无条件全表清理。
- 需要记录管理审计日志。

## 用户端 API

前缀：`/v0/user`。

鉴权：

- 注册和登录不需要 User JWT，但需要系统已初始化。
- 个人信息、日志和账单需要 User JWT。

### 认证和个人信息

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| POST | `/v0/user/register` | 占位 | 用户注册 |
| POST | `/v0/user/login` | 占位 | 用户登录 |
| GET | `/v0/user/self` | 占位 | 获取个人信息 |
| PUT | `/v0/user/self` | 占位 | 修改个人信息 |
| POST | `/v0/user/self/password` | 占位 | 修改密码 |

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

### 用量和账单

| 方法 | 路径 | 当前状态 | 说明 |
|------|------|----------|------|
| GET | `/v0/user/log` | 占位 | 当前用户调用日志 |
| GET | `/v0/user/billing` | 占位 | 当前用户账单统计 |

### 目标扩展接口

当前代码尚未注册以下用户端接口，但产品上需要纳入目标设计。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v0/user/token` | API Key 列表 |
| POST | `/v0/user/token` | 创建 API Key |
| PUT | `/v0/user/token/:id` | 编辑 API Key |
| DELETE | `/v0/user/token/:id` | 删除 API Key |
| POST | `/v0/user/redem` | 使用充值码 |
| GET | `/v0/user/payment/products` | 充值商品列表 |
| POST | `/v0/user/payment/orders` | 创建支付订单 |
| GET | `/v0/user/payment/orders` | 当前用户支付订单列表 |
| GET | `/v0/user/payment/orders/:order_no` | 当前用户支付订单详情 |
| GET | `/v0/user/models` | 可用模型和价格列表 |

### 支付接口

支付接口用于用户在线购买额度。当前目标支持 Stripe 和易支付。

用户鉴权接口：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v0/user/payment/products` | 获取可购买的充值商品 |
| POST | `/v0/user/payment/orders` | 创建支付订单并返回支付跳转信息 |
| GET | `/v0/user/payment/orders` | 查询当前用户支付订单列表 |
| GET | `/v0/user/payment/orders/:order_no` | 查询当前用户支付订单详情 |

Provider 回调接口：

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/v0/payment/stripe/webhook` | Stripe 签名 | Stripe Webhook 异步通知 |
| GET/POST | `/v0/payment/epay/notify` | 易支付签名 | 易支付异步通知 |
| GET | `/v0/payment/epay/return` | 无，仅读状态 | 易支付同步返回页 |

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

易支付通知要求：

- 校验易支付签名，排除 `sign`、`sign_type` 和空值字段后按网关规则生成签名。
- 校验 `pid`、`out_trade_no`、`money` 和成功状态。
- 入账只依赖异步通知；同步返回页只展示本地订单状态。
- 通知处理成功后返回网关要求的纯文本，例如 `success`。

安全要求：

- 客户端不能提交要增加的 `quota`，入账额度只能来自服务端商品配置。
- 支付成功更新订单和增加 `users.quota` 必须在同一事务中完成。
- 支付密钥、Stripe secret、Stripe webhook secret、易支付商户 key 不得返回给前端或写入日志明文。
- 回调接口不使用用户 JWT，但必须通过 provider 签名、订单金额和订单状态校验。

## 模型转发 API

前缀：`/v1`。

鉴权：

```http
Authorization: Bearer sk-xxxxxxxx
```

`/v1` 需要兼容 OpenAI、Gemini、Anthropic 三类入口请求格式，并根据通道配置转发到 OpenAI、Anthropic、Gemini、xAI、Azure OpenAI、Qwen、DeepSeek、OpenAI-Compatible、RouterX-Compatible 等上游。以下仅列目标接口表。

模型转发接口不使用 RouterX 统一响应。入口协议为 OpenAI 时返回 OpenAI 兼容响应，入口协议为 Gemini 时返回 Gemini 兼容响应，入口协议为 Anthropic 时返回 Anthropic 兼容响应。

### OpenAI 格式

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/responses` | Responses API |
| POST | `/v1/chat/completions` | Chat Completions |
| POST | `/v1/completions` | Legacy Completions |
| POST | `/v1/embeddings` | Embeddings |
| POST | `/v1/images/generations` | 图像生成 |
| POST | `/v1/images/edits` | 图像编辑 |
| POST | `/v1/images/variations` | 图像变体 |
| POST | `/v1/audio/transcriptions` | 语音转文本 |
| POST | `/v1/audio/translations` | 语音翻译 |
| POST | `/v1/audio/speech` | 文本转语音 |
| GET | `/v1/models` | 模型列表 |
| GET | `/v1/models/:model` | 模型详情 |
| POST | `/v1/moderations` | 内容审核 |

### Gemini 格式

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v1/models` | 模型列表 |
| GET | `/v1/models/{model}` | 模型详情 |
| POST | `/v1/models/{model}:generateContent` | 内容生成 |
| POST | `/v1/models/{model}:streamGenerateContent` | 流式内容生成 |
| POST | `/v1/models/{model}:countTokens` | Token 计数 |
| POST | `/v1/models/{model}:embedContent` | 单条 Embedding |
| POST | `/v1/models/{model}:batchEmbedContents` | 批量 Embedding |

### Anthropic 格式

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/messages` | Messages |
| POST | `/v1/messages/count_tokens` | Token 计数 |
| GET | `/v1/models` | 模型列表 |
| GET | `/v1/models/:model` | 模型详情 |

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

- `routerx.route` 用于路由偏好，不参与模型原生请求。
- `routerx.upstream` 用于补充上游 header、query 和 body 参数，但敏感鉴权 header 必须来自通道配置，不能由用户请求覆盖。
- `routerx.provider.<provider>` 仅在选中对应上游 provider 时生效。
- multipart 或非 JSON 请求可通过 `routerx` 表单字段传递 JSON 字符串，或通过 `X-RouterX-Options` header 传递 base64url JSON。
- 对 `GET /v1/models` 这类无 JSON body 的冲突路径，可使用 `?routerx_protocol=gemini` 或 `X-RouterX-Protocol: anthropic` 显式指定返回协议。

### 多层 RouterX

当上游通道也是 RouterX 时，需要保持兼容：

- 允许保留 `routerx` 扩展字段继续转发。
- 每层递增 `X-RouterX-Hop`，超过最大跳数时拒绝，避免循环。
- 每层透传或生成 `X-Request-Id`，便于跨层追踪。
- 转发到真实厂商前必须移除 `routerx` 私有字段和 `X-RouterX-*` 内部 header。

## API Key 生命周期

目标流程：

```text
用户登录 User JWT
    -> POST /v0/user/token
    -> 生成 sk- API Key
    -> 明文只返回一次
    -> DB 保存哈希或密文
    -> Redis 缓存校验结果
    -> 调用 /v1/* 时通过 Authorization Bearer 使用
```

API Key 校验规则：

- 格式必须以 `sk-` 开头。
- Token 不存在、禁用、软删除、过期均返回 401。
- 所属用户禁用或软删除返回 403。
- 额度不足返回 429。
- 鉴权成功后写入 `current_user` 和 `current_token` 上下文。

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
- 对未知兼容格式字段默认透传，不应无故拒绝。
- 对不支持的接口返回明确的 `404` 或当前格式兼容的 `unsupported_api` 错误。
- 管理端和用户端 API 使用 `/v0` 版本前缀，后续破坏性变更使用 `/v1/admin` 或 `/v1/user`，不与模型 API 混淆。
