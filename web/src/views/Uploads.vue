<script setup>
import { ref, onMounted, onBeforeUnmount, computed, watch } from 'vue'
import {
  RefreshCw, Upload, Search, X, ChevronLeft, ChevronRight, Download, Copy, Trash2,
  ImageOff, FileText, FileArchive, Film, Music, File, Filter, Images, Database, Image as ImageIcon,
  Play, Eye, Link2, ExternalLink,
} from 'lucide-vue-next'
import { api, downloadObject } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { formatBytes, relTime, fmtTime, categoryLabel, categoryLabels } from '../utils'
import ObjectThumb from '../components/ObjectThumb.vue'
import PageHeader from '../components/PageHeader.vue'
import StatCard from '../components/StatCard.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Input from '../components/ui/Input.vue'
import Select from '../components/ui/Select.vue'
import Card from '../components/ui/Card.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'
import Field from '../components/ui/Field.vue'

const items = ref([])
const buckets = ref([])
const plugins = ref([])
const loading = ref(true)
const summary = ref({ total: 0, totalBytes: 0, categories: {}, buckets: {} })
const filters = ref({ bucket: '', category: '', pluginId: '', keyword: '' })
// 预览不再把文件读成 blob：直接用后端下发的直链（优先上游、其次本机签名链），
// 浏览器自己去取字节，服务端只负责鉴权与签名。
const preview = ref(null)   // { obj }
const previewSrc = ref('')
const previewError = ref('')
const previewFallback = ref(false)
const deleting = ref('')
const copying = ref('')

const categories = Object.entries(categoryLabels)
const activeFilters = computed(() => Object.values(filters.value).filter(Boolean).length)
const previewIndex = computed(() => preview.value ? items.value.findIndex((o) => o.id === preview.value.obj.id) : -1)
const imageCount = computed(() => items.value.filter((o) => isImage(o)).length)

function isImage(o) { return (o.mimeType || '').startsWith('image/') }
function isVideo(o) { return (o.mimeType || '').startsWith('video/') }
function isAudio(o) { return (o.mimeType || '').startsWith('audio/') }
function canPreview(o) { return isImage(o) || isVideo(o) || isAudio(o) }
function previewHint(o) {
  if (isImage(o)) return '预览'
  if (isVideo(o)) return '播放'
  if (isAudio(o)) return '试听'
  return '下载'
}
// 直连优上游：上游链接能省掉本机带宽；没有或已过期时回退本站签名直链。
function srcOf(o) { return o.previewUrl || o.directPath || '' }
function extOf(o) {
  const n = o.fileName || o.objectKey || ''
  const i = n.lastIndexOf('.')
  return i > 0 ? n.slice(i + 1).slice(0, 6) : 'file'
}
const catIcons = { video: Film, audio: Music, document: FileText, archive: FileArchive, other: File }
function categoryIcon(o) { return catIcons[o.category] || File }
function primaryIcon(o) {
  if (isImage(o)) return Eye
  if (isVideo(o) || isAudio(o)) return Play
  return Download
}

function buildQuery() {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(filters.value)) if (v) q.set(k, v)
  q.set('limit', '300')
  return '/uploads?' + q.toString()
}

async function loadMeta() {
  try {
    const [b, p] = await Promise.all([api('/buckets'), api('/plugins')])
    buckets.value = b || []
    plugins.value = p || []
  } catch { /* 主列表失败时仍展示 */ }
}

async function load() {
  loading.value = true
  try {
    const r = await api(buildQuery())
    items.value = r?.items || []
    summary.value = r || { total: 0, totalBytes: 0, categories: {}, buckets: {} }
  } catch (e) { notifyError(e, '加载上传列表失败') }
  finally { loading.value = false }
}
onMounted(async () => {
  await loadMeta()
  await load()
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  closeLightbox()
})

// 灯箱打开时锁定页面滚动，避免移动端背景跟随滑动。
watch(preview, (v) => {
  document.body.style.overflow = v ? 'hidden' : ''
})

function resetFilters() {
  filters.value = { bucket: '', category: '', pluginId: '', keyword: '' }
  load()
}

