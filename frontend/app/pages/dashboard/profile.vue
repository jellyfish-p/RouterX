<script setup lang="ts">
import type { ApiResponse, UserBrief, UpdateSelfRequest, ChangePasswordRequest } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth'],
  title: '个人信息'
})

const { apiGet, apiPut, apiPost } = useApi()
const auth = useAuthStore()
const toast = useToast()

const profile = ref<UserBrief | null>(null)
const profileForm = reactive<UpdateSelfRequest>({
  display_name: '',
  email: ''
})
const passwordForm = reactive<ChangePasswordRequest>({
  old_password: '',
  new_password: ''
})

const profileLoading = ref(false)
const passwordLoading = ref(false)

onMounted(async () => {
  try {
    const res = await apiGet<ApiResponse<UserBrief>>('/v0/user/self')
    if (res.data) {
      profile.value = res.data
      profileForm.display_name = res.data.display_name || ''
      profileForm.email = res.data.email || ''
    }
  } catch {
    // handled
  }
})

async function updateProfile() {
  profileLoading.value = true
  try {
    const res = await apiPut<ApiResponse<UserBrief>>('/v0/user/self', profileForm)
    if (res.data) {
      profile.value = res.data
      auth.user = res.data
      toast.add({ title: '成功', description: '个人信息已更新', color: 'success' })
    }
  } catch {
    // handled
  } finally {
    profileLoading.value = false
  }
}

async function changePassword() {
  if (!passwordForm.old_password || !passwordForm.new_password) {
    toast.add({ title: '提示', description: '请填写新旧密码', color: 'warning' })
    return
  }
  if (passwordForm.new_password.length < 6) {
    toast.add({ title: '提示', description: '新密码至少 6 位字符', color: 'warning' })
    return
  }
  passwordLoading.value = true
  try {
    await apiPost('/v0/user/self/password', passwordForm)
    toast.add({ title: '成功', description: '密码已修改', color: 'success' })
    passwordForm.old_password = ''
    passwordForm.new_password = ''
  } catch {
    // handled
  } finally {
    passwordLoading.value = false
  }
}
</script>

<template>
  <div class="max-w-2xl">

    <UCard class="mb-6">
      <template #header>
        <h2 class="font-semibold">基本信息</h2>
      </template>
      <form class="space-y-4" @submit.prevent="updateProfile">
        <UFormField label="用户名">
          <UInput :model-value="profile?.username" disabled />
        </UFormField>
        <UFormField label="显示名称">
          <UInput v-model="profileForm.display_name" placeholder="显示名称" />
        </UFormField>
        <UFormField label="邮箱">
          <UInput v-model="profileForm.email" type="email" placeholder="邮箱" />
        </UFormField>
        <UFormField label="角色">
          <UInput
            :model-value="profile?.role === 2 ? '超级管理员' : profile?.role === 1 ? '管理员' : '用户'"
            disabled
          />
        </UFormField>
        <UFormField label="额度">
          <UInput :model-value="profile ? (profile.quota / 100000000).toFixed(2) : '--'" disabled />
        </UFormField>
        <UButton type="submit" :loading="profileLoading">保存修改</UButton>
      </form>
    </UCard>

    <UCard>
      <template #header>
        <h2 class="font-semibold">修改密码</h2>
      </template>
      <form class="space-y-4" @submit.prevent="changePassword">
        <UFormField label="旧密码" required>
          <UInput v-model="passwordForm.old_password" type="password" placeholder="输入旧密码" />
        </UFormField>
        <UFormField label="新密码" required>
          <UInput v-model="passwordForm.new_password" type="password" placeholder="至少 6 位字符" />
        </UFormField>
        <UButton type="submit" :loading="passwordLoading">修改密码</UButton>
      </form>
    </UCard>
  </div>
</template>
