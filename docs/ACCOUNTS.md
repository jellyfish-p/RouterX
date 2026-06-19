# RouterX 账号系统设计

## 目标

账号系统支持用户名、邮箱、手机号、第三方 OAuth 和 OIDC，但账号创建与登录规则必须满足以下约束。

| 规则 | 说明 |
|------|------|
| 所有账户必须设置密码 | 不允许存在纯验证码、纯 OAuth、纯 OIDC 的无密码账户 |
| 用户名 + 密码登录强制启用 | 后端不可关闭用户名密码登录能力 |
| 邮箱和手机号是可选登录标识 | 后端可分别开启或关闭邮箱、手机号登录 |
| 登录接口统一 | 用户名、邮箱、手机号登录使用同一个 API，不按标识类型拆接口 |
| 注册和登录开关分离 | 可以允许某方式登录，但禁止该方式注册 |
| 注册必须填写验证码 | 所有自助注册都必须校验验证码 |
| 登录可选择密码或验证码 | 登录时可使用密码，也可在开启后使用验证码 |
| 第三方登录不创建无密码账户 | OAuth/OIDC 首次注册必须补齐用户名和密码 |
| 注销不删除账号 | 用户注销后保留账号和身份标识，重新注册相同身份时恢复原账号 |
| 第三方注册也参与去重 | OAuth/OIDC 的 provider identity 命中已有账号时必须走恢复或绑定流程 |

账号系统的核心设计仍然是“用户资料”和“登录身份”分离，但业务层强制每个用户至少拥有一个 `username/local` 登录身份和一个有效密码。

## 当前实现边界

当前代码已经具备受 settings 控制的基础用户名密码注册、统一登录、User JWT、登录审计、管理员角色校验、API Key 鉴权、自助注销保留账号、注销密码二次确认和基础用户名恢复账号所需的账号能力。自助注册默认关闭；开启基础用户名注册时，服务端会检查 `auth.register.enabled`、`auth.register.username.enabled` 和 `auth.register.captcha.required`，新账号会应用默认额度/分组，命中已注销同名账号时会恢复原账号。本文档中的完整验证码校验、邮箱/手机号注册、OAuth/OIDC、隐私字段擦除和非用户名恢复属于目标设计，需要按阶段继续实现。

阶段边界：

- P0 收口用户名密码注册登录、User JWT、管理员权限、API Key 鉴权和基础账号安全。
- P1 增加邮箱/手机号身份、验证码、账号注销保留、恢复账号和登录审计。
- P2 增加 OAuth/OIDC、企业 SSO、第三方身份绑定和企业级风险控制。

## 登录方式

| 登录方式 | method | provider | 是否强制 | 说明 |
|----------|--------|----------|----------|------|
| 用户名 + 密码 | `username` | `local` | 是 | 强制启用，所有账户必须具备 |
| 邮箱 + 密码 | `email` | `local` | 否 | 邮箱登录开启后可用 |
| 手机号 + 密码 | `phone` | `local` | 否 | 手机号登录开启后可用 |
| 邮箱验证码 | `email` | `local` | 否 | 邮箱验证码登录开启后可用 |
| 手机号验证码 | `phone` | `local` | 否 | 手机号验证码登录开启后可用 |
| 第三方 OAuth | `oauth` | `github`、`google` 等 | 否 | 可绑定到已有有密码账户 |
| OIDC | `oidc` | 企业 IdP 别名 | 否 | 可绑定到已有有密码账户 |

## 数据模型

### `users`

`users` 保存核心用户资料和业务状态。

| 字段 | 说明 |
|------|------|
| `id` | 用户 ID |
| `username` | 主展示用户名，业务上必填 |
| `display_name` | 显示名 |
| `email` | 主邮箱，可为空 |
| `phone` | 主手机号，可为空 |
| `role` | 用户角色 |
| `quota` | 用户额度 |
| `status` | 用户状态 |
| `group_id` | 所属分组；新用户默认归入 `default` 分组 |

说明：

