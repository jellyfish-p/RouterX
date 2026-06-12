<script setup lang="ts">
import type { ApiResponse, ChannelInfo, CreateChannelRequest, FetchChannelModelsResult, PaginatedResponse, TestChannelResult, UpdateChannelRequest } from '~/types/api'

definePageMeta({
  layout: 'dashboard',
  middleware: ['setup', 'auth', 'admin'],
  title: '通道管理'
})

const { apiGet, apiPost, apiPut, apiPatch, apiDelete } = useApi()
const toast = useToast()

const channels = ref<ChannelInfo[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)

const showCreate = ref(false)
const createForm = reactive<CreateChannelRequest>({
  idx: 0,
  type: 1,
  name: '',
  models: '',
  base_url: '',
  base_urls: [],
  api_key: '',
  api_keys: [],
  key_selection_mode: 'round_robin',
  upstreams: [],
  model_rewrites: {},
  group: '',
  upstream_options: {},
  priority: 100,
  weight: 10,
  status: 1,
})
const createBaseURLsText = ref('')
const createAPIKeysText = ref('')
const createUpstreamsText = ref('')
const createModelRewritesText = ref('{}')
const createOptionsText = ref('{}')
const createLoading = ref(false)

const editChannel = ref<ChannelInfo | null>(null)
const editForm = reactive<UpdateChannelRequest>({})
const editBaseURLsText = ref('')
const editAPIKeysText = ref('')
const editUpstreamsText = ref('')
const editModelRewritesText = ref('')
const editOptionsText = ref('')
const editLoading = ref(false)

const testingId = ref<number | null>(null)
const fetchingModelsId = ref<number | null>(null)
const testResult = ref<TestChannelResult | null>(null)
const fetchedModels = ref<{ name: string, models: string[] } | null>(null)
const showTestResult = computed({
  get: () => !!testResult.value,
  set: (open: boolean) => {
    if (!open) testResult.value = null
  }
})
const showFetchedModels = computed({
  get: () => !!fetchedModels.value,
  set: (open: boolean) => {
    if (!open) fetchedModels.value = null
  }
})
const showEdit = computed({
  get: () => !!editChannel.value,
  set: (open: boolean) => {
    if (!open) editChannel.value = null
  }
})

const channelTypeItems = [
  { label: 'OpenAI', value: 1 },
  { label: 'Azure', value: 2 },
  { label: 'Claude', value: 3 },
  { label: 'Gemini', value: 4 },
  { label: 'Qwen', value: 5 },
  { label: 'DeepSeek', value: 6 },
  { label: 'xAI', value: 7 },
  { label: 'RouterX', value: 8 },
  { label: 'OpenAI Compat', value: 100 },
]

const statusItems = [
  { label: '启用', value: 1 },
  { label: '禁用', value: 0 },
  { label: '维护', value: 2 },
]

const keySelectionItems = [
  { label: '轮询', value: 'round_robin' },
  { label: '随机', value: 'random' },
]

const channelTypeLabels: Record<number, string> = Object.fromEntries(channelTypeItems.map(item => [item.value, item.label]))

