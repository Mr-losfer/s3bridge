<script setup>
import { ref, onMounted, computed, watch } from 'vue'
import { api, mcpRPC } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { session } from '../session'

const tools = ref([])
const resources = ref([])
const loading = ref(true)
const site = ref({})
const selectedTool = ref('')
const argsText = ref('{}')
const running = ref(false)
const output = ref('')
const outputIsError = ref(false)
const copied = ref('')

// 当前登录用户的 API Key：列表 + 按需解密后的完整凭据
const myKeys = ref([])
const selectedKeyId = ref('')
const keyDetail = ref(null)
const keyLoading = ref(false)
const creatingKey = ref(false)
const showKey = ref(true)

const groups = computed(() => {
  const defs = [
    { key: 'site', label: '站点与系统', hint: '查看站点信息、统计与系统设置', match: (n) => /^(site_|system_)/.test(n) },
    { key: 'plugin', label: '插件开发与运维', hint: '文档、调试、差量/全量修改、更新、参数修改、定时检测', match: (n) => /^plugin_/.test(n) },
    { key: 'alert', label: '告警', hint: '查看与确认插件异常告警', match: (n) => /^alerts_/.test(n) },
    { key: 'storage', label: '上传与存储桶', hint: '上传测试、对象列表、桶管理', match: (n) => /^(upload_|uploads_|bucket_)/.test(n) },
    { key: 'access', label: '用户与凭据', hint: '多用户管理与 API Key', match: (n) => /^(user_|apikey_)/.test(n) },
  ]
  const rest = new Set(tools.value.map((t) => t.name))
  const out = defs.map((g) => {
    const items = tools.value.filter((t) => g.match(t.name))
    items.forEach((t) => rest.delete(t.name))
    return { ...g, items }
  })
  if (rest.size) out.push({ key: 'other', label: '其他', hint: '', items: tools.value.filter((t) => rest.has(t.name)) })
  return out.filter((g) => g.items.length)
})

const currentTool = computed(() => tools.value.find((t) => t.name === selectedTool.value))
const activeToken = computed(() => keyDetail.value?.secretAccessKey || '<请先创建 API Key>')
const activeAccess = computed(() => keyDetail.value?.accessKeyId || '<AccessKeyId>')
// 隐藏明文时，示例代码中的凭据也一并遮蔽，避免屏幕上仍能看到密钥。
const visibleToken = computed(() => showKey.value ? activeToken.value : '<已隐藏，点「显示明文」查看>')

async function load() {
  loading.value = true
  try {
    const [list, res, s] = await Promise.all([
      mcpRPC('tools/list', {}), mcpRPC('resources/list', {}), api('/site').catch(() => ({})),
    ])
    tools.value = list?.result?.tools || []
    resources.value = res?.result?.resources || []
    site.value = s || {}
    if (!selectedTool.value && tools.value.length) choose(tools.value[0].name)
  } catch (e) { notifyError(e, '加载 MCP 工具清单失败') }
  finally { loading.value = false }
}
onMounted(load)

// 会话就绪后再拉取当前用户密钥；登录状态变化时自动刷新，避免显示他人凭据。
watch(() => session.user?.id, (id) => { if (id) loadKeys() }, { immediate: true })

async function loadKeys() {
  keyLoading.value = true
  try {
    const list = await api('/keys')
    myKeys.value = (list || []).filter((k) => k.userId === session.user?.id && !k.revoked)
    if (!myKeys.value.some((k) => k.id === selectedKeyId.value)) {
      selectedKeyId.value = myKeys.value[0]?.id || ''
    }
    keyDetail.value = selectedKeyId.value
      ? await api('/keys/' + encodeURIComponent(selectedKeyId.value) + '/secret')
      : null
  } catch (e) { notifyError(e, '加载当前用户 API Key 失败') }
  finally { keyLoading.value = false }
}

async function selectKey() {
  if (!selectedKeyId.value) { keyDetail.value = null; return }
  try { keyDetail.value = await api('/keys/' + encodeURIComponent(selectedKeyId.value) + '/secret') }
  catch (e) { notifyError(e, '查看密钥失败') }
}

async function createKey() {
  creatingKey.value = true
  try {
    const r = await api('/keys', {
      method: 'POST',
      body: { name: 'mcp-' + new Date().toISOString().slice(0, 10), scopes: [] },
    })
    selectedKeyId.value = r?.id || ''
    notify('API Key 已创建，下方代码已自动填入')
    await loadKeys()
  } catch (e) { notifyError(e, '创建密钥失败') }
  finally { creatingKey.value = false }
}

