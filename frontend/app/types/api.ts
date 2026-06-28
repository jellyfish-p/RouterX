export interface ApiResponse<T = unknown> {
  success: boolean
  message: string
  data: T
}

export interface PaginatedResult<T = unknown> {
  total: number
  page: number
  page_size: number
  data: T[]
}

export interface PaginatedResponse<T = unknown> extends ApiResponse<PaginatedResult<T>> {}

export interface InitStatus {
  initialized: boolean
}

export interface SetupInitRequest {
  username: string
  password: string
  display_name: string
  email: string
}

export interface LoginRequest {
  username: string
  account?: string
  password: string
}

export interface UserBrief {
  id: number
  username: string
  display_name: string
  email?: string
  role: number
  quota: number
  status: number
  group_id?: number | null
}

export interface LoginResponse {
  token?: string
  user: UserBrief
}

export interface RegisterRequest {
  username: string
  password: string
  display_name: string
  email: string
}

export interface ChangePasswordRequest {
  old_password: string
  new_password: string
}

export interface UpdateSelfRequest {
  display_name: string
  email: string
}

export interface CreateUserRequest {
  username: string
  password: string
  display_name: string
  email: string
  role: number
  quota: number
  group_id?: number | null
}

export interface UpdateUserRequest {
  display_name: string
  email: string
  role?: number
  status?: number
  group_id?: number | null
}

export interface UpdateQuotaRequest {
  quota: number
}

export interface TokenInfo {
  id: number
  user_id: number
  name: string
  status: number
  expired_at?: string | null
  quota_limit: number
  quota_used: number
  unlimited: boolean
  leak_risk_enabled: boolean
  created_at: string
  updated_at: string
  key?: string
}

export interface CreateTokenRequest {
  name: string
  quota_limit: number
  unlimited: boolean
  leak_risk_enabled?: boolean
  expired_at?: number | null
}

export interface UpdateTokenRequest {
  name?: string
  status?: number
  quota_limit?: number
  unlimited?: boolean
  leak_risk_enabled?: boolean
  expired_at?: number | null
}

export interface ChannelInfo {
  id: number
  idx: number
  type: number
  name: string
  models: string
  base_url: string
  base_urls?: string[]
  key_selection_mode: 'round_robin' | 'random'
  api_key_count: number
  upstreams?: Array<{ base_url: string; has_api_key: boolean }>
  model_rewrites?: Record<string, string>
  group: string
  upstream_options?: Record<string, unknown>
  priority: number
  weight: number
  status: number
  response_ms: number
  balance: number
  error_count: number
  created_at: string
  updated_at: string
}

export interface CreateChannelRequest {
  idx: number
  type: number
  name: string
  models: string
  base_url: string
  base_urls: string[]
  api_key: string
  api_keys: string[]
  key_selection_mode: 'round_robin' | 'random'
  upstreams: Array<{ base_url: string; api_key: string }>
  model_rewrites: Record<string, string>
  group: string
  upstream_options: Record<string, unknown>
  priority: number
  weight: number
  status: number
}

export interface UpdateChannelRequest {
  idx?: number
  type?: number
  name?: string
  models?: string
  base_url?: string
  base_urls?: string[]
  api_key?: string
  api_keys?: string[]
  key_selection_mode?: 'round_robin' | 'random'
  upstreams?: Array<{ base_url: string; api_key: string }>
  model_rewrites?: Record<string, string>
  group?: string
  upstream_options?: Record<string, unknown>
  priority?: number
  weight?: number
  status?: number
}

export interface TestChannelResult {
  success: boolean
  response_ms: number
  error?: string
  model_count?: number
}

export interface FetchChannelModelsResult {
  models: string[]
}

export interface LogEntry {
  id: number
  user_id: number
  token_id?: number
  channel_id?: number
  model: string
  prompt_tokens: number
  completion_tokens: number
  quota_used: number
  status: number
  error_msg?: string
  created_at: string
}

export interface DashboardStats {
  user_count: number
  channel_count: number
  token_count: number
  today_call_count: number
  today_quota_used: number
  active_channel_count: number
}

export interface BillingInfo {
  total_quota: number
  remaining_quota: number
  used_quota: number
  total_calls: number
}

export interface SettingEntry {
  id: number
  key: string
  value: string
  created_at: string
  updated_at: string
}

export const UserRole = {
  User: 0,
  Admin: 1,
  SuperAdmin: 2
} as const

export const UserStatus = {
  Disabled: 0,
  Enabled: 1
} as const

export const TokenStatus = {
  Disabled: 0,
  Enabled: 1
} as const

export const ChannelType = {
  OpenAI: 1,
  Azure: 2,
  Claude: 3,
  Gemini: 4,
  Qwen: 5,
  DeepSeek: 6,
  XAI: 7,
  RouterX: 8,
  OpenAICompat: 100
} as const

export const ChannelStatus = {
  Disabled: 0,
  Enabled: 1,
  ManualOff: 2
} as const

export const LogStatus = {
  Unknown: 0,
  Success: 1,
  Failed: 2
} as const
