# RouterX 错误代码与失败语义

本文档是 RouterX 错误 code、HTTP 状态、协议兼容响应、日志归因、重试和扣费语义的权威目录。它服务四类实现者：

- Handler 实现者：知道该返回什么 HTTP 和响应外形。
- Relay 实现者：知道错误是否可重试、是否增加通道错误计数、是否扣费。
- 测试实现者：知道每个失败路径应断言哪些事实。
- 运营和排障人员：知道看到某个 code 后该检查哪里。

## 总原则

- `/v0/setup/*`、`/v0/user/*`、`/v0/admin/*` 使用 RouterX 统一响应：`{ "success": false, "message": "...", "data": null }`。
- `/v1/*` 使用入口协议兼容错误响应，不返回 RouterX 统一响应包装。
- 内部 code 使用稳定英文 snake_case；展示 message 可以本地化，但不能泄露密钥、DSN、支付密钥、内部堆栈或完整敏感上游响应。
- 认证失败、权限不足、额度不足、访问控制失败和请求格式错误默认不调用上游。
- 策略拒绝、路由偏好越权、分组访问控制和限流的语义以 `docs/POLICIES.md` 为准；入口协议、APIType、流式阶段和能力等级以 `docs/PROTOCOLS.md` 为准。本文只定义错误外形和 code 语义。
- 失败默认不扣费；已有有效 usage 或流式已输出后的补偿策略必须写入 settings 和日志快照。
- 每个新增 code 都要同步 API、Relay、测试、Runbook 和追踪矩阵。

## 响应外形

### `/v0` 统一错误

当前 `/v0` 错误主要依赖 HTTP 状态和中文 message：

```json
{
  "success": false,
  "data": null,
  "message": "未登录或登录已过期"
}
```

目标增强可以增加机器可读 code，但不得破坏已有客户端：

```json
{
  "success": false,
  "data": null,
  "message": "未登录或登录已过期",
  "code": "unauthorized"
}
```

### OpenAI-compatible

```json
{
  "error": {
    "message": "no available upstream channel",
    "type": "upstream_error",
    "code": "no_available_channel"
  }
}
```

### Anthropic-compatible

```json
{
  "type": "error",
  "error": {
    "type": "upstream_error",
    "message": "no available upstream channel"
  }
}
```

### Gemini-compatible

```json
{
  "error": {
    "code": 502,
    "message": "no available upstream channel",
    "status": "UNAVAILABLE"
  }
}
```

## `/v1` 错误目录

