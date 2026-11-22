<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import {
  FileCode2, FileText, FileJson, Plus, Save, Hammer, Trash2, Pencil,
  RotateCw, Download, CircleCheck, CircleX, Loader2, Lock,
} from 'lucide-vue-next'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { formatBytes, relTime } from '../utils'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Field from '../components/ui/Field.vue'
import Input from '../components/ui/Input.vue'
import CodeBlock from '../components/CodeBlock.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'

const props = defineProps({
  id: { type: String, required: true },
  editable: { type: Boolean, default: false },
  built: { type: Boolean, default: false },
  toolchain: { type: Boolean, default: true },
  buildError: { type: String, default: '' },
})
const emit = defineEmits(['changed'])

const files = ref([])
const active = ref('')
const content = ref('')
const original = ref('')
const loading = ref(true)
const opening = ref(false)
const saving = ref(false)
const building = ref(false)
const report = ref(null)
const exportFiles = ref(null)
const newName = ref('')
const renaming = ref('')
const renameTo = ref('')

const dirty = computed(() => content.value !== original.value)
const current = computed(() => files.value.find((f) => f.name === active.value) || null)
const ro = computed(() => !current.value?.editable)

function icon(f) {
  if (f.go) return FileCode2
  if (f.name.endsWith('.json') || f.name.endsWith('.mod')) return FileJson
  return FileText
}

async function loadFiles(keep = '') {
  loading.value = true
  try {
    const r = await api(`/plugins/${encodeURIComponent(props.id)}/files`)
    files.value = r.files || []
    const next = keep || active.value || files.value[0]?.name || ''
    if (next) await open(next)
    else { active.value = ''; content.value = ''; original.value = '' }
  } catch (e) { notifyError(e, '读取插件文件失败') }
  finally { loading.value = false }
}

async function open(name) {
  if (dirty.value && !window.confirm('当前文件有未保存的修改，切换后将丢失，继续？')) return
  opening.value = true
  try {
    const r = await api(`/plugins/${encodeURIComponent(props.id)}/files/${encodeURIComponent(name)}`)
    active.value = name
    content.value = r.content || ''
    original.value = r.content || ''
  } catch (e) { notifyError(e, '打开文件失败') }
  finally { opening.value = false }
}

async function save() {
  if (!active.value || ro.value) return
  saving.value = true
  try {
    const r = await api(`/plugins/${encodeURIComponent(props.id)}/files/${encodeURIComponent(active.value)}`, {
      method: 'PUT', body: { content: content.value },
    })
    original.value = content.value
    report.value = r.build || null
    if (r.build && r.build.ok === false) {
      notify('源码已保存，但加载失败：请按下方错误输出修复', { title: active.value, type: 'error' })
    } else {
      notify('源码已保存并重新加载，立即生效', { title: active.value })
    }
    await loadFiles(active.value)
    emit('changed', r.plugin)
  } catch (e) { notifyError(e, '保存失败') }
  finally { saving.value = false }
}

async function build() {
  building.value = true
  try {
    report.value = await api(`/plugins/${encodeURIComponent(props.id)}/build`, { method: 'POST', body: {} })
    if (report.value.ok) notify(`加载成功 · ${report.value.durationMs}ms · ${report.value.files || 1} 个文件`)
    else notify('加载失败：请查看下方错误', { type: 'error' })
    emit('changed')
    await loadFiles(active.value)
  } catch (e) { notifyError(e, '加载失败') }
  finally { building.value = false }
}

async function createFile() {
  const name = newName.value.trim()
  if (!name) return
  // 新建 .go 时先写入最简骨架：空文件会立刻让加载失败，容易被误判为系统故障。
  const seed = name.endsWith('.go')
    ? `package main\n\n// ${name}：可在此实现辅助类型与函数，保存后自动生效。\n`
    : name.endsWith('.md') ? `# ${name}\n` : ''
  try {
    await api(`/plugins/${encodeURIComponent(props.id)}/files/${encodeURIComponent(name)}`, {
      method: 'PUT', body: { content: seed },
    })
    newName.value = ''
    notify('文件已创建', { title: name })
    await loadFiles(name)
    emit('changed')
  } catch (e) { notifyError(e, '创建文件失败') }
}

