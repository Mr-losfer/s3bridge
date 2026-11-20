<script setup>
import { ref, onMounted, computed } from 'vue'
import {
  Database, HardDrive, Files, RefreshCw, Plus, Copy, Pencil, Trash2, ArrowUpRight,
  Lock, Globe, TerminalSquare, BookOpen,
} from 'lucide-vue-next'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { go } from '../router'
import { formatBytes, fmtTime } from '../utils'
import { can } from '../session'
import PageHeader from '../components/PageHeader.vue'
import StatCard from '../components/StatCard.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Input from '../components/ui/Input.vue'
import Field from '../components/ui/Field.vue'
import Sheet from '../components/ui/Sheet.vue'
import Switch from '../components/ui/Switch.vue'
import Empty from '../components/ui/Empty.vue'
import CodeBlock from '../components/CodeBlock.vue'
import Skeleton from '../components/ui/Skeleton.vue'

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

function viewObjects() { go('uploads') }
function copy(text, label) {
  navigator.clipboard?.writeText(text).then(
    () => notify(label + ' 已复制'),
    () => notifyError(new Error('浏览器未授权剪贴板')),
  )
}

const sample = computed(() => `# 列出对象（把 AK/SK 换成你的 API 密钥）
export AWS_ACCESS_KEY_ID=AK_xxxxxxxx
export AWS_SECRET_ACCESS_KEY=xxxxxxxx
aws --endpoint-url ${endpoint.value} s3 ls s3://default/

# 上传 / 下载
aws --endpoint-url ${endpoint.value} s3 cp ./a.png s3://default/images/a.png
aws --endpoint-url ${endpoint.value} s3 cp s3://default/images/a.png ./a.png`)
</script>

