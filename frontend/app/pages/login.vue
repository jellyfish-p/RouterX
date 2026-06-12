<script setup lang="ts">
import type { ApiResponse, LoginRequest, LoginResponse } from '~/types/api'

definePageMeta({
  layout: 'default',
  middleware: ['setup', 'guest']
})

const { apiPost } = useApi()
const auth = useAuthStore()
const toast = useToast()
const router = useRouter()

const form = reactive<LoginRequest>({
  username: '',
  password: ''
})

const loading = ref(false)

async function handleSubmit() {
  if (!form.username || !form.password) {
    toast.add({ title: '提示', description: '请填写用户名和密码', color: 'warning' })
    return
  }
  loading.value = true
  try {
    const res = await apiPost<ApiResponse<LoginResponse>>('/v0/user/login', form)
    if (res.success && res.data.token && res.data.user) {
      auth.login(res.data.token, res.data.user)
      toast.add({ title: '成功', description: '登录成功', color: 'success' })
      if (auth.isAdmin) {
        await router.push('/admin')
      } else {
        await router.push('/dashboard')
      }
    } else {
      toast.add({ title: '登录失败', description: res.message || '用户名或密码错误', color: 'error' })
    }
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
          <UIcon name="i-lucide-log-in" class="size-12 text-primary mx-auto mb-3" />
          <h2 class="text-2xl font-bold">登录</h2>
          <p class="text-sm text-gray-500 mt-1">
            还没有账户？
            <NuxtLink to="/register" class="text-primary hover:underline">注册</NuxtLink>
          </p>
        </div>
      </template>

      <form class="space-y-4" @submit.prevent="handleSubmit">
        <UFormField label="用户名" required>
          <UInput v-model="form.username" placeholder="输入用户名" />
        </UFormField>

        <UFormField label="密码" required>
          <UInput v-model="form.password" type="password" placeholder="输入密码" />
        </UFormField>

        <UButton type="submit" block :loading="loading">
          登录
        </UButton>
      </form>
    </UCard>
  </div>
</template>
