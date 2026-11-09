<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { formatBytes, fmtTime } from '../utils'
import { can } from '../session'

const buckets = ref([])
const loading = ref(true)
const drawer = ref(false)
const editing = ref(false)
const saving = ref(false)
const busy = ref('')
const form = ref({ name: '', description: '', publicRead: false })
const endpoint = ref(typeof window !== 'undefined' ? window.location.origin + '/s3' : 'http://localhost:8080/s3')

const canWrite = computed(() => can('bucket:write'))
const totalObjects = computed(() => buckets.value.reduce((n, b) => n + (b.objects || 0), 0))
const totalSize = computed(() => buckets.value.reduce((n, b) => n + (b.totalSize || 0), 0))

async function load() {
  loading.value = true
  try { buckets.value = (await api('/buckets')) || [] }
  catch (e) { notifyError(e, '加载存储桶失败') }
  finally { loading.value = false }
}
onMounted(async () => {
  await load()
  try {
    const s = await api('/site')
    endpoint.value = window.location.origin + (s?.s3Endpoint || '/s3')
  } catch { /* 使用默认 */ }
})

function openCreate() {
  editing.value = false
  form.value = { name: '', description: '', publicRead: false }
  drawer.value = true
}
function openEdit(b) {
  editing.value = true
  form.value = { name: b.name, description: b.description || '', publicRead: !!b.publicRead }
  drawer.value = true
}

async function save() {
  const name = form.value.name.trim()
  if (!/^[a-z0-9._-]{3,63}$/.test(name)) {
    notifyError(new Error('桶名仅允许小写字母、数字、. _ -，长度 3~63'))
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await api('/buckets/' + encodeURIComponent(name), {
        method: 'PATCH', body: { description: form.value.description, publicRead: form.value.publicRead },
      })
      notify('存储桶设置已更新')
    } else {
      await api('/buckets', { method: 'POST', body: { name, description: form.value.description, publicRead: form.value.publicRead } })
      notify(`存储桶 ${name} 已创建`, { title: '创建成功' })
    }
    drawer.value = false
    await load()
  } catch (e) { notifyError(e) } finally { saving.value = false }
}

async function remove(b) {
  if (!window.confirm(`确认删除存储桶「${b.name}」？仅空桶可删除。`)) return
  busy.value = b.name
  try {
    await api('/buckets/' + encodeURIComponent(b.name), { method: 'DELETE' })
    notify(`存储桶 ${b.name} 已删除`)
    await load()
  } catch (e) { notifyError(e, '删除失败') } finally { busy.value = '' }
}

