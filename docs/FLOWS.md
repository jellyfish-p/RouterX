# RouterX 用户路径契约

## 目标

本文档描述 RouterX 的产品任务路径，不设计网页布局、视觉风格或页面组件。它回答三个问题：

- 小白自部署用户如何从空系统走到第一次成功调用。
- 技术用户如何从单通道进入多通道、路由、计费和观测进阶能力。
- 每条路径的成功证据、失败解释和权限边界是什么。

主设计原则以 `docs/DESIGN.md` 为准，具体接口以 `docs/API.md` 为准，实现落点以 `docs/IMPLEMENTATION.md` 为准。

## 路径分层

| 层级 | 用户 | 目标 | 不应要求 |
|------|------|------|----------|
| L0 开箱路径 | 小白自部署用户 | 完成第一次模型调用，并看到日志和额度变化 | 支付、OAuth/OIDC、多通道、复杂计费、流式响应 |
| L1 管理路径 | 站长和小团队管理员 | 管理用户、API Key、通道、日志、额度和设置 | 理解所有 provider 转换矩阵 |
| L2 技术路径 | 技术用户和企业用户 | 配置路由、通道分组、多 key、模型重写、计费和观测 | 破坏 L0 默认路径 |
| L3 运营路径 | 平台运营者 | 充值码、支付、价格策略、审计和报表 | 作为 P0 首次调用前置条件 |

## L0：从空系统到第一次成功调用

路径：

```text
检查初始化状态
    -> 初始化超级管理员
    -> 管理员登录
    -> 创建第一个通道
    -> 创建第一个 API Key
    -> 调用 /v1/models
    -> 调用 /v1/chat/completions
    -> 查看用户日志和额度变化
```

任务合同：

| 步骤 | 操作者 | 接口 | 成功证据 | 失败时应解释 |
|------|--------|------|----------|--------------|
| 检查初始化 | 未登录用户 | `GET /v0/setup/status` | `initialized=false/true` | DB 不可用或初始化状态不可判断 |
| 初始化管理员 | 未初始化系统 | `POST /v0/setup/init` | 超级管理员、本地身份、默认 settings 写入 | 已初始化、弱密码、用户名冲突、JWT 配置不可用 |
| 登录 | 管理员 | `POST /v0/user/login` | 返回 User JWT 和用户摘要 | 凭据错误不泄露账号存在性 |
| 创建通道 | 管理员 | `POST /v0/admin/channel` | 通道启用，密钥加密或响应脱敏 | provider 不支持、密钥缺失、模型配置非法、连通性失败 |
| 创建 API Key | 用户或管理员自己 | `POST /v0/user/token` | 返回一次性 `sk-` 明文，列表后续不展示明文 | 额度不足、用户禁用、过期时间非法 |
| 模型列表 | API 调用方 | `GET /v1/models` | OpenAI-compatible models 响应 | API Key 无效、余额不足、无可用通道 |
| 非流式调用 | API 调用方 | `POST /v1/chat/completions` | OpenAI-compatible 响应、日志成功、额度扣减 | 请求非法、无通道、上游失败、余额不足 |
| 查看结果 | 用户 | `GET /v0/user/log`、`GET /v0/user/billing` | 能看到自己的调用、usage、额度变化 | 权限不足、日志为空、统计延迟 |

L0 体验要求：

- 第一次调用前不要求配置支付、OAuth/OIDC、价格表达式、流式响应、多 provider 或观测系统。
- 所有失败都要告诉用户下一步：换 Key、充值、修请求、找管理员还是稍后重试。
- API Key 和下游密钥不出现在响应、日志、审计摘要或错误 message 中。
- 初始化后的超级管理员要么有启动额度，要么系统明确提示先调整额度。

## L0 失败归因

| 失败现象 | 用户动作 | 管理员动作 | 系统证据 |
|----------|----------|------------|----------|
| 401 `invalid_api_key` | 更换或重新创建 API Key | 检查 Token 是否存在、过期、禁用或被删除 | token 校验失败摘要 |
| 403 `user_disabled` | 联系管理员 | 启用用户或解释封禁原因 | user status |
| 429 `insufficient_quota` | 充值或联系管理员加额度 | 检查用户额度、Key 预算和有限/无限口径 | user quota、key budget |
| 400 `model_required` | 补充 model 字段 | 不需要介入，除非大量出现 | request parse error |
| 400 `unsupported_stream` / 502 `unsupported_stream_channel` | 改为非流式，或切换到 OpenAI SSE 形态通道 | 确认入口协议、APIType 和通道是否支持当前流式请求 | request stream=true、channel type |
| 502 `no_available_channel` | 换模型或联系管理员 | 检查通道启用、模型匹配、熔断和 provider adapter | 候选过滤摘要 |
| 502 `upstream_secret_error` | 联系管理员 | 检查 `ENCRYPTION_KEY` 和通道密钥 | channel id、provider、解密错误摘要 |
| 504 `upstream_timeout` | 稍后重试 | 检查下游耗时和超时设置 | duration、timeout、channel id |

## L1：站长管理路径

目标：让站长能维护一个可用的小规模自部署服务。

| 任务 | 必须能做 | 必须防止 |
|------|----------|----------|
| 用户管理 | 创建普通用户、禁用用户、调整额度、查看用户状态 | 普通用户越权查看或调整他人数据 |
| API Key 管理 | 用户创建、禁用、删除自己的 Key；管理员理解额度来源 | 用户编辑自身额度或无限标记 |
| 通道管理 | 创建、测试、启停、查看模型、配置优先级和权重 | 响应中泄露下游密钥 |
| 日志管理 | 用户查自己的日志，管理员查全局日志和筛选 | 无条件全表清理且没有审计 |
| 设置管理 | 超级管理员修改 settings，敏感值脱敏 | 普通管理员修改关键 settings |