function openLightbox(o) {
  preview.value = { obj: o }
  previewError.value = ''
  previewFallback.value = false
  previewSrc.value = srcOf(o)
  if (!previewSrc.value) previewError.value = '该对象没有可用的预览地址'
}
function closeLightbox() {
  preview.value = null
  previewSrc.value = ''
  previewError.value = ''
}
// 上游直链失效时自动退回本站直链，避免第三方签名过期后整页破图。
function onPreviewError() {
  const o = preview.value?.obj
  if (o && o.directPath && previewSrc.value !== o.directPath) {
    previewSrc.value = o.directPath
    previewFallback.value = true
    return
  }
  previewError.value = '对象无法在线预览，可下载原文件查看'
}
function step(delta) {
  if (!items.value.length) return
  const next = (previewIndex.value + delta + items.value.length) % items.value.length
  openLightbox(items.value[next])
}
function onKey(e) {
  if (!preview.value) return
  if (e.key === 'Escape') { closeLightbox(); return }
  if (e.key === 'ArrowLeft') { e.preventDefault(); step(-1) }
  if (e.key === 'ArrowRight') { e.preventDefault(); step(1) }
}

async function download(o) {
  try { await downloadObject(o.bucket, o.objectKey, o.fileName) }
  catch (e) { notifyError(e, '下载失败') }
}

async function remove(o) {
  if (!window.confirm(`确认删除对象「${o.fileName}」？该操作不可恢复。`)) return
  deleting.value = o.id
  try {
    const q = new URLSearchParams({ bucket: o.bucket, key: o.objectKey })
    await api('/objects?' + q.toString(), { method: 'DELETE' })
    notify('对象已从本地归档与索引中移除', { title: '删除成功' })
    if (preview.value?.obj.id === o.id) closeLightbox()
    await load()
  } catch (e) { notifyError(e, '删除失败') }
  finally { deleting.value = '' }
}

async function copyDirect(o) {
  try {
    let path = o.directPath
    if (!path) {
      const r = await api(`/link/sign?bucket=${encodeURIComponent(o.bucket)}&key=${encodeURIComponent(o.objectKey)}`)
      path = r.directPath
    }
    copy(window.location.origin + path, '直链', o.id)
  } catch (e) { notifyError(e, '生成直链失败') }
}

function copy(text, label, id = '') {
  copying.value = id
  navigator.clipboard?.writeText(text).then(
    () => notify(label + ' 已复制到剪贴板'),
    () => notifyError(new Error('浏览器未授权剪贴板')),
  )
  setTimeout(() => { copying.value = '' }, 1200)
}
</script>

