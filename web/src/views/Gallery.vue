<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import {
  Images, LayoutGrid, Columns3, Loader2, Search, X, ChevronLeft, ChevronRight,
  Download, Copy, ImageOff, Link2, Trash2, FileText,
} from 'lucide-vue-next'
import { api, downloadObject } from '../api'
import { notifyError, notify } from '../toast'
import { formatBytes, relTime, fmtTime } from '../utils'
import PageHeader from '../components/PageHeader.vue'
import ObjectThumb from '../components/ObjectThumb.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Select from '../components/ui/Select.vue'
import Input from '../components/ui/Input.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'

const VIEW_KEY = 's3bridge.galleryView'
const view = ref(localStorage.getItem(VIEW_KEY) === 'masonry' ? 'masonry' : 'grid')
const items = ref([])
const cursor = ref('')
const total = ref(0)
const totalBytes = ref(0)
const buckets = ref([])
const loading = ref(false)
const initial = ref(true)
const done = ref(false)
const filter = ref({ bucket: '', q: '', range: '', validity: '' })

const sentinel = ref(null)
let observer = null
let searchTimer = null

// CSS 多列在内容不足时会自动收缩列数（column-count 只是上限），
// 相册需要稳定列宽，因此按断点算出列数后自行按列切片。
const colCount = ref(4)
function updateCols() {
  const w = window.innerWidth
  colCount.value = w < 640 ? 2 : w < 1024 ? 3 : w < 1280 ? 4 : w < 1600 ? 5 : 6
}
const masonryCols = computed(() => {
  const cols = Array.from({ length: colCount.value }, () => [])
  items.value.forEach((it, i) => cols[i % colCount.value].push(it))
  return cols
})

const lightbox = ref(-1)
const current = computed(() => (lightbox.value >= 0 ? items.value[lightbox.value] : null))

// 大图直连：优先上游直链（no-referrer），过期后回退本站签名直链。
// 服务端不再把整张图读成 blob 转发，切换图片几乎没有等待。
const boxSrc = ref('')
const boxError = ref('')
const boxFallback = ref(false)
watch(current, (cur) => {
  boxError.value = ''
  boxFallback.value = false
  boxSrc.value = cur ? (cur.previewUrl || cur.directPath || '') : ''
})
function onBoxError() {
  const cur = current.value
  if (cur && cur.directPath && boxSrc.value !== cur.directPath) {
    boxSrc.value = cur.directPath
    boxFallback.value = true
    return
  }
  boxError.value = '该图片无法预览，可下载原文件查看'
}
const hasMore = computed(() => !done.value && items.value.length < total.value)

const rangeOptions = [
  { value: '', label: '全部时间' },
  { value: '86400', label: '最近 24 小时' },
  { value: '604800', label: '最近 7 天' },
  { value: '2592000', label: '最近 30 天' },
]

function setView(v) {
  view.value = v
  localStorage.setItem(VIEW_KEY, v)
}

async function load(reset = false) {
  if (loading.value) return
  loading.value = true
  if (reset) {
    cursor.value = ''
    done.value = false
  }
  try {
    const q = new URLSearchParams({ limit: '48' })
    if (cursor.value && !reset) q.set('cursor', cursor.value)
    if (filter.value.bucket) q.set('bucket', filter.value.bucket)
    if (filter.value.q) q.set('q', filter.value.q)
    if (filter.value.range) q.set('from', filter.value.range)
    if (filter.value.validity) q.set('validity', filter.value.validity)
    const r = await api('/gallery?' + q.toString())
    items.value = reset ? (r.items || []) : [...items.value, ...(r.items || [])]
    cursor.value = r.nextCursor || ''
    done.value = !r.nextCursor
    total.value = r.total || 0
    totalBytes.value = r.totalBytes || 0
    buckets.value = r.buckets || []
  } catch (e) {
    notifyError(e, '加载相册失败')
  } finally {
    loading.value = false
    initial.value = false
    nextTick(observe)
  }
}

