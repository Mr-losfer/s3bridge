<script setup>
import { ref, computed, onMounted } from 'vue'
import {
  Play, Radar, Save, Eye, EyeOff, RotateCw, Check, Trash2,
  SlidersHorizontal, Code2, History, BookOpen, Timer, FileJson, ImageOff,
} from 'lucide-vue-next'
import { api } from '../api'
import { route, go } from '../router'
import { notify, notifyError } from '../toast'
import { relTime, fmtTime, renderMarkdown, truncate, categoryLabels, categoryLabel } from '../utils'
import PageHeader from '../components/PageHeader.vue'
import StatusBadge from '../components/StatusBadge.vue'
import CodeBlock from '../components/CodeBlock.vue'
import PluginSource from '../components/PluginSource.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import CardHeader from '../components/ui/CardHeader.vue'
import CardTitle from '../components/ui/CardTitle.vue'
import CardDescription from '../components/ui/CardDescription.vue'
import CardContent from '../components/ui/CardContent.vue'
import Input from '../components/ui/Input.vue'
import Field from '../components/ui/Field.vue'
import Select from '../components/ui/Select.vue'
import Switch from '../components/ui/Switch.vue'
import Tabs from '../components/ui/Tabs.vue'
import TabContent from '../components/ui/TabContent.vue'
import Empty from '../components/ui/Empty.vue'

const id = computed(() => route.value.id)
const view = ref(null)
const form = ref({})
const showSecret = ref({})
const tab = ref('params')
const loading = ref(true)
const saving = ref(false)
const debugReport = ref(null)
const changes = ref([])
const patchJSON = ref('{\n  "priority": 120\n}')
const fullJSON = ref('{\n  "name": "",\n  "enabled": true,\n  "priority": 100,\n  "params": {},\n  "health": { "enabled": true, "intervalSecs": 300, "timeoutSecs": 8, "failThreshold": 2, "probeMode": "ping" }\n}')
const updateSpec = ref({ version: '', syncParams: true, resetToDefault: false, note: '' })
const showManifest = ref(false)

// 图片有效性巡检：跟随全局（0）或为该插件单独设定周期。
const validityHours = ref(0)
const validitySaving = ref(false)

const fields = computed(() => (view.value?.manifest?.params || []))
const fileTypeOptions = Object.entries(categoryLabels)
const schedule = ref({ priority: 100, fileTypes: [] })
const scheduleSaving = ref(false)
const currentTypes = computed(() => {
  const list = view.value?.fileTypes || []
  return list.length ? list.map(categoryLabel) : ['全部类型']
})
const volatileField = computed(() => fields.value.find((f) => f.volatile && f.expiresField))
const expiryValue = computed(() => form.value[volatileField.value?.expiresField] || '')
const expiryHint = computed(() => {
  const v = String(expiryValue.value).trim()
  if (!v) return '未设置过期时间'
  const n = Number(v)
  const t = Number.isFinite(n) && n > 0 ? new Date(n > 1e12 ? n : n * 1000) : new Date(v)
  if (isNaN(t.getTime())) return '时间格式无法识别'
  const left = t.getTime() - Date.now()
  if (left <= 0) return '已于 ' + t.toLocaleString('zh-CN') + ' 过期'
  if (left < 86400000) return '将在 ' + Math.round(left / 3600000) + ' 小时后过期'
  return '有效期至 ' + t.toLocaleString('zh-CN')
})

const tabItems = [
  { value: 'params', label: '参数配置', icon: SlidersHorizontal },
  { value: 'debug', label: '调试与检测', icon: Radar },
  { value: 'source', label: '插件源码', icon: Code2 },
  { value: 'advanced', label: '差量 / 全量', icon: FileJson },
  { value: 'changes', label: '变更记录', icon: History },
  { value: 'docs', label: '插件文档', icon: BookOpen },
]

function initForm(v) {
  const f = {}
  for (const field of v.manifest?.params || []) {
    if (field.sensitive) f[field.key] = ''
    else {
      const cur = v.params?.[field.key]
      f[field.key] = cur === undefined || cur === null ? (field.default || '') : String(cur)
    }
  }
  form.value = f
}

async function load() {
  loading.value = true
  try {
    const v = await api('/plugins/' + encodeURIComponent(id.value))
    view.value = v
    initForm(v)
    schedule.value = { priority: v.priority ?? 100, fileTypes: [...(v.fileTypes || [])] }
    validityHours.value = v.manifest?.validity?.intervalHours || 0
  } catch (e) { notifyError(e, '加载插件失败') }
  finally { loading.value = false }
}
onMounted(load)

