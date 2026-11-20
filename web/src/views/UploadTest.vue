<script setup>
import { ref, onMounted, computed, watch } from 'vue'
import {
  UploadCloud, X, Loader2, CircleCheck, Copy, ArrowRight, FileUp, Image as ImageIcon, Sparkles,
} from 'lucide-vue-next'
import { api, uploadFile } from '../api'
import { notify, notifyError } from '../toast'
import { formatBytes, fmtTime, categoryLabel } from '../utils'
import { go } from '../router'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Input from '../components/ui/Input.vue'
import Select from '../components/ui/Select.vue'
import Field from '../components/ui/Field.vue'
import Empty from '../components/ui/Empty.vue'

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
const fileInput = ref(null)

const isImage = computed(() => (file.value?.type || '').startsWith('image/'))
const canUpload = computed(() => !!file.value && !uploading.value)

const EXT_CATEGORY = {
  image: ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'avif', 'heic'],
  video: ['mp4', 'mov', 'mkv', 'webm', 'avi'],
  audio: ['mp3', 'wav', 'flac', 'aac', 'm4a'],
  document: ['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'csv', 'json'],
  archive: ['zip', 'tar', 'gz', '7z', 'rar'],
}
// 与服务端 CategoryOf 保持同一优先级：MIME 优先，其次扩展名。
function detectCategory(f) {
  const mime = (f.type || '').toLowerCase().split(';')[0]
  if (mime.startsWith('image/')) return 'image'
  if (mime.startsWith('video/')) return 'video'
  if (mime.startsWith('audio/')) return 'audio'
  if (mime.startsWith('text/') || /pdf|word|excel|sheet|presentation|json|xml|csv|markdown/.test(mime)) return 'document'
  if (/zip|tar|gzip|compressed|7z|rar/.test(mime)) return 'archive'
  const ext = (f.name.split('.').pop() || '').toLowerCase()
  for (const [cat, list] of Object.entries(EXT_CATEGORY)) if (list.includes(ext)) return cat
  return 'other'
}

const category = computed(() => (file.value ? detectCategory(file.value) : ''))
// 只列出能承接当前类型的插件；服务端还会按权重与健康状态做最终选择。
const eligiblePlugins = computed(() => {
  if (!category.value) return plugins.value
  return plugins.value.filter((p) => !p.fileTypes?.length || p.fileTypes.includes(category.value))
})
const autoPick = computed(() => eligiblePlugins.value[0] || null)

watch(eligiblePlugins, (list) => {
  if (pluginId.value && !list.some((p) => p.id === pluginId.value)) pluginId.value = ''
})

