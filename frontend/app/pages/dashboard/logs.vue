<script setup lang="ts">
import type { LogEntry, PaginatedResponse } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth'],
  title: '调用日志'
})

const { apiGet } = useApi()

const logs = ref<LogEntry[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)

async function fetchLogs() {
  loading.value = true
  try {
    const res = await apiGet<PaginatedResponse<LogEntry>>(`/v0/user/log?page=${page.value}&page_size=${pageSize}`)
    logs.value = res.data?.data || []
    total.value = res.data?.total || 0
  } catch {
    // handled
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  fetchLogs()
})

watch(page, () => {
  fetchLogs()
})
</script>

<template>
  <div>

    <UTable
      :rows="logs"
      :columns="[
        { key: 'id', label: 'ID' },
        { key: 'model', label: '模型' },
        { key: 'prompt_tokens', label: '输入 Token' },
        { key: 'completion_tokens', label: '输出 Token' },
        { key: 'quota_used', label: '消耗额度' },
        { key: 'status', label: '状态' },
        { key: 'created_at', label: '时间' }
      ]"
      :loading="loading"
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

    <div class="flex justify-center mt-4">
      <UPagination
        v-model:page="page"
        :total="total"
        :page-size="pageSize"
      />
    </div>
  </div>
</template>
