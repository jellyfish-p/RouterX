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

function statusLabel(status: number) {
  if (status === 1) return '成功'
  if (status === 2) return '失败'
  return '未知'
}
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
    <div class="overflow-x-auto rounded border border-gray-200">
      <table class="min-w-full text-sm">
        <thead class="bg-gray-50 text-left text-gray-500">
          <tr>
            <th class="px-3 py-2 font-medium">模型</th>
            <th class="px-3 py-2 font-medium">输入 Token</th>
            <th class="px-3 py-2 font-medium">输出 Token</th>
            <th class="px-3 py-2 font-medium">消耗额度</th>
            <th class="px-3 py-2 font-medium">状态</th>
            <th class="px-3 py-2 font-medium">时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="log in recentLogs" :key="log.id" class="border-t border-gray-100">
            <td class="px-3 py-2">{{ log.model }}</td>
            <td class="px-3 py-2">{{ log.prompt_tokens }}</td>
            <td class="px-3 py-2">{{ log.completion_tokens }}</td>
            <td class="px-3 py-2">{{ (log.quota_used / 100000000).toFixed(4) }}</td>
            <td class="px-3 py-2">
              <UBadge :color="log.status === 1 ? 'success' : log.status === 2 ? 'error' : 'neutral'">
                {{ statusLabel(log.status) }}
              </UBadge>
            </td>
            <td class="px-3 py-2">{{ new Date(log.created_at).toLocaleString() }}</td>
          </tr>
          <tr v-if="recentLogs.length === 0">
            <td colspan="6" class="px-3 py-8 text-center text-gray-500">暂无调用</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
