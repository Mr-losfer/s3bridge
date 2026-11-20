<script setup>
import { ref, onMounted, computed } from 'vue'
import {
  KeyRound, Plus, RefreshCw, Copy, Eye, EyeOff, BookOpen, ShieldCheck, Trash2,
  CircleCheck, Server, X, ExternalLink,
} from 'lucide-vue-next'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { fmtTime, relTime } from '../utils'
import { session } from '../session'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Input from '../components/ui/Input.vue'
import Field from '../components/ui/Field.vue'
import Sheet from '../components/ui/Sheet.vue'
import Dialog from '../components/ui/Dialog.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'
import CodeBlock from '../components/CodeBlock.vue'

const keys = ref([])
const allPerms = ref([])
const creds = ref(null)
const loading = ref(true)
const drawer = ref(false)
const saving = ref(false)
const busy = ref('')
const created = ref(null)
const revealed = ref(null)
const revealBusy = ref('')
const showSecret = ref(true)
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
    notify('密钥已创建，之后随时可点「查看」再次获取明文')
    await load()
  } catch (e) { notifyError(e, '创建失败') } finally { saving.value = false }
}

// 明文由服务端按需解密返回，任何时间都能重新查看，不需要一次性抄录。
async function reveal(k) {
  revealBusy.value = k.id
  try {
    revealed.value = await api('/keys/' + encodeURIComponent(k.id) + '/secret')
    showSecret.value = true
  } catch (e) { notifyError(e, '查看密钥失败') }
  finally { revealBusy.value = '' }
}

