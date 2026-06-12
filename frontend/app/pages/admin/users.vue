<script setup lang="ts">
import type {
  ApiResponse, UserBrief, CreateUserRequest, UpdateUserRequest, UpdateQuotaRequest, PaginatedResponse
} from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth', 'admin'],
  title: '用户管理'
})

const { apiGet, apiPost, apiPut, apiPatch, apiDelete } = useApi()
const toast = useToast()

const users = ref<UserBrief[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const keyword = ref('')
const loading = ref(false)

const showCreate = ref(false)
const createForm = reactive<CreateUserRequest>({
  username: '',
  password: '',
  display_name: '',
  email: '',
  role: 0,
  quota: 100000000,
})
const createLoading = ref(false)

const editUser = ref<UserBrief | null>(null)
const editForm = reactive<UpdateUserRequest>({
  display_name: '',
  email: '',
})
const editLoading = ref(false)

const quotaUser = ref<UserBrief | null>(null)
const quotaForm = reactive<UpdateQuotaRequest>({ quota: 0 })
const quotaLoading = ref(false)

async function fetchUsers() {
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: String(page.value),
      page_size: String(pageSize),
    })
    if (keyword.value) params.set('keyword', keyword.value)
    const res = await apiGet<PaginatedResponse<UserBrief>>(`/v0/admin/user?${params}`)
    users.value = res.data?.data || []
    total.value = res.data?.total || 0
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
    await apiPost('/v0/admin/user', createForm)
    toast.add({ title: '成功', description: '用户已创建', color: 'success' })
    showCreate.value = false
    createForm.username = ''
    createForm.password = ''
    await fetchUsers()
  } catch {
    // handled
  } finally {
    createLoading.value = false
  }
}

async function handleEdit() {
  if (!editUser.value) return
  editLoading.value = true
  try {
    await apiPut(`/v0/admin/user/${editUser.value.id}`, editForm)
    toast.add({ title: '成功', description: '用户已更新', color: 'success' })
    editUser.value = null
    await fetchUsers()
  } catch {
    // handled
  } finally {
    editLoading.value = false
  }
}

async function handleQuota() {
  if (!quotaUser.value) return
  quotaLoading.value = true
  try {
    await apiPatch(`/v0/admin/user/${quotaUser.value.id}/quota`, quotaForm)
    toast.add({ title: '成功', description: '额度已调整', color: 'success' })
    quotaUser.value = null
    await fetchUsers()
  } catch {
    // handled
  } finally {
    quotaLoading.value = false
  }
}

async function handleDelete(user: UserBrief) {
  if (!confirm(`确定删除用户 "${user.username}"？`)) return
  try {
    await apiDelete(`/v0/admin/user/${user.id}`)
    toast.add({ title: '成功', description: '用户已删除', color: 'success' })
    await fetchUsers()
  } catch {
    // handled
  }
}

function startEdit(user: UserBrief) {
  editUser.value = user
  editForm.display_name = user.display_name || ''
  editForm.email = user.email || ''
}

function startQuota(user: UserBrief) {
  quotaUser.value = user
  quotaForm.quota = user.quota
}

onMounted(() => fetchUsers())
watch(page, () => fetchUsers())
</script>

<template>
  <div>

    <div class="flex justify-between items-center mb-4">
      <div class="flex gap-2">
        <UInput v-model="keyword" placeholder="搜索用户..." @input="page = 1; fetchUsers()" />
      </div>
      <UButton @click="showCreate = true">创建用户</UButton>
    </div>

    <UTable
      :rows="users"
      :columns="[
        { key: 'id', label: 'ID' },
        { key: 'username', label: '用户名' },
        { key: 'display_name', label: '显示名' },
        { key: 'email', label: '邮箱' },
        { key: 'role', label: '角色' },
        { key: 'quota', label: '额度' },
        { key: 'status', label: '状态' },
        { key: 'actions', label: '操作' }
      ]"
      :loading="loading"
    >
      <template #role-data="{ row }">
        <UBadge :color="row.role === 2 ? 'error' : row.role === 1 ? 'warning' : 'neutral'">
          {{ row.role === 2 ? '超级管理员' : row.role === 1 ? '管理员' : '用户' }}
        </UBadge>
      </template>
      <template #quota-data="{ row }">
        {{ (row.quota / 100000000).toFixed(2) }}
      </template>
      <template #status-data="{ row }">
        <UBadge :color="row.status === 1 ? 'success' : 'error'">
          {{ row.status === 1 ? '启用' : '禁用' }}
        </UBadge>
      </template>
      <template #actions-data="{ row }">
        <div class="flex gap-1">
          <UButton variant="ghost" size="xs" @click="startEdit(row)">编辑</UButton>
          <UButton variant="ghost" size="xs" @click="startQuota(row)">额度</UButton>
          <UButton variant="ghost" size="xs" color="error" @click="handleDelete(row)">删除</UButton>
        </div>
      </template>
    </UTable>

    <div class="flex justify-center mt-4">
      <UPagination v-model:page="page" :total="total" :page-size="pageSize" />
    </div>

    <!-- Create Modal -->
    <UModal v-model:open="showCreate" title="创建用户">
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
              { label: '用户', value: 0 },
              { label: '管理员', value: 1 },
            ]" />
          </UFormField>
          <UFormField label="额度">
            <UInput v-model.number="createForm.quota" type="number" />
          </UFormField>
          <UButton type="submit" block :loading="createLoading">创建</UButton>
        </form>
      </template>
    </UModal>

    <!-- Edit Modal -->
    <UModal v-model:open="editUser" :open="!!editUser" title="编辑用户">
      <template #body>
        <form v-if="editUser" class="space-y-4" @submit.prevent="handleEdit">
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

    <!-- Quota Modal -->
    <UModal v-model:open="quotaUser" :open="!!quotaUser" title="调整额度">
      <template #body>
        <form v-if="quotaUser" class="space-y-4" @submit.prevent="handleQuota">
          <p class="text-sm text-gray-500">
            用户: {{ quotaUser.username }} (当前额度: {{ (quotaUser.quota / 100000000).toFixed(2) }})
          </p>
          <UFormField label="新额度" required>
            <UInput v-model.number="quotaForm.quota" type="number" />
          </UFormField>
          <UButton type="submit" block :loading="quotaLoading">保存</UButton>
        </form>
      </template>
    </UModal>
  </div>
</template>
