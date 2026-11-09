<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { fmtTime, relTime } from '../utils'
import { session } from '../session'

const keys = ref([])
const allPerms = ref([])
const creds = ref(null)
const loading = ref(true)
const drawer = ref(false)
const saving = ref(false)
const busy = ref('')
const created = ref(null)
const form = ref({ name: '', expiresInDays: 0, scopes: [] })
const copied = ref('')

const endpoint = computed(() => creds?.endpoint || '/s3')
const fullEndpoint = computed(() => window.location.origin + endpoint.value)
const region = computed(() => creds?.region || 'us-east-1')

async function load() {
  loading.value = true
  try {
    const [k, c, p] = await Promise.all([api('/keys'), api('/s3/credentials'), api('/permissions')])
    keys.value = k || []
    creds.value = c
    allPerms.value = p?.permissions || []
  } catch (e) { notifyError(e, '加载密钥失败') }
  finally { loading.value = false }
}
onMounted(load)

function openCreate() {
  form.value = { name: '', expiresInDays: 0, scopes: [] }
  drawer.value = true
}
function toggleScope(p) {
  const i = form.value.scopes.indexOf(p)
  if (i >= 0) form.value.scopes.splice(i, 1)
  else form.value.scopes.push(p)
}

async function create() {
  if (!form.value.name.trim()) { notifyError(new Error('请填写密钥名称')); return }
  saving.value = true
  try {
    const r = await api('/keys', {
      method: 'POST',
      body: {
        name: form.value.name.trim(),
        expiresInDays: Number(form.value.expiresInDays) || 0,
        scopes: form.value.scopes,
      },
    })
    created.value = r
    drawer.value = false
    notify('密钥已创建，请立即保存 SecretAccessKey', { title: '仅显示一次' })
    await load()
  } catch (e) { notifyError(e, '创建失败') } finally { saving.value = false }
}

async function revoke(k) {
  if (!window.confirm(`确认撤销密钥「${k.name}」？使用该密钥的 S3 请求会立即失效。`)) return
  busy.value = k.id
  try {
    await api('/keys/' + encodeURIComponent(k.id), { method: 'DELETE' })
    notify(`密钥 ${k.name} 已撤销`)
    await load()
  } catch (e) { notifyError(e, '撤销失败') } finally { busy.value = '' }
}

function copy(text, label) {
  copied.value = label
  navigator.clipboard?.writeText(text).then(
    () => notify(label + ' 已复制到剪贴板'),
    () => notifyError(new Error('浏览器未授权剪贴板')),
  )
  setTimeout(() => { copied.value = '' }, 1400)
}