| Code | HTTP | Type / Status | 来源 | 上游调用 | 重试 | 扣费 | 日志要求 | 处理动作 |
|------|------|---------------|------|----------|------|------|----------|----------|
| `service_not_initialized` | 503 | `server_error` / `UNAVAILABLE` | 初始化中间件 | 否 | 否 | 否 | request_id、path | 先完成 `/v0/setup/init` |
| `invalid_request` | 400 | `invalid_request_error` / `INVALID_ARGUMENT` | 请求转换或通用参数错误 | 否 | 否 | 否 | 解析失败摘要 | 修正请求 |
| `invalid_json` | 400 | `invalid_request_error` / `INVALID_ARGUMENT` | JSON 解析失败 | 否 | 否 | 否 | body 读取或解析摘要，不保存完整 body | 修正 JSON |
| `invalid_multipart` | 400 | `invalid_request_error` / `INVALID_ARGUMENT` | multipart 表单解析失败 | 否 | 否 | 否 | content-type、boundary 和字段摘要，不保存完整文件内容 | 修正表单边界、字段或文件 |
| `model_required` | 400 | `invalid_request_error` / `INVALID_ARGUMENT` | 缺少模型 | 否 | 否 | 否 | 缺少字段 | 补充 model |
| `unsupported_stream` | 400 | `invalid_request_error` / `INVALID_ARGUMENT` | 当前入口或 APIType 尚未开启流式 | 否 | 否 | 否 | stream=true | 使用非流式或等待对应协议流式实现 |
| `unsupported_stream_channel` | 502 | `upstream_error` / `UNAVAILABLE` | 流式请求命中非 OpenAI SSE 形态通道 | 否 | 否 | 否 | channel_id、channel_type、api_type | 换用 OpenAI-compatible 流式通道或等待对应 provider chunk 转换 |
| `unsupported_multipart_channel` | 502 | `upstream_error` / `UNAVAILABLE` | multipart 请求命中暂不支持文件表单透传的上游 adapter | 否 | 否 | 否 | channel_id、channel_type、api_type | 换用 OpenAI-compatible 通道或等待对应 provider multipart 转换 |
| `invalid_routerx_options` | 400 | `invalid_request_error` / `INVALID_ARGUMENT` | `routerx` 结构非法 | 否 | 否 | 否 | 私有字段解析摘要 | 修正 `routerx` |
| `invalid_routerx_route` | 400 | `invalid_request_error` / `INVALID_ARGUMENT` | 路由偏好格式非法 | 否 | 否 | 否 | route 摘要 | 修正路由偏好 |
| `unsupported_api` | 404 | `invalid_request_error` / `NOT_FOUND` | 已知前缀下不支持的 API | 否 | 否 | 否 | api_type、path | 换用支持的接口 |
| `model_not_found` | 404 | `not_found_error` / `NOT_FOUND` | 模型详情或模型匹配失败 | 否 | 否 | 否 | model | 检查模型名 |
| `invalid_api_key` | 401 | `authentication_error` / `UNAUTHENTICATED` | API Key 缺失或无效 | 否 | 否 | 否 | token hash 摘要或空 key 原因 | 更换 API Key |
| `expired_api_key` | 401 | `authentication_error` / `UNAUTHENTICATED` | API Key 过期 | 否 | 否 | 否 | token_id | 重新创建 API Key |
| `user_disabled` | 403 | `permission_error` / `PERMISSION_DENIED` | 用户禁用或软删除 | 否 | 否 | 否 | user_id、token_id | 联系管理员 |
| `model_not_allowed` | 403 | `permission_error` / `PERMISSION_DENIED` | 策略或 API Key scope 禁止访问该模型 | 否 | 否 | 否 | model、scope 摘要、策略版本 | 调整模型名或访问策略 |
| `token_forbidden` | 403 | `permission_error` / `PERMISSION_DENIED` | Token 禁用、软删除或策略禁止 | 否 | 否 | 否 | token_id、原因 | 启用或重建 API Key |
| `route_forbidden` | 403 | `permission_error` / `PERMISSION_DENIED` | `routerx.route` 越权 | 否 | 否 | 否 | route 摘要、拒绝原因 | 移除偏好或调整权限 |
| `rate_limit_exceeded` | 429 | `rate_limit_error` / `RESOURCE_EXHAUSTED` | 限流 | 否 | 客户端可稍后重试 | 否 | 限流维度和 key 摘要 | 降低并发或等待窗口 |
| `insufficient_quota` | 429 | `insufficient_quota` 或 `rate_limit_error` / `RESOURCE_EXHAUSTED` | 预检余额或 Key 预算不足，或扣费失败 | 预检失败时否 | 否 | 预检失败不扣；已调用后按事务结果 | user quota、key budget、quota_used | 充值、调整额度或提高 Key 预算 |
| `no_available_channel` | 502 | `upstream_error` / `UNAVAILABLE` | 没有候选通道 | 否 | 否 | 否 | model、候选过滤摘要 | 检查通道启用、模型、熔断和访问控制 |
| `unsupported_channel` | 502 | `upstream_error` / `UNAVAILABLE` | 通道类型无 Adapter | 否 | 否 | 否 | channel_id、type | 补 Adapter 或禁用通道 |
| `upstream_secret_error` | 502 | `upstream_error` / `UNAVAILABLE` | 上游密钥缺失或不可解密 | 否 | 否 | 否 | channel_id、provider、解密错误摘要 | 检查 `ENCRYPTION_KEY` 和通道密钥 |
| `upstream_request_failed` | 502 | `upstream_error` / `UNAVAILABLE` | 网络错误或请求发送失败 | 可能未到达上游 | 非流式可按策略重试 | 否 | channel_id、耗时、错误摘要 | 检查网络和上游可用性 |
| `upstream_response_failed` | 502 | `upstream_error` / `UNAVAILABLE` | 响应读取失败 | 是 | 非流式可按策略重试 | 否，除非已有 usage | channel_id、状态、读取错误 | 检查上游和响应大小 |
| `upstream_conversion_failed` | 502 | `upstream_error` / `UNAVAILABLE` | 上游响应无法转换 | 是 | 否 | 否，除非已有 usage | adapter、api_type、脱敏摘要 | 修复 Adapter 或降级字段 |
| `usage_missing` | 502 | `upstream_error` / `UNAVAILABLE` | 上游成功响应缺少 usage，且 `billing.usage_missing_strategy=reject` | 是 | 否 | 否 | channel_id、api_type、usage 策略 | 检查上游 usage、Adapter 或改回最低计费策略 |
| `upstream_timeout` | 504 | `upstream_error` / `DEADLINE_EXCEEDED` | 上游超时 | 不确定 | 非流式可按策略重试 | 否，除非已有 usage | channel_id、timeout、耗时 | 检查超时和上游状态 |
| `upstream_400` | 502 或 400 | `upstream_error` / `INVALID_ARGUMENT` | 上游认为请求错误 | 是 | 否 | 否 | 上游状态、脱敏摘要 | 检查转换和请求参数 |
| `upstream_401` | 502 | `upstream_error` / `UNAVAILABLE` | 上游认证失败 | 是 | 否 | 否 | channel_id、provider | 检查上游密钥 |
| `upstream_403` | 502 | `upstream_error` / `UNAVAILABLE` | 上游权限不足 | 是 | 否 | 否 | channel_id、provider | 检查上游账号权限 |
| `upstream_429` | 429 | `rate_limit_error` / `RESOURCE_EXHAUSTED` | 上游限流 | 是 | 非流式可按 `relay.retry_count` 和 `relay.retry_on_status` 换候选通道 | 否，除非已有 usage | channel_id、provider、上游状态 | 降低并发或切换通道 |
| `upstream_5xx` | 502 | `upstream_error` / `UNAVAILABLE` | 上游临时故障 | 是 | 默认 500/502/503/504 可按白名单重试 | 否，除非已有 usage | status、channel_id、重试次数 | 检查上游健康和熔断 |
| `billing_failed` | 500 | `server_error` / `INTERNAL` | 扣费事务或日志事实异常 | 可能已调用 | 否 | 按事务结果 | quota_used、事务错误 | 人工核对账单 |
| `insufficient_quota_after_usage` | 429 | `rate_limit_error` / `RESOURCE_EXHAUSTED` | 实际 usage 超过可扣额度 | 是 | 否 | 按事务结果 | usage、quota_used、余额 | 调整预留和并发策略 |
| `model_list_failed` | 500 | `server_error` / `INTERNAL` | 模型列表聚合失败 | 不一定 | 否 | 否 | provider、channel 摘要 | 检查通道模型列表 |
| `internal_error` | 500 | `server_error` / `INTERNAL` | 未分类内部错误或 panic | 不确定 | 否 | 按日志事实 | request_id、脱敏错误 | 查看系统日志 |