function observe() {
  observer?.disconnect()
  if (!sentinel.value || done.value) return
  observer = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting)) load(false)
  }, { rootMargin: '600px 0px' })
  observer.observe(sentinel.value)
}

function onSearch() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => load(true), 320)
}

watch(() => [filter.value.bucket, filter.value.range, filter.value.validity], () => load(true))

// ---------- 灯箱 ----------
function openAt(i) {
  lightbox.value = i
  document.body.style.overflow = 'hidden'
}
function closeBox() {
  lightbox.value = -1
  document.body.style.overflow = ''
}
function step(delta) {
  if (lightbox.value < 0) return
  const next = lightbox.value + delta
  if (next < 0 || next >= items.value.length) return
  lightbox.value = next
}
function onKey(ev) {
  if (lightbox.value < 0) return
  if (ev.key === 'Escape') closeBox()
  if (ev.key === 'ArrowRight') step(1)
  if (ev.key === 'ArrowLeft') step(-1)
}
async function copyKey() {
  if (!current.value) return
  try {
    await navigator.clipboard.writeText(`s3://${current.value.bucket}/${current.value.objectKey}`)
    notify('已复制 S3 路径', { title: current.value.fileName })
  } catch { notifyError(new Error('浏览器未授权剪贴板')) }
}
async function copyDirect() {
  const cur = current.value
  if (!cur) return
  try {
    let path = cur.directPath
    if (!path) {
      const r = await api(`/link/sign?bucket=${encodeURIComponent(cur.bucket)}&key=${encodeURIComponent(cur.objectKey)}`)
      path = r.directPath
    }
    await navigator.clipboard.writeText(window.location.origin + path)
    notify('已复制直链（7 天有效）', { title: cur.fileName })
  } catch { notifyError(new Error('生成直链失败')) }
}
async function removeCurrent() {
  const cur = current.value
  if (!cur) return
  if (!window.confirm(`确认删除「${cur.fileName}」？该操作不可恢复。`)) return
  try {
    const q = new URLSearchParams({ bucket: cur.bucket, key: cur.objectKey })
    await api('/objects?' + q.toString(), { method: 'DELETE' })
    notify('已删除', { title: cur.fileName })
    items.value = items.value.filter((o) => o.id !== cur.id)
    total.value = Math.max(0, total.value - 1)
    closeBox()
  } catch (e) { notifyError(e, '删除失败') }
}
async function save() {
  if (!current.value) return
  try {
    await downloadObject(current.value.bucket, current.value.objectKey, current.value.fileName)
  } catch (e) { notifyError(e, '下载失败') }
}

onMounted(() => {
  updateCols()
  load(true)
  window.addEventListener('keydown', onKey)
  window.addEventListener('resize', updateCols)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
  window.removeEventListener('resize', updateCols)
  observer?.disconnect()
  document.body.style.overflow = ''
  clearTimeout(searchTimer)
})
</script>