async function fetchChannels() {
  loading.value = true
  try {
    const res = await apiGet<PaginatedResponse<ChannelInfo>>(`/v0/admin/channel?page=${page.value}&page_size=${pageSize}`)
    channels.value = res.data?.data || []
    total.value = res.data?.total || 0
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  let payload: CreateChannelRequest
  try {
    payload = buildCreatePayload()
  } catch (err) {
    toast.add({ title: '配置格式有误', description: err instanceof Error ? err.message : '请检查 JSON 配置', color: 'warning' })
    return
  }
  if (!payload.name || !payload.models || !hasAnyKey(payload)) {
    toast.add({ title: '提示', description: '请填写名称、模型和至少一个 API Key', color: 'warning' })
    return
  }
  createLoading.value = true
  try {
    await apiPost('/v0/admin/channel', payload)
    toast.add({ title: '成功', description: '通道已创建', color: 'success' })
    showCreate.value = false
    resetCreateForm()
    await fetchChannels()
  } finally {
    createLoading.value = false
  }
}

async function handleEdit() {
  if (!editChannel.value) return
  let payload: UpdateChannelRequest
  try {
    payload = buildEditPayload()
  } catch (err) {
    toast.add({ title: '配置格式有误', description: err instanceof Error ? err.message : '请检查 JSON 配置', color: 'warning' })
    return
  }
  editLoading.value = true
  try {
    await apiPut(`/v0/admin/channel/${editChannel.value.id}`, payload)
    toast.add({ title: '成功', description: '通道已更新', color: 'success' })
    editChannel.value = null
    await fetchChannels()
  } finally {
    editLoading.value = false
  }
}

async function handleTest(channel: ChannelInfo) {
  testingId.value = channel.id
  testResult.value = null
  try {
    const res = await apiPost<ApiResponse<TestChannelResult>>(`/v0/admin/channel/${channel.id}/test`)
    testResult.value = res.data || null
  } finally {
    testingId.value = null
  }
}

async function handleFetchModels(channel: ChannelInfo) {
  fetchingModelsId.value = channel.id
  fetchedModels.value = null
  try {
    const res = await apiGet<ApiResponse<FetchChannelModelsResult>>(`/v0/admin/channel/${channel.id}/models`)
    fetchedModels.value = { name: channel.name, models: res.data?.models || [] }
  } finally {
    fetchingModelsId.value = null
  }
}

async function handleToggle(channel: ChannelInfo) {
  const action = channel.status === 1 ? 'disable' : 'enable'
  await apiPatch(`/v0/admin/channel/${channel.id}/${action}`)
  toast.add({ title: '成功', description: channel.status === 1 ? '通道已禁用' : '通道已启用', color: 'success' })
  await fetchChannels()
}

async function handleDelete(channel: ChannelInfo) {
  if (!confirm(`确定完全删除通道 "${channel.name}"？`)) return
  await apiDelete(`/v0/admin/channel/${channel.id}`)
  toast.add({ title: '成功', description: '通道已删除', color: 'success' })
  await fetchChannels()
}

function startEdit(ch: ChannelInfo) {
  editChannel.value = ch
  Object.keys(editForm).forEach((key) => delete editForm[key as keyof UpdateChannelRequest])
  editForm.idx = ch.idx
  editForm.type = ch.type
  editForm.name = ch.name
  editForm.models = ch.models
  editForm.base_url = ch.base_url
  editForm.key_selection_mode = ch.key_selection_mode
  editForm.group = ch.group
  editForm.priority = ch.priority
  editForm.weight = ch.weight
  editForm.status = ch.status
  editBaseURLsText.value = (ch.base_urls || []).join('\n')
  editAPIKeysText.value = ''
  editUpstreamsText.value = ''
  editModelRewritesText.value = JSON.stringify(ch.model_rewrites || {}, null, 2)
  editOptionsText.value = JSON.stringify(ch.upstream_options || {}, null, 2)
}

function buildCreatePayload(): CreateChannelRequest {
  return {
    ...createForm,
    base_urls: parseList(createBaseURLsText.value),
    api_keys: parseList(createAPIKeysText.value),
    upstreams: parseUpstreams(createUpstreamsText.value),
    model_rewrites: parseStringObject(createModelRewritesText.value),
    upstream_options: parseObject(createOptionsText.value),
  }
}

function buildEditPayload(): UpdateChannelRequest {
  const payload: UpdateChannelRequest = { ...editForm }
  payload.base_urls = parseList(editBaseURLsText.value)
  if (editAPIKeysText.value.trim()) {
    payload.api_keys = parseList(editAPIKeysText.value)
  }
  if (editUpstreamsText.value.trim()) {
    payload.upstreams = parseUpstreams(editUpstreamsText.value)
  }
  if (editModelRewritesText.value.trim()) {
    payload.model_rewrites = parseStringObject(editModelRewritesText.value)
  }
  if (editOptionsText.value.trim()) {
    payload.upstream_options = parseObject(editOptionsText.value)
  }
  return payload
}

function resetCreateForm() {
  createForm.idx = 0
  createForm.type = 1
  createForm.name = ''
  createForm.models = ''
  createForm.base_url = ''
  createForm.api_key = ''
  createForm.key_selection_mode = 'round_robin'
  createForm.group = ''
  createForm.priority = 100
  createForm.weight = 10
  createForm.status = 1
  createBaseURLsText.value = ''
  createAPIKeysText.value = ''
  createUpstreamsText.value = ''
  createModelRewritesText.value = '{}'
  createOptionsText.value = '{}'
}

function parseList(value: string) {
  return value.split(/[\n,]/).map(item => item.trim()).filter(Boolean)
}

function parseObject(value: string) {
  const trimmed = value.trim()
  if (!trimmed) return {}
  const parsed = JSON.parse(trimmed)
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error('JSON 必须是对象')
  }
  return parsed as Record<string, unknown>
}

