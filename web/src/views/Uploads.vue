<script setup>
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { api, downloadObject, getToken } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { formatBytes, relTime, fmtTime, categoryLabel, categoryLabels } from '../utils'
import ObjectThumb from '../components/ObjectThumb.vue'

const items = ref([])
const buckets = ref([])
const plugins = ref([])
const loading = ref(true)
const summary = ref({ total: 0, totalBytes: 0, categories: {}, buckets: {} })
const filters = ref({ bucket: '', category: '', pluginId: '', keyword: '' })
const preview = ref(null)   // { obj, url, loading, error }
const deleting = ref('')
const copying = ref('')

const categories = Object.entries(categoryLabels)
const activeFilters = computed(() => Object.values(filters.value).filter(Boolean).length)
const previewIndex = computed(() => preview.value ? items.value.findIndex((o) => o.id === preview.value.obj.id) : -1)
const imageCount = computed(() => items.value.filter((o) => isImage(o)).length)

function isImage(o) { return (o.mimeType || '').startsWith('image/') }
function extOf(o) {
  const n = o.fileName || o.objectKey || ''
  const i = n.lastIndexOf('.')
  return i > 0 ? n.slice(i + 1).slice(0, 6) : 'file'
}
function categoryIcon(o) {
  return { video: '▶', audio: '♪', document: '¶', archive: '▤', other: '◇' }[o.category] || '◇'
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
  closeLightbox(true)
})

function resetFilters() {
  filters.value = { bucket: '', category: '', pluginId: '', keyword: '' }
  load()
}

