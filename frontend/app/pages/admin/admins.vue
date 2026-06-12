<script setup lang="ts">
import type { ApiResponse, UserBrief, PaginatedResponse } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth', 'admin'],
  title: '管理员管理'
})

const { apiGet, apiPost, apiPut, apiDelete } = useApi()
const auth = useAuthStore()
const toast = useToast()

const admins = ref<UserBrief[]>([])
const loading = ref(false)

const showCreate = ref(false)
const createForm = reactive({
  username: '',
  password: '',
  display_name: '',
  email: '',
  role: 1,
  quota: -1,
})
const createLoading = ref(false)

const editAdmin = ref<UserBrief | null>(null)
const editForm = reactive({
  display_name: '',
  email: '',
})
const editLoading = ref(false)
const showEdit = computed({
  get: () => !!editAdmin.value,
  set: (open: boolean) => {
    if (!open) editAdmin.value = null
  }
})

async function fetchAdmins() {
  loading.value = true
  try {
    const res = await apiGet<PaginatedResponse<UserBrief>>('/v0/admin/admin?page_size=100')
    admins.value = res.data?.data || []
  } catch {
    // handled
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  if (!createForm.username || !createForm.password) {
    toast.add({ title: '提示', description: '请填写用户名和密码', color: 'warning' })
    return
  }
  createLoading.value = true
  try {
    await apiPost('/v0/admin/admin', createForm)
    toast.add({ title: '成功', description: '管理员已创建', color: 'success' })
    showCreate.value = false
    createForm.username = ''
    createForm.password = ''
    await fetchAdmins()
  } catch {
    // handled
  } finally {
    createLoading.value = false
  }
}

async function handleEdit() {
  if (!editAdmin.value) return
  editLoading.value = true
  try {
    await apiPut(`/v0/admin/admin/${editAdmin.value.id}`, editForm)
    toast.add({ title: '成功', description: '已更新', color: 'success' })
    editAdmin.value = null
    await fetchAdmins()
  } catch {
    // handled
  } finally {
    editLoading.value = false
  }
}

async function handleDelete(admin: UserBrief) {
  if (!confirm(`确定删除管理员 "${admin.username}"？`)) return
  try {
    await apiDelete(`/v0/admin/admin/${admin.id}`)
    toast.add({ title: '成功', description: '已删除', color: 'success' })
    await fetchAdmins()
  } catch {
    // handled
  }
}

function startEdit(admin: UserBrief) {
  editAdmin.value = admin
  editForm.display_name = admin.display_name || ''
  editForm.email = admin.email || ''
}

onMounted(() => fetchAdmins())
</script>

<template>
  <div>

    <div class="flex justify-between items-center mb-4">
      <p class="text-sm text-gray-500">仅超级管理员可操作</p>
      <UButton v-if="auth.isSuperAdmin" @click="showCreate = true">创建管理员</UButton>
    </div>

    <div class="overflow-x-auto rounded border border-gray-200">
      <table class="min-w-full text-sm">
        <thead class="bg-gray-50 text-left text-gray-500">
          <tr>
            <th class="px-3 py-2 font-medium">ID</th>
            <th class="px-3 py-2 font-medium">用户名</th>
            <th class="px-3 py-2 font-medium">显示名</th>
            <th class="px-3 py-2 font-medium">邮箱</th>
            <th class="px-3 py-2 font-medium">角色</th>
            <th class="px-3 py-2 font-medium">状态</th>
            <th class="px-3 py-2 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" class="px-3 py-8 text-center text-gray-500">加载中</td>
          </tr>
          <tr v-for="admin in admins" v-else :key="admin.id" class="border-t border-gray-100">
            <td class="px-3 py-2">{{ admin.id }}</td>
            <td class="px-3 py-2 font-medium">{{ admin.username }}</td>
            <td class="px-3 py-2">{{ admin.display_name || '-' }}</td>
            <td class="px-3 py-2">{{ admin.email || '-' }}</td>
            <td class="px-3 py-2">
              <UBadge :color="admin.role === 2 ? 'error' : 'warning'">
                {{ admin.role === 2 ? '超级管理员' : '管理员' }}
              </UBadge>
            </td>
            <td class="px-3 py-2">
              <UBadge :color="admin.status === 1 ? 'success' : 'error'">
                {{ admin.status === 1 ? '启用' : '禁用' }}
              </UBadge>
            </td>
            <td class="px-3 py-2">
              <div v-if="auth.isSuperAdmin" class="flex gap-1">
                <UButton variant="ghost" size="xs" @click="startEdit(admin)">编辑</UButton>
                <UButton variant="ghost" size="xs" color="error" @click="handleDelete(admin)">删除</UButton>
              </div>
            </td>
          </tr>
          <tr v-if="!loading && admins.length === 0">
            <td colspan="7" class="px-3 py-8 text-center text-gray-500">暂无管理员</td>
          </tr>
        </tbody>
      </table>
    </div>

    <UModal v-model:open="showCreate" title="创建管理员">
      <template #body>
        <form class="space-y-4" @submit.prevent="handleCreate">
          <UFormField label="用户名" required>
            <UInput v-model="createForm.username" />
          </UFormField>
          <UFormField label="密码" required>
            <UInput v-model="createForm.password" type="password" />
          </UFormField>
          <UFormField label="显示名称">
            <UInput v-model="createForm.display_name" />
          </UFormField>
          <UFormField label="邮箱">
            <UInput v-model="createForm.email" type="email" />
          </UFormField>
          <UFormField label="角色">
            <USelect v-model="createForm.role" :items="[
              { label: '管理员', value: 1 },
            ]" />
          </UFormField>
          <UButton type="submit" block :loading="createLoading">创建</UButton>
        </form>
      </template>
    </UModal>

    <UModal v-model:open="showEdit" title="编辑管理员">
      <template #body>
        <form v-if="editAdmin" class="space-y-4" @submit.prevent="handleEdit">
          <UFormField label="显示名称">
            <UInput v-model="editForm.display_name" />
          </UFormField>
          <UFormField label="邮箱">
            <UInput v-model="editForm.email" type="email" />
          </UFormField>
          <UButton type="submit" block :loading="editLoading">保存</UButton>
        </form>
      </template>
    </UModal>
  </div>
</template>
