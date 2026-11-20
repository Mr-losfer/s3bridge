<script setup>
import { ref, onMounted, computed, watch } from 'vue'
import {
  KeyRound, Plus, RefreshCw, Eye, EyeOff, Copy, TerminalSquare, Wrench, BookOpen,
  ShieldCheck, Server, ChevronRight, Search, Loader2, Play, ExternalLink, CircleCheck,
} from 'lucide-vue-next'
import { api, mcpRPC } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { session } from '../session'
import PageHeader from '../components/PageHeader.vue'
import StatCard from '../components/StatCard.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Input from '../components/ui/Input.vue'
import Select from '../components/ui/Select.vue'
import Field from '../components/ui/Field.vue'
import CodeBlock from '../components/CodeBlock.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'

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
const toolQuery = ref('')

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
  return out
    .filter((g) => g.items.length)
    .map((g) => ({ ...g, items: g.items.filter((t) => !toolQuery.value.trim() || (t.name + ' ' + t.description).toLowerCase().includes(toolQuery.value.trim().toLowerCase())) }))
    .filter((g) => g.items.length)
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
    <PageHeader
      title="MCP 能力"
      description="后台每个按钮都对应一个 MCP 工具：AI 客户端可执行站点查看、插件文档、开发调试、差量与全量修改、插件更新、参数修改等全部操作，结果与审计记录完全一致。"
    >
      <template #actions>
        <Button variant="outline" @click="load"><RefreshCw />刷新</Button>
        <Button variant="ghost" @click="go('docs', 'mcp')"><BookOpen />完整文档<ExternalLink /></Button>
      </template>
    </PageHeader>

    <!-- 当前用户 API Key -->
    <Card class="overflow-hidden">
      <div class="flex flex-col gap-3 border-b p-4 sm:flex-row sm:items-center sm:justify-between">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <KeyRound class="size-4 text-muted-foreground" />
            <h2 class="text-sm font-semibold">当前登录用户的 API Key</h2>
            <Badge variant="primary">{{ session.user?.displayName || session.user?.username }}</Badge>
          </div>
          <p class="mt-1 text-[11.5px] leading-relaxed text-muted-foreground">
            下方代码已自动填入真实密钥，复制即可使用；无需另找地方生成，随时可回来查看。
          </p>
        </div>
        <div class="flex shrink-0 items-center gap-1.5">
          <Button size="sm" variant="ghost" :disabled="keyLoading" @click="loadKeys"><RefreshCw />刷新密钥</Button>
          <Button size="sm" variant="outline" @click="go('keys')">管理密钥<ChevronRight /></Button>
        </div>
      </div>

      <div v-if="keyDetail" class="p-4">
        <div class="flex flex-wrap items-center gap-2">
          <Select v-model="selectedKeyId" class="w-full sm:w-64" @update:model-value="selectKey">
            <option v-for="k in myKeys" :key="k.id" :value="k.id">{{ k.name }} · {{ k.id }}</option>
          </Select>
          <Badge :variant="keyDetail.revoked ? 'outline' : 'success'">{{ keyDetail.revoked ? '已撤销' : '有效' }}</Badge>
          <Button size="sm" variant="ghost" @click="showKey = !showKey">
            <component :is="showKey ? EyeOff : Eye" />{{ showKey ? '隐藏明文' : '显示明文' }}
          </Button>
          <Button size="sm" variant="outline" :disabled="creatingKey" @click="createKey"><Plus />新建密钥</Button>
          <Loader2 v-if="keyLoading" class="size-3.5 animate-spin text-muted-foreground" />
        </div>

        <div class="mt-3 grid gap-2.5 text-[11.5px]">
          <div v-for="row in [
            { k: 'API Key / Bearer', v: showKey ? keyDetail.secretAccessKey : '••••••••••••••••••••••••', label: 'mcp-token' },
            { k: 'AccessKeyId', v: keyDetail.accessKeyId, label: 'mcp-access' },
            { k: 'SecretAccessKey', v: showKey ? keyDetail.secretAccessKey : '••••••••••••••••••••••••', label: 'mcp-secret' },
          ]" :key="row.k" class="grid gap-1 sm:grid-cols-[9rem_1fr] sm:items-center sm:gap-2">
            <span class="text-muted-foreground">{{ row.k }}</span>
            <div class="flex min-w-0 items-center gap-1.5">
              <code class="min-w-0 flex-1 break-all rounded border bg-muted/40 px-2 py-1 font-mono text-[10.5px]">{{ row.v }}</code>
              <Button size="icon-sm" variant="outline" aria-label="复制" @click="copy(row.v.startsWith('•') ? '' : row.v, row.label)">
                <component :is="copied === row.label ? CircleCheck : Copy" />
              </Button>
            </div>
          </div>
        </div>

        <div class="mt-4">
          <div class="mb-1.5 flex items-center justify-between">
            <p class="text-[10.5px] font-medium tracking-wide text-muted-foreground">一键接入代码（已填入真实密钥）</p>
            <Button size="sm" variant="ghost" class="h-6 px-1.5 text-[10.5px]" @click="copy(envExample, 'mcp-env')"><Copy />复制全部</Button>
          </div>
          <CodeBlock :code="envExample" label="env" max-height="240px" />
          <p class="mt-2 text-[10.5px] leading-relaxed text-muted-foreground">
            明文由服务端 AES-256-GCM 加密保存，仅本人或管理员登录后按需解密；下方 HTTP / stdio 示例也已自动替换为这把密钥。
          </p>
        </div>
      </div>

      <div v-else-if="!keyLoading" class="p-4">
        <Empty title="当前账号还没有 API Key" description="点一下即可创建，随后这里会直接显示明文与现成的 MCP 对接代码。">
          <Button size="sm" :disabled="creatingKey" @click="createKey"><Plus />{{ creatingKey ? '创建中…' : '立即创建 API Key' }}</Button>
        </Empty>
      </div>
      <div v-else class="space-y-2 p-4"><Skeleton class="h-8" /><Skeleton class="h-24" /></div>
    </Card>

    <div class="mt-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard label="工具数量" :value="tools.length" unit="tools" hint="tools/list 实时返回" :icon="Wrench" />
      <StatCard label="文档资源" :value="resources.length" unit="resources" hint="resources/read 可读取" :icon="BookOpen" />
      <StatCard label="协议版本" value="2024-11-05" hint="JSON-RPC 2.0" :icon="ShieldCheck" value-class="text-lg sm:text-2xl" />
      <StatCard label="HTTP 端点" value="POST /mcp" hint="另支持 stdio 独立进程" :icon="Server" value-class="text-lg sm:text-2xl" />
    </div>

    <!-- 在线调试台 -->
    <div class="mt-6">
      <div class="mb-3 flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 class="text-sm font-semibold">工具在线调试</h2>
          <p class="mt-0.5 text-[11.5px] text-muted-foreground">直接调用 tools/call，参数为 JSON 对象；权限与后台一致。</p>
        </div>
        <Badge variant="outline">tools/call</Badge>
      </div>

      <div class="grid gap-4 xl:grid-cols-2">
        <Card class="min-w-0 p-4">
          <Field label="选择工具">
            <Select :model-value="selectedTool" @update:model-value="(v) => { selectedTool = v; choose(v) }">
              <option v-for="t in tools" :key="t.name" :value="t.name">{{ t.name }}</option>
            </Select>
          </Field>
          <p class="mt-1.5 min-h-[2.4em] text-[11px] leading-relaxed text-muted-foreground">{{ currentTool?.description }}</p>

          <Field label="Arguments（JSON）" class="mt-3">
            <textarea
              v-model="argsText" spellcheck="false"
              class="min-h-[190px] w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-[11.5px] leading-relaxed shadow-xs focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25"
            />
          </Field>
          <p class="mt-1.5 text-[11px] text-muted-foreground">必填：{{ (currentTool?.inputSchema?.required || []).join(', ') || '无' }}</p>

          <div class="mt-3 flex items-center justify-end gap-2">
            <Button size="sm" variant="ghost" @click="choose(selectedTool)"><RefreshCw />重置示例</Button>
            <Button size="sm" :disabled="running || !selectedTool" @click="run">
              <Loader2 v-if="running" class="animate-spin" />
              <Play v-else />
              {{ running ? '执行中…' : '执行调用' }}
            </Button>
          </div>
        </Card>

        <Card class="min-w-0 p-4">
          <div class="mb-2 flex items-center justify-between">
            <div class="flex items-center gap-2">
              <Badge :variant="outputIsError ? 'destructive' : 'success'">{{ outputIsError ? 'ERROR' : 'RESULT' }}</Badge>
              <span class="font-mono text-[10.5px] text-muted-foreground">{{ selectedTool }}</span>
            </div>
            <Button v-if="output" size="sm" variant="ghost" @click="copy(output, '结果')"><Copy />复制</Button>
          </div>
          <CodeBlock :code="output || '// 点击「执行调用」查看工具返回的 JSON\n// 结果来自 MCP Server，与后台页面同源'" label="response" max-height="380px" />
        </Card>
      </div>
    </div>

    <!-- 工具清单 -->
    <div class="mt-6">
      <div class="mb-3 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 class="text-sm font-semibold">工具清单</h2>
          <p class="mt-0.5 text-[11.5px] text-muted-foreground">按能力分组，点击行可载入调试台。</p>
        </div>
        <div class="relative w-full sm:w-64">
          <Search class="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input v-model="toolQuery" placeholder="搜索工具名或说明" class="h-8 pl-8 text-xs" />
        </div>
      </div>

      <div v-if="loading" class="space-y-2"><Skeleton v-for="i in 5" :key="i" class="h-24 rounded-xl" /></div>
      <div v-else class="space-y-3">
        <Card v-for="g in groups" :key="g.key" class="overflow-hidden">
          <div class="flex flex-wrap items-center gap-2 border-b bg-muted/25 px-4 py-2.5">
            <h3 class="text-[12.5px] font-semibold">{{ g.label }}</h3>
            <span class="text-[10.5px] text-muted-foreground">{{ g.hint }}</span>
            <Badge variant="outline" class="ml-auto">{{ g.items.length }}</Badge>
          </div>
          <div class="divide-y">
            <button
              v-for="t in g.items" :key="t.name"
              class="group flex w-full items-start gap-3 px-4 py-2.5 text-left transition-colors hover:bg-muted/40"
              @click="jumpToTool(t.name)"
            >
              <span class="mt-0.5 min-w-0 flex-1">
                <span class="block font-mono text-[11px] font-medium text-foreground">{{ t.name }}</span>
                <span class="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">{{ t.description }}</span>
                <span v-if="(t.inputSchema?.required || []).length" class="mt-1 block font-mono text-[10px] text-muted-foreground/80">
                  必填：{{ (t.inputSchema.required || []).join(', ') }}
                </span>
              </span>
              <ChevronRight class="mt-1 size-3.5 shrink-0 text-muted-foreground/40 transition-transform group-hover:translate-x-0.5 group-hover:text-muted-foreground" />
            </button>
          </div>
        </Card>
        <Card v-if="!groups.length">
          <Empty title="没有匹配的工具" description="换个关键词试试。" />
        </Card>
      </div>
    </div>

    <!-- 接入配置 -->
    <div class="mt-6">
      <div class="mb-3">
        <h2 class="text-sm font-semibold">接入配置</h2>
        <p class="mt-0.5 text-[11.5px] text-muted-foreground">HTTP 直连或 stdio 独立进程，二选一 · 代码中的密钥已按上方选中的密钥自动填写。</p>
      </div>
      <div class="grid gap-4 xl:grid-cols-2">
        <div class="min-w-0">
          <p class="mb-1.5 text-[10.5px] font-medium tracking-wide text-muted-foreground">HTTP / STREAMABLE HTTP</p>
          <CodeBlock :code="httpExample" label="curl" max-height="220px" />
        </div>
        <div class="min-w-0">
          <p class="mb-1.5 text-[10.5px] font-medium tracking-wide text-muted-foreground">CLAUDE DESKTOP / CURSOR（STDIO）</p>
          <CodeBlock :code="desktopConfig" label="mcp.json" max-height="220px" />
        </div>
      </div>

      <Card class="mt-4 p-4">
        <div class="flex items-center gap-2">
          <BookOpen class="size-4 text-muted-foreground" />
          <p class="text-[10.5px] font-medium tracking-wide text-muted-foreground">文档资源（resources/list）</p>
        </div>
        <div class="mt-2 flex flex-wrap gap-1.5">
          <Badge v-for="r in resources" :key="r.uri" variant="outline" :title="r.description">{{ r.uri }}</Badge>
          <span v-if="!resources.length" class="text-[11px] text-muted-foreground">暂无资源</span>
        </div>
        <p class="mt-2.5 text-[11px] leading-relaxed text-muted-foreground">
          工具需要鉴权：HTTP 端点使用 <code class="rounded border bg-muted px-1 py-px font-mono text-[10px]">Authorization: Bearer &lt;API Key&gt;</code>，
          stdio 模式通过 <code class="rounded border bg-muted px-1 py-px font-mono text-[10px]">S3BRIDGE_TOKEN</code> 环境变量传入。
        </p>
      </Card>
    </div>
  </div>
</template>