## 当前代码事实和目标收敛

当前代码已经使用 `service.HTTPError` 表达 `/v1` 主链路错误；API Key 鉴权、用户禁用、配额预检查和基础下游错误会按入口协议输出 OpenAI-compatible、Anthropic 或 Gemini 错误结构。以下差异需要在后续收敛时保持兼容：

| 当前事实 | 目标口径 |
|----------|----------|
| Anthropic/Gemini wrapper 转换失败当前可能返回 `response_conversion_failed`。 | 统一归入 `upstream_conversion_failed`，可保留 `response_conversion_failed` 作为兼容别名。 |
| `parseRelayRequest` 对缺少 model 当前可能走 `invalid_request`。 | 对外目标使用 `model_required`，日志可保留原始解析错误摘要。 |
| 上游 400/401/403 当前多以 502 + `upstream_<status>` 返回。 | P1 可按入口协议细化；默认不重试，只有管理员显式加入 `relay.retry_on_status` 时才会换候选，401/403 仍应优先归因通道配置。 |
| 超时已拆分为 `upstream_timeout`。 | 由 `TestChatCompletionUpstreamTimeoutMapping` 覆盖，便于告警和客户端重试判断。 |
| `/v0` 统一响应当前没有稳定 code 字段。 | 若增加 code，需要保持旧字段并更新 API 文档和测试。 |