function choose(name) {
  selectedTool.value = name
  const t = tools.value.find((x) => x.name === name)
  const required = t?.inputSchema?.required || []
  const props = t?.inputSchema?.properties || {}
  const sample = {}
  for (const k of required) {
    const p = props[k] || {}
    if (p.type === 'number') sample[k] = 0
    else if (p.type === 'boolean') sample[k] = true
    else if (p.type === 'object') sample[k] = {}
    else if (p.type === 'array') sample[k] = []
    else if (k === 'id') sample[k] = 'workbuddy'
    else if (k === 'name') sample[k] = 'demo'
    else sample[k] = ''
  }
  argsText.value = JSON.stringify(sample, null, 2)
  output.value = ''
}

async function run() {
  let args
  try { args = JSON.parse(argsText.value || '{}') }
  catch (e) { notifyError(new Error('参数不是合法 JSON：' + e.message)); return }
  running.value = true
  outputIsError.value = false
  try {
    const r = await mcpRPC('tools/call', { name: selectedTool.value, arguments: args })
    if (r?.error) {
      output.value = JSON.stringify(r.error, null, 2)
      outputIsError.value = true
    } else {
      const c = r?.result?.content?.[0]
      output.value = c?.text || JSON.stringify(r?.result, null, 2)
      outputIsError.value = !!r?.result?.isError
    }
  } catch (e) { output.value = String(e); outputIsError.value = true }
  finally { running.value = false }
}

