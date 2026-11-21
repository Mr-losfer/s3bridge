<script setup>
import { ref, onMounted, computed } from 'vue'
import {
  Plus, RefreshCw, Radar, Settings2, Search, Minus, Power, ArrowRight,
  TriangleAlert, BookOpen, SlidersHorizontal, FileType2, Activity, CheckCheck,
} from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { notify, notifyError } from '../toast'
import { relTime, statusText, truncate, categoryLabels, categoryLabel } from '../utils'
import PageHeader from '../components/PageHeader.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Input from '../components/ui/Input.vue'
import Field from '../components/ui/Field.vue'
import Select from '../components/ui/Select.vue'
import Sheet from '../components/ui/Sheet.vue'
import Switch from '../components/ui/Switch.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'

const plugins = ref([])
const loading = ref(true)
const busyId = ref('')
const healthTarget = ref(null)
const healthForm = ref({})
const saving = ref(false)
const weightDraft = ref({})
const query = ref('')

const unhealthyCount = computed(() => plugins.value.filter((p) => p.status === 'unhealthy').length)
const customCount = computed(() => plugins.value.filter((p) => p.custom).length)
const fileTypeOptions = Object.entries(categoryLabels)
const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return plugins.value
  return plugins.value.filter((p) =>
    [p.name, p.id, p.kind, p.description].some((v) => String(v || '').toLowerCase().includes(q)))
})

const createOpen = ref(false)
const creating = ref(false)
const templates = ref([])
const createForm = ref({ id: '', name: '', version: '', description: '', template: 'blank', enabled: true, priority: 100, fileTypes: [] })

async function openCreate() {
  createForm.value = { id: '', name: '', version: '', description: '', template: 'blank', enabled: true, priority: 100, fileTypes: [] }
  createOpen.value = true
  if (!templates.value.length) {
    try {
      const r = await api('/plugins/registry')
      templates.value = r.registry || []
    } catch { /* 模板拉取失败时仍可选空白骨架 */ }
  }
}
function toggleCreateType(key) {
  const list = createForm.value.fileTypes
  const i = list.indexOf(key)
  if (i >= 0) list.splice(i, 1)
  else list.push(key)
}
async function submitCreate() {
  const id = createForm.value.id.trim().toLowerCase()
  if (!/^[a-z][a-z0-9][a-z0-9_-]{0,38}$/.test(id)) {
    notifyError(new Error('插件 ID 需小写字母开头，2-40 位，仅含小写字母/数字/下划线/连字符'))
    return
  }
  creating.value = true
  try {
    const body = { ...createForm.value, id, priority: Number(createForm.value.priority) || 0, fileTypes: createForm.value.fileTypes }
    const r = await api('/plugins', { method: 'POST', body })
    notify('插件已创建并编译完成，去详情页继续写源码', { title: id })
    createOpen.value = false
    await load()
    go('plugin', r.created || id)
  } catch (e) { notifyError(e, '创建插件失败') } finally { creating.value = false }
}

async function load() {
  loading.value = true
  try {
    plugins.value = (await api('/plugins')) || []
    const draft = {}
    for (const p of plugins.value) draft[p.id] = p.priority
    weightDraft.value = draft
  }
  catch (e) { notifyError(e, '加载插件失败') }
  finally { loading.value = false }
}
onMounted(load)

// 权重决定调度顺序：数值越大越优先，同权重时更健康的插件优先。
async function savePriority(p) {
  const next = Number(weightDraft.value[p.id])
  if (!Number.isFinite(next) || next === p.priority) return
  busyId.value = p.id
  try {
    await api(`/plugins/${encodeURIComponent(p.id)}`, { method: 'PATCH', body: { priority: next } })
    notify(`${p.name} 权重已设为 ${next}，越大越优先被调度`, { title: '调度权重已更新' })
    await load()
  } catch (e) {
    weightDraft.value[p.id] = p.priority
    notifyError(e, '权重保存失败')
  } finally { busyId.value = '' }
}
function adjustPriority(p, delta) {
  const cur = Number(weightDraft.value[p.id] ?? p.priority)
  weightDraft.value[p.id] = Math.max(0, Math.min(9999, cur + delta))
  savePriority(p)
}
function typeLabel(p) {
  if (!p.fileTypes || !p.fileTypes.length) return ['全部类型']
  return p.fileTypes.map(categoryLabel)
}

