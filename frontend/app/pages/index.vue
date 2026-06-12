<script setup lang="ts">
definePageMeta({
  middleware: ['setup']
})

const auth = useAuthStore()
const router = useRouter()

onMounted(async () => {
  if (auth.isLoggedIn) {
    if (auth.isAdmin) {
      await navigateTo('/admin')
    } else {
      await navigateTo('/dashboard')
    }
  }
})
</script>

<template>
  <div class="max-w-4xl mx-auto px-4 py-20 text-center">
    <UIcon name="i-lucide-route" class="size-20 mx-auto text-primary mb-6" />
    <h1 class="text-4xl font-bold mb-4">RouterX</h1>
    <p class="text-xl text-gray-500 dark:text-gray-400 mb-8 max-w-2xl mx-auto">
      开源的 AI 模型网关，统一管理多个 LLM 厂商的 API 访问，支持用户管理、额度控制和调用日志。
    </p>
    <div class="flex justify-center gap-4">
      <UButton to="/login" size="lg">登录</UButton>
      <UButton to="/register" variant="outline" size="lg">注册</UButton>
    </div>
    <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mt-16 text-left">
      <div class="p-6 rounded-lg border border-gray-200 dark:border-gray-800">
        <UIcon name="i-lucide-shuffle" class="size-8 text-primary mb-3" />
        <h3 class="font-semibold mb-2">多厂商路由</h3>
        <p class="text-sm text-gray-500">支持 OpenAI、Azure、Claude、Gemini、Qwen、DeepSeek 等多种厂商，智能路由选择最佳通道。</p>
      </div>
      <div class="p-6 rounded-lg border border-gray-200 dark:border-gray-800">
        <UIcon name="i-lucide-shield-check" class="size-8 text-primary mb-3" />
        <h3 class="font-semibold mb-2">权限与额度控制</h3>
        <p class="text-sm text-gray-500">精细的用户管理和 API Key 管理，支持额度分配、限流和过期控制。</p>
      </div>
      <div class="p-6 rounded-lg border border-gray-200 dark:border-gray-800">
        <UIcon name="i-lucide-chart-bar" class="size-8 text-primary mb-3" />
        <h3 class="font-semibold mb-2">调用日志与分析</h3>
        <p class="text-sm text-gray-500">完整的调用日志记录，支持用量统计和费用分析。</p>
      </div>
    </div>
  </div>
</template>