- `users.username` 在当前模型上可以为空是为了迁移兼容，但业务层创建账户时必须写入用户名。
- `users` 不保存密码，不负责登录唯一性。
- 如果实现使用数字 `group_id`，初始化或迁移必须保证存在 code 为 `default` 的用户分组；如果暂未建分组表，策略层必须把空分组归一为 `default`。
- 用户注销时不删除 `users` 记录，不软删除核心账号，不创建可绕过去重的新账号。
- 注销账号建议通过状态字段表达，当前模型可先复用禁用状态，后续可增加 `canceled` 或 `closed` 状态。

### `user_identities`

`user_identities` 保存所有登录身份。

| 字段 | 说明 |
|------|------|
| `user_id` | 绑定用户 |
| `method` | 登录方式，`username/email/phone/oauth/oidc` |
| `provider` | 提供方，`local` 或第三方名称 |
| `identifier` | 登录标识，例如用户名、邮箱、手机号、OAuth user id、OIDC sub |
| `password_hash` | 密码哈希，至少 `username/local` 身份必须有值 |
| `verified_at` | 认证或验证时间 |
| `last_used_at` | 最近登录时间 |

唯一约束：

```text
(method, provider, identifier)
```

必备身份：

```text
每个 users.id 必须有且至少有一个：
method = username
provider = local
identifier = 用户名
password_hash = bcrypt hash
```

示例：

| method | provider | identifier | password_hash | 说明 |
|--------|----------|------------|---------------|------|
| `username` | `local` | `alice` | bcrypt hash | 必备身份 |
| `email` | `local` | `alice@example.com` | 可为空 | 邮箱登录标识，密码登录时使用账户主密码校验 |
| `phone` | `local` | `+8613800000000` | 可为空 | 手机号登录标识，密码登录时使用账户主密码校验 |
| `oauth` | `github` | `12345678` | 空 | 第三方绑定身份 |
| `oidc` | `corp` | `00uabc...` | 空 | 企业 SSO 绑定身份 |

密码校验规则：

- 账户密码以 `username/local` 身份上的 `password_hash` 为准。
- 邮箱和手机号密码登录不要求各自 identity 存储独立密码。
- 修改密码时只更新账户主密码，即 `username/local` 的 `password_hash`。
- 用户注销时不删除 `user_identities`，必须保留用户名、邮箱、手机号、OAuth、OIDC 标识用于后续去重和账号恢复。

## 配置开关

登录开关和注册开关必须分离。开启登录不代表允许用该方式注册。

### 登录开关

| key | 默认 | 说明 |
|-----|------|------|
| `auth.login.username_password.enabled` | `true` | 当前已落地；强制为 true，不允许关闭 |
| `auth.login.email_password.enabled` | `false` | 当前已落地；是否允许已有本地邮箱身份使用邮箱 + 密码登录 |
| `auth.login.phone_password.enabled` | `false` | 当前已落地；是否允许已有本地手机号身份使用手机号 + 密码登录 |
| `auth.login.email_code.enabled` | `false` | 当前已校验；邮箱验证码登录仍属后续增强 |
| `auth.login.phone_code.enabled` | `false` | 当前已校验；手机号验证码登录仍属后续增强 |
| `auth.login.oauth.enabled` | `false` | 当前已校验；OAuth 登录仍属后续增强 |
| `auth.login.oidc.enabled` | `false` | 当前已校验；OIDC 登录仍属后续增强 |

### 注册开关

| key | 默认 | 说明 |
|-----|------|------|
| `auth.register.enabled` | `false` | 当前已落地；是否开放用户自助注册；自部署商业级默认关闭，由管理员按运营需要开启 |
| `auth.register.username.enabled` | `true` | 当前已落地；开启自助注册后，是否允许用户名自助注册 |
| `auth.register.email.enabled` | `false` | 当前已校验；邮箱自助注册入口仍属后续增强 |
| `auth.register.phone.enabled` | `false` | 当前已校验；手机号自助注册入口仍属后续增强 |
| `auth.register.oauth.enabled` | `false` | 是否允许 OAuth 首次登录自动进入注册流程 |
| `auth.register.oidc.enabled` | `false` | 是否允许 OIDC 首次登录自动进入注册流程 |
| `auth.register.captcha.required` | `true` | 当前已 fail-closed；无验证码请求会被拒绝，完整验证码校验仍属后续增强 |
| `auth.register.default_quota` | `0` | 当前已落地；注册默认额度 |
| `auth.register.default_group_id` | `default` | 当前已落地；注册默认分组，支持 group 名称或数字 ID |

