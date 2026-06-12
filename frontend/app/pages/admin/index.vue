<script setup lang="ts">
import type { ApiResponse, DashboardStats } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth', 'admin'],
  title: '管理仪表盘'
})

const { apiGet } = useApi()

const stats = ref<DashboardStats | null>(null)
const loading = ref(true)

onMounted(async () => {
  try {
    const res = await apiGet<ApiResponse<DashboardStats>>('/v0/admin/dashboard')
    stats.value = res.data
  } catch {
    // handled
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div>

    <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4 mb-8">
      <UCard v-for="item in [
        { label: '用户总数', value: stats?.user_count, icon: 'i-lucide-users', color: 'text-blue-500' },
        { label: '通道总数', value: stats?.channel_count, icon: 'i-lucide-network', color: 'text-green-500' },
        { label: '令牌总数', value: stats?.token_count, icon: 'i-lucide-key', color: 'text-orange-500' },
        { label: '今日调用', value: stats?.today_call_count, icon: 'i-lucide-phone-call', color: 'text-purple-500' },
        { label: '今日消耗', value: stats?.today_quota_used, icon: 'i-lucide-coins', color: 'text-red-500' },
        { label: '活跃通道', value: stats?.active_channel_count, icon: 'i-lucide-activity', color: 'text-teal-500' }
      ]" :key="item.label">
        <div class="text-center">
          <UIcon :name="item.icon" :class="['size-8 mx-auto mb-2', item.color]" />
          <p class="text-xs text-gray-500">{{ item.label }}</p>
          <p class="text-xl font-bold">{{ item.value ?? '--' }}</p>
        </div>
      </UCard>
    </div>
  </div>
</template>