function parseStringObject(value: string) {
  const parsed = parseObject(value)
  return Object.fromEntries(Object.entries(parsed).map(([key, item]) => [key, String(item)]))
}

function parseUpstreams(value: string) {
  const trimmed = value.trim()
  if (!trimmed) return []
  if (trimmed.startsWith('[')) {
    const parsed = JSON.parse(trimmed)
    if (!Array.isArray(parsed)) throw new Error('上游键值对 JSON 必须是数组')
    return parsed.map(item => ({
      base_url: String(item.base_url || '').trim(),
      api_key: String(item.api_key || '').trim(),
    })).filter(item => item.base_url || item.api_key)
  }
  return trimmed.split('\n').map((line) => {
    const [baseURL = '', apiKey = ''] = line.split('|')
    return { base_url: baseURL.trim(), api_key: apiKey.trim() }
  }).filter(item => item.base_url || item.api_key)
}

function hasAnyKey(payload: CreateChannelRequest) {
  return !!payload.api_key.trim() || payload.api_keys.length > 0 || payload.upstreams.some(item => item.api_key.trim())
}

onMounted(() => fetchChannels())
watch(page, () => fetchChannels())
</script>

<template>
  <div>
    <div class="flex justify-between items-center mb-4">
      <p class="text-sm text-gray-500">管理上游模型通道</p>
      <UButton @click="showCreate = true">创建通道</UButton>
    </div>

    <div class="overflow-x-auto rounded border border-gray-200">
      <table class="min-w-full text-sm">
        <thead class="bg-gray-50 text-left text-gray-500">
          <tr>
            <th class="px-3 py-2 font-medium">排序</th>
            <th class="px-3 py-2 font-medium">类型</th>
            <th class="px-3 py-2 font-medium">名称</th>
            <th class="px-3 py-2 font-medium">模型</th>
            <th class="px-3 py-2 font-medium">分组</th>
            <th class="px-3 py-2 font-medium">Key</th>
            <th class="px-3 py-2 font-medium">优先级</th>
            <th class="px-3 py-2 font-medium">权重</th>
            <th class="px-3 py-2 font-medium">状态</th>
            <th class="px-3 py-2 font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="10" class="px-3 py-8 text-center text-gray-500">加载中</td>
          </tr>
          <tr v-for="channel in channels" v-else :key="channel.id" class="border-t border-gray-100">
            <td class="px-3 py-2">{{ channel.idx }}</td>
            <td class="px-3 py-2">{{ channelTypeLabels[channel.type] || `类型 ${channel.type}` }}</td>
            <td class="px-3 py-2 font-medium">{{ channel.name }}</td>
            <td class="px-3 py-2 max-w-64">
              <span class="line-clamp-2">{{ channel.models }}</span>
            </td>
            <td class="px-3 py-2">{{ channel.group || '-' }}</td>
            <td class="px-3 py-2">{{ channel.api_key_count }}</td>
            <td class="px-3 py-2">{{ channel.priority }}</td>
            <td class="px-3 py-2">{{ channel.weight }}</td>
            <td class="px-3 py-2">
              <UBadge :color="channel.status === 1 ? 'success' : channel.status === 2 ? 'warning' : 'error'">
                {{ channel.status === 1 ? '启用' : channel.status === 2 ? '维护' : '禁用' }}
              </UBadge>
            </td>
            <td class="px-3 py-2">
              <div class="flex gap-1 flex-wrap">
                <UButton variant="ghost" size="xs" @click="startEdit(channel)">编辑</UButton>
                <UButton variant="ghost" size="xs" :loading="testingId === channel.id" @click="handleTest(channel)">测试</UButton>
                <UButton variant="ghost" size="xs" :loading="fetchingModelsId === channel.id" @click="handleFetchModels(channel)">模型</UButton>
                <UButton variant="ghost" size="xs" @click="handleToggle(channel)">{{ channel.status === 1 ? '禁用' : '启用' }}</UButton>
                <UButton variant="ghost" size="xs" color="error" @click="handleDelete(channel)">删除</UButton>
              </div>
            </td>
          </tr>
          <tr v-if="!loading && channels.length === 0">
            <td colspan="10" class="px-3 py-8 text-center text-gray-500">暂无通道</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="flex justify-center mt-4">
      <UPagination v-model:page="page" :total="total" :page-size="pageSize" />
    </div>

    <UModal v-model:open="showTestResult" title="测试结果">
      <template #body>
        <div v-if="testResult" class="space-y-3">
          <div class="flex items-center gap-2">
            <UIcon :name="testResult.success ? 'i-lucide-circle-check' : 'i-lucide-circle-x'"
                   :class="testResult.success ? 'text-green-500' : 'text-red-500'" class="size-6" />
            <span class="font-semibold">{{ testResult.success ? '连接成功' : '连接失败' }}</span>
          </div>
          <p>响应时间: {{ testResult.response_ms }}ms</p>
          <p v-if="testResult.model_count !== undefined">模型数量: {{ testResult.model_count }}</p>
          <p v-if="testResult.error" class="text-red-500">{{ testResult.error }}</p>
        </div>
      </template>
    </UModal>

    <UModal v-model:open="showFetchedModels" title="上游模型">
      <template #body>
        <div v-if="fetchedModels" class="space-y-2">
          <p class="text-sm text-gray-500">{{ fetchedModels.name }}</p>
          <div class="max-h-96 overflow-auto rounded border border-gray-200 p-3 text-sm">
            <p v-for="model in fetchedModels.models" :key="model">{{ model }}</p>
          </div>
        </div>
      </template>
    </UModal>

    <UModal v-model:open="showCreate" title="创建通道">
      <template #body>
        <form class="space-y-4" @submit.prevent="handleCreate">
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <UFormField label="排序 idx">
              <UInput v-model.number="createForm.idx" type="number" />
            </UFormField>
            <UFormField label="类型" required>
              <USelect v-model="createForm.type" :items="channelTypeItems" />
            </UFormField>
            <UFormField label="名称" required>
              <UInput v-model="createForm.name" />
            </UFormField>
            <UFormField label="分组">
              <UInput v-model="createForm.group" placeholder="default" />
            </UFormField>
          </div>

          <UFormField label="模型列表" required>
            <UInput v-model="createForm.models" placeholder="gpt-4o,gpt-4o-mini 或 *" />
          </UFormField>
          <UFormField label="单上游 Base URL">
            <UInput v-model="createForm.base_url" placeholder="留空使用该类型默认地址" />
          </UFormField>
          <UFormField label="多上游 Base URL">
            <UTextarea v-model="createBaseURLsText" :rows="3" placeholder="每行一个 URL" />
          </UFormField>
          <UFormField label="单 API Key">
            <UInput v-model="createForm.api_key" type="password" />
          </UFormField>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <UFormField label="多 API Key">
              <UTextarea v-model="createAPIKeysText" :rows="3" placeholder="每行一个 key" />
            </UFormField>
            <UFormField label="Key 选择模式">
              <USelect v-model="createForm.key_selection_mode" :items="keySelectionItems" />
            </UFormField>
          </div>
          <UFormField label="上游 URL/Key 键值对">
            <UTextarea v-model="createUpstreamsText" :rows="3" placeholder="每行: https://api.example.com|sk-..." />
          </UFormField>
          <UFormField label="模型名重写 JSON">
            <UTextarea v-model="createModelRewritesText" :rows="3" placeholder='{"gpt-4o":"claude-sonnet-4-5"}' />
          </UFormField>
          <UFormField label="扩展配置 JSON">
            <UTextarea v-model="createOptionsText" :rows="3" placeholder="{}" />
          </UFormField>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
            <UFormField label="优先级">
              <UInput v-model.number="createForm.priority" type="number" />
            </UFormField>
            <UFormField label="权重">
              <UInput v-model.number="createForm.weight" type="number" />
            </UFormField>
            <UFormField label="状态">
              <USelect v-model="createForm.status" :items="statusItems" />
            </UFormField>
          </div>
          <UButton type="submit" block :loading="createLoading">创建</UButton>
        </form>
      </template>
    </UModal>

    <UModal v-model:open="showEdit" title="编辑通道">
      <template #body>
        <form v-if="editChannel" class="space-y-4" @submit.prevent="handleEdit">
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <UFormField label="排序 idx">
              <UInput v-model.number="editForm.idx" type="number" />
            </UFormField>
            <UFormField label="类型">
              <USelect v-model="editForm.type" :items="channelTypeItems" />
            </UFormField>
            <UFormField label="名称">
              <UInput v-model="editForm.name" />
            </UFormField>
            <UFormField label="分组">
              <UInput v-model="editForm.group" />
            </UFormField>
          </div>

          <UFormField label="模型列表">
            <UInput v-model="editForm.models" />
          </UFormField>
          <UFormField label="单上游 Base URL">
            <UInput v-model="editForm.base_url" />
          </UFormField>
          <UFormField label="多上游 Base URL">
            <UTextarea v-model="editBaseURLsText" :rows="3" placeholder="每行一个 URL" />
          </UFormField>
          <UFormField label="替换单 API Key">
            <UInput v-model="editForm.api_key" type="password" placeholder="留空表示不修改" />
          </UFormField>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <UFormField label="替换多 API Key">
              <UTextarea v-model="editAPIKeysText" :rows="3" placeholder="留空表示不修改" />
            </UFormField>
            <UFormField label="Key 选择模式">
              <USelect v-model="editForm.key_selection_mode" :items="keySelectionItems" />
            </UFormField>
          </div>
          <UFormField label="替换上游 URL/Key 键值对">
            <UTextarea v-model="editUpstreamsText" :rows="3" placeholder="留空表示不修改" />
          </UFormField>
          <UFormField label="模型名重写 JSON">
            <UTextarea v-model="editModelRewritesText" :rows="3" />
          </UFormField>
          <UFormField label="扩展配置 JSON">
            <UTextarea v-model="editOptionsText" :rows="3" />
          </UFormField>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
            <UFormField label="优先级">
              <UInput v-model.number="editForm.priority" type="number" />
            </UFormField>
            <UFormField label="权重">
              <UInput v-model.number="editForm.weight" type="number" />
            </UFormField>
            <UFormField label="状态">
              <USelect v-model="editForm.status" :items="statusItems" />
            </UFormField>
          </div>
          <UButton type="submit" block :loading="editLoading">保存</UButton>
        </form>
      </template>
    </UModal>
  </div>
</template>