async function removeFile(f) {
  if (!window.confirm(`删除文件 ${f.name}？删除后会立即重新加载。`)) return
  try {
    const r = await api(`/plugins/${encodeURIComponent(props.id)}/files/${encodeURIComponent(f.name)}`, { method: 'DELETE' })
    report.value = r.build || null
    if (active.value === f.name) active.value = ''
    notify('文件已删除', { title: f.name })
    await loadFiles()
    emit('changed')
  } catch (e) { notifyError(e, '删除失败') }
}

async function doRename(f) {
  const to = renameTo.value.trim()
  if (!to) return
  try {
    const r = await api(`/plugins/${encodeURIComponent(props.id)}/files/rename`, {
      method: 'POST', body: { from: f.name, to },
    })
    report.value = r.build || null
    renaming.value = ''
    renameTo.value = ''
    notify('文件已重命名', { title: to })
    await loadFiles(to)
    emit('changed')
  } catch (e) { notifyError(e, '重命名失败') }
}

async function exportAll() {
  try {
    const r = await api(`/plugins/${encodeURIComponent(props.id)}/export`)
    exportFiles.value = r
  } catch (e) { notifyError(e, '导出失败') }
}

function downloadExport() {
  const r = exportFiles.value
  if (!r) return
  const blob = new Blob([JSON.stringify(r, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${props.id}-plugin.json`
  a.click()
  URL.revokeObjectURL(url)
}

watch(() => props.id, () => loadFiles())
onMounted(() => loadFiles())
</script>

<template>
  <div class="grid gap-4 lg:grid-cols-[15rem_1fr]">
    <!-- 文件树 -->
    <Card class="h-fit overflow-hidden">
      <div class="flex items-center justify-between border-b px-3 py-2">
        <span class="text-[12px] font-medium">插件文件夹</span>
        <Badge variant="outline">{{ files.length }} 个文件</Badge>
      </div>
      <div v-if="loading" class="space-y-1.5 p-3">
        <Skeleton v-for="i in 4" :key="i" class="h-7" />
      </div>
      <ul v-else class="max-h-[26rem] overflow-y-auto p-1.5">
        <li v-for="f in files" :key="f.name">
          <button
            class="group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors"
            :class="f.name === active ? 'bg-accent text-foreground' : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground'"
            @click="open(f.name)"
          >
            <component :is="icon(f)" class="size-3.5 shrink-0" :class="f.go ? 'text-primary' : ''" />
            <span class="min-w-0 flex-1 truncate font-mono text-[11.5px]">{{ f.name }}</span>
            <Lock v-if="!f.editable" class="size-3 shrink-0 opacity-60" />
            <Badge v-else-if="f.entry" variant="primary" class="px-1 py-0 text-[9px]">入口</Badge>
            <span class="hidden shrink-0 text-[9.5px] text-muted-foreground group-hover:hidden xl:inline">{{ formatBytes(f.size) }}</span>
          </button>
          <div v-if="renaming === f.name" class="flex gap-1 px-2 pb-1.5">
            <Input v-model="renameTo" :placeholder="f.name" class="h-7 text-[11px]" @keyup.enter="doRename(f)" />
            <Button size="sm" class="h-7 px-2" @click="doRename(f)">改</Button>
          </div>
        </li>
      </ul>

      <div v-if="editable" class="space-y-2 border-t p-2.5">
        <div class="flex gap-1.5">
          <Input v-model="newName" placeholder="新增文件，如 helper.go" class="h-8 text-[11.5px]" @keyup.enter="createFile" />
          <Button size="icon" class="size-8 shrink-0" aria-label="新增文件" :disabled="!newName.trim()" @click="createFile"><Plus /></Button>
        </div>
        <div class="flex flex-wrap gap-1.5">
          <Button size="sm" variant="outline" class="flex-1" :disabled="building || !toolchain" @click="build">
            <Loader2 v-if="building" class="animate-spin" /><Hammer v-else />重新加载
          </Button>
          <Button size="sm" variant="ghost" @click="loadFiles(active)"><RotateCw /></Button>
        </div>
      </div>
    </Card>

    <!-- 编辑器 -->
    <div class="min-w-0 space-y-3">
      <div v-if="buildError" class="flex items-start gap-2 rounded-xl border border-destructive/25 bg-destructive/[0.05] p-3">
        <CircleX class="mt-0.5 size-4 shrink-0 text-destructive" />
        <div class="min-w-0">
          <p class="text-[12px] font-medium text-destructive">最近一次加载失败</p>
          <pre class="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-words font-mono text-[10.5px] leading-relaxed text-muted-foreground">{{ buildError }}</pre>
        </div>
      </div>

      <Card v-if="current" class="overflow-hidden">
        <div class="flex flex-wrap items-center justify-between gap-2 border-b px-3.5 py-2.5">
          <div class="flex min-w-0 items-center gap-2">
            <component :is="icon(current)" class="size-3.5 shrink-0 text-muted-foreground" />
            <span class="truncate font-mono text-[12px] font-medium">{{ current.name }}</span>
            <Badge v-if="!current.editable" variant="outline">系统维护 · 只读</Badge>
            <Badge v-if="dirty" variant="warning">未保存</Badge>
          </div>
          <div class="flex items-center gap-1.5">
            <span class="hidden font-mono text-[10px] text-muted-foreground sm:inline">{{ relTime(current.modTime) }}</span>
            <Button v-if="current.editable" size="sm" :disabled="saving || !dirty" @click="save">
              <Loader2 v-if="saving" class="animate-spin" /><Save v-else />保存并生效
            </Button>
            <Button v-if="current.editable" size="icon" variant="ghost" aria-label="重命名" @click="renaming = current.name; renameTo = current.name">
              <Pencil />
            </Button>
            <Button v-if="current.editable" size="icon" variant="ghost" aria-label="删除" @click="removeFile(current)"><Trash2 /></Button>
          </div>
        </div>
        <textarea
          v-model="content" :readonly="ro" spellcheck="false"
          class="min-h-[26rem] w-full resize-y border-0 bg-background px-3.5 py-3 font-mono text-[11.5px] leading-[1.7] outline-none read-only:bg-muted/30"
          :class="opening && 'opacity-60'"
        />
      </Card>
      <Card v-else>
        <Empty title="选择左侧文件开始编辑" description="插件就是一个 Go 文件夹：plugin.json 描述参数，*.go 实现上传逻辑。" />
      </Card>

      <Card v-if="report" class="overflow-hidden">
        <div class="flex flex-wrap items-center justify-between gap-2 border-b px-3.5 py-2.5">
          <span class="text-[12.5px] font-medium">加载结果</span>
          <div class="flex items-center gap-2">
            <Badge :variant="report.ok ? 'success' : 'destructive'">
              <component :is="report.ok ? CircleCheck : CircleX" class="mr-1 size-3" />{{ report.ok ? 'PASS' : 'FAIL' }}
            </Badge>
            <span class="font-mono text-[10.5px] text-muted-foreground">{{ report.durationMs }}ms</span>
            <span v-if="report.size" class="font-mono text-[10.5px] text-muted-foreground">{{ formatBytes(report.size) }}</span>
          </div>
        </div>
        <div v-if="report.sha256" class="border-b px-3.5 py-1.5 font-mono text-[10px] text-muted-foreground">sha256 {{ report.sha256 }}</div>
        <CodeBlock v-if="report.output" :code="report.output" label="go build" max-height="240px" />
        <p v-else class="px-3.5 py-3 text-[11.5px] text-muted-foreground">源码已加载，入口已注册，可以直接调用。</p>
      </Card>

      <div class="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" @click="exportAll"><Download />导出全部源码</Button>
        <span class="text-[11px] text-muted-foreground">导出内容为 JSON，可在另一实例逐文件写回复刻该插件。</span>
      </div>
      <div v-if="exportFiles">
        <CodeBlock :code="JSON.stringify(exportFiles, null, 2)" label="export" max-height="320px" />
        <Button size="sm" variant="outline" class="mt-2" @click="downloadExport"><Download />下载 JSON</Button>
      </div>
    </div>
  </div>
</template>
