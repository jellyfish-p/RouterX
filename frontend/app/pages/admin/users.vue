<script setup lang="ts">
import type {
  PaginatedResponse,
  UserBrief,
  CreateUserRequest,
  UpdateUserRequest,
  UpdateQuotaRequest,
} from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth', 'admin'],
  title: '用户管理'
})

const quotaUnit = 100000000
const { apiGet, apiPost, apiPut, apiPatch, apiDelete } = useApi()
const toast = useToast()

const users = ref<UserBrief[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const keyword = ref('')
const roleFilter = ref<number | undefined>()
const statusFilter = ref<number | undefined>()
const groupFilterText = ref('')
const loading = ref(false)

const showCreate = ref(false)
const createForm = reactive<CreateUserRequest>({
  username: '',
  password: '',
  display_name: '',
  email: '',
  role: 0,
  quota: quotaUnit,
  group_id: null,
})
const createQuotaText = ref('1')
const createGroupText = ref('')
const createLoading = ref(false)

const editUser = ref<UserBrief | null>(null)
const editForm = reactive<UpdateUserRequest>({
  display_name: '',
  email: '',
  status: 1,
  group_id: null,
})
const editGroupText = ref('')
const editLoading = ref(false)
const showEdit = computed({
  get: () => !!editUser.value,
  set: (open: boolean) => {
    if (!open) editUser.value = null
  }
})

const quotaUser = ref<UserBrief | null>(null)
const quotaForm = reactive<UpdateQuotaRequest>({ quota: 0 })
const quotaText = ref('0')
const quotaLoading = ref(false)
const showQuota = computed({
  get: () => !!quotaUser.value,
  set: (open: boolean) => {
    if (!open) quotaUser.value = null
  }
})

const roleItems = [
  { label: '用户', value: 0 },
]

const roleFilterItems = [
  { label: '全部角色', value: undefined },
  { label: '用户', value: 0 },
  { label: '管理员', value: 1 },
  { label: '超级管理员', value: 2 },
]

const statusItems = [
  { label: '启用', value: 1 },
  { label: '禁用', value: 0 },
]

const statusFilterItems = [
  { label: '全部状态', value: undefined },
  ...statusItems,
]

async function fetchUsers() {
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: String(page.value),
      page_size: String(pageSize),
    })
    if (keyword.value.trim()) params.set('keyword', keyword.value.trim())
    if (roleFilter.value !== undefined) params.set('role', String(roleFilter.value))
    if (statusFilter.value !== undefined) params.set('status', String(statusFilter.value))
    let groupID: number | null
    try {
      groupID = parseOptionalID(groupFilterText.value)
    } catch (err) {
      toast.add({ title: '筛选条件有误', description: err instanceof Error ? err.message : '请检查分组 ID', color: 'warning' })
      return
    }
    if (groupID !== null) params.set('group_id', String(groupID))
    const res = await apiGet<PaginatedResponse<UserBrief>>(`/v0/admin/user?${params}`)
    users.value = res.data?.data || []
    total.value = res.data?.total || 0
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  let payload: CreateUserRequest
  try {
    payload = buildCreatePayload()
  } catch (err) {
    toast.add({ title: '配置格式有误', description: err instanceof Error ? err.message : '请检查额度或分组 ID', color: 'warning' })
    return
  }
  if (!payload.username.trim() || !payload.password) {
    toast.add({ title: '提示', description: '请填写用户名和密码', color: 'warning' })
    return
  }
  createLoading.value = true
  try {
    await apiPost('/v0/admin/user', payload)
    toast.add({ title: '成功', description: '用户已创建', color: 'success' })
    showCreate.value = false
    resetCreateForm()
    await fetchUsers()
  } finally {
    createLoading.value = false
  }
}

