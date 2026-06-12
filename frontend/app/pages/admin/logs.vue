<script setup lang="ts">
import type { LogEntry, PaginatedResponse } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth', 'admin'],
  title: '调用日志'
})

const { apiGet, apiDelete } = useApi()
const toast = useToast()

const logs = ref<LogEntry[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)

async function fetchLogs() {
  loading.value = true
  try {
    const res = await apiGet<PaginatedResponse<LogEntry>>(`/v0/admin/log?page=${page.value}&page_size=${pageSize}`)
    logs.value = res.data?.data || []
    total.value = res.data?.total || 0
  } catch {
    // handled
  } finally {
    loading.value = false
  }
}

async function handleClear() {
  if (!confirm('确定清除日志？此操作不可撤销。')) return
  try {
    await apiDelete('/v0/admin/log')
    toast.add({ title: '成功', description: '日志已清除', color: 'success' })
    await fetchLogs()
  } catch {
    // handled
  }
}

onMounted(() => fetchLogs())
watch(page, () => fetchLogs())

function statusLabel(status: number) {
  if (status === 1) return '成功'
  if (status === 2) return '失败'
  return '未知'
}
</script>

<template>
  <div>

    <div class="flex justify-end mb-4">
      <UButton variant="outline" color="error" @click="handleClear">清除日志</UButton>
    </div>

    <div class="overflow-x-auto rounded border border-gray-200">
      <table class="min-w-full text-sm">
        <thead class="bg-gray-50 text-left text-gray-500">
          <tr>
            <th class="px-3 py-2 font-medium">ID</th>
            <th class="px-3 py-2 font-medium">用户 ID</th>
            <th class="px-3 py-2 font-medium">通道 ID</th>
            <th class="px-3 py-2 font-medium">模型</th>
            <th class="px-3 py-2 font-medium">输入 Token</th>
            <th class="px-3 py-2 font-medium">输出 Token</th>
            <th class="px-3 py-2 font-medium">消耗额度</th>
            <th class="px-3 py-2 font-medium">状态</th>
            <th class="px-3 py-2 font-medium">时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="9" class="px-3 py-8 text-center text-gray-500">加载中</td>
          </tr>
          <tr v-for="log in logs" v-else :key="log.id" class="border-t border-gray-100">
            <td class="px-3 py-2">{{ log.id }}</td>
            <td class="px-3 py-2">{{ log.user_id }}</td>
            <td class="px-3 py-2">{{ log.channel_id || '-' }}</td>
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
          <tr v-if="!loading && logs.length === 0">
            <td colspan="9" class="px-3 py-8 text-center text-gray-500">暂无日志</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="flex justify-center mt-4">
      <UPagination v-model:page="page" :total="total" :page-size="pageSize" />
    </div>
  </div>
</template>
