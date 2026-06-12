<script setup lang="ts">
import type { ApiResponse, RegisterRequest } from '~/types/api'

definePageMeta({
  layout: 'default',
  middleware: ['setup', 'guest']
})

const { apiPost } = useApi()
const toast = useToast()
const router = useRouter()

const form = reactive<RegisterRequest>({
  username: '',
  password: '',
  display_name: '',
  email: ''
})

const loading = ref(false)

async function handleSubmit() {
  if (!form.username || !form.password) {
    toast.add({ title: '提示', description: '请填写用户名和密码', color: 'warning' })
    return
  }
  if (form.password.length < 6) {
    toast.add({ title: '提示', description: '密码至少 6 位字符', color: 'warning' })
    return
  }
  loading.value = true
  try {
    const res = await apiPost<ApiResponse>('/v0/user/register', form)
    if (res.success) {
      toast.add({ title: '成功', description: '注册成功，请登录', color: 'success' })
      await router.push('/login')
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
          <UIcon name="i-lucide-user-plus" class="size-12 text-primary mx-auto mb-3" />
          <h2 class="text-2xl font-bold">注册</h2>
          <p class="text-sm text-gray-500 mt-1">
            已有账户？
            <NuxtLink to="/login" class="text-primary hover:underline">登录</NuxtLink>
          </p>
        </div>
      </template>

      <form class="space-y-4" @submit.prevent="handleSubmit">
        <UFormField label="用户名" required>
          <UInput v-model="form.username" placeholder="3-64 位字符" />
        </UFormField>

        <UFormField label="密码" required>
          <UInput v-model="form.password" type="password" placeholder="至少 6 位字符" />
        </UFormField>

        <UFormField label="显示名称">
          <UInput v-model="form.display_name" placeholder="可选" />
        </UFormField>

        <UFormField label="邮箱">
          <UInput v-model="form.email" type="email" placeholder="可选" />
        </UFormField>

        <UButton type="submit" block :loading="loading">
          注册
        </UButton>
      </form>
    </UCard>
  </div>
</template>