async function handleEdit() {
  if (!editUser.value) return
  let payload: UpdateUserRequest
  try {
    payload = buildEditPayload()
  } catch (err) {
    toast.add({ title: '配置格式有误', description: err instanceof Error ? err.message : '请检查分组 ID', color: 'warning' })
    return
  }
  editLoading.value = true
  try {
    await apiPut(`/v0/admin/user/${editUser.value.id}`, payload)
    toast.add({ title: '成功', description: '用户已更新', color: 'success' })
    editUser.value = null
    await fetchUsers()
  } finally {
    editLoading.value = false
  }
}

async function handleQuota() {
  if (!quotaUser.value) return
  try {
    quotaForm.quota = quotaToUnits(quotaText.value)
  } catch (err) {
    toast.add({ title: '配置格式有误', description: err instanceof Error ? err.message : '请检查额度', color: 'warning' })
    return
  }
  quotaLoading.value = true
  try {
    await apiPatch(`/v0/admin/user/${quotaUser.value.id}/quota`, quotaForm)
    toast.add({ title: '成功', description: '额度已调整', color: 'success' })
    quotaUser.value = null
    await fetchUsers()
  } finally {
    quotaLoading.value = false
  }
}

async function handleDelete(user: UserBrief) {
  if (!confirm(`确定删除用户 "${user.username}"？`)) return
  await apiDelete(`/v0/admin/user/${user.id}`)
  toast.add({ title: '成功', description: '用户已删除', color: 'success' })
  await fetchUsers()
}

function startEdit(user: UserBrief) {
  editUser.value = user
  editForm.display_name = user.display_name || ''
  editForm.email = user.email || ''
  editForm.status = user.status
  editForm.group_id = user.group_id ?? null
  editGroupText.value = user.group_id ? String(user.group_id) : ''
}

function startQuota(user: UserBrief) {
  quotaUser.value = user
  quotaForm.quota = user.quota
  quotaText.value = quotaFromUnits(user.quota)
}

function buildCreatePayload(): CreateUserRequest {
  const groupID = parseOptionalID(createGroupText.value)
  createForm.quota = quotaToUnits(createQuotaText.value)
  createForm.group_id = groupID
  createForm.role = 0
  return {
    username: createForm.username.trim(),
    password: createForm.password,
    display_name: createForm.display_name.trim(),
    email: createForm.email.trim(),
    role: createForm.role,
    quota: createForm.quota,
    group_id: createForm.group_id,
  }
}

function buildEditPayload(): UpdateUserRequest {
  return {
    display_name: editForm.display_name.trim(),
    email: editForm.email.trim(),
    status: editForm.status,
    group_id: parseOptionalID(editGroupText.value),
  }
}

function resetCreateForm() {
  createForm.username = ''
  createForm.password = ''
  createForm.display_name = ''
  createForm.email = ''
  createForm.role = 0
  createForm.quota = quotaUnit
  createForm.group_id = null
  createQuotaText.value = '1'
  createGroupText.value = ''
}

function parseOptionalID(value: string) {
  const trimmed = value.trim()
  if (!trimmed) return null
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new Error('分组 ID 必须是正整数')
  }
  return parsed
}

function quotaToUnits(value: string) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed < 0) {
    throw new Error('额度必须是非负数字')
  }
  return Math.round(parsed * quotaUnit)
}

function quotaFromUnits(value: number) {
  return (value / quotaUnit).toString()
}

function roleLabel(role: number) {
  if (role === 2) return '超级管理员'
  if (role === 1) return '管理员'
  return '用户'
}

function resetAndFetch() {
  page.value = 1
  fetchUsers()
}

onMounted(() => fetchUsers())
watch(page, () => fetchUsers())
</script>