async function toggle(p) {
  busyId.value = p.id
  try {
    await api(`/plugins/${encodeURIComponent(p.id)}/toggle`, { method: 'POST', body: { enabled: !p.enabled } })
    notify(`${p.name} 已${p.enabled ? '停用' : '启用'}`)
    await load()
  } catch (e) { notifyError(e) } finally { busyId.value = '' }
}

async function check(p) {
  busyId.value = p.id
  try {
    const v = await api(`/plugins/${encodeURIComponent(p.id)}/check`, { method: 'POST', body: {} })
    notify(`${p.name} 检测完成：${v.status}`)
    await load()
  } catch (e) { notifyError(e) } finally { busyId.value = '' }
}

async function checkAll() {
  for (const p of plugins.value.filter((x) => x.enabled)) {
    try { await api(`/plugins/${encodeURIComponent(p.id)}/check`, { method: 'POST', body: {} }) } catch { /* 单个失败继续 */ }
  }
  notify('全部插件检测完成')
  await load()
}

function openHealth(p) {
  healthTarget.value = p
  healthForm.value = {
    enabled: p.health?.enabled ?? true,
    intervalSecs: p.health?.intervalSecs ?? 300,
    timeoutSecs: p.health?.timeoutSecs ?? 8,
    failThreshold: p.health?.failThreshold ?? 2,
    probeMode: p.health?.probeMode || 'ping',
  }
}
async function saveHealth() {
  saving.value = true
  try {
    await api(`/plugins/${encodeURIComponent(healthTarget.value.id)}/health`, {
      method: 'PUT',
      body: { ...healthForm.value, intervalSecs: Number(healthForm.value.intervalSecs), timeoutSecs: Number(healthForm.value.timeoutSecs), failThreshold: Number(healthForm.value.failThreshold) },
    })
    notify('定时检测配置已保存，调度器已重建', { title: healthTarget.value.name })
    healthTarget.value = null
    await load()
  } catch (e) { notifyError(e) } finally { saving.value = false }
}
</script>