function jumpToTool(name) {
  choose(name)
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function copy(text, label) {
  // 隐藏明文时禁止复制真实凭据，避免「屏幕看不到但剪贴板是明文」的错觉。
  if (!showKey.value && label.startsWith('mcp-')) {
    notifyError(new Error('请先点「显示明文」再复制'))
    return
  }
  copied.value = label
  navigator.clipboard?.writeText(text).then(
    () => notify(label + ' 已复制到剪贴板'),
    () => notifyError(new Error('浏览器未授权剪贴板')),
  )
  setTimeout(() => { copied.value = '' }, 1400)
}

const envExample = computed(() => `# S3Bridge MCP / REST / S3 接入（已填入当前登录用户的真实密钥）
export S3BRIDGE_URL=${window.location.origin}
export S3BRIDGE_TOKEN=${visibleToken.value}

# S3 标准接口（AWS SigV4）
export AWS_ACCESS_KEY_ID=${activeAccess.value}
export AWS_SECRET_ACCESS_KEY=${visibleToken.value}
aws --endpoint-url ${window.location.origin}/s3 s3 ls`)

const httpExample = computed(() => `curl -s -X POST ${window.location.origin}/mcp \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${visibleToken.value}" \\
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"site_info","arguments":{}}}'`)

const desktopConfig = computed(() => `{
  "mcpServers": {
    "s3bridge": {
      "command": "s3bridge-mcp",
      "env": {
        "S3BRIDGE_URL": "${window.location.origin}",
        "S3BRIDGE_TOKEN": "${visibleToken.value}"
      }
    }
  }
}`)
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">MCP / Model Context Protocol</div>
        <h1 class="page-title">MCP 能力</h1>
        <p class="page-desc">后台每个按钮都对应一个 MCP 工具：AI 客户端可执行站点查看、插件文档、开发调试、差量与全量修改、插件更新、参数修改等全部操作，结果与审计记录完全一致。</p>
      </div>
      <div class="btn-group">
        <button class="btn" @click="load">↻ 刷新</button>
        <button class="btn" @click="go('docs', 'mcp')">完整文档 →</button>
      </div>
    </div>

    <!-- 当前登录用户的 API Key：明文 + 已填好真实凭据的对接代码 -->
    <div class="section">
      <div class="section-head">
        <div>
          <h2 class="section-title">当前登录用户的 API Key</h2>
          <p class="section-note">下方代码已自动填入 <strong>{{ session.user?.displayName || session.user?.username || '当前账号' }}</strong> 的真实密钥，复制即可使用；无需另找地方生成，随时可回来查看。</p>
        </div>
        <div class="btn-group">
          <button class="btn btn--sm" :disabled="keyLoading" @click="loadKeys">↻ 刷新密钥</button>
          <button class="btn btn--sm" @click="go('keys')">管理密钥 →</button>
        </div>
      </div>
      <div class="panel" style="margin-top:18px">
        <div v-if="keyDetail">
          <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:18px">
            <span class="tag tag--good">归属：{{ session.user?.username }}</span>
            <select v-model="selectedKeyId" style="max-width:280px" @change="selectKey">
              <option v-for="k in myKeys" :key="k.id" :value="k.id">{{ k.name }} · {{ k.id }}</option>
            </select>
            <span class="status" :class="keyDetail.revoked ? 'status--disabled' : 'status--healthy'">{{ keyDetail.revoked ? '已撤销' : '有效' }}</span>
            <button class="btn btn--quiet btn--sm" @click="showKey = !showKey">{{ showKey ? '隐藏明文' : '显示明文' }}</button>
            <button class="btn btn--sm" :disabled="creatingKey" @click="createKey">＋ 新建密钥</button>
            <span v-if="keyLoading" class="spinner"></span>
          </div>
          <dl class="kv" style="grid-template-columns:175px 1fr">
            <dt>API Key / Bearer</dt>
            <dd class="mono" style="word-break:break-all;display:flex;gap:8px;align-items:center;flex-wrap:wrap">
              <span>{{ showKey ? keyDetail.secretAccessKey : '••••••••••••••••••••••••' }}</span>
              <button class="btn btn--sm" @click="copy(keyDetail.secretAccessKey, 'mcp-token')">{{ copied === 'mcp-token' ? '已复制' : '复制' }}</button>
            </dd>
            <dt>AccessKeyId</dt>
            <dd class="mono" style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
              <span>{{ keyDetail.accessKeyId }}</span>
              <button class="btn btn--sm" @click="copy(keyDetail.accessKeyId, 'mcp-access')">{{ copied === 'mcp-access' ? '已复制' : '复制' }}</button>
            </dd>
            <dt>SecretAccessKey</dt>
            <dd class="mono" style="word-break:break-all;display:flex;gap:8px;align-items:center;flex-wrap:wrap">
              <span>{{ showKey ? keyDetail.secretAccessKey : '••••••••••••••••••••••••' }}</span>
              <button class="btn btn--sm" @click="copy(keyDetail.secretAccessKey, 'mcp-secret')">{{ copied === 'mcp-secret' ? '已复制' : '复制' }}</button>
            </dd>
          </dl>
          <div style="display:flex;justify-content:space-between;align-items:center;gap:12px;margin:22px 0 10px">
            <div class="eyebrow">一键接入代码（已填入真实密钥）</div>
            <button class="btn btn--sm" @click="copy(envExample, 'mcp-env')">{{ copied === 'mcp-env' ? '已复制' : '复制全部' }}</button>
          </div>
          <pre class="codeblock"><code>{{ envExample }}</code></pre>
          <p class="tiny muted" style="margin:12px 0 0">明文由服务端 AES-256-GCM 加密保存，仅本人或管理员登录后按需解密；下方 HTTP / stdio 示例也已自动替换为这把密钥。</p>
        </div>
        <div v-else class="empty">
          <div class="empty-title">当前账号还没有 API Key</div>
          <p>点一下即可创建，随后这里会直接显示明文与现成的 MCP 对接代码。</p>
          <button class="btn btn--primary btn--sm" :disabled="creatingKey || keyLoading" @click="createKey">
            {{ creatingKey ? '创建中…' : '立即创建 API Key' }}
          </button>
        </div>
      </div>
    </div>

    <div class="metrics" style="grid-template-columns:repeat(4,1fr)">
      <div class="metric"><div class="metric-label">工具数量</div><div class="metric-value">{{ tools.length }}<span class="metric-unit">tools</span></div><div class="metric-foot">tools/list 实时返回</div></div>
      <div class="metric"><div class="metric-label">文档资源</div><div class="metric-value">{{ resources.length }}<span class="metric-unit">resources</span></div><div class="metric-foot">resources/read 可读取</div></div>
      <div class="metric"><div class="metric-label">协议版本</div><div class="metric-value" style="font-size:26px">2024-11-05</div><div class="metric-foot">JSON-RPC 2.0</div></div>
      <div class="metric"><div class="metric-label">HTTP 端点</div><div class="metric-value" style="font-size:26px">POST /mcp</div><div class="metric-foot">另支持 stdio 独立进程</div></div>
    </div>

    <!-- 在线调试台 -->
    <div class="section">
      <div class="section-head"><div><h2 class="section-title">工具在线调试</h2><p class="section-note">直接调用 tools/call，参数为 JSON 对象；权限与后台一致</p></div></div>
      <div style="display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:18px;margin-top:18px">
        <div class="panel">
          <div class="field" style="margin-bottom:16px">
            <label>选择工具</label>
            <select v-model="selectedTool" @change="choose(selectedTool)">
              <option v-for="t in tools" :key="t.name" :value="t.name">{{ t.name }}</option>
            </select>
            <div class="field-help">{{ currentTool?.description }}</div>
          </div>
          <div class="field">
            <label>Arguments（JSON）</label>
            <textarea v-model="argsText" style="min-height:190px" spellcheck="false"></textarea>
            <div class="field-help">
              必填：{{ (currentTool?.inputSchema?.required || []).join(', ') || '无' }}
            </div>
          </div>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:18px">
            <button class="btn" @click="choose(selectedTool)">重置示例</button>
            <button class="btn btn--primary" :disabled="running || !selectedTool" @click="run">
              <span v-if="running" class="spinner"></span>{{ running ? '执行中…' : '执行调用' }}
            </button>
          </div>
        </div>
        <div class="panel" :style="outputIsError ? 'border-color:var(--accent)' : ''">
          <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:14px">
            <span class="tag" :class="outputIsError ? 'tag--accent' : 'tag--good'">{{ outputIsError ? 'ERROR' : 'RESULT' }}</span>
            <button v-if="output" class="btn btn--quiet btn--sm" @click="copy(output, '结果')">复制结果</button>
          </div>
          <pre class="codeblock" style="margin:0;min-height:280px;max-height:460px">{{ output || '// 点击「执行调用」查看工具返回的 JSON\n// 结果来自 MCP Server，与后台页面同源' }}</pre>
        </div>
      </div>
    </div>

    <!-- 工具清单 -->
    <div class="section">
      <div class="section-head"><div><h2 class="section-title">工具清单</h2><p class="section-note">按能力分组，点击行可载入调试台</p></div></div>
      <div v-for="g in groups" :key="g.key" style="margin-top:22px">
        <div style="display:flex;align-items:baseline;gap:12px;margin-bottom:10px">
          <h3 class="field-group-title" style="font-size:17px;margin:0">{{ g.label }}</h3>
          <span class="tiny muted">{{ g.hint }} · {{ g.items.length }} 个工具</span>
        </div>
        <div class="table-wrap">
          <table>
            <thead><tr><th style="width:230px">工具名</th><th>说明</th><th style="width:230px">必填参数</th><th style="width:60px"></th></tr></thead>
            <tbody>
              <tr v-for="t in g.items" :key="t.name" class="is-clickable" @click="jumpToTool(t.name)">
                <td class="mono tiny strong">{{ t.name }}</td>
                <td class="tiny">{{ t.description }}</td>
                <td class="mono tiny muted">{{ (t.inputSchema?.required || []).join(', ') || '—' }}</td>
                <td class="right"><span class="muted">→</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div v-if="loading" class="empty"><span class="spinner"></span> 正在加载工具…</div>
    </div>

    <!-- 接入配置 -->
    <div class="section">
      <div class="section-head"><div><h2 class="section-title">接入配置</h2><p class="section-note">HTTP 直连或 stdio 独立进程，二选一 · 代码中的密钥已按上方选中的密钥自动填写</p></div></div>
      <div style="display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:18px;margin-top:18px">
        <div>
          <div class="eyebrow" style="margin-bottom:10px">HTTP / Streamable HTTP</div>
          <pre class="codeblock"><code>{{ httpExample }}</code></pre>
        </div>
        <div>
          <div class="eyebrow" style="margin-bottom:10px">Claude Desktop / Cursor（stdio）</div>
          <pre class="codeblock"><code>{{ desktopConfig }}</code></pre>
        </div>
      </div>
      <div class="panel" style="margin-top:18px">
        <div class="eyebrow" style="margin-bottom:10px">文档资源（resources/list）</div>
        <div class="inline-list">
          <span v-for="r in resources" :key="r.uri" class="tag" :title="r.description">{{ r.uri }}</span>
          <span v-if="!resources.length" class="muted tiny">暂无资源</span>
        </div>
        <p class="tiny muted" style="margin:12px 0 0">工具需要鉴权：HTTP 端点使用 <code class="code">Authorization: Bearer &lt;API Key&gt;</code>，stdio 模式通过 <code class="code">S3BRIDGE_TOKEN</code> 环境变量传入。当前账号的密钥明文与可复制代码就在本页顶部，也可在「API 密钥」页面随时查看或新建。</p>
      </div>
    </div>
  </div>
</template>