async function openLightbox(o) {
  closeLightbox(true)
  preview.value = { obj: o, url: '', loading: isImage(o), error: '' }
  if (!isImage(o)) return
  const target = o.id
  try {
    const q = new URLSearchParams({ bucket: o.bucket, key: o.objectKey })
    const res = await fetch('/api/objects/download?' + q.toString(), {
      headers: { Authorization: 'Bearer ' + getToken() },
    })
    if (!res.ok) throw new Error('对象读取失败（HTTP ' + res.status + '）')
    const blob = await res.blob()
    if (!preview.value || preview.value.obj.id !== target) return
    preview.value.url = URL.createObjectURL(blob)
  } catch (e) {
    if (preview.value?.obj.id === target) preview.value.error = e.message || '图片加载失败'
  } finally {
    if (preview.value?.obj.id === target) preview.value.loading = false
  }
}
function closeLightbox(silent = false) {
  if (preview.value?.url) URL.revokeObjectURL(preview.value.url)
  preview.value = null
  if (!silent) { /* 关闭动画无需额外处理 */ }
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
    <div class="page-head">
      <div>
        <div class="eyebrow">Objects / 上传归档</div>
        <h1 class="page-title">上传列表</h1>
        <p class="page-desc">图片为主的网格视图：缩略图按需加载，点击任意对象在弹窗中查看大图与完整元数据。归档路径为 <code class="code">{bucket}/{文件类型}/{年月}/</code>。</p>
      </div>
      <div class="btn-group">
        <button class="btn" @click="load">↻ 刷新</button>
        <button class="btn btn--primary" @click="go('uploadtest')">↑ 上传测试</button>
      </div>
    </div>

    <div class="metrics" style="grid-template-columns:repeat(4,1fr)">
      <div class="metric">
        <div class="metric-label">当前结果</div>
        <div class="metric-value">{{ summary.total ?? items.length }}<span class="metric-unit">objects</span></div>
        <div class="metric-foot">合计 {{ formatBytes(summary.totalBytes) }}</div>
      </div>
      <div class="metric">
        <div class="metric-label">图片</div>
        <div class="metric-value">{{ imageCount }}<span class="metric-unit">images</span></div>
        <div class="metric-foot">{{ Object.entries(summary.categories || {}).map(([k, v]) => categoryLabel(k) + ' ' + v).join(' · ') || '暂无数据' }}</div>
      </div>
      <div class="metric">
        <div class="metric-label">涉及存储桶</div>
        <div class="metric-value">{{ Object.keys(summary.buckets || {}).length }}<span class="metric-unit">buckets</span></div>
        <div class="metric-foot">{{ Object.keys(summary.buckets || {}).join(', ') || '暂无数据' }}</div>
      </div>
      <div class="metric">
        <div class="metric-label">生效筛选</div>
        <div class="metric-value">{{ activeFilters }}<span class="metric-unit">项</span></div>
        <div class="metric-foot">按 bucket / 类型 / 插件 / 关键字组合过滤</div>
      </div>
    </div>

    <div class="section">
      <div class="section-head">
        <div><h2 class="section-title">归档对象</h2><p class="section-note">最多展示最近 300 条；完整数据可通过 S3 ListObjectsV2 分页获取</p></div>
      </div>

      <div class="filters" style="margin:18px 0 20px">
        <div class="field">
          <label>存储桶</label>
          <select v-model="filters.bucket" @change="load">
            <option value="">全部存储桶</option>
            <option v-for="b in buckets" :key="b.name" :value="b.name">{{ b.name }}</option>
          </select>
        </div>
        <div class="field">
          <label>文件类型</label>
          <select v-model="filters.category" @change="load">
            <option value="">全部类型</option>
            <option v-for="[k, label] in categories" :key="k" :value="k">{{ label }}</option>
          </select>
        </div>
        <div class="field">
          <label>来源插件</label>
          <select v-model="filters.pluginId" @change="load">
            <option value="">全部插件</option>
            <option v-for="p in plugins" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <div class="field" style="min-width:210px">
          <label>文件名关键字</label>
          <input v-model="filters.keyword" placeholder="输入后回车" @keyup.enter="load" />
        </div>
        <button class="btn btn--primary" @click="load">筛选</button>
        <button v-if="activeFilters" class="btn btn--quiet" @click="resetFilters">清除</button>
      </div>

      <div v-if="loading" class="empty"><span class="spinner"></span> 正在加载…</div>
      <div v-else-if="items.length" class="media-grid">
        <button v-for="o in items" :key="o.id" class="media-card" @click="openLightbox(o)">
          <ObjectThumb v-if="isImage(o)" :bucket="o.bucket" :object-key="o.objectKey" :alt="o.fileName" />
          <div v-else class="media-thumb">
            <div class="media-icon">{{ categoryIcon(o) }}</div>
          </div>
          <span class="media-ext">{{ extOf(o) }}</span>
          <span class="media-badge" :class="{ 'tag--accent': false }">{{ categoryLabel(o.category) }}</span>
          <div class="media-meta">
            <div class="media-name" :title="o.fileName">{{ o.fileName }}</div>
            <div class="media-sub">
              <span>{{ formatBytes(o.size) }}</span>
              <span>{{ relTime(o.createdAt) }}</span>
            </div>
          </div>
        </button>
      </div>
      <div v-else class="empty media-empty">
        <div class="empty-title">{{ activeFilters ? '没有符合条件的对象' : '还没有上传记录' }}</div>
        <p>{{ activeFilters ? '试试放宽筛选条件。' : '去上传测试台用一张图片验证插件链路。' }}</p>
        <button class="btn btn--primary btn--sm" @click="go('uploadtest')">开始上传</button>
      </div>
    </div>

    <!-- 图片灯箱：左侧大图，右侧元数据。Teleport 到 body 避免被页面容器的 transform 限制定位 -->
    <Teleport to="body">
      <div v-if="preview" class="lightbox" @click.self="closeLightbox()">
      <div class="lightbox-stage" @click.self="closeLightbox()">
        <button class="lightbox-close" title="关闭 (Esc)" @click="closeLightbox()">✕</button>
        <button class="lightbox-nav prev" title="上一个 (←)" @click="step(-1)">‹</button>
        <img v-if="preview.url" :src="preview.url" :alt="preview.obj.fileName" @click.stop />
        <div v-else-if="preview.loading" class="spinner" style="border-color:rgba(255,255,255,.25);border-top-color:#fff"></div>
        <div v-else class="lightbox-noimg">
          <div class="media-icon" style="color:rgba(255,255,255,.6);font-size:52px">{{ categoryIcon(preview.obj) }}</div>
          <p class="tiny" style="color:rgba(255,255,255,.72);margin-top:12px">
            {{ preview.error || '该类型不支持在线预览，请在右侧下载原文件' }}
          </p>
        </div>
        <button class="lightbox-nav next" title="下一个 (→)" @click="step(1)">›</button>
        <div class="lightbox-count">{{ previewIndex + 1 }} / {{ items.length }}</div>
      </div>

      <aside class="lightbox-side">
        <div class="lightbox-side-head">
          <div class="eyebrow">Object Detail</div>
          <h2 class="lightbox-title">{{ preview.obj.fileName }}</h2>
          <p class="tiny muted" style="margin:6px 0 0">{{ categoryLabel(preview.obj.category) }} · {{ formatBytes(preview.obj.size) }} · {{ relTime(preview.obj.createdAt) }}</p>
        </div>
        <div class="lightbox-side-body">
          <dl class="lightbox-kv">
            <dt>存储桶</dt><dd class="mono">{{ preview.obj.bucket }}</dd>
            <dt>对象键</dt><dd class="mono">{{ preview.obj.objectKey }}</dd>
            <dt>MIME</dt><dd class="mono">{{ preview.obj.mimeType }}</dd>
            <dt>大小</dt><dd class="mono">{{ preview.obj.size.toLocaleString() }} 字节</dd>
            <dt>来源插件</dt><dd class="mono">{{ preview.obj.pluginId }}<span class="muted"> · {{ preview.obj.provider || '—' }}</span></dd>
            <dt>归档时间</dt><dd>{{ fmtTime(preview.obj.createdAt) }}</dd>
            <dt>签名到期</dt><dd>{{ preview.obj.urlExpireAt ? fmtTime(preview.obj.urlExpireAt) : '长期有效（本地归档）' }}</dd>
            <dt>SHA-256</dt><dd class="mono">{{ preview.obj.sha256 }}</dd>
          </dl>

          <div class="divider" style="margin:18px 0"></div>

          <div class="field">
            <label>第三方返回 URL</label>
            <div class="mono tiny" style="word-break:break-all">{{ preview.obj.remoteUrl || '—' }}</div>
            <div class="field-help">临时签名链接可能过期；本地归档对象通过 S3 接口读取长期有效</div>
          </div>
          <div class="field" style="margin-top:14px">
            <label>本地归档路径</label>
            <div class="mono tiny" style="word-break:break-all">{{ preview.obj.localPath || '—' }}</div>
          </div>
        </div>
        <div class="lightbox-side-foot">
          <button class="btn btn--primary btn--sm" @click="download(preview.obj)">下载原文件</button>
          <button class="btn btn--sm" @click="copy(`s3://${preview.obj.bucket}/${preview.obj.objectKey}`, 'S3 URI')">复制 S3 路径</button>
          <button v-if="preview.obj.remoteUrl" class="btn btn--quiet btn--sm" @click="copy(preview.obj.remoteUrl, '第三方 URL')">复制外链</button>
          <button class="btn btn--quiet btn--sm" :disabled="deleting === preview.obj.id" @click="remove(preview.obj)">删除</button>
        </div>
      </aside>
      </div>
    </Teleport>
  </div>
</template>
