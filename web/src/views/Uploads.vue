<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, downloadObject, getToken } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { formatBytes, relTime, fmtTime, categoryLabel, truncate, categoryLabels } from '../utils'

const items = ref([])
const buckets = ref([])
const plugins = ref([])
const loading = ref(true)
const summary = ref({ total: 0, totalBytes: 0, categories: {}, buckets: {} })
const filters = ref({ bucket: '', category: '', pluginId: '', keyword: '' })
const preview = ref(null)
const deleting = ref('')
const copying = ref('')

const categories = Object.entries(categoryLabels)
const activeFilters = computed(() => Object.values(filters.value).filter(Boolean).length)

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
onMounted(async () => { await loadMeta(); await load() })

function resetFilters() {
  filters.value = { bucket: '', category: '', pluginId: '', keyword: '' }
  load()
}

async function openPreview(o) {
  if (!(o.mimeType || '').startsWith('image/')) {
    notifyError(new Error('仅图片支持在线预览，其他类型请下载'))
    return
  }
  try {
    const q = new URLSearchParams({ bucket: o.bucket, key: o.objectKey })
    const res = await fetch('/api/objects/download?' + q.toString(), {
      headers: { Authorization: 'Bearer ' + getToken() },
    })
    if (!res.ok) throw new Error('对象读取失败')
    const blob = await res.blob()
    if (preview.value?.url) URL.revokeObjectURL(preview.value.url)
    preview.value = { url: URL.createObjectURL(blob), obj: o }
  } catch (e) { notifyError(e, '预览失败') }
}
function closePreview() {
  if (preview.value?.url) URL.revokeObjectURL(preview.value.url)
  preview.value = null
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
        <p class="page-desc">所有插件的输出统一落库，按 <strong>存储桶</strong> 与 <strong>文件类型</strong> 双维度归档，可直接下载或用 S3 接口取回。</p>
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
        <div class="metric-label">文件类型</div>
        <div class="metric-value">{{ Object.keys(summary.categories || {}).length }}<span class="metric-unit">类</span></div>
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
        <div><h2 class="section-title">归档对象</h2><p class="section-note">归档路径：{bucket}/{文件类型}/{年月}/，对象键已做路径穿越清洗</p></div>
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

      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>文件</th><th>类型</th><th>存储桶</th><th>来源插件</th><th>大小</th><th>归档时间</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="o in items" :key="o.id">
              <td style="max-width:340px">
                <div class="cell-main">{{ truncate(o.fileName, 46) }}</div>
                <div class="cell-sub mono" :title="o.objectKey">{{ truncate(o.objectKey, 62) }}</div>
              </td>
              <td><span class="tag" :class="{ 'tag--accent': o.category === 'image' }">{{ categoryLabel(o.category) }}</span></td>
              <td class="mono tiny">{{ o.bucket }}</td>
              <td class="mono tiny">{{ o.provider || o.pluginId || '—' }}</td>
              <td class="num mono tiny nowrap">{{ formatBytes(o.size) }}</td>
              <td class="muted tiny nowrap">{{ relTime(o.createdAt) }}<div class="cell-sub">{{ fmtTime(o.createdAt) }}</div></td>
              <td>
                <div class="row-actions">
                  <button v-if="(o.mimeType || '').startsWith('image/')" class="btn btn--sm" @click="openPreview(o)">预览</button>
                  <button class="btn btn--sm" @click="download(o)">下载</button>
                  <button class="btn btn--quiet btn--sm" @click="copy(`s3://${o.bucket}/${o.objectKey}`, 'S3 URI', o.id)">{{ copying === o.id ? '已复制' : 'S3 路径' }}</button>
                  <button class="btn btn--quiet btn--sm" :disabled="deleting === o.id" @click="remove(o)" title="删除">✕</button>
                </div>
              </td>
            </tr>
            <tr v-if="!items.length && !loading">
              <td colspan="7">
                <div class="empty">
                  <div class="empty-title">{{ activeFilters ? '没有符合条件的对象' : '还没有上传记录' }}</div>
                  <p>{{ activeFilters ? '试试放宽筛选条件。' : '去上传测试台用一张图片验证插件链路。' }}</p>
                  <button class="btn btn--primary btn--sm" @click="go('uploadtest')">开始上传</button>
                </div>
              </td>
            </tr>
            <tr v-if="loading"><td colspan="7"><div class="empty"><span class="spinner"></span> 正在加载…</div></td></tr>
          </tbody>
        </table>
      </div>
      <p class="tiny muted" style="margin-top:12px">最多显示 300 条最近记录；完整数据可通过 S3 <code class="code">ListObjectsV2</code> 分页获取。</p>
    </div>

    <!-- 图片预览 -->
    <template v-if="preview">
      <div class="drawer-backdrop" @click="closePreview"></div>
      <div style="position:fixed;inset:0;z-index:62;display:grid;place-items:center;pointer-events:none;padding:32px">
        <div class="panel" style="pointer-events:auto;max-width:min(860px,92vw);max-height:88vh;overflow:auto;padding:20px">
          <div style="display:flex;justify-content:space-between;align-items:center;gap:16px;margin-bottom:16px">
            <div>
              <div class="eyebrow">Object Preview</div>
              <div class="strong">{{ preview.obj.fileName }}</div>
              <div class="mono tiny muted">{{ preview.obj.bucket }}/{{ preview.obj.objectKey }} · {{ formatBytes(preview.obj.size) }}</div>
            </div>
            <button class="icon-btn" @click="closePreview">✕</button>
          </div>
          <img :src="preview.url" :alt="preview.obj.fileName" style="max-width:100%;max-height:62vh;display:block;margin:0 auto;border:1px solid var(--line)" />
          <div class="divider"></div>
          <dl class="kv" style="grid-template-columns:110px 1fr">
            <dt>MIME</dt><dd class="mono">{{ preview.obj.mimeType }}</dd>
            <dt>SHA-256</dt><dd class="mono tiny" style="word-break:break-all">{{ preview.obj.sha256 }}</dd>
            <dt>签名到期</dt><dd>{{ preview.obj.urlExpireAt ? fmtTime(preview.obj.urlExpireAt) : '长期有效（本地归档）' }}</dd>
          </dl>
          <div style="margin-top:18px;display:flex;gap:8px;justify-content:flex-end">
            <button class="btn" @click="download(preview.obj)">下载原图</button>
            <button class="btn btn--primary" @click="closePreview">关闭</button>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