async function revoke(k) {
  if (!window.confirm(`确认撤销密钥「${k.name}」？使用该密钥的 S3 请求会立即失效。`)) return
  busy.value = k.id
  try {
    await api('/keys/' + encodeURIComponent(k.id), { method: 'DELETE' })
    notify(`密钥 ${k.name} 已撤销`)
    if (revealed.value?.id === k.id) revealed.value = null
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

const revealCode = computed(() => {
  const r = revealed.value
  if (!r) return ''
  return `# REST / MCP：同一把密钥直接作为 Bearer Token
curl -s -H "Authorization: Bearer ${r.secretAccessKey}" ${window.location.origin}/api/me

# S3 标准接口（AWS SigV4）
export AWS_ACCESS_KEY_ID=${r.accessKeyId}
export AWS_SECRET_ACCESS_KEY=${r.secretAccessKey}
aws --endpoint-url ${fullEndpoint.value} s3 ls`
})
</script>

<template>
  <div>
    <PageHeader
      title="API 密钥与 S3 凭据"
      description="同一把密钥既可作为 REST Bearer 令牌，也可作为 S3 SigV4 的 AccessKeyId / SecretAccessKey。每个用户只看到自己的密钥，且随时可重新查看明文。"
    >
      <template #actions>
        <Button variant="outline" @click="load"><RefreshCw />刷新</Button>
        <Button variant="ghost" @click="go('docs', 's3-api')"><BookOpen />S3 文档</Button>
        <Button @click="openCreate"><Plus />创建密钥</Button>
      </template>
    </PageHeader>

    <!-- 创建结果 -->
    <div v-if="created" class="mb-5 overflow-hidden rounded-xl border border-success/25 bg-success/[0.05]">
      <div class="flex items-start gap-2.5 p-4">
        <CircleCheck class="mt-0.5 size-4 shrink-0 text-success" />
        <div class="min-w-0 flex-1">
          <p class="text-[13px] font-medium">密钥已创建</p>
          <p class="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">
            明文已加密保存，无需一次性抄录。关闭后随时可在列表点「查看」再次获取，也可在「MCP 能力」页面直接复制现成的对接代码。
          </p>
        </div>
        <Button size="icon-sm" variant="ghost" aria-label="关闭" @click="created = null"><X /></Button>
      </div>
      <div class="space-y-2 border-t border-success/15 px-4 py-3 text-[11.5px]">
        <div class="grid grid-cols-[6.5rem_1fr] items-center gap-2">
          <span class="text-muted-foreground">AccessKeyId</span>
          <div class="flex min-w-0 items-center gap-1.5">
            <code class="min-w-0 flex-1 break-all font-mono text-[10.5px]">{{ created.accessKeyId }}</code>
            <Button size="sm" variant="outline" @click="copy(created.accessKeyId, 'AccessKeyId')"><Copy />复制</Button>
          </div>
        </div>
        <div class="grid grid-cols-[6.5rem_1fr] items-center gap-2">
          <span class="text-muted-foreground">SecretAccessKey</span>
          <div class="flex min-w-0 items-center gap-1.5">
            <code class="min-w-0 flex-1 break-all font-mono text-[10.5px]">{{ created.secretAccessKey }}</code>
            <Button size="sm" variant="outline" @click="copy(created.secretAccessKey, 'SecretAccessKey')"><Copy />复制</Button>
          </div>
        </div>
      </div>
      <div class="px-4 pb-4">
        <CodeBlock :code="awsEnv" label="aws-cli" />
      </div>
    </div>

    <!-- S3 接入点 -->
    <Card class="p-4 sm:p-5">
      <div class="flex items-center gap-2">
        <Server class="size-4 text-muted-foreground" />
        <h2 class="text-sm font-semibold">S3 接入点</h2>
        <Badge variant="outline" class="ml-auto">SigV4 · path-style</Badge>
      </div>
      <div class="mt-3 space-y-2.5 text-[11.5px]">
        <div class="flex items-center gap-2">
          <span class="w-24 shrink-0 text-muted-foreground">Endpoint</span>
          <code class="min-w-0 flex-1 break-all rounded border bg-muted/50 px-1.5 py-0.5 font-mono text-[10.5px]">{{ fullEndpoint }}</code>
          <Button size="icon-sm" variant="ghost" aria-label="复制 Endpoint" @click="copy(fullEndpoint, 'Endpoint')"><Copy /></Button>
        </div>
        <div class="flex gap-2">
          <span class="w-24 shrink-0 text-muted-foreground">Region</span>
          <span class="font-mono text-[10.5px]">{{ region }}</span>
        </div>
        <div class="flex flex-col gap-1 sm:flex-row sm:gap-2">
          <span class="w-24 shrink-0 text-muted-foreground">示例命令</span>
          <code class="break-all font-mono text-[10.5px]">{{ creds?.example || 'aws --endpoint-url ' + fullEndpoint + ' s3 ls' }}</code>
        </div>
      </div>
    </Card>

    <!-- 密钥列表 -->
    <div class="mt-6">
      <div class="mb-3 flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 class="text-sm font-semibold">密钥列表</h2>
          <p class="mt-0.5 text-[11.5px] text-muted-foreground">共 {{ keys.length }} 把 · 作用域留空表示继承用户全部权限 · 点「查看」随时取回明文</p>
        </div>
        <Badge variant="success"><ShieldCheck class="size-3" />AES-256-GCM</Badge>
      </div>

      <Card class="overflow-hidden">
        <div v-if="loading" class="space-y-2 p-4"><Skeleton v-for="i in 3" :key="i" class="h-11" /></div>

        <div v-else class="hidden overflow-x-auto md:block">
          <table class="data-table">
            <thead>
              <tr><th>名称</th><th>AccessKeyId</th><th class="hidden lg:table-cell">Secret</th><th>作用域</th><th>状态</th><th class="hidden xl:table-cell">创建 / 最后使用</th><th class="w-36"></th></tr>
            </thead>
            <tbody>
              <tr v-for="k in keys" :key="k.id" :class="k.revoked && 'opacity-55'">
                <td>
                  <div class="font-medium">{{ k.name }}</div>
                  <div class="mt-0.5 font-mono text-[10px] text-muted-foreground">{{ k.id }} · {{ k.userId === session.user?.id ? '本人' : k.userId }}</div>
                </td>
                <td class="font-mono text-[10.5px]">{{ k.accessKey }}</td>
                <td class="hidden font-mono text-[10.5px] text-muted-foreground lg:table-cell">{{ k.secretHint || '••••••••' }}</td>
                <td class="max-w-[13rem]">
                  <div class="flex flex-wrap gap-1">
                    <Badge v-for="s in (k.scopes || []).slice(0, 3)" :key="s" variant="outline">{{ s }}</Badge>
                    <Badge v-if="(k.scopes || []).length > 3" variant="outline">+{{ k.scopes.length - 3 }}</Badge>
                    <Badge v-if="!(k.scopes || []).length" variant="success">全部权限</Badge>
                  </div>
                </td>
                <td>
                  <Badge :variant="k.revoked ? 'outline' : 'success'">{{ k.revoked ? '已撤销' : '有效' }}</Badge>
                  <div v-if="k.expiresAt" class="mt-1 text-[10px] text-muted-foreground">到期 {{ fmtTime(k.expiresAt) }}</div>
                </td>
                <td class="hidden whitespace-nowrap text-[11px] text-muted-foreground xl:table-cell">
                  {{ fmtTime(k.createdAt) }}<div class="mt-0.5 text-[10px]">{{ k.lastUsedAt ? relTime(k.lastUsedAt) : '从未使用' }}</div>
                </td>
                <td>
                  <div class="flex items-center justify-end gap-1">
                    <Button size="sm" variant="outline" :disabled="revealBusy === k.id" @click="reveal(k)"><Eye />{{ revealBusy === k.id ? '…' : '查看' }}</Button>
                    <Button size="icon-sm" variant="ghost" aria-label="复制 AccessKeyId" @click="copy(k.accessKey, 'AccessKeyId')"><Copy /></Button>
                    <Button v-if="!k.revoked" size="icon-sm" variant="ghost" aria-label="撤销" class="text-muted-foreground hover:text-destructive" :disabled="busy === k.id" @click="revoke(k)"><Trash2 /></Button>
                  </div>
                </td>
              </tr>
              <tr v-if="!keys.length">
                <td colspan="7">
                  <Empty title="还没有密钥" description="创建一把密钥即可用标准 S3 客户端接入，之后随时可回来查看明文。">
                    <Button size="sm" @click="openCreate"><Plus />创建密钥</Button>
                  </Empty>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="!loading" class="divide-y md:hidden">
          <div v-for="k in keys" :key="k.id" class="p-3.5" :class="k.revoked && 'opacity-55'">
            <div class="flex items-start gap-2">
              <div class="min-w-0 flex-1">
                <p class="truncate text-[13px] font-medium">{{ k.name }}</p>
                <p class="mt-0.5 break-all font-mono text-[10px] text-muted-foreground">{{ k.accessKey }}</p>
              </div>
              <Badge :variant="k.revoked ? 'outline' : 'success'">{{ k.revoked ? '已撤销' : '有效' }}</Badge>
            </div>
            <div class="mt-2 flex flex-wrap gap-1">
              <Badge v-for="s in (k.scopes || []).slice(0, 3)" :key="s" variant="outline">{{ s }}</Badge>
              <Badge v-if="!(k.scopes || []).length" variant="success">全部权限</Badge>
            </div>
            <div class="mt-2.5 flex items-center gap-1.5 border-t pt-2.5">
              <Button size="sm" variant="outline" class="flex-1" :disabled="revealBusy === k.id" @click="reveal(k)"><Eye />查看</Button>
              <Button size="sm" variant="ghost" @click="copy(k.accessKey, 'AccessKeyId')"><Copy />复制</Button>
              <Button v-if="!k.revoked" size="sm" variant="ghost" class="text-muted-foreground" :disabled="busy === k.id" @click="revoke(k)"><Trash2 />撤销</Button>
            </div>
          </div>
          <Empty v-if="!keys.length" title="还没有密钥" description="创建一把密钥即可用标准 S3 客户端接入。">
            <Button size="sm" @click="openCreate"><Plus />创建密钥</Button>
          </Empty>
        </div>
      </Card>
      <p class="mt-2.5 text-[11px] leading-relaxed text-muted-foreground">
        SecretAccessKey 在服务端以 AES-256-GCM 加密存储，鉴权使用 SHA-256 摘要比对；明文不写入日志与状态文件，仅登录后的本人或管理员可按需解密查看。
      </p>
    </div>

    <!-- 查看凭据 -->
    <Dialog
      :open="!!revealed" :title="revealed?.name || '未命名密钥'"
      :description="revealed ? `${revealed.id} · ${revealed.revoked ? '已撤销' : '有效'} · 创建于 ${fmtTime(revealed.createdAt)}` : ''"
      class="max-w-xl"
      @update:open="(v) => { if (!v) revealed = null }"
    >
      <div v-if="revealed" class="space-y-3">
        <p class="rounded-md border border-warning/25 bg-warning/[0.07] px-3 py-2 text-[11.5px] leading-relaxed text-warning">
          这把密钥同时用于 REST / MCP 的 Bearer 鉴权与 S3 SigV4 签名。明文可随时再来查看，请勿提交到公开仓库或聊天记录。
        </p>

        <div class="space-y-2.5 text-[11.5px]">
          <div>
            <div class="flex items-center justify-between">
              <span class="text-muted-foreground">API Key / Bearer</span>
              <button class="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground" @click="showSecret = !showSecret">
                <component :is="showSecret ? EyeOff : Eye" class="size-3.5" />{{ showSecret ? '隐藏' : '显示' }}
              </button>
            </div>
            <div class="mt-1 flex items-center gap-1.5">
              <code class="min-w-0 flex-1 break-all rounded-md border bg-muted/40 px-2 py-1.5 font-mono text-[10.5px]">{{ showSecret ? revealed.secretAccessKey : '••••••••••••••••••••••••' }}</code>
              <Button size="icon-sm" variant="outline" aria-label="复制" @click="copy(revealed.secretAccessKey, 'reveal-bearer')"><Copy /></Button>
            </div>
          </div>
          <div>
            <span class="text-muted-foreground">AccessKeyId</span>
            <div class="mt-1 flex items-center gap-1.5">
              <code class="min-w-0 flex-1 break-all rounded-md border bg-muted/40 px-2 py-1.5 font-mono text-[10.5px]">{{ revealed.accessKeyId }}</code>
              <Button size="icon-sm" variant="outline" aria-label="复制" @click="copy(revealed.accessKeyId, 'reveal-access')"><Copy /></Button>
            </div>
          </div>
          <div class="grid gap-1 sm:grid-cols-[7rem_1fr] sm:gap-2">
            <span class="text-muted-foreground">Endpoint / Region</span>
            <span class="break-all font-mono text-[10.5px]">{{ fullEndpoint }} · {{ revealed.region || region }}</span>
          </div>
          <div class="grid gap-1 sm:grid-cols-[7rem_1fr] sm:gap-2">
            <span class="text-muted-foreground">状态 / 作用域</span>
            <span>{{ revealed.revoked ? '已撤销' : '有效' }} · {{ (revealed.scopes || []).length ? revealed.scopes.join(', ') : '继承用户全部权限' }}<span v-if="revealed.expiresAt"> · 到期 {{ fmtTime(revealed.expiresAt) }}</span></span>
          </div>
        </div>

        <div>
          <p class="mb-1.5 text-[10.5px] font-medium tracking-wide text-muted-foreground">可直接复制的对接代码</p>
          <CodeBlock :code="revealCode" label="credentials" max-height="260px" />
        </div>
      </div>
      <template #footer>
        <Button size="sm" variant="ghost" class="mr-auto" @click="copy(revealCode, 'reveal-code')"><Copy />复制对接代码</Button>
        <Button size="sm" variant="outline" @click="go('mcp')">MCP 示例<ExternalLink /></Button>
        <Button size="sm" @click="revealed = null">关闭</Button>
      </template>
    </Dialog>

    <!-- 创建 -->
    <Sheet v-model:open="drawer" title="创建 API 密钥" description="生成的密钥归属当前登录用户，用于 REST 与 S3 双通道鉴权。">
      <div class="grid gap-4">
        <Field label="密钥名称" required>
          <Input v-model="form.name" placeholder="例如 laptop-awscli / ci-pipeline" />
        </Field>
        <Field label="有效期（天）" hint="0 表示永不过期；到期后密钥自动失效">
          <Input v-model.number="form.expiresInDays" type="number" min="0" />
        </Field>

        <div>
          <div class="flex items-center justify-between">
            <p class="text-xs font-medium text-foreground/80">作用域（可选）</p>
            <button v-if="form.scopes.length" class="text-[11px] text-muted-foreground hover:text-foreground" @click="form.scopes = []">清空</button>
          </div>
          <p class="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">留空 = 继承用户当前全部权限；选中后密钥仅能执行这些操作。</p>
          <div class="mt-2.5 grid grid-cols-1 gap-1.5 sm:grid-cols-2">
            <button
              v-for="p in allPerms" :key="p" type="button"
              class="flex items-center gap-2 rounded-md border px-2.5 py-2 text-left font-mono text-[10.5px] transition-colors"
              :class="form.scopes.includes(p) ? 'border-primary/40 bg-primary/[0.06] text-primary' : 'bg-background text-muted-foreground hover:border-foreground/20'"
              @click="toggleScope(p)"
            >
              <span class="grid size-3.5 shrink-0 place-items-center rounded-[3px] border" :class="form.scopes.includes(p) ? 'border-primary bg-primary text-primary-foreground' : 'border-input'">
                <CircleCheck v-if="form.scopes.includes(p)" class="size-2.5" />
              </span>
              {{ p }}
            </button>
          </div>
        </div>
      </div>
      <p class="mt-4 rounded-md border bg-muted/40 px-3 py-2 text-[11.5px] leading-relaxed text-muted-foreground">
        创建后可在密钥列表随时点「查看」获取明文，不需要一次性保存；仅本人与管理员登录后可见。
      </p>
      <template #footer>
        <Button variant="outline" @click="drawer = false">取消</Button>
        <Button :disabled="saving" @click="create">{{ saving ? '生成中…' : '生成密钥' }}</Button>
      </template>
    </Sheet>
  </div>
</template>