<template>
  <div>
    <div class="flex flex-wrap justify-between items-center gap-3 mb-4">
      <div class="flex flex-wrap gap-2">
        <UInput v-model="keyword" placeholder="搜索用户" @input="resetAndFetch" />
        <USelect v-model="roleFilter" :items="roleFilterItems" class="w-36" @change="resetAndFetch" />
        <USelect v-model="statusFilter" :items="statusFilterItems" class="w-36" @change="resetAndFetch" />
        <UInput v-model="groupFilterText" placeholder="分组 ID" class="w-28" @input="resetAndFetch" />
      </div>
      <UButton @click="showCreate = true">创建用户</UButton>
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
            <th class="px-3 py-2 font-medium">额度</th>
            <th class="px-3 py-2 font-medium">分组</th>
            <th class="px-3 py-2 font-medium">状态</th>
            <th class="px-3 py-2 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="9" class="px-3 py-8 text-center text-gray-500">加载中</td>
          </tr>
          <tr v-for="user in users" v-else :key="user.id" class="border-t border-gray-100">
            <td class="px-3 py-2">{{ user.id }}</td>
            <td class="px-3 py-2 font-medium">{{ user.username }}</td>
            <td class="px-3 py-2">{{ user.display_name || '-' }}</td>
            <td class="px-3 py-2">{{ user.email || '-' }}</td>
            <td class="px-3 py-2">
              <UBadge :color="user.role === 2 ? 'error' : user.role === 1 ? 'warning' : 'neutral'">
                {{ roleLabel(user.role) }}
              </UBadge>
            </td>
            <td class="px-3 py-2">{{ (user.quota / quotaUnit).toFixed(2) }}</td>
            <td class="px-3 py-2">{{ user.group_id || '-' }}</td>
            <td class="px-3 py-2">
              <UBadge :color="user.status === 1 ? 'success' : 'error'">
                {{ user.status === 1 ? '启用' : '禁用' }}
              </UBadge>
            </td>
            <td class="px-3 py-2">
              <div class="flex gap-1 flex-wrap">
                <UButton variant="ghost" size="xs" @click="startEdit(user)">编辑</UButton>
                <UButton variant="ghost" size="xs" @click="startQuota(user)">额度</UButton>
                <UButton variant="ghost" size="xs" color="error" @click="handleDelete(user)">删除</UButton>
              </div>
            </td>
          </tr>
          <tr v-if="!loading && users.length === 0">
            <td colspan="9" class="px-3 py-8 text-center text-gray-500">暂无用户</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="flex justify-center mt-4">
      <UPagination v-model:page="page" :total="total" :page-size="pageSize" />
    </div>

    <UModal v-model:open="showCreate" title="创建用户">
      <template #body>
        <form class="space-y-4" @submit.prevent="handleCreate">
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
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
              <USelect v-model="createForm.role" :items="roleItems" />
            </UFormField>
            <UFormField label="分组 ID">
              <UInput v-model="createGroupText" placeholder="留空为默认分组" />
            </UFormField>
            <UFormField label="额度">
              <UInput v-model="createQuotaText" type="number" min="0" step="0.01" />
            </UFormField>
          </div>
          <UButton type="submit" block :loading="createLoading">创建</UButton>
        </form>
      </template>
    </UModal>

    <UModal v-model:open="showEdit" title="编辑用户">
      <template #body>
        <form v-if="editUser" class="space-y-4" @submit.prevent="handleEdit">
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <UFormField label="显示名称">
              <UInput v-model="editForm.display_name" />
            </UFormField>
            <UFormField label="邮箱">
              <UInput v-model="editForm.email" type="email" />
            </UFormField>
            <UFormField label="分组 ID">
              <UInput v-model="editGroupText" placeholder="留空清除分组" />
            </UFormField>
            <UFormField label="状态">
              <USelect v-model="editForm.status" :items="statusItems" />
            </UFormField>
          </div>
          <UButton type="submit" block :loading="editLoading">保存</UButton>
        </form>
      </template>
    </UModal>

    <UModal v-model:open="showQuota" title="调整额度">
      <template #body>
        <form v-if="quotaUser" class="space-y-4" @submit.prevent="handleQuota">
          <p class="text-sm text-gray-500">
            {{ quotaUser.username }} 当前额度: {{ (quotaUser.quota / quotaUnit).toFixed(2) }}
          </p>
          <UFormField label="新额度" required>
            <UInput v-model="quotaText" type="number" min="0" step="0.01" />
          </UFormField>
          <UButton type="submit" block :loading="quotaLoading">保存</UButton>
        </form>
      </template>
    </UModal>
  </div>
</template>
