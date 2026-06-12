<script setup lang="ts">
import type { ApiResponse, BillingInfo, PaginatedResponse, LogEntry } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth'],
  title: '概览'
})

const auth = useAuthStore()
const { apiGet } = useApi()

const billing = ref<BillingInfo | null>(null)
const recentLogs = ref<LogEntry[]>([])

onMounted(async () => {
  try {
    const [billingRes, logRes] = await Promise.all([
      apiGet<ApiResponse<BillingInfo>>('/v0/user/billing'),
      apiGet<PaginatedResponse<LogEntry>>('/v0/user/log?page=1&page_size=5')
    ])
    billing.value = billingRes.data
    recentLogs.value = logRes.data?.data || []
  } catch {
    // handled
  }
})
</script>

<template>
  <div>

    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
      <UCard>
        <div class="flex items-center gap-3">
          <UIcon name="i-lucide-coins" class="size-8 text-primary" />
          <div>
            <p class="text-sm text-gray-500">剩余额度</p>
            <p class="text-xl font-bold">{{ billing ? (billing.remaining_quota / 100000000).toFixed(2) : '--' }}</p>
          </div>
        </div>
      </UCard>
      <UCard>
        <div class="flex items-center gap-3">
          <UIcon name="i-lucide-trending-up" class="size-8 text-blue-500" />
          <div>
            <p class="text-sm text-gray-500">已用额度</p>
            <p class="text-xl font-bold">{{ billing ? (billing.used_quota / 100000000).toFixed(2) : '--' }}</p>
          </div>
        </div>
      </UCard>
      <UCard>
        <div class="flex items-center gap-3">
          <UIcon name="i-lucide-phone-call" class="size-8 text-green-500" />
          <div>
            <p class="text-sm text-gray-500">总调用次数</p>
            <p class="text-xl font-bold">{{ billing?.total_calls ?? '--' }}</p>
          </div>
        </div>
      </UCard>
      <UCard>
        <div class="flex items-center gap-3">
          <UIcon name="i-lucide-key" class="size-8 text-orange-500" />
          <div>
            <p class="text-sm text-gray-500">账户角色</p>
            <p class="text-xl font-bold">{{ auth.isSuperAdmin ? '超级管理员' : auth.isAdmin ? '管理员' : '用户' }}</p>
          </div>
        </div>
      </UCard>
    </div>

    <h2 class="text-lg font-semibold mb-4">最近调用</h2>
    <UTable
      :rows="recentLogs"
      :columns="[
        { key: 'model', label: '模型' },
        { key: 'prompt_tokens', label: '输入 Token' },
        { key: 'completion_tokens', label: '输出 Token' },
        { key: 'quota_used', label: '消耗额度' },
        { key: 'status', label: '状态' },
        { key: 'created_at', label: '时间' }
      ]"
    >
      <template #status-data="{ row }">
        <UBadge :color="row.status === 1 ? 'success' : row.status === 2 ? 'error' : 'neutral'">
          {{ row.status === 1 ? '成功' : row.status === 2 ? '失败' : '未知' }}
        </UBadge>
      </template>
      <template #quota_used-data="{ row }">
        {{ (row.quota_used / 100000000).toFixed(4) }}
      </template>
      <template #created_at-data="{ row }">
        {{ new Date(row.created_at).toLocaleString() }}
      </template>
    </UTable>
  </div>
</template>