function toggleScheduleType(key) {
  const list = schedule.value.fileTypes
  const i = list.indexOf(key)
  if (i >= 0) list.splice(i, 1)
  else list.push(key)
}
async function saveSchedule() {
  scheduleSaving.value = true
  try {
    await api(`/plugins/${encodeURIComponent(id.value)}`, {
      method: 'PATCH',
      body: { priority: Number(schedule.value.priority) || 0, fileTypes: schedule.value.fileTypes },
    })
    notify('调度设置已保存：权重越大越优先，文件类型决定可承接范围', { title: '调度已更新' })
    await load()
  } catch (e) { notifyError(e, '保存调度设置失败') } finally { scheduleSaving.value = false }
}

async function saveParams() {
  saving.value = true
  try {
    const params = {}
    for (const f of fields.value) {
      const val = form.value[f.key]
      if (val === undefined) continue
      const str = String(val)
      if (f.sensitive && str === '') continue // 敏感字段留空 = 保持原值
      params[f.key] = str
    }
    await api(`/plugins/${encodeURIComponent(id.value)}/params`, { method: 'POST', body: { params } })
    notify('参数已保存并加密落盘', { title: view.value.name })
    await load()
  } catch (e) { notifyError(e, '保存失败') } finally { saving.value = false }
}

async function parseToken() {
  const auth = form.value.authorization || form.value.token || ''
  if (!auth.trim()) { notify('请先粘贴 Bearer Token 再解析', { title: '需要 Token', type: 'error' }); return }
  try {
    const v = await api(`/plugins/${encodeURIComponent(id.value)}/parse-token`, { method: 'POST', body: { authorization: auth } })
    if (volatileField.value?.expiresField) form.value[volatileField.value.expiresField] = String(v.unix)
    if (v.xUserId && form.value.x_user_id !== undefined && !form.value.x_user_id) form.value.x_user_id = v.xUserId
    notify(`解析成功：过期 ${v.tokenExpiresAt}${v.xUserId ? '，sub=' + v.xUserId : ''}`, { title: 'Token 已解析' })
  } catch (e) { notifyError(e, '解析失败') }
}

async function runDebug() {
  try { debugReport.value = await api(`/plugins/${encodeURIComponent(id.value)}/debug`, { method: 'POST', body: {} }) }
  catch (e) { notifyError(e, '调试失败') }
}
async function runCheck() {
  try {
    const v = await api(`/plugins/${encodeURIComponent(id.value)}/check`, { method: 'POST', body: {} })
    notify('当前状态：' + v.status, { title: '检测完成' })
    await load()
  } catch (e) { notifyError(e) }
}
async function loadChanges() {
  try { changes.value = (await api(`/plugins/${encodeURIComponent(id.value)}/changes?limit=50`)) || [] }
  catch (e) { notifyError(e) }
}
async function applyPatch() {
  try {
    await api(`/plugins/${encodeURIComponent(id.value)}`, { method: 'PATCH', body: JSON.parse(patchJSON.value) })
    notify('差量修改已应用', { title: 'plugin_patch' }); await load()
  } catch (e) { notifyError(e, '差量修改失败') }
}
async function applyReplace() {
  if (!window.confirm('全量替换会重置未提供的参数为默认值，确认继续？')) return
  try {
    await api(`/plugins/${encodeURIComponent(id.value)}`, { method: 'PUT', body: JSON.parse(fullJSON.value) })
    notify('全量配置已替换', { title: 'plugin_replace' }); await load()
  } catch (e) { notifyError(e, '全量修改失败') }
}
async function applyUpdate() {
  try {
    await api(`/plugins/${encodeURIComponent(id.value)}/update`, { method: 'POST', body: updateSpec.value })
    notify('插件已更新', { title: 'plugin_update' }); await load(); await loadChanges()
  } catch (e) { notifyError(e, '更新失败') }
}

async function saveValidity() {
  if (!view.value?.manifest) return
  validitySaving.value = true
  try {
    const manifest = JSON.parse(JSON.stringify(view.value.manifest))
    manifest.validity = { ...(manifest.validity || {}), intervalHours: Number(validityHours.value) || 0 }
    await api(`/plugins/${encodeURIComponent(id.value)}/manifest`, { method: 'PUT', body: { manifest } })
    notify(
      Number(validityHours.value) > 0
        ? `该插件产出的图片每 ${validityHours.value} 小时巡检一次`
        : '已改为跟随系统设置的全局巡检间隔',
      { title: '图片有效性' },
    )
    await load()
  } catch (e) { notifyError(e, '保存失败') } finally { validitySaving.value = false }
}