### 验证码配置

| key | 默认 | 说明 |
|-----|------|------|
| `auth.captcha.register.type` | `image` | 注册验证码类型，支持 `image/email/sms` 等实现 |
| `auth.captcha.login.email.enabled` | `false` | 是否允许邮箱验证码登录 |
| `auth.captcha.login.phone.enabled` | `false` | 是否允许短信验证码登录 |
| `auth.captcha.ttl_seconds` | `300` | 验证码有效期 |
| `auth.captcha.max_attempts` | `5` | 单个验证码最大尝试次数 |

配置判定规则：

- `auth.login.username_password.enabled` 是系统硬约束，配置层不得关闭。
- 服务端必须校验登录和注册开关，不依赖前端隐藏。
- 可以开启邮箱密码登录但关闭邮箱注册，此时只有已绑定本地邮箱身份的用户可以用邮箱密码登录。
- 可以开启手机号密码登录但关闭手机号注册，此时只有已绑定本地手机号身份的用户可以用手机号密码登录。
- 关闭某种登录方式不删除已绑定身份，只禁止继续使用该方式登录。
- 管理员创建账户不受自助注册开关限制，但仍必须设置用户名和密码。
- 默认关闭自助注册不影响用户名密码登录，也不影响管理员创建用户或后续邀请用户。
- 当前基础用户名密码注册只有在 `auth.register.enabled=true`、`auth.register.username.enabled=true` 且 `auth.register.captcha.required=false` 时接受无验证码请求；生产启用公开注册前应先落地验证码。

## 注册设计

### 统一注册接口

自助注册使用统一接口，不按用户名、邮箱、手机号拆分不同 API。

```text
POST /v0/user/register
```

目标请求：

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

字段规则：

| 字段 | 要求 |
|------|------|
| `username` | 必填，所有账户必须设置用户名 |
| `password` | 必填，所有账户必须设置密码 |
| `register_method` | 必填，表示本次注册入口使用 `username/email/phone` 中哪一种 |
| `captcha_id` | 必填 |
| `captcha_code` | 必填 |
| `email` | 当 `register_method=email` 时必填 |
| `phone` | 当 `register_method=phone` 时必填 |

注册流程：

```text
POST /v0/user/register
    -> 检查 auth.register.enabled
    -> 检查 register_method 对应注册开关
    -> 校验 captcha_id + captcha_code
    -> 校验 username 必填
    -> 使用 username/email/phone 查询是否命中已有账号身份
    -> 如命中注销账号，进入账号恢复流程
    -> 如命中正常账号，返回账号已存在
    -> 校验 password 强度
    -> 如提供 email，规范化并参与身份去重
    -> 如提供 phone，规范化并参与身份去重
    -> bcrypt 哈希密码
    -> 创建 users
    -> 创建 username/local identity，写入 password_hash
    -> 按注册信息创建 email/phone identity
    -> 返回用户信息或 JWT
```

注册规则：

- 所有注册方式都必须提交用户名、密码和验证码。
- 当前基础实现只支持用户名密码注册；目标 `register_method=username` 时必须开启 `auth.register.username.enabled`。
- `register_method=email` 时必须开启 `auth.register.email.enabled`，并且必须填写邮箱。
- `register_method=phone` 时必须开启 `auth.register.phone.enabled`，并且必须填写手机号。
- 即使用邮箱或手机号注册，也必须同时创建用户名和密码。
- 注册验证码是防刷验证码，不等同于邮箱或手机号归属验证。
- 邮箱和手机号归属验证可作为后续绑定验证或注册前置验证单独实现。
- 注册前必须同时检查正常账号和注销账号的 identity，不能只检查未注销账号。
- 命中注销账号时不能创建新 `users`，必须按恢复账号处理，防止通过注销后重新注册绕过欠款、封禁或历史风控。

## 注销与账号恢复

用户注销是账号状态变更，不是数据删除。

注销目标：

- 停止该账号继续登录和调用 API。
- 保留账号、身份标识、欠款、日志和风控记录。
- 当用户用相同用户名、邮箱、手机号、OAuth identity 或 OIDC subject 重新注册时恢复原账号，而不是创建新账号。

注销流程：