## `/v0` 错误语义

`/v0` 主要面向控制台和管理端，默认不要求所有错误都有机器 code，但 HTTP 状态必须稳定。

| 场景 | HTTP | 响应要求 |
|------|------|----------|
| 参数错误 | 400 | message 指出参数无效，不返回内部结构。 |
| 未登录或 JWT 失效 | 401 | message 可模糊，不泄露账号存在性。 |
| 权限不足 | 403 | message 指出需要权限或操作不允许。 |
| 资源不存在 | 404 | 不泄露其他用户资源是否存在。 |
| 状态冲突 | 409 | 用于重复初始化、唯一性冲突、状态不可变更。 |
| 限流 | 429 | message 提示稍后重试。 |
| 内部错误 | 500 | message 简洁，详细原因进入脱敏日志。 |

## 重试语义

| 错误类型 | 客户端重试 | RouterX 换通道重试 | 说明 |
|----------|------------|---------------------|------|
| 请求格式错误 | 否 | 否 | 调用方必须修正请求。 |
| 鉴权或权限错误 | 否 | 否 | 重试会扩大风险。 |
| 余额不足 | 否 | 否 | 需要充值或调整额度。 |
| 限流 | 可延迟重试 | 可按策略换候选通道 | 需要保留 Retry-After 或日志摘要。 |
| 上游 400 | 否 | 默认否；显式加入 `relay.retry_on_status` 后非流式可换候选 | 多数是转换或参数问题，只有确认多通道差异时才建议放开。 |
| 上游 401/403 | 否 | 默认否；生产环境不建议加入 `relay.retry_on_status` | 通道密钥或权限问题，不应放大请求。 |
| 上游 429 | 可延迟重试 | 非流式可按 `relay.retry_count` 和 `relay.retry_on_status` 换候选通道 | 注意供应商风控。 |
| 上游 5xx/网络错误/超时 | 可重试 | 非流式未输出前可按 `relay.retry_count` 重试；HTTP 状态码由 `relay.retry_on_status` 控制 | 流式输出后不能切换通道。 |
| 计费失败 | 否 | 否 | 需要保护账单事实。 |

## 日志字段要求

失败日志至少需要支持以下事实，P0 可先保存摘要，P1/P2 继续结构化：

| 字段 | 要求 |
|------|------|
| `request_id` | 能关联 HTTP 访问日志、调用日志和管理审计。 |
| `user_id` / `token_id` | 认证前失败可为空，认证后失败必须记录。 |
| `channel_id` | 尚未选中通道时可为空。 |
| `model` | 保存调用方请求模型名。 |
| `error_code` | 目标字段，用于结构化统计。 |
| `error_source` | request、auth、quota、route、channel、upstream、billing、system。 |
| `upstream_status` | 上游已返回 HTTP 时记录。 |
| `retry_count` | 发生重试时记录；当前基础实现以多条失败/成功日志表示每次尝试。 |
| `quota_used` | 失败默认 0；已有 usage 或补偿扣费时必须可解释。 |
| `error_msg` | 脱敏摘要，不包含密钥、DSN、完整 prompt 或完整响应。 |

## 测试要求

新增或改变错误 code 时，测试至少覆盖：

- HTTP 状态正确。
- `/v1` 错误外形符合入口协议，不返回 `{success,data,message}`。
- 错误 code、type/status 和 message 不泄露敏感信息。
- 是否调用上游符合目录中的“上游调用”列。
- 是否扣费符合目录中的“扣费”列。
- 失败日志包含排障所需摘要。
- 可重试错误和不可重试错误在通道错误计数、重试次数和熔断行为上可区分。

## 文档同步

错误语义改动需要同步检查：

- `docs/API.md`：接口错误格式、状态码和鉴权边界。
- `docs/RELAY.md`：错误来源、重试、熔断和日志要求。
- `docs/SECURITY.md`：错误是否影响安全控制点。
- `docs/TESTING.md`：错误格式和失败事实断言。
- `docs/TRACEABILITY.md`：对应能力验收证据。
- `docs/OPERATIONS.md`：排障动作和告警分类。
- `docs/RUNBOOKS.md`：用户、管理员和运维看到该 code 后的检查顺序与安全动作。