<template>
  <div>
    <PageHeader
      title="插件列表"
      description="每个插件把一家第三方上传接口转换成统一标准输出。权重越大越优先被调度，并可限定承接的文件类型；上传时先判定文件类型，再在同类型插件中按权重选择。"
    >
      <template #actions>
        <Button variant="outline" @click="checkAll"><Radar />全部检测</Button>
        <Button variant="ghost" size="icon" aria-label="刷新" @click="load"><RefreshCw /></Button>
        <Button variant="ghost" @click="go('docs', 'plugin-development')"><BookOpen />开发文档</Button>
        <Button @click="openCreate"><Plus />创建插件</Button>
      </template>
    </PageHeader>

    <div v-if="unhealthyCount" class="mb-5 flex flex-col gap-3 rounded-xl border border-destructive/25 bg-destructive/[0.05] p-3.5 sm:flex-row sm:items-center">
      <TriangleAlert class="mt-0.5 size-4 shrink-0 text-destructive" />
      <div class="min-w-0 flex-1">
        <p class="text-[13px] font-medium text-destructive">{{ unhealthyCount }} 个插件异常，已生成告警</p>
        <p class="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">
          常见原因：临时 Bearer Token 过期、必填参数缺失、上游服务不可达。进入插件详情更新参数后执行调试即可恢复。
        </p>
      </div>
      <Button size="sm" variant="outline" class="shrink-0 border-destructive/30 text-destructive hover:bg-destructive/10" @click="go('alerts')">查看告警</Button>
    </div>

    <Card class="overflow-hidden">
      <div class="flex flex-col gap-3 p-3.5 sm:flex-row sm:items-center sm:justify-between sm:px-4">
        <div class="relative w-full sm:max-w-xs">
          <Search class="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input v-model="query" placeholder="搜索插件名称 / ID / 类型" class="h-8 pl-8 text-xs" />
        </div>
        <div class="flex items-center gap-3 text-[11.5px] text-muted-foreground">
          <span class="inline-flex items-center gap-1.5"><Activity class="size-3.5" />{{ plugins.length }} 个插件</span>
          <span class="inline-flex items-center gap-1.5"><SlidersHorizontal class="size-3.5" />按权重降序</span>
          <span class="hidden items-center gap-1.5 sm:inline-flex"><FileType2 class="size-3.5" />{{ customCount }} 个自定义</span>
        </div>
      </div>

      <div v-if="loading" class="space-y-2 border-t p-4">
        <Skeleton v-for="i in 4" :key="i" class="h-12" />
      </div>

      <!-- 桌面表格 -->
      <div v-else class="hidden overflow-x-auto border-t md:block">
        <table class="data-table">
          <thead>
            <tr>
              <th>插件</th><th>状态</th><th class="w-32">调度权重</th><th>文件类型</th>
              <th>定时检测</th><th class="hidden xl:table-cell">提示</th><th class="w-40"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in filtered" :key="p.id" :class="!p.enabled && 'opacity-60'">
              <td class="cursor-pointer" @click="go('plugin', p.id)">
                <div class="flex items-center gap-2 font-medium">
                  {{ p.name }}
                  <Badge v-if="!p.enabled" variant="outline">停用</Badge>
                </div>
                <div class="mt-0.5 font-mono text-[10.5px] text-muted-foreground">{{ p.id }} · v{{ p.version }} · {{ p.kind }}</div>
              </td>
              <td><StatusBadge :status="p.status" /></td>
              <td>
                <div class="inline-flex h-7 items-stretch overflow-hidden rounded-md border bg-background">
                  <button class="grid w-6 place-items-center text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:opacity-40" :disabled="busyId === p.id" title="降低权重" @click="adjustPriority(p, -10)">
                    <Minus class="size-3" />
                  </button>
                  <input
                    class="w-12 border-x bg-transparent text-center font-mono text-[11.5px] outline-none [appearance:textfield] focus:bg-accent/50 [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
                    type="number" min="0" max="9999" step="10"
                    :value="weightDraft[p.id]" :disabled="busyId === p.id"
                    @input="weightDraft[p.id] = $event.target.value"
                    @keyup.enter="savePriority(p)" @blur="savePriority(p)"
                  />
                  <button class="grid w-6 place-items-center text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:opacity-40" :disabled="busyId === p.id" title="提高权重" @click="adjustPriority(p, 10)">
                    <Plus class="size-3" />
                  </button>
                </div>
                <p class="mt-1 text-[10px] text-muted-foreground">越大越优先</p>
              </td>
              <td>
                <div class="flex max-w-[10rem] flex-wrap gap-1">
                  <Badge v-for="t in typeLabel(p)" :key="t" :variant="t === '全部类型' ? 'primary' : 'outline'">{{ t }}</Badge>
                </div>
              </td>
              <td>
                <div class="font-mono text-[11px]">{{ p.health?.enabled ? p.health.intervalSecs + 's / ' + p.health.failThreshold + ' 次' : '已关闭' }}</div>
                <div class="mt-0.5 text-[10.5px] text-muted-foreground">
                  {{ relTime(p.lastCheckedAt) }}<span v-if="p.latencyMs"> · {{ p.latencyMs }}ms</span>
                  <button class="ml-1.5 text-primary hover:underline" @click="openHealth(p)">设置</button>
                </div>
              </td>
              <td class="hidden max-w-[14rem] text-[11.5px] xl:table-cell">
                <span v-if="p.warning" class="text-warning">{{ truncate(p.warning, 56) }}</span>
                <span v-else class="text-muted-foreground">{{ truncate(p.statusMessage, 56) || '—' }}</span>
              </td>
              <td>
                <div class="flex items-center justify-end gap-1">
                  <Button size="sm" variant="outline" :disabled="busyId === p.id" @click="check(p)">检测</Button>
                  <Button size="sm" variant="ghost" @click="go('plugin', p.id)">配置</Button>
                  <Button size="sm" variant="ghost" :disabled="busyId === p.id" :title="p.enabled ? '停用' : '启用'" @click="toggle(p)">
                    <Power :class="p.enabled ? 'text-muted-foreground' : 'text-success'" />
                  </Button>
                </div>
              </td>
            </tr>
            <tr v-if="!filtered.length">
              <td colspan="7">
                <Empty :title="query ? '没有匹配的插件' : '没有插件'" :description="query ? '换个关键词试试。' : '内置插件类型会在启动后自动注册。'" />
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 移动卡片 -->
      <div v-if="!loading" class="space-y-2.5 border-t p-3 md:hidden">
        <div v-for="p in filtered" :key="p.id" class="rounded-lg border bg-background p-3" :class="!p.enabled && 'opacity-65'">
          <div class="flex items-start gap-2">
            <button class="min-w-0 flex-1 text-left" @click="go('plugin', p.id)">
              <p class="truncate text-[13px] font-medium">{{ p.name }}</p>
              <p class="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">{{ p.id }} · v{{ p.version }}</p>
            </button>
            <StatusBadge :status="p.status" />
          </div>
          <div class="mt-2.5 flex flex-wrap items-center gap-1.5">
            <Badge v-for="t in typeLabel(p)" :key="t" :variant="t === '全部类型' ? 'primary' : 'outline'">{{ t }}</Badge>
            <span class="ml-auto font-mono text-[10.5px] text-muted-foreground">权重 {{ p.priority }}</span>
          </div>
          <p v-if="p.warning" class="mt-2 line-clamp-2 text-[11px] leading-relaxed text-warning">{{ p.warning }}</p>
          <p v-else-if="p.statusMessage" class="mt-2 line-clamp-2 text-[11px] leading-relaxed text-muted-foreground">{{ p.statusMessage }}</p>
          <div class="mt-2.5 flex items-center gap-1.5 border-t pt-2.5">
            <Button size="sm" variant="outline" class="flex-1" :disabled="busyId === p.id" @click="check(p)">检测</Button>
            <Button size="sm" variant="secondary" class="flex-1" @click="go('plugin', p.id)">配置</Button>
            <Button size="sm" variant="ghost" @click="openHealth(p)"><Settings2 /></Button>
            <Button size="sm" variant="ghost" :disabled="busyId === p.id" @click="toggle(p)"><Power /></Button>
          </div>
        </div>
        <Empty v-if="!filtered.length" :title="query ? '没有匹配的插件' : '没有插件'" :description="query ? '换个关键词试试。' : '内置插件类型会在启动后自动注册。'" />
      </div>
    </Card>

    <Card class="mt-5">
      <div class="p-4 sm:p-5">
        <div class="flex items-center gap-2">
          <SlidersHorizontal class="size-4 text-muted-foreground" />
          <h2 class="text-sm font-semibold">调度与参数规则</h2>
        </div>
        <p class="mt-2 text-[12.5px] leading-relaxed text-muted-foreground">
          参数表单由插件清单的 <code class="rounded border bg-muted px-1 py-px font-mono text-[11px]">Params</code> 自动生成，按类型渲染为文本框、密码框、下拉框或开关。
          <strong class="text-foreground">调度权重</strong>决定同类型插件被选中的优先级；<strong class="text-foreground">文件类型</strong>限制插件只承接图片、文档、压缩包等指定类别，不勾选表示全部。
          标记为临时参数的字段（如 WorkBuddy 的 Bearer Token）需要设置过期时间，系统会在到期前 24 小时提醒。
        </p>
      </div>
    </Card>

    <!-- 创建插件 -->
    <Sheet v-model:open="createOpen" title="创建 Go 插件" description="生成一个插件文件夹（plugin.json + main.go）并立即编译。之后可在详情页直接编辑 Go 源码，保存即生效。" class="max-w-2xl">
      <div class="grid gap-4 sm:grid-cols-2">
        <Field label="插件 ID" required hint="全局唯一，小写字母开头，2-40 位，仅含小写字母/数字/下划线/连字符">
          <Input v-model="createForm.id" placeholder="例如 my-uploader" />
        </Field>
        <Field label="显示名称"><Input v-model="createForm.name" placeholder="留空则使用插件 ID" /></Field>
        <Field label="版本号"><Input v-model="createForm.version" placeholder="1.0.0" /></Field>
        <Field label="起始模板" hint="模板决定 main.go 的初始实现，之后可以随意改写">
          <Select v-model="createForm.template">
            <option value="blank">空白骨架（从零实现任意协议）</option>
            <option value="http-multipart">HTTP 表单上传（multipart 转发）</option>
          </Select>
        </Field>
        <Field label="说明" class="sm:col-span-2">
          <textarea v-model="createForm.description" placeholder="这个插件对接哪家服务" class="flex min-h-20 w-full resize-y rounded-md border border-input bg-background px-3 py-2 text-[13px] shadow-xs placeholder:text-muted-foreground/70 focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25" />
        </Field>
        <Field label="调度权重" hint="数值越大越优先被选中；同权重时健康插件优先">
          <Input v-model.number="createForm.priority" type="number" min="0" max="9999" step="10" />
        </Field>
        <div class="flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5">
          <div>
            <p class="text-[12.5px] font-medium">立即启用</p>
            <p class="text-[11px] text-muted-foreground">创建后加入可用插件池</p>
          </div>
          <Switch v-model="createForm.enabled" />
        </div>
        <Field label="可承接文件类型" hint="不勾选表示全部类型；上传时按文件类型匹配插件，再按权重从高到低选择。" class="sm:col-span-2">
          <div class="flex flex-wrap gap-1.5">
            <button
              v-for="[key, label] in fileTypeOptions" :key="key" type="button"
              class="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs transition-colors"
              :class="createForm.fileTypes.includes(key)
                ? 'border-primary/40 bg-primary/[0.08] font-medium text-primary'
                : 'bg-background text-muted-foreground hover:border-foreground/25 hover:text-foreground'"
              @click="toggleCreateType(key)"
            >
              <CheckCheck v-if="createForm.fileTypes.includes(key)" class="size-3" />
              {{ label }}
            </button>
          </div>
        </Field>
      </div>

      <div class="mt-5 rounded-lg border border-primary/20 bg-primary/[0.04] p-3.5 text-[11.5px] leading-relaxed text-muted-foreground">
        插件是真正的 Go 程序：可以发起任意次请求、使用 PUT / 分片上传、做加解密与签名，不受模板限制。
        创建后在详情页「插件源码」里编辑并保存即自动编译生效，也可以用 MCP 的
        <code class="rounded border bg-background px-1 py-px font-mono text-[10.5px]">plugin_file_write</code> /
        <code class="rounded border bg-background px-1 py-px font-mono text-[10.5px]">plugin_build</code> 由 Agent 全程开发。当前已有 {{ customCount }} 个自定义插件。
      </div>

      <template #footer>
        <Button variant="outline" @click="createOpen = false">取消</Button>
        <Button :disabled="creating" @click="submitCreate">{{ creating ? '创建中…' : '创建插件' }}</Button>
      </template>
    </Sheet>

    <!-- 定时检测设置 -->
    <Sheet
      :open="!!healthTarget" :title="`定时检测 · ${healthTarget?.name || ''}`"
      description="每个插件独立配置，保存后调度器会重建该插件的检测任务。"
      @update:open="(v) => { if (!v) healthTarget = null }"
    >
      <div class="grid gap-4 sm:grid-cols-2">
        <div class="flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5 sm:col-span-2">
          <div>
            <p class="text-[12.5px] font-medium">启用定时检测</p>
            <p class="text-[11px] text-muted-foreground">按下方间隔自动探活</p>
          </div>
          <Switch v-model="healthForm.enabled" />
        </div>
        <Field label="检测间隔（秒）" required hint="最小 10 秒，避免对第三方造成压力">
          <Input v-model.number="healthForm.intervalSecs" type="number" min="10" />
        </Field>
        <Field label="单次超时（秒）"><Input v-model.number="healthForm.timeoutSecs" type="number" min="1" max="120" /></Field>
        <Field label="失败阈值" hint="连续失败达到该次数后标记为异常并生成告警">
          <Input v-model.number="healthForm.failThreshold" type="number" min="1" />
        </Field>
        <Field label="探活模式">
          <Select v-model="healthForm.probeMode">
            <option value="ping">ping — 轻量连通性探测（无副作用）</option>
            <option value="upload-sample">upload-sample — 样例上传</option>
          </Select>
        </Field>
      </div>
      <p class="mt-4 text-[11.5px] leading-relaxed text-muted-foreground">
        检测失败会累加计数；达到阈值后插件状态变为「异常」，列表行标红并生成一条告警。检测恢复成功时，未确认告警会自动标记为「系统已恢复」。
      </p>
      <template #footer>
        <Button variant="outline" @click="healthTarget = null">取消</Button>
        <Button :disabled="saving" @click="saveHealth">{{ saving ? '保存中…' : '保存设置' }}</Button>
      </template>
    </Sheet>
  </div>
</template>
