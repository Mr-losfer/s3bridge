<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, uploadFile, getToken } from '../api'
import { notify, notifyError } from '../toast'
import { formatBytes, fmtTime, categoryLabel } from '../utils'
import { go } from '../router'

const plugins = ref([])
const buckets = ref([])
const file = ref(null)
const preview = ref('')
const bucket = ref('default')
const pluginId = ref('')
const objectKey = ref('')
const dragging = ref(false)
const uploading = ref(false)
const progress = ref(0)
const result = ref(null)
const history = ref([])

const isImage = computed(() => (file.value?.type || '').startsWith('image/'))
const canUpload = computed(() => !!file.value && !uploading.value)

async function loadMeta() {
  try {
    const [p, b] = await Promise.all([api('/plugins'), api('/buckets')])
    plugins.value = (p || []).filter((x) => x.enabled)
    buckets.value = b || []
    if (!pluginId.value && plugins.value.length) pluginId.value = plugins.value[0].id
    if (!buckets.value.find((x) => x.name === bucket.value) && buckets.value.length) bucket.value = buckets.value[0].name
  } catch (e) { notifyError(e, '加载配置失败') }
}
onMounted(loadMeta)

function pick(f) {
  if (!f) return
  file.value = f
  result.value = null
  if (preview.value) URL.revokeObjectURL(preview.value)
  preview.value = f.type.startsWith('image/') ? URL.createObjectURL(f) : ''
}
function onDrop(e) {
  dragging.value = false
  pick(e.dataTransfer?.files?.[0])
}
function onSelect(e) { pick(e.target.files?.[0]) }

async function submit() {
  if (!canUpload.value) return
  uploading.value = true
  progress.value = 0
  result.value = null
  try {
    const obj = await uploadFile({
      file: file.value, bucket: bucket.value, plugin: pluginId.value,
      objectKey: objectKey.value.trim(), onProgress: (p) => { progress.value = p },
    })
    result.value = obj
    history.value.unshift(obj)
    notify('文件已归档，可通过 S3 标准接口取回', { title: '上传成功' })
  } catch (e) { notifyError(e, '上传失败') }
  finally { uploading.value = false }
}