<template>
  <div>
    <PageHeader
      title="存储桶管理"
      description="每个桶独立命名空间，对象可按桶过滤查看。桶同时映射到 S3 接口，用各自用户的 API Key 通过 SigV4 访问。"
    >
      <template #actions>
        <Button variant="outline" @click="load"><RefreshCw />刷新</Button>
        <Button v-if="canWrite" @click="openCreate"><Plus />新建存储桶</Button>
      </template>
    </PageHeader>

    <div class="grid grid-cols-2 gap-3 lg:grid-cols-3">
      <StatCard label="存储桶" :value="buckets.length" unit="buckets" hint="默认桶 default 在启动时自动创建" :icon="Database" />
      <StatCard label="对象总数" :value="totalObjects" unit="objects" hint="跨全部存储桶" :icon="Files" />
      <StatCard label="占用空间" :value="formatBytes(totalSize)" hint="本地归档副本" :icon="HardDrive" class="col-span-2 lg:col-span-1" />
    </div>

    <div v-if="loading" class="mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      <Skeleton v-for="i in 3" :key="i" class="h-44 rounded-xl" />
    </div>

    <div v-else-if="buckets.length" class="mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      <Card v-for="b in buckets" :key="b.name" class="flex flex-col p-4">
        <div class="flex items-start justify-between gap-2">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="grid size-8 shrink-0 place-items-center rounded-lg bg-primary/[0.08] text-primary"><Database class="size-4" /></span>
              <div class="min-w-0">
                <p class="truncate font-mono text-[13px] font-semibold">{{ b.name }}</p>
                <p class="font-mono text-[10px] text-muted-foreground">s3://{{ b.name }}</p>
              </div>
            </div>
          </div>
          <Badge :variant="b.publicRead ? 'warning' : 'success'" class="shrink-0">
            <component :is="b.publicRead ? Globe : Lock" class="size-3" />{{ b.publicRead ? '公开读' : '私有' }}
          </Badge>
        </div>

        <p class="mt-3 line-clamp-2 min-h-[2.4em] text-[11.5px] leading-relaxed text-muted-foreground">{{ b.description || '未填写说明' }}</p>

        <div class="mt-3 grid grid-cols-2 gap-2 border-t pt-3 text-[11.5px]">
          <div>
            <p class="text-muted-foreground">对象数</p>
            <p class="mt-0.5 font-mono font-medium tabular-nums">{{ b.objects || 0 }}</p>
          </div>
          <div>
            <p class="text-muted-foreground">容量</p>
            <p class="mt-0.5 font-mono font-medium tabular-nums">{{ formatBytes(b.totalSize) }}</p>
          </div>
        </div>

        <p class="mt-2 text-[10.5px] text-muted-foreground">创建于 {{ fmtTime(b.createdAt) }}</p>

        <div class="mt-3 flex items-center gap-1.5 border-t pt-2.5">
          <Button size="sm" variant="outline" class="flex-1" @click="viewObjects(b)">查看对象<ArrowUpRight /></Button>
          <Button size="icon-sm" variant="ghost" aria-label="复制 S3 URI" @click="copy(`s3://${b.name}`, 'S3 URI')"><Copy /></Button>
          <template v-if="canWrite">
            <Button size="icon-sm" variant="ghost" aria-label="编辑" @click="openEdit(b)"><Pencil /></Button>
            <Button size="icon-sm" variant="ghost" aria-label="删除" class="text-muted-foreground hover:text-destructive" :disabled="busy === b.name" @click="remove(b)"><Trash2 /></Button>
          </template>
        </div>
      </Card>
    </div>

    <Card v-else class="mt-5">
      <Empty title="还没有存储桶" description="创建一个桶用于按业务隔离上传文件。">
        <Button v-if="canWrite" size="sm" @click="openCreate"><Plus />新建存储桶</Button>
      </Empty>
    </Card>

    <div class="mt-6">
      <div class="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 class="flex items-center gap-2 text-sm font-semibold"><TerminalSquare class="size-4 text-muted-foreground" />S3 接入示例</h2>
          <p class="mt-0.5 text-[11.5px] text-muted-foreground">桶名直接作为 S3 Bucket 使用，凭据见「API 密钥」。</p>
        </div>
        <Button size="sm" variant="ghost" @click="go('docs', 's3-api')"><BookOpen />S3 接口文档<ArrowUpRight /></Button>
      </div>
      <CodeBlock :code="sample" label="aws-cli" />
    </div>

    <Sheet v-model:open="drawer" :title="editing ? '编辑存储桶' : '新建存储桶'" description="桶名创建后不可修改，删除前需清空对象。">
      <div class="grid gap-4">
        <Field label="桶名" required hint="小写字母、数字、点、下划线、连字符，长度 3~63">
          <Input v-model="form.name" :disabled="editing" placeholder="例如 assets、user-uploads" class="font-mono" />
        </Field>
        <Field label="说明">
          <textarea
            v-model="form.description" placeholder="这个桶用来存什么"
            class="flex min-h-20 w-full resize-y rounded-md border border-input bg-background px-3 py-2 text-[13px] shadow-xs placeholder:text-muted-foreground/70 focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25"
          />
        </Field>
        <div class="flex items-start justify-between gap-4 rounded-lg border px-3 py-3">
          <div>
            <p class="text-[12.5px] font-medium">允许公开读</p>
            <p class="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">开启后无签名也可 GET 对象；关闭时需要有效 SigV4 签名或登录会话。</p>
          </div>
          <Switch v-model="form.publicRead" class="mt-0.5 shrink-0" />
        </div>
      </div>
      <p class="mt-4 rounded-md border border-warning/25 bg-warning/[0.07] px-3 py-2 text-[11.5px] leading-relaxed text-warning">
        删除存储桶前必须先删除桶内全部对象；非空桶会被服务端拒绝删除。
      </p>
      <template #footer>
        <Button variant="outline" @click="drawer = false">取消</Button>
        <Button :disabled="saving" @click="save">{{ saving ? '保存中…' : editing ? '保存修改' : '创建' }}</Button>
      </template>
    </Sheet>
  </div>
</template>