<template>
  <div>
    <PageHeader
      title="上传列表"
      description="图片为主的网格视图：缩略图按需加载，点击任意对象查看大图与完整元数据。归档路径为 {bucket}/{文件类型}/{年月}/。"
    >
      <template #actions>
        <Button variant="outline" @click="load"><RefreshCw />刷新</Button>
        <Button @click="go('uploadtest')"><Upload />上传测试</Button>
      </template>
    </PageHeader>

    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard label="当前结果" :value="summary.total ?? items.length" unit="objects" :hint="`合计 ${formatBytes(summary.totalBytes)}`" :icon="Images" />
      <StatCard
        label="图片" :value="imageCount" unit="images"
        :hint="Object.entries(summary.categories || {}).map(([k, v]) => categoryLabel(k) + ' ' + v).join(' · ') || '暂无数据'"
        :icon="ImageIcon"
      />
      <StatCard label="涉及存储桶" :value="Object.keys(summary.buckets || {}).length" unit="buckets" :hint="Object.keys(summary.buckets || {}).join(', ') || '暂无数据'" :icon="Database" />
      <StatCard label="生效筛选" :value="activeFilters" unit="项" hint="按 bucket / 类型 / 插件 / 关键字组合过滤" :icon="Filter" />
    </div>

    <Card class="mt-5 p-3.5 sm:p-4">
      <div class="grid grid-cols-2 gap-2.5 sm:flex sm:flex-wrap sm:items-end">
        <Field label="存储桶" class="sm:w-40">
          <Select v-model="filters.bucket" @update:model-value="load">
            <option value="">全部存储桶</option>
            <option v-for="b in buckets" :key="b.name" :value="b.name">{{ b.name }}</option>
          </Select>
        </Field>
        <Field label="文件类型" class="sm:w-36">
          <Select v-model="filters.category" @update:model-value="load">
            <option value="">全部类型</option>
            <option v-for="[k, label] in categories" :key="k" :value="k">{{ label }}</option>
          </Select>
        </Field>
        <Field label="来源插件" class="sm:w-40">
          <Select v-model="filters.pluginId" @update:model-value="load">
            <option value="">全部插件</option>
            <option v-for="p in plugins" :key="p.id" :value="p.id">{{ p.name }}</option>
          </Select>
        </Field>
        <Field label="文件名关键字" class="col-span-2 sm:w-52 sm:flex-1">
          <div class="relative">
            <Search class="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input v-model="filters.keyword" placeholder="输入后回车" class="pl-8" @keyup.enter="load" />
          </div>
        </Field>
        <div class="col-span-2 flex gap-2 sm:col-span-1">
          <Button class="flex-1 sm:flex-none" @click="load"><Search />筛选</Button>
          <Button v-if="activeFilters" variant="ghost" @click="resetFilters"><X />清除</Button>
        </div>
      </div>
    </Card>

    <div v-if="loading" class="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
      <Skeleton v-for="i in 10" :key="i" class="aspect-[4/3] rounded-xl" />
    </div>

    <div v-else-if="items.length" class="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
      <div
        v-for="o in items" :key="o.id"
        class="group relative cursor-pointer overflow-hidden rounded-xl border bg-card text-left transition-all duration-200 hover:-translate-y-0.5 hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        :tabindex="0"
        @click="canPreview(o) ? openLightbox(o) : download(o)"
        @keyup.enter="canPreview(o) ? openLightbox(o) : download(o)"
      >
        <div class="relative">
          <ObjectThumb v-if="isImage(o)" :bucket="o.bucket" :object-key="o.objectKey" :alt="o.fileName" />
          <div v-else class="flex aspect-[4/3] w-full flex-col items-center justify-center gap-2 bg-muted/60">
            <component :is="categoryIcon(o)" class="size-7 text-muted-foreground/60" />
            <span class="text-[10px] text-muted-foreground">{{ previewHint(o) }}</span>
          </div>
          <span
            class="absolute left-1.5 top-1.5 rounded px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-wide text-white backdrop-blur-sm"
            :class="o.invalid ? 'bg-destructive/90' : 'bg-black/65'"
            :title="o.invalid ? (o.validity?.error || '巡检确认该图片已无法返回') : o.fileName"
          >
            {{ o.invalid ? '已失效' : extOf(o) }}
          </span>
          <Badge variant="outline" class="absolute right-1.5 top-1.5 border-border/60 bg-background/90 backdrop-blur-sm">{{ categoryLabel(o.category) }}</Badge>

          <!-- 类型对应的快捷操作：图片看大图、音视频直接播、其余下载 -->
          <div class="absolute inset-x-1.5 bottom-1.5 flex items-center justify-end gap-1 opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100">
            <button
              class="grid size-7 place-items-center rounded-md bg-black/65 text-white backdrop-blur-sm transition-colors hover:bg-black/80"
              :title="previewHint(o)" :aria-label="previewHint(o)"
              @click.stop="canPreview(o) ? openLightbox(o) : download(o)"
            ><component :is="primaryIcon(o)" class="size-3.5" /></button>
            <button
              v-if="canPreview(o)"
              class="grid size-7 place-items-center rounded-md bg-black/65 text-white backdrop-blur-sm transition-colors hover:bg-black/80"
              title="下载原文件" aria-label="下载原文件"
              @click.stop="download(o)"
            ><Download class="size-3.5" /></button>
            <button
              class="grid size-7 place-items-center rounded-md bg-black/65 text-white backdrop-blur-sm transition-colors hover:bg-black/80"
              title="复制直链" aria-label="复制直链"
              @click.stop="copyDirect(o)"
            ><Link2 class="size-3.5" /></button>
            <button
              class="grid size-7 place-items-center rounded-md bg-black/65 text-white backdrop-blur-sm transition-colors hover:bg-destructive"
              title="删除" aria-label="删除"
              :disabled="deleting === o.id"
              @click.stop="remove(o)"
            ><Trash2 class="size-3.5" /></button>
          </div>
        </div>
        <div class="border-t px-2.5 py-2">
          <p class="truncate text-[12px] font-medium" :title="o.fileName">{{ o.fileName }}</p>
          <div class="mt-0.5 flex items-center justify-between gap-2 font-mono text-[10px] text-muted-foreground">
            <span>{{ formatBytes(o.size) }}</span>
            <span>{{ relTime(o.createdAt) }}</span>
          </div>
        </div>
      </div>
    </div>

    <Card v-else class="mt-4">
      <Empty
        :title="activeFilters ? '没有符合条件的对象' : '还没有上传记录'"
        :description="activeFilters ? '试试放宽筛选条件。' : '去上传测试台用一张图片验证插件链路。'"
      >
        <Button size="sm" @click="activeFilters ? resetFilters() : go('uploadtest')">{{ activeFilters ? '清除筛选' : '开始上传' }}</Button>
      </Empty>
    </Card>

    <!-- 灯箱：桌面左图右数据；移动端上图、下信息 -->
    <Teleport to="body">
      <div v-if="preview" class="fixed inset-0 z-[120] flex flex-col bg-[oklch(0.13_0.004_60)] lg:grid lg:grid-cols-[minmax(0,1fr)_360px] lg:grid-rows-1" @click.self="closeLightbox()">
        <div class="relative flex min-h-0 flex-1 items-center justify-center px-4 py-12 sm:px-12">
          <button
            class="absolute right-3 top-3 z-10 grid size-9 place-items-center rounded-full bg-white/10 text-white/80 backdrop-blur transition-colors hover:bg-white/20 hover:text-white"
            aria-label="关闭" @click="closeLightbox()"
          ><X class="size-4.5" /></button>

          <button
            v-if="items.length > 1"
            class="absolute left-2 top-1/2 z-10 grid size-9 -translate-y-1/2 place-items-center rounded-full bg-white/10 text-white/80 backdrop-blur transition-colors hover:bg-white/20 hover:text-white sm:left-4"
            aria-label="上一个" @click="step(-1)"
          ><ChevronLeft class="size-5" /></button>

          <!-- 预览一律直连：图片/视频由浏览器自己去上游或本站直链取字节，
               服务端不转发正文；全局 no-referrer 让防盗链的 CDN 也放行。 -->
          <img
            v-if="isImage(preview.obj) && previewSrc && !previewError"
            :src="previewSrc" :alt="preview.obj.fileName" referrerpolicy="no-referrer"
            class="max-h-full max-w-full rounded object-contain shadow-2xl" @click.stop @error="onPreviewError"
          />
          <video
            v-else-if="isVideo(preview.obj) && previewSrc && !previewError"
            :src="previewSrc" controls autoplay playsinline
            class="max-h-full max-w-full rounded shadow-2xl" @click.stop @error="onPreviewError"
          />
          <div
            v-else-if="isAudio(preview.obj) && previewSrc && !previewError"
            class="w-full max-w-md rounded-2xl border border-white/10 bg-white/5 p-6 text-center"
            @click.stop
          >
            <Music class="mx-auto size-9 text-white/50" />
            <p class="mt-3 truncate text-[12px] text-white/80">{{ preview.obj.fileName }}</p>
            <audio :src="previewSrc" controls autoplay class="mt-4 w-full" @error="onPreviewError" />
          </div>
          <div v-else class="flex max-w-xs flex-col items-center gap-2 px-4 text-center">
            <component :is="categoryIcon(preview.obj)" class="size-10 text-white/45" />
            <p class="text-[11.5px] leading-relaxed text-white/60">{{ previewError || '该类型不支持在线预览，请在右侧下载原文件' }}</p>
          </div>

          <button
            v-if="items.length > 1"
            class="absolute right-2 top-1/2 z-10 grid size-9 -translate-y-1/2 place-items-center rounded-full bg-white/10 text-white/80 backdrop-blur transition-colors hover:bg-white/20 hover:text-white sm:right-4"
            aria-label="下一个" @click="step(1)"
          ><ChevronRight class="size-5" /></button>

          <div class="absolute bottom-3 left-1/2 -translate-x-1/2 font-mono text-[10.5px] tracking-wide text-white/50">
            {{ previewIndex + 1 }} / {{ items.length }}
          </div>
        </div>

        <aside class="flex max-h-[46dvh] shrink-0 flex-col border-t border-white/10 bg-background lg:max-h-none lg:border-l lg:border-t-0">
          <div class="border-b px-4 py-3">
            <p class="text-[10px] font-medium tracking-[0.08em] text-muted-foreground">OBJECT DETAIL</p>
            <h2 class="mt-1 break-words text-[15px] font-semibold leading-snug">{{ preview.obj.fileName }}</h2>
            <p class="mt-1 text-[11px] text-muted-foreground">
              {{ categoryLabel(preview.obj.category) }} · {{ formatBytes(preview.obj.size) }} · {{ relTime(preview.obj.createdAt) }}
            </p>
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
            <dl class="space-y-2 text-[11.5px]">
              <div v-for="row in [
                { k: '存储桶', v: preview.obj.bucket, mono: true },
                { k: '对象键', v: preview.obj.objectKey, mono: true },
                { k: 'MIME', v: preview.obj.mimeType, mono: true },
                { k: '大小', v: preview.obj.size.toLocaleString() + ' 字节', mono: true },
                { k: '来源插件', v: preview.obj.pluginId + ' · ' + (preview.obj.provider || '—'), mono: true },
                { k: '归档时间', v: fmtTime(preview.obj.createdAt), mono: false },
                { k: '签名到期', v: preview.obj.urlExpireAt ? fmtTime(preview.obj.urlExpireAt) : '长期有效（本地归档）', mono: false },
                { k: 'SHA-256', v: preview.obj.sha256, mono: true },
              ]" :key="row.k" class="grid grid-cols-[4.5rem_1fr] gap-2">
                <dt class="text-muted-foreground">{{ row.k }}</dt>
                <dd :class="['break-all', row.mono && 'font-mono text-[10.5px]']">{{ row.v }}</dd>
              </div>
            </dl>

            <div class="mt-4 space-y-2 border-t pt-3">
              <div>
                <p class="text-[10.5px] text-muted-foreground">第三方返回 URL</p>
                <p class="mt-0.5 break-all font-mono text-[10.5px]">{{ preview.obj.remoteUrl || '—' }}</p>
              </div>
              <div>
                <p class="text-[10.5px] text-muted-foreground">本地归档路径</p>
                <p class="mt-0.5 break-all font-mono text-[10.5px]">{{ preview.obj.localPath || '—' }}</p>
              </div>
              <div>
                <p class="flex items-center gap-1 text-[10.5px] text-muted-foreground">
                  直链
                  <span class="rounded bg-muted px-1 py-0.5 text-[9px]">
                    {{ preview.obj.invalid ? '已失效 · 占位图' : (preview.obj.previewUrl && !previewFallback ? '直连上游 · no-referrer' : '本站签名 · 支持 Range') }}
                  </span>
                </p>
                <p class="mt-0.5 break-all font-mono text-[10.5px]">{{ preview.obj.directPath || '—' }}</p>
              </div>
            </div>
          </div>

          <div class="flex flex-wrap gap-2 border-t bg-muted/30 px-4 py-3">
            <Button size="sm" @click="download(preview.obj)"><Download />下载原文件</Button>
            <Button size="sm" variant="outline" @click="copy(`s3://${preview.obj.bucket}/${preview.obj.objectKey}`, 'S3 URI')"><Copy />复制 S3 路径</Button>
            <Button size="sm" variant="outline" @click="copyDirect(preview.obj)"><Link2 />复制直链</Button>
            <Button v-if="preview.obj.remoteUrl" size="sm" variant="ghost" @click="copy(preview.obj.remoteUrl, '第三方 URL')">复制外链</Button>
            <Button size="sm" variant="ghost" class="ml-auto text-destructive hover:bg-destructive/10 hover:text-destructive" :disabled="deleting === preview.obj.id" @click="remove(preview.obj)">
              <Trash2 />删除
            </Button>
          </div>
        </aside>
      </div>
    </Teleport>
  </div>
</template>