```text
用户发起注销
    -> 校验当前密码或二次验证
    -> 禁用该用户所有 API Key
    -> 标记 users.status 为注销或禁用状态
    -> 保留 users、user_identities、logs、欠款和风控记录
    -> 清理当前登录会话
```

当前已落地基础接口 `DELETE /v0/user/self`：要求当前 User JWT 和本地密码二次确认，限制普通用户只能注销自己；服务端将 `users.status` 置为禁用、禁用该用户已启用 API Key 并写入 `user.self_cancel` 审计。缺少密码或密码错误时拒绝注销，用户和 API Key 状态保持不变，并写入 `user.self_cancel_denied` 拒绝审计，错误码区分 `self_cancel_password_required` 和 `self_cancel_password_invalid`，审计摘要不保存密码。该接口不删除 `users`、`user_identities`、`tokens`、`logs`、余额或额度流水。当前基础用户名注册会在相同 `username/local` identity 命中已注销普通用户时恢复原账号，写入 `user.recover` 审计，并要求用户用本次提交的新密码重新登录；旧 API Key 不会自动恢复启用。当前版本尚未实现隐私字段擦除和邮箱/手机号/OAuth/OIDC 恢复入口。

注销后保留的数据：

| 数据 | 保留原因 |
|------|----------|
| `users.id` | 保持历史日志、账单、欠款和审计关联 |
| `user_identities` | 后续注册去重和账号恢复 |
| `tokens` | 保留审计记录，状态改为禁用 |
| `logs` | 审计、账单和风控 |
| `quota` 或欠款信息 | 防止注销后重新注册逃避欠款 |

恢复流程：

```text
用户重新注册
    -> 校验注册验证码
    -> 规范化 username/email/phone 或第三方 identity
    -> 查询 user_identities 是否命中注销账号
    -> 命中后要求完成身份验证和设置新密码
    -> 恢复 users.status
    -> 更新 username/local password_hash
    -> 重新启用或要求重新创建 API Key
```

恢复规则：

- 恢复账号必须重新设置密码。
- 恢复账号必须完成本次注册验证码校验。
- 如通过邮箱或手机号恢复，应校验对应邮箱或手机号归属。
- 如通过 OAuth/OIDC 恢复，必须校验 provider 返回的稳定身份标识。
- 欠款、历史消费、风控标签不能因恢复而清零。
- API Key 默认不自动恢复启用，建议要求用户重新创建。

当前基础用户名恢复实现保留原 `quota`、`group_id` 和历史事实，只更新 `users.status`、可选展示资料和本地身份密码；恢复不会重新应用 `auth.register.default_quota` 或默认分组。

隐私处理：

- 如果需要满足隐私删除诉求，可以清空展示资料和非必要个人资料。
- 用于去重的身份标识必须保留，或迁移为不可逆 hash 后保留匹配能力。
- 不能删除可用于识别历史欠款和封禁的 identity。

## 登录设计

### 统一登录接口

用户名、邮箱、手机号登录使用同一个接口。

```text
POST /v0/user/login
```

管理端不提供独立登录接口，管理员同样通过 `/v0/user/login` 获取 User JWT，再由 `/v0/admin/*` 根据 `role` 校验管理权限。

密码登录请求：

```json
{
  "account": "alice@example.com",
  "credential_type": "password",
  "password": "password"
}
```

验证码登录请求：

```json
{
  "account": "+8613800000000",
  "credential_type": "code",
  "captcha_id": "login-code-id",
  "captcha_code": "123456"
}
```

字段说明：

| 字段 | 说明 |
|------|------|
| `account` | 用户名、邮箱或手机号，由后端识别或按匹配顺序查询 |
| `credential_type` | `password` 或 `code` |
| `password` | 密码登录时必填 |
| `captcha_id` | 验证码登录时必填 |
| `captcha_code` | 验证码登录时必填 |

### 账号识别

后端不暴露不同登录接口，但内部需要识别 `account`。

识别顺序建议：

```text
1. 如果符合邮箱格式，尝试 email identity
2. 如果符合手机号格式，规范化后尝试 phone identity
3. 否则尝试 username identity
```

为了避免账号枚举：

- 不向前端返回识别出的账号类型。
- 登录失败统一返回“账号或凭据错误”。
- 对同一个 `account` 做统一失败次数限制。

