<script setup lang="ts">
import type { ApiResponse, SetupInitRequest } from '~/types/api'

definePageMeta({
  layout: 'default'
})

const { apiPost, apiGet } = useApi()
const toast = useToast()
const router = useRouter()

const form = reactive<SetupInitRequest>({
  username: 'admin',
  password: '',
  display_name: 'Administrator',
  email: ''
})

const loading = ref(false)
const systemReady = ref(false)

onMounted(async () => {
  try {
    const res = await apiGet<ApiResponse<{ initialized: boolean }>>('/v0/setup/status')
    if (res.data?.initialized) {
      systemReady.value = true
      await router.push('/login')
    }
  } catch {
    // ignore
  }
})

async function handleSubmit() {
  if (!form.username || !form.password) {
    toast.add({ title: '提示', description: '请填写用户名和密码', color: 'warning' })
    return
  }
  loading.value = true
  try {
    await apiPost('/v0/setup/init', form)
    toast.add({ title: '成功', description: '系统初始化完成，请登录', color: 'success' })
    await router.push('/login')
  } catch {
    // error handled in useApi
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="min-h-[80vh] flex items-center justify-center px-4">
    <UCard class="w-full max-w-md">
      <template #header>
        <div class="text-center">
          <UIcon name="i-lucide-rocket" class="size-12 text-primary mx-auto mb-3" />
          <h2 class="text-2xl font-bold">系统初始化</h2>
          <p class="text-sm text-gray-500 mt-1">创建超级管理员账户以开始使用</p>
        </div>
      </template>

      <div v-if="systemReady" class="text-center py-4">
        <UIcon name="i-lucide-circle-check" class="size-12 text-green-500 mx-auto mb-3" />
        <p>系统已初始化，正在跳转...</p>
      </div>

      <form v-else class="space-y-4" @submit.prevent="handleSubmit">
        <UFormField label="用户名" required>
          <UInput v-model="form.username" placeholder="admin" />
        </UFormField>

        <UFormField label="密码" required>
          <UInput v-model="form.password" type="password" placeholder="至少 6 位字符" />
        </UFormField>

        <UFormField label="显示名称">
          <UInput v-model="form.display_name" placeholder="Administrator" />
        </UFormField>

        <UFormField label="邮箱">
          <UInput v-model="form.email" type="email" placeholder="admin@example.com" />
        </UFormField>

        <UButton type="submit" block :loading="loading">
          初始化系统
        </UButton>
      </form>
    </UCard>
  </div>
</template>