function viewObjects(b) { go('uploads') }
function copy(text, label) {
  navigator.clipboard?.writeText(text).then(
    () => notify(label + ' 已复制'),
    () => notifyError(new Error('浏览器未授权剪贴板')),
  )
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Buckets / 存储桶</div>
        <h1 class="page-title">存储桶管理</h1>
        <p class="page-desc">每个桶独立命名空间，对象可按桶过滤查看。桶同时映射到 S3 接口，用各自用户的 API Key 通过 SigV4 访问。</p>
      </div>
      <div class="btn-group">
        <button class="btn" @click="load">↻ 刷新</button>
        <button v-if="canWrite" class="btn btn--primary" @click="openCreate">＋ 新建存储桶</button>
      </div>
    </div>

    <div class="metrics" style="grid-template-columns:repeat(3,1fr)">
      <div class="metric"><div class="metric-label">存储桶</div><div class="metric-value">{{ buckets.length }}<span class="metric-unit">buckets</span></div><div class="metric-foot">默认桶 default 在启动时自动创建</div></div>
      <div class="metric"><div class="metric-label">对象总数</div><div class="metric-value">{{ totalObjects }}<span class="metric-unit">objects</span></div><div class="metric-foot">跨全部存储桶</div></div>
      <div class="metric"><div class="metric-label">占用空间</div><div class="metric-value">{{ formatBytes(totalSize) }}</div><div class="metric-foot">本地归档副本</div></div>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">全部存储桶</h2><p class="section-note">对象数与容量实时统计</p></div></div>
      <div class="table-wrap" style="margin-top:18px">
        <table>
          <thead><tr><th>桶名</th><th>对象数</th><th>容量</th><th>访问</th><th>说明</th><th>创建时间</th><th></th></tr></thead>
          <tbody>
            <tr v-for="b in buckets" :key="b.name">
              <td>
                <div class="cell-main mono">{{ b.name }}</div>
                <div class="cell-sub mono tiny">s3://{{ b.name }}</div>
              </td>
              <td class="num mono">{{ b.objects || 0 }}</td>
              <td class="num mono">{{ formatBytes(b.totalSize) }}</td>
              <td><span class="tag" :class="b.publicRead ? 'tag--warn' : 'tag--good'">{{ b.publicRead ? '公开读' : '私有' }}</span></td>
              <td class="tiny muted" style="max-width:240px">{{ b.description || '—' }}</td>
              <td class="muted tiny nowrap">{{ fmtTime(b.createdAt) }}</td>
              <td>
                <div class="row-actions">
                  <button class="btn btn--sm" @click="viewObjects(b)">查看对象</button>
                  <button class="btn btn--quiet btn--sm" @click="copy(`s3://${b.name}`, 'S3 URI')">复制</button>
                  <template v-if="canWrite">
                    <button class="btn btn--quiet btn--sm" @click="openEdit(b)">编辑</button>
                    <button class="btn btn--quiet btn--sm" :disabled="busy === b.name" @click="remove(b)">删除</button>
                  </template>
                </div>
              </td>
            </tr>
            <tr v-if="!buckets.length && !loading"><td colspan="7"><div class="empty"><div class="empty-title">还没有存储桶</div><p>创建一个桶用于按业务隔离上传文件。</p></div></td></tr>
            <tr v-if="loading"><td colspan="7"><div class="empty"><span class="spinner"></span> 正在加载…</div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">S3 接入示例</h2><p class="section-note">桶名直接作为 S3 Bucket 使用，凭据见「API 密钥」</p></div></div>
      <div class="panel" style="margin-top:18px">
        <pre class="codeblock"><code># 列出对象（把 AK/SK 换成你的 API 密钥）
export AWS_ACCESS_KEY_ID=AK_xxxxxxxx
export AWS_SECRET_ACCESS_KEY=xxxxxxxx
aws --endpoint-url {{ endpoint }} s3 ls s3://default/

# 上传 / 下载
aws --endpoint-url {{ endpoint }} s3 cp ./a.png s3://default/images/a.png
aws --endpoint-url {{ endpoint }} s3 cp s3://default/images/a.png ./a.png</code></pre>
        <p class="tiny muted" style="margin:14px 0 0">完整接口说明见 <button class="btn btn--quiet btn--sm" style="padding:0 4px" @click="go('docs', 's3-api')">S3 开放接口文档 →</button>，每个用户使用自己的 API Key 签名，权限隔离。</p>
      </div>
    </div>

    <template v-if="drawer">
      <div class="drawer-backdrop" @click="drawer = false"></div>
      <aside class="drawer">
        <div class="drawer-head">
          <div>
            <div class="eyebrow">Bucket</div>
            <h2 class="section-title">{{ editing ? '编辑存储桶' : '新建存储桶' }}</h2>
            <p class="section-note">桶名创建后不可修改，删除前需清空对象</p>
          </div>
          <button class="icon-btn" @click="drawer = false">✕</button>
        </div>
        <div class="drawer-body">
          <div class="form-grid">
            <div class="field" style="grid-column:1/-1">
              <label>桶名 <span class="req">*</span></label>
              <input v-model="form.name" :disabled="editing" placeholder="例如 assets、user-uploads" />
              <div class="field-help">小写字母、数字、点、下划线、连字符，长度 3~63</div>
            </div>
            <div class="field" style="grid-column:1/-1">
              <label>说明</label>
              <textarea v-model="form.description" placeholder="这个桶用来存什么"></textarea>
            </div>
            <div class="field" style="grid-column:1/-1">
              <label>访问控制</label>
              <div class="checkbox-row"><input type="checkbox" v-model="form.publicRead" /><span>允许公开读（无签名也可 GET 对象）</span></div>
              <div class="field-help">关闭时需要有效 SigV4 签名或登录会话才能读取</div>
            </div>
          </div>
          <div class="divider"></div>
          <div class="alert-strip" style="margin:0">
            <div style="font-family:var(--serif);font-size:20px;color:var(--accent)">!</div>
            <div class="alert-strip-body">删除存储桶前必须先删除桶内全部对象；非空桶会被服务端拒绝删除。</div>
          </div>
        </div>
        <div class="drawer-foot">
          <button class="btn" @click="drawer = false">取消</button>
          <button class="btn btn--primary" :disabled="saving" @click="save">{{ saving ? '保存中…' : editing ? '保存修改' : '创建' }}</button>
        </div>
      </aside>
    </template>
  </div>
</template>