async function loadMeta() {
  try {
    const [p, b] = await Promise.all([api('/plugins'), api('/buckets')])
    plugins.value = (p || []).filter((x) => x.enabled)
    buckets.value = b || []
    // 默认保持自动选择：由服务端按文件类型 + 权重 + 健康状态挑选。
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
function clearFile() {
  if (preview.value) URL.revokeObjectURL(preview.value)
  preview.value = ''
  file.value = null
  result.value = null
  if (fileInput.value) fileInput.value.value = ''
}

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
    <PageHeader
      title="上传图片测试"
      description="选一张图片，走真实插件链路：第三方上传 → 统一标准输出 → 按 bucket 与文件类型归档 → S3 可读取。"
    >
      <template #actions>
        <Button variant="outline" @click="go('uploads')">查看上传列表<ArrowRight /></Button>
      </template>
    </PageHeader>

    <div class="grid items-start gap-5 lg:grid-cols-[minmax(0,1.15fr)_minmax(320px,0.85fr)]">
      <div>
        <div
          class="group relative flex min-h-[15rem] cursor-pointer flex-col items-center justify-center rounded-xl border border-dashed bg-card p-6 text-center transition-colors sm:min-h-[17rem] sm:p-10"
          :class="dragging ? 'border-primary bg-primary/[0.04]' : 'hover:border-foreground/25 hover:bg-muted/30'"
          @dragover.prevent="dragging = true" @dragleave.prevent="dragging = false" @drop.prevent="onDrop"
          @click="fileInput?.click()"
        >
          <input ref="fileInput" type="file" class="hidden" @change="onSelect" />

          <template v-if="!file">
            <div class="mx-auto grid size-11 place-items-center rounded-xl border bg-muted/50 text-muted-foreground transition-transform group-hover:-translate-y-0.5">
              <UploadCloud class="size-5" />
            </div>
            <p class="mt-3 text-[14px] font-medium">拖放文件到这里，或点击选择</p>
            <p class="mt-1 text-[11.5px] leading-relaxed text-muted-foreground">支持图片、文档、音视频与压缩包；单文件上限由服务端配置决定</p>
          </template>

          <template v-else>
            <div class="flex flex-col items-center gap-3">
              <img v-if="preview" :src="preview" alt="预览" class="max-h-52 max-w-full rounded-lg border object-contain" @click.stop />
              <div v-else class="grid size-12 place-items-center rounded-xl border bg-muted/50 text-muted-foreground">
                <FileUp class="size-5" />
              </div>
              <div class="min-w-0 max-w-full">
                <p class="truncate text-[13px] font-medium">{{ file.name }}</p>
                <p class="mt-0.5 font-mono text-[10.5px] text-muted-foreground">{{ file.type || '未知类型' }} · {{ formatBytes(file.size) }}</p>
              </div>
              <Button size="sm" variant="outline" @click.stop="clearFile"><X />移除文件</Button>
            </div>
          </template>
        </div>

        <Card class="mt-4 p-4">
          <div class="grid gap-3.5 sm:grid-cols-2">
            <Field label="目标存储桶" required>
              <Select v-model="bucket">
                <option v-for="b in buckets" :key="b.name" :value="b.name">{{ b.name }}{{ b.publicRead ? '（公开读）' : '' }}</option>
              </Select>
            </Field>
            <Field label="上传插件">
              <Select v-model="pluginId">
                <option value="">自动选择（按文件类型 + 权重）</option>
                <option v-for="p in eligiblePlugins" :key="p.id" :value="p.id">{{ p.name }} · 权重 {{ p.priority }} · {{ p.status }}</option>
              </Select>
            </Field>
            <div class="sm:col-span-2">
              <div class="flex flex-wrap items-center gap-1.5 rounded-md border bg-muted/30 px-3 py-2 text-[11.5px] text-muted-foreground">
                <Sparkles class="size-3.5 text-primary" />
                <template v-if="category">
                  检测到 <Badge variant="primary">{{ categoryLabel(category) }}</Badge>
                  可承接插件 {{ eligiblePlugins.length }} 个<template v-if="autoPick">；自动模式会选 <strong class="text-foreground">{{ autoPick.name }}</strong>（权重 {{ autoPick.priority }}）</template>
                </template>
                <template v-else>选择文件后自动按类型过滤插件</template>
              </div>
            </div>
            <Field label="对象键（可选）" hint="对象键会做路径穿越清洗；归档目录为 {bucket}/{文件类型}/{年月}/" class="sm:col-span-2">
              <Input v-model="objectKey" placeholder="例如 images/2026/banner.png，留空自动生成" />
            </Field>
          </div>

          <div class="mt-4 flex flex-wrap items-center gap-2">
            <Button :disabled="!canUpload" @click="submit">
              <Loader2 v-if="uploading" class="animate-spin" />
              <UploadCloud v-else />
              {{ uploading ? '上传中 ' + progress + '%' : '开始上传' }}
            </Button>
            <Button variant="outline" :disabled="uploading" @click="clearFile">清空</Button>
            <span v-if="uploading" class="text-[11px] text-muted-foreground">正在调用插件并等待第三方响应…</span>
          </div>

          <div v-if="uploading" class="mt-3.5 h-1.5 overflow-hidden rounded-full bg-muted">
            <div class="h-full rounded-full bg-primary transition-[width] duration-200" :style="{ width: progress + '%' }" />
          </div>
        </Card>
      </div>

      <div class="space-y-4">
        <div class="flex items-end justify-between gap-3">
          <div>
            <h2 class="text-sm font-semibold">标准输出</h2>
            <p class="mt-0.5 text-[11.5px] text-muted-foreground">所有插件返回同一结构</p>
          </div>
          <Badge variant="outline">UploadResult</Badge>
        </div>

        <Card v-if="result">
          <div class="flex items-center justify-between gap-3 border-b px-4 py-3">
            <Badge variant="success"><CircleCheck class="size-3" />SUCCESS</Badge>
            <span class="font-mono text-[10.5px] text-muted-foreground">{{ result.provider }}</span>
          </div>
          <div class="space-y-2 p-4 text-[11.5px]">
            <div v-for="row in [
              { k: '文件名', v: result.fileName, mono: false },
              { k: '对象键', v: result.objectKey, mono: true },
              { k: '文件类型', v: categoryLabel(result.category) + ' · ' + result.mimeType, mono: false },
              { k: '命中插件', v: result.pluginId + '（' + result.provider + '）', mono: true },
              { k: '大小', v: formatBytes(result.size), mono: true },
              { k: '存储桶', v: result.bucket, mono: true },
              { k: '签名到期', v: result.urlExpireAt ? fmtTime(result.urlExpireAt) : '长期有效', mono: false },
              { k: 'SHA-256', v: result.sha256, mono: true },
            ]" :key="row.k" class="grid grid-cols-[4.5rem_1fr] gap-2">
              <span class="text-muted-foreground">{{ row.k }}</span>
              <span :class="['break-all', row.mono && 'font-mono text-[10.5px]']">{{ row.v }}</span>
            </div>

            <div class="mt-3 border-t pt-3">
              <p class="text-[10.5px] text-muted-foreground">第三方返回 URL</p>
              <div class="mt-1 flex items-center gap-1.5">
                <Input :model-value="result.remoteUrl" readonly class="h-8 font-mono text-[10px]" />
                <Button size="icon-sm" variant="outline" aria-label="复制 URL" @click="copy(result.remoteUrl, 'URL')"><Copy /></Button>
              </div>
              <p class="mt-1 text-[10.5px] leading-relaxed text-muted-foreground">临时签名链接可能过期；本地归档对象通过 S3 接口读取长期有效。</p>
            </div>
          </div>
          <div class="border-t bg-muted/30 px-4 py-3">
            <Button size="sm" variant="outline" class="w-full" @click="go('uploads')"><ImageIcon />在上传列表中查看</Button>
          </div>
        </Card>

        <Card v-else>
          <Empty title="等待上传" description="完成一次上传后，这里会显示统一标准输出与归档信息。">
            <div class="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <UploadCloud class="size-3.5" />拖入文件即可开始
            </div>
          </Empty>
        </Card>

        <Card v-if="history.length > 1" class="overflow-hidden">
          <div class="border-b px-4 py-2.5">
            <p class="text-[12px] font-medium">本次会话记录</p>
          </div>
          <div class="divide-y">
            <div v-for="(h, i) in history" :key="i" class="flex items-center gap-3 px-4 py-2">
              <span class="min-w-0 flex-1 truncate text-[11.5px]">{{ h.fileName }}</span>
              <Badge variant="outline">{{ categoryLabel(h.category) }}</Badge>
              <span class="font-mono text-[10.5px] text-muted-foreground">{{ formatBytes(h.size) }}</span>
            </div>
          </div>
        </Card>
      </div>
    </div>
  </div>
</template>