<template>
  <div>
    <PageHeader
      title="图片相册"
      description="跨存储桶汇集全站图片，像相册一样浏览：流式加载、两种卡片布局、点开即看大图与元数据；巡检失效的图片会被明确标记。"
    >
      <template #actions>
        <div class="flex items-center gap-1 rounded-lg border bg-card p-1">
          <button
            class="inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-[12px] font-medium transition-colors"
            :class="view === 'grid' ? 'bg-muted text-foreground' : 'text-muted-foreground hover:text-foreground'"
            @click="setView('grid')"
          >
            <LayoutGrid class="size-3.5" />网格
          </button>
          <button
            class="inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-[12px] font-medium transition-colors"
            :class="view === 'masonry' ? 'bg-muted text-foreground' : 'text-muted-foreground hover:text-foreground'"
            @click="setView('masonry')"
          >
            <Columns3 class="size-3.5" />瀑布流
          </button>
        </div>
      </template>
    </PageHeader>

    <div class="mb-4 grid grid-cols-2 gap-2.5 sm:flex sm:items-center">
      <Select v-model="filter.bucket" class="h-9 w-full sm:w-44">
        <option value="">全部存储桶</option>
        <option v-for="b in buckets" :key="b.name" :value="b.name">{{ b.name }}（{{ b.objects }}）</option>
      </Select>
      <Select v-model="filter.range" class="h-9 w-full sm:w-40">
        <option v-for="r in rangeOptions" :key="r.value" :value="r.value">{{ r.label }}</option>
      </Select>
      <Select v-model="filter.validity" class="h-9 w-full sm:w-36">
        <option value="">全部有效性</option>
        <option value="ok">正常</option>
        <option value="invalid">已失效</option>
      </Select>
      <div class="relative col-span-2 w-full sm:col-span-1 sm:w-64">
        <Search class="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="filter.q" placeholder="搜索文件名或对象键" class="h-9 pl-8" @input="onSearch" />
      </div>
      <div class="col-span-2 flex items-center gap-3 text-[11.5px] text-muted-foreground sm:col-span-1 sm:ml-auto">
        <span class="flex items-center gap-1.5"><Images class="size-3.5" />{{ total }} 张</span>
        <span class="hidden sm:inline">共 {{ formatBytes(totalBytes) }}</span>
      </div>
    </div>

    <div v-if="initial" class="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-5">
      <Skeleton v-for="i in 10" :key="i" class="aspect-[4/3] rounded-xl" />
    </div>

    <template v-else-if="items.length">
      <!-- 网格：等比方格，密度高 -->
      <div v-if="view === 'grid'" class="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-5 2xl:grid-cols-6">
        <button
          v-for="(it, i) in items" :key="it.id"
          class="group relative overflow-hidden rounded-xl border bg-card text-left transition-shadow hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          @click="openAt(i)"
        >
          <ObjectThumb :bucket="it.bucket" :object-key="it.objectKey" :alt="it.fileName" :thumb-width="480" />
          <span
            v-if="it.validity?.status === 'invalid'"
            class="absolute left-1.5 top-1.5 inline-flex items-center gap-1 rounded-md bg-destructive/90 px-1.5 py-0.5 text-[10px] font-medium text-white shadow-sm"
            :title="it.validity.error || '巡检确认该图片已无法返回'"
          >
            <ImageOff class="size-3" />已失效
          </span>
          <span class="pointer-events-none absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/70 via-black/25 to-transparent px-2.5 pb-2 pt-6 opacity-0 transition-opacity group-hover:opacity-100">
            <span class="block truncate text-[11px] font-medium text-white">{{ it.fileName }}</span>
            <span class="mt-0.5 block text-[10px] text-white/70">{{ it.bucket }} · {{ formatBytes(it.size) }}</span>
          </span>
        </button>
      </div>

      <!-- 瀑布流：保留原始比例，适合竖图与截图混排 -->
      <div v-else class="flex items-start gap-3">
        <div v-for="(col, ci) in masonryCols" :key="ci" class="flex min-w-0 flex-1 flex-col gap-3">
          <button
            v-for="it in col" :key="it.id"
            class="group block w-full overflow-hidden rounded-xl border bg-card text-left transition-shadow hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            @click="openAt(items.indexOf(it))"
          >
            <div class="relative">
              <ObjectThumb :bucket="it.bucket" :object-key="it.objectKey" :alt="it.fileName" :thumb-width="480" aspect="auto" />
              <span
                v-if="it.validity?.status === 'invalid'"
                class="absolute left-1.5 top-1.5 inline-flex items-center gap-1 rounded-md bg-destructive/90 px-1.5 py-0.5 text-[10px] font-medium text-white shadow-sm"
                :title="it.validity.error || '巡检确认该图片已无法返回'"
              >
                <ImageOff class="size-3" />已失效
              </span>
            </div>
            <span class="block px-2.5 py-2">
              <span class="block truncate text-[11px] font-medium">{{ it.fileName }}</span>
              <span class="mt-0.5 block truncate text-[10px] text-muted-foreground">{{ it.bucket }} · {{ formatBytes(it.size) }} · {{ relTime(it.createdAt) }}</span>
            </span>
          </button>
        </div>
      </div>

      <div ref="sentinel" class="flex h-16 items-center justify-center">
        <span v-if="loading" class="flex items-center gap-2 text-[11.5px] text-muted-foreground">
          <Loader2 class="size-3.5 animate-spin" />加载中…
        </span>
        <span v-else-if="!hasMore" class="text-[11.5px] text-muted-foreground">已加载全部 {{ total }} 张图片</span>
      </div>
    </template>

    <Empty
      v-else
      :title="filter.q ? '没有匹配的图片' : '相册还是空的'"
      :description="filter.q ? '换个关键词，或清空筛选条件。' : '上传图片后会自动出现在这里，支持跨桶统一浏览。'"
    >
      <Button v-if="filter.q || filter.bucket || filter.range || filter.validity" variant="outline" @click="filter = { bucket: '', q: '', range: '', validity: '' }; load(true)">
        清空筛选
      </Button>
    </Empty>

    <!-- 预览：左侧大图 + 右侧文件信息，信息密度与上传列表、桶列表一致 -->
    <Teleport to="body">
      <div
        v-if="current"
        class="fixed inset-0 z-[70] flex flex-col bg-[oklch(0.13_0.004_60)] lg:grid lg:grid-cols-[minmax(0,1fr)_380px] lg:grid-rows-1"
        @click.self="closeBox"
      >
        <div class="relative flex min-h-0 flex-1 items-center justify-center px-4 py-12 sm:px-12">
          <button
            class="absolute right-3 top-3 z-10 grid size-9 place-items-center rounded-full bg-white/10 text-white/80 backdrop-blur transition-colors hover:bg-white/20 hover:text-white"
            aria-label="关闭" @click="closeBox"
          ><X class="size-4.5" /></button>

          <button
            v-if="lightbox > 0"
            class="absolute left-2 top-1/2 z-10 grid size-9 -translate-y-1/2 place-items-center rounded-full bg-white/10 text-white/80 backdrop-blur transition-colors hover:bg-white/20 hover:text-white sm:left-4"
            aria-label="上一张" @click="step(-1)"
          ><ChevronLeft class="size-5" /></button>

          <!-- 直连上游 CDN，配合全局 no-referrer 绕过防盗链；失败自动退回本站签名直链 -->
          <img
            v-if="boxSrc && !boxError"
            :key="current.id"
            :src="boxSrc"
            :alt="current.fileName"
            referrerpolicy="no-referrer"
            class="max-h-full max-w-full rounded-lg object-contain shadow-2xl"
            @error="onBoxError"
          />
          <div v-else class="flex max-w-xs flex-col items-center gap-2 px-4 text-center text-white/50">
            <ImageOff class="size-8" />
            <span class="text-[11.5px] leading-relaxed">{{ boxError || '该图片无法预览，可下载查看原文件' }}</span>
          </div>

          <button
            v-if="lightbox < items.length - 1"
            class="absolute right-2 top-1/2 z-10 grid size-9 -translate-y-1/2 place-items-center rounded-full bg-white/10 text-white/80 backdrop-blur transition-colors hover:bg-white/20 hover:text-white sm:right-4"
            aria-label="下一张" @click="step(1)"
          ><ChevronRight class="size-5" /></button>

          <div class="absolute bottom-3 left-1/2 -translate-x-1/2 font-mono text-[10.5px] tracking-wide text-white/50">
            {{ lightbox + 1 }} / {{ items.length }}
          </div>
        </div>

        <aside class="flex max-h-[46dvh] shrink-0 flex-col border-t border-white/10 bg-background lg:max-h-none lg:border-l lg:border-t-0">
          <div class="border-b px-4 py-3">
            <p class="text-[10px] font-medium tracking-[0.08em] text-muted-foreground">IMAGE DETAIL</p>
            <h2 class="mt-1 break-words text-[15px] font-semibold leading-snug">{{ current.fileName }}</h2>
            <p class="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
              <span>{{ current.mimeType }}</span><span>·</span>
              <span>{{ formatBytes(current.size) }}</span><span>·</span>
              <span>{{ relTime(current.createdAt) }}</span>
              <span
                v-if="current.validity?.status === 'invalid'"
                class="inline-flex items-center gap-1 rounded bg-destructive/90 px-1.5 py-0.5 text-[10px] text-white"
                :title="current.validity.error || ''"
              ><ImageOff class="size-3" />巡检失效</span>
            </p>
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
            <dl class="space-y-2 text-[11.5px]">
              <div v-for="row in [
                { k: '存储桶', v: current.bucket, mono: true },
                { k: '对象键', v: current.objectKey, mono: true },
                { k: 'MIME', v: current.mimeType, mono: true },
                { k: '大小', v: current.size.toLocaleString() + ' 字节', mono: true },
                { k: '来源', v: (current.pluginId || current.provider || '本地归档'), mono: true },
                { k: '归档时间', v: fmtTime(current.createdAt), mono: false },
                { k: '签名到期', v: current.urlExpireAt ? fmtTime(current.urlExpireAt) : '长期有效（本地归档）', mono: false },
                { k: 'SHA-256', v: current.sha256 || '—', mono: true },
                { k: '巡检', v: current.validity?.checkedAt ? (current.validity.status === 'ok' ? '正常' : '失效') + ' · ' + relTime(current.validity.checkedAt) : '未检测', mono: false },
              ]" :key="row.k" class="grid grid-cols-[4.5rem_1fr] gap-2">
                <dt class="text-muted-foreground">{{ row.k }}</dt>
                <dd :class="['break-all', row.mono && 'font-mono text-[10.5px]']">{{ row.v }}</dd>
              </div>
            </dl>

            <div class="mt-4 space-y-2 border-t pt-3">
              <div>
                <p class="flex items-center gap-1 text-[10.5px] text-muted-foreground">
                  直链
                  <span class="rounded bg-muted px-1 py-0.5 text-[9px]">
                    {{ current.previewUrl && !boxFallback ? '直连上游 · no-referrer' : '本站签名 · 支持 Range' }}
                  </span>
                </p>
                <p class="mt-0.5 break-all font-mono text-[10.5px]">{{ current.directPath || '—' }}</p>
              </div>
              <div>
                <p class="text-[10.5px] text-muted-foreground">第三方返回 URL</p>
                <p class="mt-0.5 break-all font-mono text-[10.5px]">{{ current.remoteUrl || '—' }}</p>
              </div>
              <div>
                <p class="text-[10.5px] text-muted-foreground">本地归档路径</p>
                <p class="mt-0.5 break-all font-mono text-[10.5px]">{{ current.localPath || '—' }}</p>
              </div>
            </div>
          </div>

          <div class="flex flex-wrap gap-2 border-t bg-muted/30 px-4 py-3">
            <Button size="sm" @click="save"><Download />下载原文件</Button>
            <Button size="sm" variant="outline" @click="copyDirect"><Link2 />复制直链</Button>
            <Button size="sm" variant="ghost" @click="copyKey"><FileText />复制 S3 路径</Button>
            <Button
              size="sm" variant="ghost"
              class="ml-auto text-destructive hover:bg-destructive/10 hover:text-destructive"
              @click="removeCurrent"
            ><Trash2 />删除</Button>
          </div>
        </aside>
      </div>
    </Teleport>
  </div>
</template>
