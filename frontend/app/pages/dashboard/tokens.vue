<script setup lang="ts">
import type { ApiResponse, TokenInfo, CreateTokenRequest, UpdateTokenRequest, PaginatedResponse } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth'],
  title: 'API 令牌'
})

const { apiGet, apiPost, apiPut, apiDelete } = useApi()
const toast = useToast()

const tokens = ref<TokenInfo[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)

const showCreate = ref(false)
const createForm = reactive<CreateTokenRequest>({
  name: '',
  remain_quota: -1,
  unlimited: true,
  expired_at: null
})
const createLoading = ref(false)
const newTokenKey = ref<string | null>(null)

const editingToken = ref<TokenInfo | null>(null)
const editForm = reactive<UpdateTokenRequest>({
  name: undefined,
  status: undefined,
  remain_quota: undefined,
  unlimited: undefined,
  expired_at: undefined
})
const editLoading = ref(false)

async function fetchTokens() {
  loading.value = true
  try {
    const res = await apiGet<PaginatedResponse<TokenInfo>>(`/v0/user/token?page=${page.value}&page_size=${pageSize}`)
    tokens.value = res.data?.data || []
    total.value = res.data?.total || 0
  } catch {
    // handled
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  if (!createForm.name) {
    toast.add({ title: '提示', description: '请输入令牌名称', color: 'warning' })
    return
  }
  createLoading.value = true
  try {
    const res = await apiPost<ApiResponse<TokenInfo>>('/v0/user/token', createForm)
    if (res.data) {
      if (res.data.key) {
        newTokenKey.value = res.data.key
      }
      toast.add({ title: '成功', description: '令牌已创建', color: 'success' })
      showCreate.value = false
      createForm.name = ''
      await fetchTokens()
    }
  } catch {
    // handled
  } finally {
    createLoading.value = false
  }
}

function startEdit(token: TokenInfo) {
  editingToken.value = token
  editForm.name = token.name
  editForm.status = undefined
  editForm.remain_quota = undefined
  editForm.unlimited = undefined
  editForm.expired_at = undefined
}

async function handleEdit() {
  if (!editingToken.value) return
  editLoading.value = true
  try {
    await apiPut(`/v0/user/token/${editingToken.value.id}`, editForm)
    toast.add({ title: '成功', description: '令牌已更新', color: 'success' })
    editingToken.value = null
    await fetchTokens()
  } catch {
    // handled
  } finally {
    editLoading.value = false
  }
}

async function handleDelete(token: TokenInfo) {
  if (!confirm('确定删除此令牌？')) return
  try {
    await apiDelete(`/v0/user/token/${token.id}`)
    toast.add({ title: '成功', description: '令牌已删除', color: 'success' })
    await fetchTokens()
  } catch {
    // handled
  }
}

async function copyKey() {
  if (newTokenKey.value) {
    await navigator.clipboard.writeText(newTokenKey.value)
    toast.add({ title: '已复制', description: '请妥善保存，此密钥只显示一次', color: 'info' })
  }
}

onMounted(() => {
  fetchTokens()
})
</script>

<template>
  <div>

    <div class="flex justify-between items-center mb-4">
      <p class="text-sm text-gray-500">管理 API Key，用于调用 /v1 转发接口</p>
      <UButton @click="showCreate = true">创建令牌</UButton>
    </div>

    <UTable
      :rows="tokens"
      :columns="[
        { key: 'name', label: '名称' },
        { key: 'status', label: '状态' },
        { key: 'remain_quota', label: '剩余额度' },
        { key: 'expired_at', label: '过期时间' },
        { key: 'created_at', label: '创建时间' },
        { key: 'actions', label: '操作' }
      ]"
      :loading="loading"
    >
      <template #status-data="{ row }">
        <UBadge :color="row.status === 1 ? 'success' : 'error'">
          {{ row.status === 1 ? '启用' : '禁用' }}
        </UBadge>
      </template>
      <template #remain_quota-data="{ row }">
        {{ row.unlimited ? '无限' : (row.remain_quota / 100000000).toFixed(2) }}
      </template>
      <template #expired_at-data="{ row }">
        {{ row.expired_at ? new Date(row.expired_at).toLocaleString() : '永不过期' }}
      </template>
      <template #created_at-data="{ row }">
        {{ new Date(row.created_at).toLocaleString() }}
      </template>
      <template #actions-data="{ row }">
        <div class="flex gap-2">
          <UButton variant="ghost" size="xs" @click="startEdit(row)">编辑</UButton>
          <UButton variant="ghost" size="xs" color="error" @click="handleDelete(row)">删除</UButton>
        </div>
      </template>
    </UTable>

    <!-- Create Modal -->
    <UModal v-model:open="showCreate" title="创建 API 令牌">
      <template #body>
        <form class="space-y-4" @submit.prevent="handleCreate">
          <UFormField label="名称" required>
            <UInput v-model="createForm.name" placeholder="令牌名称" />
          </UFormField>
          <UFormField label="额度">
            <div class="flex items-center gap-3">
              <UCheckbox v-model="createForm.unlimited" label="无限额度" />
            </div>
          </UFormField>
          <UFormField v-if="!createForm.unlimited" label="剩余额度">
            <UInput v-model.number="createForm.remain_quota" type="number" placeholder="0 表示无限" />
          </UFormField>
          <UButton type="submit" block :loading="createLoading">创建</UButton>
        </form>

        <div v-if="newTokenKey" class="mt-4 p-4 bg-green-50 dark:bg-green-950 rounded-lg">
          <p class="font-semibold text-green-700 dark:text-green-300 mb-2">令牌已创建，请保存密钥：</p>
          <div class="flex items-center gap-2">
            <code class="flex-1 bg-white dark:bg-gray-800 p-2 rounded text-xs break-all">{{ newTokenKey }}</code>
            <UButton icon="i-lucide-copy" variant="ghost" size="xs" @click="copyKey" />
          </div>
          <p class="text-xs text-red-500 mt-2">此密钥仅在当前页面显示一次，请立即复制保存。</p>
        </div>
      </template>
    </UModal>

    <!-- Edit Modal -->
    <UModal v-model:open="editingToken" :open="!!editingToken" title="编辑令牌">
      <template #body>
        <form v-if="editingToken" class="space-y-4" @submit.prevent="handleEdit">
          <UFormField label="名称">
            <UInput v-model="editForm.name" />
          </UFormField>
          <UFormField label="状态">
            <USelect
              v-model="editForm.status"
              :items="[
                { label: '启用', value: 1 },
                { label: '禁用', value: 0 }
              ]"
              placeholder="不修改"
            />
          </UFormField>
          <UButton type="submit" block :loading="editLoading">保存</UButton>
        </form>
      </template>
    </UModal>
  </div>
</template>