async function deletePlugin() {
  if (!window.confirm(`确认删除插件「${view.value?.name || id.value}」？该操作不可撤销，变更记录会保留。`)) return
  try {
    await api('/plugins/' + encodeURIComponent(id.value), { method: 'DELETE' })
    notify('自定义插件已删除')
    go('plugins')
  } catch (e) { notifyError(e, '删除失败') }
}

function switchTab(t) {
  tab.value = t
  if (t === 'changes') loadChanges()
}
</script>

<template>
  <div>
    <PageHeader
      :title="view?.name || id"
      :description="view?.description || '插件详情与参数配置'"
      back="plugins"
      back-label="插件列表"
    >
      <template #actions>
        <StatusBadge :status="view?.status || 'unknown'" />
        <Badge variant="primary">权重 {{ view?.priority ?? '—' }}</Badge>
        <Badge variant="outline">{{ currentTypes.join(' / ') }}</Badge>
        <Badge variant="outline">v{{ view?.version }}</Badge>
        <Badge variant="outline">rev {{ view?.revision }}</Badge>
      </template>
    </PageHeader>

    <div v-if="view?.warning" class="mb-4 flex items-start gap-2.5 rounded-xl border border-warning/25 bg-warning/[0.07] p-3.5">
      <Timer class="mt-0.5 size-4 shrink-0 text-warning" />
      <div>
        <p class="text-[12.5px] font-medium text-warning">临时参数提醒</p>
        <p class="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">{{ view.warning }}</p>
      </div>
    </div>
    <div v-if="view?.statusMessage" class="mb-4 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border bg-card px-3.5 py-2.5 text-[12px]">
      <span class="text-[10.5px] text-muted-foreground">最近状态</span>
      <span>{{ view.statusMessage }}</span>
      <span class="text-[10.5px] text-muted-foreground">· {{ relTime(view.lastCheckedAt) }} · {{ view.latencyMs }}ms</span>
    </div>

    <Tabs :model-value="tab" :items="tabItems" @update:model-value="switchTab">
      <!-- 参数配置 -->
      <TabContent value="params">
        <div class="grid gap-4">
          <Card>
            <CardHeader class="pb-4">
              <div class="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <CardTitle>调度设置</CardTitle>
                  <CardDescription class="mt-1">上传时先判定文件类型，再在同类型插件中按权重从高到低选择；同权重时更健康的插件优先。</CardDescription>
                </div>
                <Badge variant="primary">{{ currentTypes.join(' / ') }}</Badge>
              </div>
            </CardHeader>
            <CardContent class="grid gap-4 border-t pt-4 sm:grid-cols-[10rem_1fr]">
              <Field label="调度权重" hint="数值越大越优先；列表按权重降序即为实际调度顺序">
                <Input v-model.number="schedule.priority" type="number" min="0" max="9999" step="10" />
              </Field>
              <Field label="可承接文件类型" hint="不勾选表示全部类型；例如只勾选「图片」后，文档与压缩包会自动路由到其他插件。">
                <div class="flex flex-wrap gap-1.5">
                  <button
                    v-for="[key, label] in fileTypeOptions" :key="key" type="button"
                    class="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs transition-colors"
                    :class="schedule.fileTypes.includes(key)
                      ? 'border-primary/40 bg-primary/[0.08] font-medium text-primary'
                      : 'bg-background text-muted-foreground hover:border-foreground/25 hover:text-foreground'"
                    @click="toggleScheduleType(key)"
                  >
                    <Check v-if="schedule.fileTypes.includes(key)" class="size-3" />
                    {{ label }}
                  </button>
                </div>
              </Field>
              <div class="flex flex-wrap items-center gap-2 sm:col-span-2">
                <Button size="sm" :disabled="scheduleSaving" @click="saveSchedule">
                  <Save />{{ scheduleSaving ? '保存中…' : '保存调度设置' }}
                </Button>
                <span class="text-[11px] text-muted-foreground">也可用 MCP 的 plugin_patch 设置 priority / fileTypes</span>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader class="pb-4">
              <div class="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <CardTitle>图片有效性巡检</CardTitle>
                  <CardDescription class="mt-1">
                    该插件产出并归档到本站的图片，会按周期用 HEAD 请求确认「仍能返回图片」，只读响应头、不下载正文。
                  </CardDescription>
                </div>
                <Badge variant="outline">{{ validityHours > 0 ? validityHours + ' 小时' : '跟随系统' }}</Badge>
              </div>
            </CardHeader>
            <CardContent class="grid gap-4 border-t pt-4 sm:grid-cols-[12rem_1fr]">
              <Field label="巡检间隔（小时）" hint="0 表示跟随系统设置的全局间隔；连续失败达到阈值才标记失效并告警。">
                <div class="flex items-center gap-1.5">
                  <Input v-model.number="validityHours" type="number" min="0" max="720" step="1" />
                  <ImageOff class="size-3.5 shrink-0 text-muted-foreground" />
                </div>
              </Field>
              <div class="flex flex-wrap items-center gap-2 sm:items-end">
                <Button size="sm" :disabled="validitySaving" @click="saveValidity">
                  <Save />{{ validitySaving ? '保存中…' : '保存巡检间隔' }}
                </Button>
                <span class="text-[11px] text-muted-foreground">默认 72 小时（3 天）一次；也可用 MCP 的 plugin_manifest_update 修改 validity.intervalHours</span>
              </div>
            </CardContent>
          </Card>

          <Card v-for="g in (view?.grouped || [])" :key="g.name">
            <CardHeader class="pb-4">
              <CardTitle>{{ g.name || '通用' }}</CardTitle>
              <CardDescription>清单定义的配置项，按类型自动渲染</CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4 border-t pt-4 sm:grid-cols-2">
              <Field
                v-for="f in g.fields" :key="f.key"
                :label="f.label" :required="f.required" :hint="f.help"
                :class="f.type === 'textarea' && 'sm:col-span-2'"
              >
                <template #default>
                  <div class="flex items-center gap-1.5">
                    <Badge v-if="f.volatile" variant="warning">临时</Badge>
                    <Badge v-if="f.sensitive" variant="outline">加密</Badge>
                    <span class="font-mono text-[10px] text-muted-foreground">{{ f.key }}</span>
                  </div>
                  <div v-if="f.type === 'bool'" class="flex items-center gap-2 py-1.5">
                    <Switch :model-value="form[f.key] === 'true'" @update:model-value="form[f.key] = $event ? 'true' : 'false'" />
                    <span class="text-xs text-muted-foreground">{{ f.help || '启用' }}</span>
                  </div>
                  <Select v-else-if="f.type === 'select'" v-model="form[f.key]">
                    <option v-for="o in f.options" :key="o.value" :value="o.value">{{ o.label }}</option>
                  </Select>
                  <textarea
                    v-else-if="f.type === 'textarea'" v-model="form[f.key]" :placeholder="f.placeholder || f.help"
                    class="flex min-h-24 w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-[12px] leading-relaxed shadow-xs placeholder:text-muted-foreground/70 focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25"
                  />
                  <div v-else class="flex gap-1.5">
                    <Input
                      v-model="form[f.key]"
                      :type="f.sensitive && !showSecret[f.key] ? 'password' : (f.type === 'number' || f.type === 'duration' ? 'number' : 'text')"
                      :placeholder="f.sensitive && view?.params?.[f.key] ? '已保存：' + view.params[f.key] + '（留空表示不修改）' : (f.placeholder || f.help || '')"
                      :min="f.type === 'number' ? 0 : undefined"
                    />
                    <Button v-if="f.sensitive" variant="outline" size="icon" :aria-label="showSecret[f.key] ? '隐藏' : '显示'" @click="showSecret[f.key] = !showSecret[f.key]">
                      <component :is="showSecret[f.key] ? EyeOff : Eye" />
                    </Button>
                  </div>
                  <p v-if="volatileField && f.key === volatileField.expiresField" class="mt-1 flex items-center gap-1 text-[11.5px] text-warning">
                    <Timer class="size-3.5" />{{ expiryHint }}
                  </p>
                </template>
              </Field>
            </CardContent>
          </Card>

          <div v-if="volatileField" class="flex flex-col gap-3 rounded-xl border border-warning/25 bg-warning/[0.06] p-3.5 sm:flex-row sm:items-center sm:justify-between">
            <div class="min-w-0">
              <p class="text-[12.5px] font-medium">临时凭据助手</p>
              <p class="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">
                粘贴 Bearer Token 后一键解析 exp / sub，自动回填过期时间与 X-User-Id。{{ expiryHint }}
              </p>
            </div>
            <Button size="sm" variant="outline" class="shrink-0" @click="parseToken"><RotateCw />从 Token 解析</Button>
          </div>

          <div v-if="fields.length" class="flex flex-wrap items-center gap-2">
            <Button :disabled="saving" @click="saveParams"><Save />{{ saving ? '保存中…' : '保存参数配置' }}</Button>
            <Button variant="outline" @click="load"><RotateCw />重新载入</Button>
            <span class="text-[11.5px] text-muted-foreground">敏感字段留空表示保持原值；服务端以 AES-256-GCM 加密存储。</span>
          </div>
        </div>
      </TabContent>

      <!-- 调试与检测 -->
      <TabContent value="debug">
        <div class="flex flex-wrap items-center gap-2">
          <Button @click="runDebug"><Play />运行插件调试</Button>
          <Button variant="outline" @click="runCheck"><Radar />立即健康检测</Button>
        </div>
        <p class="mt-3 text-[11.5px] leading-relaxed text-muted-foreground">
          调试不会上传真实文件：依次检查参数合法性、临时凭据有效期与上游连通性。
        </p>

        <Card v-if="debugReport" class="mt-4 overflow-hidden">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-[13px] font-medium">诊断结果</span>
              <span class="font-mono text-[10.5px] text-muted-foreground">v{{ debugReport.version }} · {{ fmtTime(debugReport.checkedAt) }}</span>
            </div>
            <Badge :variant="debugReport.healthy ? 'success' : 'destructive'">{{ debugReport.healthy ? 'PASS' : 'FAIL' }}</Badge>
          </div>
          <div class="overflow-x-auto">
            <table class="data-table">
              <thead><tr><th>步骤</th><th class="w-20">结论</th><th>详情</th><th class="w-20 text-right">耗时</th></tr></thead>
              <tbody>
                <tr v-for="(s, i) in debugReport.steps" :key="i">
                  <td class="font-medium">{{ s.name }}</td>
                  <td>
                    <Badge :variant="s.status === 'pass' ? 'success' : s.status === 'warn' ? 'warning' : 'destructive'">{{ s.status.toUpperCase() }}</Badge>
                  </td>
                  <td class="max-w-[34rem] text-[11.5px] leading-relaxed text-muted-foreground">
                    <span class="break-words">{{ s.detail }}</span>
                  </td>
                  <td class="text-right font-mono text-[11px]">{{ s.durationMs }}ms</td>
                </tr>
              </tbody>
            </table>
          </div>
        </Card>
        <Card v-else class="mt-4">
          <Empty title="尚未运行调试" description="点击上方按钮，三步诊断插件配置与上游链路。">
            <Button size="sm" @click="runDebug"><Play />开始诊断</Button>
          </Empty>
        </Card>
      </TabContent>

      <!-- 插件源码：真正的 Go 文件夹 -->
      <TabContent value="source">
        <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <h2 class="text-sm font-semibold">插件源码</h2>
            <p class="mt-1 max-w-3xl text-[11.5px] leading-relaxed text-muted-foreground">
              插件是一个 Go 文件夹：<code class="rounded border bg-muted px-1 py-px font-mono text-[10.5px]">plugin.json</code> 声明参数与元信息，
              <code class="rounded border bg-muted px-1 py-px font-mono text-[10.5px]">*.go</code> 实现上传逻辑。保存任意 .go 文件即自动加载并生效，
              与 MCP 的 plugin_files / plugin_file_write / plugin_build 完全同源。
            </p>
          </div>
          <div class="flex items-center gap-1.5">
            <Badge :variant="view?.editable ? 'success' : 'warning'">{{ view?.editable ? '可编辑' : '内置 · 源码只读' }}</Badge>
            <Badge :variant="view?.built ? 'success' : 'outline'">{{ view?.built ? '已加载' : '未加载' }}</Badge>
            <Badge variant="outline">rev {{ view?.revision }}</Badge>
          </div>
        </div>
        <PluginSource
          :id="id" :editable="!!view?.editable" :built="!!view?.built"
          :build-error="view?.buildError || ''"
          @changed="load"
        />
        <div class="mt-4 flex flex-wrap items-center gap-2">
          <Button v-if="view?.custom" variant="destructive" size="sm" @click="deletePlugin"><Trash2 />删除插件</Button>
          <span class="text-[11px] text-muted-foreground">内置插件不可删除，只能停用；自定义插件删除后变更记录仍保留。</span>
        </div>
      </TabContent>

      <!-- 差量 / 全量 / 更新 -->
      <TabContent value="advanced">
        <p class="mb-4 max-w-3xl text-[11.5px] leading-relaxed text-muted-foreground">
          与 MCP 的 plugin_patch / plugin_replace / plugin_update 工具等价。所有写入都会记录到变更审计。
        </p>
        <div class="grid gap-4 lg:grid-cols-3">
          <Card>
            <CardHeader class="pb-4"><CardTitle>差量修改</CardTitle><CardDescription>plugin_patch · 只覆盖传入字段，params 按键合并</CardDescription></CardHeader>
            <CardContent class="border-t pt-4">
              <Field label="Patch JSON">
                <textarea v-model="patchJSON" class="min-h-36 w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-[11.5px] shadow-xs focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25" />
              </Field>
              <Button class="mt-3 w-full" @click="applyPatch">应用差量修改</Button>
            </CardContent>
          </Card>
          <Card>
            <CardHeader class="pb-4"><CardTitle>全量修改</CardTitle><CardDescription>plugin_replace · 未提供的参数重置为默认值</CardDescription></CardHeader>
            <CardContent class="border-t pt-4">
              <Field label="Full JSON">
                <textarea v-model="fullJSON" class="min-h-36 w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-[11.5px] shadow-xs focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25" />
              </Field>
              <Button variant="destructive" class="mt-3 w-full" @click="applyReplace">全量替换配置</Button>
            </CardContent>
          </Card>
          <Card>
            <CardHeader class="pb-4"><CardTitle>插件更新</CardTitle><CardDescription>plugin_update · 升级版本并同步清单新增参数</CardDescription></CardHeader>
            <CardContent class="space-y-3 border-t pt-4">
              <Field label="目标版本"><Input v-model="updateSpec.version" placeholder="留空使用内置版本" /></Field>
              <label class="flex items-center gap-2 text-[12px]"><Switch v-model="updateSpec.syncParams" />补齐清单中新增参数</label>
              <label class="flex items-center gap-2 text-[12px]"><Switch v-model="updateSpec.resetToDefault" />重置全部参数为默认值</label>
              <Field label="更新说明"><Input v-model="updateSpec.note" placeholder="可选" /></Field>
              <Button class="w-full" @click="applyUpdate">执行更新</Button>
            </CardContent>
          </Card>
        </div>
      </TabContent>

      <!-- 变更记录 -->
      <TabContent value="changes">
        <Card class="overflow-hidden">
          <div class="overflow-x-auto">
            <table class="data-table">
              <thead><tr><th>时间</th><th>动作</th><th class="hidden sm:table-cell">操作者</th><th>说明</th><th class="hidden lg:table-cell">字段变更</th></tr></thead>
              <tbody>
                <tr v-for="c in changes" :key="c.id">
                  <td class="whitespace-nowrap text-[11px] text-muted-foreground">{{ fmtTime(c.createdAt) }}</td>
                  <td><Badge variant="outline">{{ c.action }}</Badge></td>
                  <td class="hidden font-mono text-[11px] sm:table-cell">{{ c.actor }}</td>
                  <td class="text-[11.5px]">{{ c.message }}</td>
                  <td class="hidden max-w-[22rem] break-words font-mono text-[10.5px] text-muted-foreground lg:table-cell">{{ truncate(JSON.stringify(c.diff || {}), 140) }}</td>
                </tr>
                <tr v-if="!changes.length">
                  <td colspan="5"><Empty title="暂无变更记录" description="参数修改、更新与调试都会在此留痕。" /></td>
                </tr>
              </tbody>
            </table>
          </div>
        </Card>
      </TabContent>

      <!-- 文档 -->
      <TabContent value="docs">
        <Card>
          <CardContent class="pt-4">
            <div class="doc-prose" v-html="renderMarkdown(view?.manifest?.docs || '该插件未提供接入说明。')" />
          </CardContent>
        </Card>
        <div class="mt-5">
          <div class="mb-2 flex items-center justify-between gap-3">
            <div>
              <h2 class="text-sm font-semibold">插件清单</h2>
              <p class="mt-0.5 text-[11.5px] text-muted-foreground">plugin.json 是参数 Schema 的唯一来源，参数表单由它自动生成</p>
            </div>
            <Button size="sm" variant="outline" @click="showManifest = !showManifest">{{ showManifest ? '收起 Manifest' : '完整 Manifest' }}</Button>
          </div>
          <div v-if="showManifest" class="mt-3">
            <CodeBlock :code="JSON.stringify(view?.manifest, null, 2)" label="manifest.json" max-height="420px" />
          </div>
        </div>
      </TabContent>
    </Tabs>
  </div>
</template>