L1 成功标准：

- 管理员能解释“为什么这个用户不能调用”。
- 管理员能解释“为什么这个模型没有可用通道”。
- 管理员能解释“这次调用消耗了多少额度”。
- 禁用用户、Token 或通道后立即影响后续调用。

## L2：技术用户进阶路径

### 多通道与上游解析

路径：

```text
单通道 base_url/api_key
    -> 多 api_keys
    -> 多 base_urls
    -> upstreams 绑定 base_url + key
    -> priority + weight
    -> model_rewrites
    -> channel_group
```

规则：

- `upstreams` 非空时优先使用，并把 base URL 和 key 视为绑定对。
- `api_keys` 和 `base_urls` 是外层简化能力，不与 `upstreams` 交叉组合。
- `priority` 先于 `weight`；同 priority 内按 weight 选择。
- `weight <= 0` 按 1 处理。
- `model_rewrites` 只改变上游模型名，不改变用户账单里的原始请求语义。

### 路由偏好

技术用户可以通过 API Key/channel-group scope 表达偏好，但不能越权。
策略决策顺序、访问控制和冲突规则以 `docs/POLICIES.md` 为准。入口协议、APIType、上游厂商和能力等级以 `docs/PROTOCOLS.md` 为准。

| 场景 | 行为 |
|------|------|
| 偏好合法且候选可用 | 接受偏好并记录日志摘要 |
| 偏好字段未知但安全 | 忽略未知字段并记录 |
| 偏好格式非法 | 返回入口协议兼容 400 |
| 偏好要求无权限通道或 provider | 返回入口协议兼容 403 |
| 偏好筛选后无候选 | 返回无可用通道错误 |

### 计费进阶

路径：

```text
P0 usage.total_tokens -> quota_used
    -> model_prices
    -> channel_model_prices
    -> user_group ratio
    -> channel_group ratio
    -> access rule snapshot
    -> billing snapshot
```

规则：

- P0 成功调用必须有 `logs.quota_used`。
- P1 价格表达式计算基础费用，倍率在表达式之后应用。
- 历史日志保存规则快照，规则变更不能改变旧账单解释。
- 支付、充值码和退款是运营增强，不改变模型调用事实链。

### 观测进阶

技术用户最终应能从日志或指标回答：

- 哪个用户、哪个 Token、哪个模型、哪个通道发生了调用。
- 请求为什么选中这个通道。
- usage 从上游、adapter、本地估算还是最低规则而来。
- 失败是请求问题、权限问题、额度问题、路由问题、通道配置问题还是下游问题。
- 管理员在什么时候改过关键 settings、通道、价格或额度。

## L3：运营路径

运营能力是商业闭环增强，但不是 P0 前置条件。
支付、充值码、退款、人工补账和额度流水的详细契约以 `docs/PAYMENTS.md` 为准。

| 能力 | 阶段 | 入口 | 成功证据 |
|------|------|------|----------|
| 充值码 | P1/P2 | 用户兑换接口、管理员生成接口 | 兑换幂等、额度入账、审计可查 |
| 在线支付 | P2 | Stripe、易支付 | 回调签名校验、金额校验、重复通知不重复入账 |
| 价格策略 | P1 | `model_prices`、`channel_model_prices`、settings 倍率 | 计费快照可还原 |
| 用户分组 | P1 | 用户分组、通道分组访问控制，语义见 `docs/POLICIES.md` | 普通用户不能访问未授权通道分组 |
| 报表 | P2 | 日志聚合、账单聚合、指标 | 消耗、收入、错误率和通道状态可查 |

## 控制台能力边界

控制台和等价接口的完整能力契约以 `docs/CONSOLE.md` 为准。本文档不定义页面怎么排版，只保留用户路径中必须可见的核心状态：

| 能力 | 必须显示或返回 | 不能显示 |
|------|----------------|----------|
| 初始化 | 是否已初始化、初始化失败原因 | 数据库 DSN 明文 |
| API Key | 名称、状态、过期时间、剩余额度、最近使用时间 | API Key 完整明文，创建后再次展示 |
| 通道 | provider、模型、启用状态、优先级、权重、错误计数、密钥脱敏状态 | 下游 API Key 明文 |
| 调用日志 | user、token、channel、model、usage、quota、status、error 摘要 | 未脱敏请求体、响应体、密钥 |
| settings | 分类、当前值、默认值、生效方式、是否敏感 | 敏感值明文 |
| 账单 | 调用数、token 数、消耗额度、余额变化 | 把 API Key 预算设置误当模型消费 |

## 测试映射

| 用户路径 | 测试 |
|----------|------|
| 初始化和启动额度 | `TestSetupBootstrapAdminQuota` |
| settings 默认值和 readiness | `TestSettingsValidationAndReadiness` |
| Chat 成功调用 | `TestChatCompletionSuccessLogsAndDeductsQuota` |
| 请求错误不调用下游 | `TestChatCompletionInvalidRequestDoesNotCallUpstream` |
| 下游错误映射 | `TestChatCompletionUpstreamErrorMapping` |
| 预检拒绝不调用下游 | `TestRelayPrecheckRejectsBeforeUpstream` |
| 通道高级配置 | `TestChannelRoutingConfigResolution` |
| 日志账单一致 | `TestUserBillingMatchesLogs` |

新增用户路径时，应先补这里的路径合同，再补 `docs/CONSOLE.md`、`docs/API.md`、`docs/TESTING.md` 和 `docs/IMPLEMENTATION.md`。