### 密码登录流程

```text
接收 account + password
    -> 识别候选 identity method
    -> 检查对应登录开关
    -> 查询 user_identities(method, local, identifier)
    -> 关联 users
    -> 检查用户 status
    -> 查询该用户 username/local 主身份
    -> bcrypt 校验主身份 password_hash
    -> 更新本次使用 identity 的 last_used_at
    -> 签发包含 role 的 User JWT
    -> 写 user.login 审计，摘要不包含密码或 JWT
```

密码登录开关：

- 用户名密码登录总是启用。
- 邮箱密码登录需要 `auth.login.email_password.enabled=true`。
- 手机号密码登录需要 `auth.login.phone_password.enabled=true`。

### 验证码登录流程

验证码登录只适用于邮箱和手机号，不适用于用户名。

```text
接收 account + captcha
    -> 识别 email 或 phone
    -> 检查对应验证码登录开关
    -> 查询 user_identities(method, local, identifier)
    -> 关联 users
    -> 检查用户 status
    -> 校验验证码
    -> 确认用户存在 username/local 主身份和密码
    -> 更新 last_used_at
    -> 签发包含 role 的 User JWT
```

验证码登录规则：

- 邮箱验证码登录需要 `auth.login.email_code.enabled=true`。
- 手机号验证码登录需要 `auth.login.phone_code.enabled=true`。
- 即使使用验证码登录，账户也必须已经设置密码。
- 验证码只存 Redis，短 TTL，一次性消费。
- 验证码发送和校验都需要限流。

## 邮箱规则

邮箱规范化：

- 去除首尾空格。
- 转小写。
- 不默认删除 `+tag`，避免改变用户实际邮箱。

邮箱注册：

- 需要 `auth.register.email.enabled=true`。
- 必须同时提供用户名、密码和注册验证码。
- 注册成功后创建 `email/local` identity。

邮箱登录：

- 密码登录需要 `auth.login.email_password.enabled=true`。
- 验证码登录需要 `auth.login.email_code.enabled=true`。
- 登录 API 仍然是统一登录接口，不能新增邮箱专用登录接口。

## 手机号规则

手机号规范化：

- 统一保存 E.164 格式，例如 `+8613800000000`。
- 不允许同一个规范化手机号绑定多个用户。

手机号注册：

- 需要 `auth.register.phone.enabled=true`。
- 必须同时提供用户名、密码和注册验证码。
- 注册成功后创建 `phone/local` identity。

手机号登录：

- 密码登录需要 `auth.login.phone_password.enabled=true`。
- 验证码登录需要 `auth.login.phone_code.enabled=true`。
- 登录 API 仍然是统一登录接口，不能新增手机号专用登录接口。

## OAuth 登录

目标能力：

- 支持多个 OAuth Provider。
- 每个 Provider 可独立开启登录。
- OAuth 登录可以绑定已有账户。
- OAuth 首次注册可单独配置是否允许。
- OAuth 不允许创建无密码账户。

建议配置结构：

| key | 说明 |
|-----|------|
| `oauth.github.enabled` | 是否启用 GitHub OAuth 登录 |
| `oauth.github.register_enabled` | 是否允许 GitHub OAuth 首次注册 |
| `oauth.github.client_id` | Client ID |
| `oauth.github.client_secret` | Client Secret，使用 `ENCRYPTION_KEY` 或 KMS 加密存储 |
| `oauth.github.auth_url` | 授权地址 |
| `oauth.github.token_url` | Token 地址 |
| `oauth.github.userinfo_url` | 用户信息地址 |
| `oauth.github.scopes` | scope 列表 |

授权流程：

```text
GET /v0/user/oauth/:provider/login
    -> 检查 provider enabled
    -> 生成 state
    -> state 存 Redis，短 TTL
    -> 跳转 provider auth_url

GET /v0/user/oauth/:provider/callback
    -> 校验 state
    -> code 换 token
    -> 获取 userinfo
    -> 提取 provider user id
    -> 查询 user_identities(method=oauth, provider, identifier)
    -> 已存在且账号正常则登录
    -> 已存在且账号已注销则进入恢复账号流程
    -> 不存在则检查 provider.register_enabled
    -> 进入补齐注册信息页面
    -> 用户填写 username/password/captcha
    -> 对 username/email/oauth identity 执行统一账号去重
    -> 创建 users + username/local identity + oauth identity
```