function copy(text, label) {
  navigator.clipboard?.writeText(text).then(
    () => notify(label + ' 已复制到剪贴板'),
    () => notifyError(new Error('浏览器未授权剪贴板')),
  )
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Upload / 测试台</div>
        <h1 class="page-title">上传图片测试</h1>
        <p class="page-desc">选一张图片，走真实插件链路：第三方上传 → 统一标准输出 → 按 bucket 与文件类型归档 → S3 可读取。</p>
      </div>
      <button class="btn" @click="go('uploads')">查看上传列表 →</button>
    </div>

    <div style="display:grid;grid-template-columns:minmax(0,1.1fr) minmax(300px,.9fr);gap:36px;align-items:start">
      <div>
        <div class="drop-zone" :class="{ 'is-over': dragging }"
          @dragover.prevent="dragging = true" @dragleave.prevent="dragging = false" @drop.prevent="onDrop"
          @click="$refs.fileInput?.click()">
          <div class="drop-zone-icon">↑</div>
          <div style="font-family:var(--serif);font-size:22px;margin-top:12px">拖放文件到这里，或点击选择</div>
          <p class="muted tiny" style="margin:8px 0 0">支持图片、文档、音视频与压缩包；单文件上限由服务端配置决定</p>
          <input ref="fileInput" type="file" style="display:none" @change="onSelect" />
          <div v-if="file" class="panel" style="margin-top:22px;text-align:left;display:inline-block">
            <div class="strong">{{ file.name }}</div>
            <div class="mono tiny muted">{{ file.type || '未知类型' }} · {{ formatBytes(file.size) }}</div>
          </div>
          <img v-if="preview" :src="preview" class="preview-img" alt="预览" />
        </div>

        <div class="form-grid" style="margin-top:26px">
          <div class="field">
            <label>目标存储桶 <span class="req">*</span></label>
            <select v-model="bucket">
              <option v-for="b in buckets" :key="b.name" :value="b.name">{{ b.name }}{{ b.publicRead ? '（公开读）' : '' }}</option>
            </select>
          </div>
          <div class="field">
            <label>上传插件</label>
            <select v-model="pluginId">
              <option value="">自动选择（健康优先）</option>
              <option v-for="p in plugins" :key="p.id" :value="p.id">{{ p.name }} · {{ p.status }}</option>
            </select>
          </div>
          <div class="field" style="grid-column:1/-1">
            <label>对象键（可选）</label>
            <input v-model="objectKey" placeholder="例如 images/2026/banner.png，留空自动生成" />
            <div class="field-help">对象键会做路径穿越清洗；归档目录为 {bucket}/{文件类型}/{年月}/</div>
          </div>
        </div>

        <div style="margin-top:24px;display:flex;gap:12px;align-items:center;flex-wrap:wrap">
          <button class="btn btn--primary" :disabled="!canUpload" @click="submit">
            <span v-if="uploading" class="spinner"></span>
            {{ uploading ? '上传中 ' + progress + '%' : '开始上传' }}
          </button>
          <button class="btn" :disabled="uploading" @click="file = null; preview = ''; result = null">清空</button>
          <span v-if="uploading" class="tiny muted">正在调用插件并等待第三方响应…</span>
        </div>
        <div v-if="uploading" style="height:3px;background:var(--line);margin-top:16px">
          <div style="height:100%;background:var(--accent);transition:width .2s var(--ease)" :style="{ width: progress + '%' }"></div>
        </div>
      </div>

      <div>
        <div class="section-head" style="margin-bottom:16px"><div><h2 class="section-title">标准输出</h2><p class="section-note">所有插件返回同一结构</p></div></div>
        <div v-if="result" class="panel">
          <div style="display:flex;justify-content:space-between;gap:10px;align-items:center;margin-bottom:16px">
            <span class="tag tag--good">SUCCESS</span>
            <span class="mono tiny muted">{{ result.provider }}</span>
          </div>
          <dl class="kv" style="grid-template-columns:100px 1fr">
            <dt>文件名</dt><dd>{{ result.fileName }}</dd>
            <dt>对象键</dt><dd class="mono" style="word-break:break-all">{{ result.objectKey }}</dd>
            <dt>文件类型</dt><dd>{{ categoryLabel(result.category) }} · <span class="mono tiny">{{ result.mimeType }}</span></dd>
            <dt>大小</dt><dd class="mono">{{ formatBytes(result.size) }}</dd>
            <dt>存储桶</dt><dd class="mono">{{ result.bucket }}</dd>
            <dt>签名到期</dt><dd>{{ result.urlExpireAt ? fmtTime(result.urlExpireAt) : '长期有效' }}</dd>
            <dt>SHA-256</dt><dd class="mono tiny" style="word-break:break-all">{{ result.sha256 }}</dd>
          </dl>
          <div class="divider"></div>
          <div class="field">
            <label>第三方返回 URL</label>
            <div class="field-inline">
              <input :value="result.remoteUrl" readonly class="mono" style="font-size:11px" />
              <button class="btn btn--sm" @click="copy(result.remoteUrl, 'URL')">复制</button>
            </div>
            <div class="field-help">临时签名链接可能过期；本地归档对象通过 S3 接口读取长期有效。</div>
          </div>
        </div>
        <div v-else class="empty" style="border:1px solid var(--line);background:var(--surface)">
          <div class="empty-title">等待上传</div>
          <p>完成一次上传后，这里会显示统一标准输出与归档信息。</p>
        </div>

        <div v-if="history.length > 1" class="section" style="margin-top:30px">
          <div class="section-head"><div><h2 class="section-title">本次会话记录</h2></div></div>
          <table style="margin-top:8px">
            <thead><tr><th>文件</th><th>类型</th><th>大小</th></tr></thead>
            <tbody>
              <tr v-for="(h, i) in history" :key="i">
                <td class="tiny">{{ h.fileName }}</td>
                <td><span class="tag">{{ categoryLabel(h.category) }}</span></td>
                <td class="num mono tiny">{{ formatBytes(h.size) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>