const awsEnv = computed(() => `export AWS_ACCESS_KEY_ID=${created.value?.accessKeyId || '<AccessKeyId>'}
export AWS_SECRET_ACCESS_KEY=${created.value?.secretAccessKey || '<SecretAccessKey>'}
aws --endpoint-url ${fullEndpoint.value} s3 ls`)
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Credentials / API 密钥</div>
        <h1 class="page-title">API 密钥与 S3 凭据</h1>
        <p class="page-desc">同一把密钥既可作为 REST <code class="code">Bearer</code> 令牌，也可作为 S3 SigV4 的 AccessKeyId / SecretAccessKey。每个用户只看到自己的密钥。</p>
      </div>
      <div class="btn-group">
        <button class="btn" @click="load">↻ 刷新</button>
        <button class="btn" @click="go('docs', 's3-api')">S3 接口文档 →</button>
        <button class="btn btn--primary" @click="openCreate">＋ 创建密钥</button>
      </div>
    </div>

    <!-- 一次性明文 -->
    <div v-if="created" class="alert-strip" style="display:block;border-left-width:3px;margin-bottom:22px">
      <div style="display:flex;justify-content:space-between;gap:12px;align-items:flex-start">
        <div>
          <div class="alert-strip-title">⚠ SecretAccessKey 仅显示这一次，请立即保存</div>
          <div class="alert-strip-body">关闭此提示后无法再次取出明文，只能重新创建密钥。</div>
        </div>
        <button class="icon-btn" @click="created = null">✕</button>
      </div>
      <dl class="kv" style="grid-template-columns:150px 1fr;margin-top:16px">
        <dt>AccessKeyId</dt>
        <dd class="mono" style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">{{ created.accessKeyId }}<button class="btn btn--sm" @click="copy(created.accessKeyId, 'AccessKeyId')">复制</button></dd>
        <dt>SecretAccessKey</dt>
        <dd class="mono" style="word-break:break-all;display:flex;gap:8px;align-items:center;flex-wrap:wrap">{{ created.secretAccessKey }}<button class="btn btn--sm" @click="copy(created.secretAccessKey, 'SecretAccessKey')">复制</button></dd>
      </dl>
      <pre class="codeblock" style="margin-top:16px"><code>{{ awsEnv }}</code></pre>
      <div style="display:flex;gap:8px;margin-top:14px">
        <button class="btn btn--sm" @click="copy(awsEnv, '环境变量')">复制环境变量</button>
        <button class="btn btn--quiet btn--sm" @click="created = null">我已保存，关闭</button>
      </div>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">S3 接入点</h2><p class="section-note">用 AWS SDK / aws-cli / s3cmd 直接访问，无需改造业务代码</p></div></div>
      <div class="panel" style="margin-top:18px">
        <dl class="kv">
          <dt>Endpoint</dt><dd><code class="code">{{ fullEndpoint }}</code> <button class="btn btn--quiet btn--sm" style="padding:2px 6px" @click="copy(fullEndpoint, 'Endpoint')">复制</button></dd>
          <dt>Region</dt><dd class="mono">{{ region }}</dd>
          <dt>签名版本</dt><dd class="mono">AWS Signature V4 · path-style addressing</dd>
          <dt>示例命令</dt><dd><code class="code">{{ creds?.example || 'aws --endpoint-url ' + fullEndpoint + ' s3 ls' }}</code></dd>
          <dt>说明</dt><dd class="tiny muted">{{ creds?.note }}</dd>
        </dl>
      </div>
    </div>

    <div class="section">
      <div class="section-head">
        <div><h2 class="section-title">密钥列表</h2><p class="section-note">共 {{ keys.length }} 把 · 作用域留空表示继承用户全部权限</p></div>
      </div>
      <div class="table-wrap" style="margin-top:18px">
        <table>
          <thead><tr><th>名称</th><th>AccessKeyId</th><th>Secret</th><th>作用域</th><th>状态</th><th>创建 / 最后使用</th><th></th></tr></thead>
          <tbody>
            <tr v-for="k in keys" :key="k.id" :class="{ 'is-unhealthy': k.revoked }">
              <td>
                <div class="cell-main">{{ k.name }}</div>
                <div class="cell-sub mono tiny">{{ k.id }} · {{ k.userId === session.user?.id ? '本人' : k.userId }}</div>
              </td>
              <td class="mono tiny">{{ k.accessKey }}</td>
              <td class="mono tiny muted">{{ k.secretHint || '••••••••' }}</td>
              <td style="max-width:220px">
                <div class="inline-list">
                  <span v-for="s in (k.scopes || []).slice(0, 3)" :key="s" class="tag">{{ s }}</span>
                  <span v-if="(k.scopes || []).length > 3" class="tag">+{{ k.scopes.length - 3 }}</span>
                  <span v-if="!(k.scopes || []).length" class="tag tag--good">全部权限</span>
                </div>
              </td>
              <td><span class="status" :class="k.revoked ? 'status--disabled' : 'status--healthy'">{{ k.revoked ? '已撤销' : '有效' }}</span><div v-if="k.expiresAt" class="cell-sub tiny">到期 {{ fmtTime(k.expiresAt) }}</div></td>
              <td class="muted tiny nowrap">{{ fmtTime(k.createdAt) }}<div class="cell-sub">{{ k.lastUsedAt ? relTime(k.lastUsedAt) : '从未使用' }}</div></td>
              <td>
                <div class="row-actions">
                  <button class="btn btn--quiet btn--sm" @click="copy(k.accessKey, 'AccessKeyId')">{{ copied === 'AccessKeyId' ? '已复制' : '复制' }}</button>
                  <button v-if="!k.revoked" class="btn btn--quiet btn--sm" :disabled="busy === k.id" @click="revoke(k)">撤销</button>
                </div>
              </td>
            </tr>
            <tr v-if="!keys.length && !loading"><td colspan="7"><div class="empty"><div class="empty-title">还没有密钥</div><p>创建一把密钥即可用标准 S3 客户端接入。</p><button class="btn btn--primary btn--sm" @click="openCreate">创建密钥</button></div></td></tr>
            <tr v-if="loading"><td colspan="7"><div class="empty"><span class="spinner"></span> 正在加载…</div></td></tr>
          </tbody>
        </table>
      </div>
      <p class="tiny muted" style="margin-top:12px">SecretAccessKey 在服务端以 AES-256-GCM 加密存储，鉴权使用 SHA-256 摘要比对；明文不会写入日志或持久化文件。</p>
    </div>

    <!-- 创建抽屉 -->
    <template v-if="drawer">
      <div class="drawer-backdrop" @click="drawer = false"></div>
      <aside class="drawer">
        <div class="drawer-head">
          <div>
            <div class="eyebrow">New Credential</div>
            <h2 class="section-title">创建 API 密钥</h2>
            <p class="section-note">生成的密钥归属当前登录用户，用于 REST 与 S3 双通道鉴权</p>
          </div>
          <button class="icon-btn" @click="drawer = false">✕</button>
        </div>
        <div class="drawer-body">
          <div class="form-grid">
            <div class="field" style="grid-column:1/-1">
              <label>密钥名称 <span class="req">*</span></label>
              <input v-model="form.name" placeholder="例如 laptop-awscli / ci-pipeline" />
            </div>
            <div class="field">
              <label>有效期（天）</label>
              <input v-model.number="form.expiresInDays" type="number" min="0" />
              <div class="field-help">0 表示永不过期；到期后密钥自动失效</div>
            </div>
          </div>
          <div class="field-group">
            <h3 class="field-group-title">作用域（可选）</h3>
            <p class="section-note" style="margin-bottom:14px">留空 = 继承用户当前全部权限；选中后密钥仅能执行这些操作，适合最小权限下发。</p>
            <div style="display:grid;grid-template-columns:repeat(2,1fr);gap:10px">
              <label v-for="p in allPerms" :key="p" class="checkbox-row" style="padding:8px 10px;border:1px solid var(--line);background:var(--surface);cursor:pointer">
                <input type="checkbox" :checked="form.scopes.includes(p)" @change="toggleScope(p)" />
                <span class="mono tiny">{{ p }}</span>
              </label>
            </div>
            <div style="margin-top:12px"><button class="btn btn--quiet btn--sm" @click="form.scopes = []">清空选择（继承全部权限）</button></div>
          </div>
          <div class="divider"></div>
          <div class="alert-strip" style="margin:0">
            <div style="font-family:var(--serif);font-size:20px;color:var(--accent)">!</div>
            <div class="alert-strip-body">创建成功后 SecretAccessKey 明文只展示一次。请保存到密码管理器，之后无法找回。</div>
          </div>
        </div>
        <div class="drawer-foot">
          <button class="btn" @click="drawer = false">取消</button>
          <button class="btn btn--primary" :disabled="saving" @click="create">{{ saving ? '生成中…' : '生成密钥' }}</button>
        </div>
      </aside>
    </template>
  </div>
</template>