绑定规则：

- 已登录用户可以绑定 OAuth 身份。
- 未登录用户回调时，如果邮箱匹配已有用户，不应自动绑定，除非用户先完成该账户的密码登录。
- 一个 OAuth identity 只能绑定一个用户。
- 禁用 OAuth 登录后，不删除绑定身份，但禁止用该身份登录。
- OAuth 首次注册必须检查 `oauth/provider/identifier` 是否命中注销账号，命中时恢复原账号，不能创建新账号。

## OIDC 登录

OIDC 适合企业 SSO。

建议配置：

| key | 说明 |
|-----|------|
| `oidc.corp.enabled` | 是否启用该 OIDC Provider 登录 |
| `oidc.corp.register_enabled` | 是否允许该 OIDC Provider 首次注册 |
| `oidc.corp.issuer` | Issuer URL |
| `oidc.corp.client_id` | Client ID |
| `oidc.corp.client_secret` | Client Secret，使用 `ENCRYPTION_KEY` 或 KMS 加密存储 |
| `oidc.corp.redirect_url` | 回调地址 |
| `oidc.corp.scopes` | `openid profile email` 等 |
| `oidc.corp.role_claim` | 可选角色 claim |
| `oidc.corp.group_claim` | 可选分组 claim |

OIDC 流程：

```text
Discovery issuer
    -> 获取 authorization_endpoint / token_endpoint / jwks_uri
    -> Authorization Code Flow
    -> 校验 state 和 nonce
    -> 校验 id_token 签名、aud、iss、exp
    -> 使用 sub 作为 identifier
    -> 查询 user_identities(method=oidc, provider, sub)
    -> 已存在且账号正常则登录
    -> 已存在且账号已注销则进入恢复账号流程
    -> 不存在则检查 provider.register_enabled
    -> 进入补齐注册信息页面
    -> 用户填写 username/password/captcha
    -> 对 username/email/oidc identity 执行统一账号去重
    -> 创建 users + username/local identity + oidc identity
```

安全要求：

- 必须校验 `state`。
- 必须校验 `nonce`。
- 必须校验 ID Token 签名。
- 必须校验 `aud`、`iss`、`exp`。
- OIDC `sub` 是稳定唯一标识，不应使用 email 作为主 identifier。
- OIDC 首次注册也必须设置用户名、密码和验证码。
- OIDC 首次注册必须检查 `oidc/provider/sub` 是否命中注销账号，命中时恢复原账号，不能创建新账号。

## 管理员账号

管理员账号本质上仍是 `users`，通过 `role` 区分权限。

权限：

| role | 权限 |
|------|------|
| `0` | 普通用户 |
| `1` | 管理员 |
| `2` | 超级管理员 |

规则：

- 初始化时创建第一个超级管理员。
- 初始化超级管理员必须设置用户名和密码。
- 至少保留一个启用的超级管理员。
- 管理员和普通用户都使用 `/v0/user/login` 获取 User JWT。
- `/v0/admin/*` 在 User JWT 基础上校验管理员或超级管理员角色。
- API 调用使用 API Key，不使用 User JWT。

角色能力边界：

| 能力 | 普通用户 | 管理员 | 超级管理员 |
|------|----------|--------|------------|
| 查看和修改自己的资料 | 是 | 是 | 是 |
| 创建、禁用、删除自己的 API Key | 是 | 是 | 是 |
| 调整自己的额度或 API Key 无限额度 | 否 | 通过管理接口调整普通用户 | 可调整并审计 |
| 管理普通用户 | 否 | 是 | 是 |
| 管理通道 | 否 | 是 | 是 |
| 查看全局日志和看板 | 否 | 是 | 是 |
| 修改 settings | 否 | 否 | 是 |
| 管理管理员账号 | 否 | 否 | 是 |

API Key 只代表模型调用凭据，不继承 User JWT 的管理能力。即使 API Key 属于管理员或超级管理员，也不能调用 `/v0/admin/*` 或 `/v0/user/*`。

## 会话设计

### 管理端会话

