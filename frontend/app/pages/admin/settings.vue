<script setup lang="ts">
import type { ApiResponse, SettingEntry } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth', 'admin'],
  title: '系统设置'
})

const auth = useAuthStore()
const { apiGet, apiPut } = useApi()
const toast = useToast()

const settings = ref<SettingEntry[]>([])
const loading = ref(true)
const saving = ref(false)

const editableSettings = ref<Record<string, string>>({})

async function fetchSettings() {
  loading.value = true
  try {
    const res = await apiGet<ApiResponse<SettingEntry[]>>('/v0/admin/setting')
    if (Array.isArray(res.data)) {
      settings.value = res.data
      const map: Record<string, string> = {}
      for (const s of res.data) {
        map[s.key] = s.value
      }
      editableSettings.value = map
    }
  } catch {
    // handled
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (!auth.isSuperAdmin) {
    toast.add({ title: '禁止', description: '仅超级管理员可修改设置', color: 'error' })
    return
  }
  saving.value = true
  try {
    await apiPut('/v0/admin/setting', settings.value.map(s => ({
      key: s.key,
      value: editableSettings.value[s.key] || s.value
    })))
    toast.add({ title: '成功', description: '设置已保存', color: 'success' })
    await fetchSettings()
  } catch {
    // handled
  } finally {
    saving.value = false
  }
}

onMounted(() => fetchSettings())
</script>

<template>
  <div>

    <div class="flex justify-between items-center mb-4">
      <p class="text-sm text-gray-500">仅超级管理员可修改</p>
      <UButton v-if="auth.isSuperAdmin" :loading="saving" @click="saveSettings">保存设置</UButton>
    </div>

    <div v-if="loading" class="text-center py-8 text-gray-500">加载中...</div>

    <div v-else class="space-y-4 max-w-3xl">
      <UCard v-for="setting in settings" :key="setting.key">
        <div class="space-y-2">
          <label class="text-sm font-semibold">{{ setting.key }}</label>
          <UInput
            v-if="auth.isSuperAdmin"
            v-model="editableSettings[setting.key]"
            :placeholder="setting.value"
          />
          <p v-else class="text-sm text-gray-500 break-all">{{ setting.value }}</p>
        </div>
      </UCard>
    </div>

    <div v-if="!loading && settings.length === 0" class="text-center py-8 text-gray-500">
      暂无设置项
    </div>
  </div>
</template>
