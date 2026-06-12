<script setup lang="ts">
import type { ApiResponse, BillingInfo } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth'],
  title: '账单'
})

const { apiGet } = useApi()

const billing = ref<BillingInfo | null>(null)
const loading = ref(true)

onMounted(async () => {
  try {
    const res = await apiGet<ApiResponse<BillingInfo>>('/v0/user/billing')
    billing.value = res.data
  } catch {
    // handled
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div>

    <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
      <UCard>
        <div class="text-center">
          <UIcon name="i-lucide-wallet" class="size-10 text-primary mx-auto mb-2" />
          <p class="text-sm text-gray-500">总配额</p>
          <p class="text-2xl font-bold">{{ billing ? (billing.total_quota / 100000000).toFixed(2) : '--' }}</p>
        </div>
      </UCard>
      <UCard>
        <div class="text-center">
          <UIcon name="i-lucide-credit-card" class="size-10 text-green-500 mx-auto mb-2" />
          <p class="text-sm text-gray-500">剩余额度</p>
          <p class="text-2xl font-bold">{{ billing ? (billing.remaining_quota / 100000000).toFixed(2) : '--' }}</p>
        </div>
      </UCard>
      <UCard>
        <div class="text-center">
          <UIcon name="i-lucide-bar-chart-3" class="size-10 text-blue-500 mx-auto mb-2" />
          <p class="text-sm text-gray-500">已用额度</p>
          <p class="text-2xl font-bold">{{ billing ? (billing.used_quota / 100000000).toFixed(2) : '--' }}</p>
        </div>
      </UCard>
    </div>

    <UCard>
      <template #header>
        <h2 class="font-semibold">使用统计</h2>
      </template>
      <div v-if="billing" class="space-y-4">
        <div class="flex justify-between py-2 border-b border-gray-100 dark:border-gray-800">
          <span class="text-gray-500">总调用次数</span>
          <span class="font-semibold">{{ billing.total_calls }}</span>
        </div>
        <div class="flex justify-between py-2 border-b border-gray-100 dark:border-gray-800">
          <span class="text-gray-500">总配额</span>
          <span class="font-semibold">{{ (billing.total_quota / 100000000).toFixed(2) }}</span>
        </div>
        <div class="flex justify-between py-2 border-b border-gray-100 dark:border-gray-800">
          <span class="text-gray-500">已用额度</span>
          <span class="font-semibold text-blue-600">{{ (billing.used_quota / 100000000).toFixed(2) }}</span>
        </div>
        <div class="flex justify-between py-2">
          <span class="text-gray-500">剩余额度</span>
          <span class="font-semibold text-green-600">{{ (billing.remaining_quota / 100000000).toFixed(2) }}</span>
        </div>
      </div>
      <div v-else-if="loading" class="text-center py-8 text-gray-500">
        加载中...
      </div>
    </UCard>
  </div>
</template>