管理端不单独签发管理端专用凭证，复用 User JWT 作为登录态。

要求：

- JWT 中必须包含 user id、role、session id 等权限校验所需字段。
- `/v0/admin/*` 需要校验 JWT 有效性和管理员角色。
- 超级管理员接口需要额外校验 `RoleSuper`。
- 用户禁用、角色变更或 session version 变化后应使旧 JWT 失效。

### User 会话

用户控制台使用与管理端相同的 User JWT。

要求：

- JWT 过期时间由 `jwt.user_expire_hours` 控制。
- JWT 中只放 user id、role、session id 等必要字段。
- 敏感资料每次从 DB 或缓存读取。
- 用户禁用后应通过 session version 或 Redis 黑名单使旧 JWT 失效。

### API Key

API Key 用于 `/v1/*`，不等同于登录态。

要求：

- 支持创建、禁用、删除、过期时间、额度限制。
- 明文只返回一次。
- 鉴权成功后记录最近使用时间。
- 删除或禁用后立即清理 Redis 缓存。

## 账号绑定和解绑

绑定规则：

- 一个用户可以绑定多个 identity。
- 一个 identity 不能绑定多个用户。
- 用户不能解绑 `username/local` 主身份。
- 用户可以修改用户名，但必须同步更新 `username/local` identity。
- 用户可以绑定或解绑邮箱、手机号、OAuth、OIDC 身份。
- 解绑邮箱或手机号后，对应方式无法登录，但用户名密码登录仍可用。

解绑流程：

```text
用户或管理员发起解绑
    -> 校验权限
    -> 禁止解绑 username/local 主身份
    -> 软删除目标 user_identity
    -> 清理相关会话或缓存
```

## 验证码设计

验证码用途分为注册验证码和登录验证码。

| 类型 | 用途 | 是否强制 |
|------|------|----------|
| 注册验证码 | 防止注册刷号 | 强制 |
| 邮箱登录验证码 | 邮箱验证码登录 | 可选开启 |
| 手机登录验证码 | 手机号验证码登录 | 可选开启 |
| 邮箱绑定验证码 | 验证邮箱归属 | 可选，但建议开启 |
| 手机绑定验证码 | 验证手机号归属 | 可选，但建议开启 |

验证码存储：

- 只存 Redis。
- 设置短 TTL。
- 一次性消费。
- 限制发送频率。
- 限制校验失败次数。

## 风险控制

| 风险 | 控制措施 |
|------|----------|
| 账号枚举 | 登录和找回密码统一错误提示 |
| 暴力破解 | IP、account、用户维度限流 |
| 注册刷号 | 注册验证码强制校验，IP 和设备指纹限流 |
| 注销后重新注册逃避欠款 | 注销不删除账号和 identity，重新注册相同身份时恢复原账号 |
| OAuth/OIDC 重复注册绕过去重 | 第三方 provider identity 与本地 identity 一起参与账号去重 |
| 验证码爆破 | Redis 记录尝试次数，超过阈值失效 |
| OAuth CSRF | state 一次性校验 |
| OIDC 重放 | nonce 校验 |
| JWT 泄露 | 短有效期、禁用后失效、前端安全存储策略 |
| 密码泄露 | bcrypt 哈希，不保存明文 |
| API Key 泄露 | 明文只返回一次，数据库保存 SHA256 哈希，缓存键也不使用明文 |
| Provider 接管 | 不因相同 email 自动绑定第三方身份，必须先完成账户登录或补齐注册流程 |

## 实施阶段

| 阶段 | 内容 |
|------|------|
| P0 | 用户名密码强制注册和登录、User JWT、管理员权限校验、API Key 鉴权 |
| P1 | 注册验证码、邮箱身份、邮箱注册开关、邮箱密码登录、邮箱验证码登录 |
| P1 | 手机号身份、手机注册开关、手机号密码登录、短信验证码登录、注销保留账号和恢复账号 |
| P2 | OAuth Provider 配置、登录、绑定、补齐密码注册流程、OAuth identity 去重恢复 |
| P2 | OIDC Discovery、Authorization Code Flow、企业 SSO、补齐密码注册流程、OIDC subject 去重恢复 |
| P3 | MFA、多会话管理、登录风险检测和更完整会话审计 |
