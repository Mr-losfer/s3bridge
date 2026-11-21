<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import {
  Images, LayoutGrid, Columns3, Loader2, Search, X, ChevronLeft, ChevronRight,
  Download, Copy, ImageOff,
} from 'lucide-vue-next'
import { api, getToken, downloadObject } from '../api'
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

// 灯箱大图同样需要 Bearer，因此走 blob 加载；切换图片时作废旧 URL 防止内存堆积。
const boxSrc = ref('')
const boxLoading = ref(false)
let boxSeq = 0
watch(current, async (cur) => {
  boxLoading.value = true
  boxSrc.value = ''
  if (!cur) { boxLoading.value = false; return }
  const seq = ++boxSeq
  try {
    const q = new URLSearchParams({ bucket: cur.bucket, key: cur.objectKey, w: '1600' })
    const res = await fetch('/api/objects/thumb?' + q.toString(), {
      headers: { Authorization: 'Bearer ' + getToken() },
    })
    if (!res.ok) throw new Error('HTTP ' + res.status)
    const blob = await res.blob()
    if (seq !== boxSeq) return
    if (boxSrc.value) URL.revokeObjectURL(boxSrc.value)
    boxSrc.value = URL.createObjectURL(blob)
  } catch { /* 失败时保留占位，不打断浏览 */ } finally {
    if (seq === boxSeq) boxLoading.value = false
  }
})
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
  if (boxSrc.value) URL.revokeObjectURL(boxSrc.value)
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

    <!-- 灯箱 -->
    <Teleport to="body">
      <div
        v-if="current"
        class="fixed inset-0 z-[70] flex flex-col bg-black/88 backdrop-blur-sm"
        @click.self="closeBox"
      >
        <div class="flex items-center gap-3 px-4 py-3 text-white/90">
          <ImageOff v-if="!current.mimeType?.startsWith('image/')" class="size-4" />
          <div class="min-w-0 flex-1">
            <p class="truncate text-[13px] font-medium">{{ current.fileName }}</p>
            <p class="mt-0.5 truncate font-mono text-[10.5px] text-white/50">{{ current.bucket }}/{{ current.objectKey }}</p>
          </div>
          <Button variant="ghost" class="text-white/80 hover:bg-white/10 hover:text-white" @click="save"><Download />下载</Button>
          <Button variant="ghost" class="text-white/80 hover:bg-white/10 hover:text-white" @click="copyKey"><Copy />复制路径</Button>
          <Button variant="ghost" size="icon" class="text-white/80 hover:bg-white/10 hover:text-white" aria-label="关闭" @click="closeBox">
            <X />
          </Button>
        </div>

        <div class="relative flex min-h-0 flex-1 items-center justify-center px-3 pb-3" @click.self="closeBox">
          <button
            v-if="lightbox > 0"
            class="absolute left-2 z-10 grid size-9 place-items-center rounded-full bg-black/40 text-white/80 transition hover:bg-black/60 hover:text-white sm:left-4"
            aria-label="上一张" @click="step(-1)"
          >
            <ChevronLeft class="size-5" />
          </button>
          <img
            v-if="boxSrc"
            :key="current.id"
            :src="boxSrc"
            :alt="current.fileName"
            class="max-h-full max-w-full rounded-lg object-contain shadow-2xl"
          />
          <div v-else-if="boxLoading" class="flex flex-col items-center gap-2 text-white/60">
            <Loader2 class="size-6 animate-spin" />
            <span class="text-[11px]">加载大图…</span>
          </div>
          <div v-else class="flex flex-col items-center gap-2 text-white/50">
            <ImageOff class="size-7" />
            <span class="text-[11px]">该图片无法预览，可下载查看原文件</span>
          </div>
          <button
            v-if="lightbox < items.length - 1"
            class="absolute right-2 z-10 grid size-9 place-items-center rounded-full bg-black/40 text-white/80 transition hover:bg-black/60 hover:text-white sm:right-4"
            aria-label="下一张" @click="step(1)"
          >
            <ChevronRight class="size-5" />
          </button>
        </div>

        <div class="flex flex-wrap items-center gap-x-5 gap-y-1 border-t border-white/10 px-4 py-2.5 text-[11px] text-white/60">
          <span>{{ formatBytes(current.size) }}</span>
          <span>{{ current.mimeType }}</span>
          <span>{{ fmtTime(current.createdAt) }}</span>
          <span v-if="current.pluginId">via {{ current.pluginId }}</span>
          <span
            v-if="current.validity?.status === 'invalid'"
            class="inline-flex items-center gap-1 rounded bg-destructive/80 px-1.5 py-0.5 text-white"
            :title="current.validity.error || ''"
          >
            <ImageOff class="size-3" />巡检失效 · {{ relTime(current.validity.checkedAt) }}
          </span>
          <span v-else-if="current.validity?.status === 'ok'" class="text-white/45">
            巡检正常 · {{ relTime(current.validity.checkedAt) }}
          </span>
          <span class="ml-auto">{{ lightbox + 1 }} / {{ items.length }}</span>
        </div>
      </div>
    </Teleport>
  </div>
</template>
