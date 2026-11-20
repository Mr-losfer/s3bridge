<script setup>
import { ref, onMounted, computed } from 'vue'
import { BookOpen, FileText, Server, TerminalSquare, ChevronRight, Loader2, ExternalLink } from 'lucide-vue-next'
import { api } from '../api'
import { route } from '../router'
import { notifyError } from '../toast'
import { renderMarkdown, formatBytes } from '../utils'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/ui/Badge.vue'
import Card from '../components/ui/Card.vue'
import Select from '../components/ui/Select.vue'
import Skeleton from '../components/ui/Skeleton.vue'

const docs = ref([])
const selected = ref('')
const content = ref('')
const title = ref('')
const meta = ref({})
const loading = ref(true)
const docLoading = ref(false)

const html = computed(() => renderMarkdown(content.value))

async function loadDoc(name) {
  if (!name) return
  docLoading.value = true
  try {
    const d = await api('/docs/' + encodeURIComponent(name))
    selected.value = name
    title.value = d.title
    content.value = d.content || ''
  } catch (e) { notifyError(e, '加载文档失败') }
  finally { docLoading.value = false }
}

function select(name) {
  if (name === selected.value) return
  loadDoc(name)
  // replaceState 不触发 hashchange，避免组件被 :key 重建导致闪烁；同时保持链接可分享/刷新。
  try { window.history.replaceState(null, '', '#/docs/' + encodeURIComponent(name)) } catch { /* 忽略 */ }
}

onMounted(async () => {
  try {
    const r = await api('/docs')
    docs.value = r?.docs || []
    meta.value = r || {}
    const want = route.value.id && docs.value.some((d) => d.name === route.value.id)
      ? route.value.id
      : (docs.value[0]?.name || '')
    await loadDoc(want)
  } catch (e) { notifyError(e, '加载文档索引失败') }
  finally { loading.value = false }
})

function currentSize(name) {
  const d = docs.value.find((x) => x.name === name)
  return d ? formatBytes(d.size) : ''
}
</script>

<template>
  <div>
    <PageHeader
      title="文档中心"
      description="文档随服务端版本一起发布，后台、MCP 工具与仓库 docs/ 始终是同一份内容。"
    >
      <template #actions>
        <Badge variant="outline"><BookOpen class="size-3" />{{ docs.length }} 篇文档</Badge>
      </template>
    </PageHeader>

    <div class="grid gap-4 lg:grid-cols-[15rem_minmax(0,1fr)] lg:items-start">
      <div class="space-y-3 lg:sticky lg:top-20">
        <Card class="overflow-hidden">
          <div class="border-b bg-muted/25 px-3 py-2">
            <p class="text-[10.5px] font-medium tracking-wide text-muted-foreground">文档目录</p>
          </div>
          <div class="hidden p-1.5 lg:block">
            <button
              v-for="d in docs" :key="d.name"
              class="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-[12px] transition-colors"
              :class="selected === d.name ? 'bg-primary/[0.08] font-medium text-primary' : 'text-muted-foreground hover:bg-accent hover:text-foreground'"
              @click="select(d.name)"
            >
              <FileText class="size-3.5 shrink-0" />
              <span class="min-w-0 flex-1 truncate">{{ d.title }}</span>
              <span class="font-mono text-[9.5px] opacity-60">{{ currentSize(d.name) }}</span>
            </button>
            <p v-if="!docs.length && !loading" class="px-3 py-4 text-[11px] text-muted-foreground">文档索引为空</p>
          </div>
          <div class="p-2 lg:hidden">
            <Select :model-value="selected" @update:model-value="select">
              <option v-for="d in docs" :key="d.name" :value="d.name">{{ d.title }}</option>
            </Select>
          </div>
        </Card>

        <Card class="hidden p-3.5 lg:block">
          <p class="text-[10.5px] font-medium tracking-wide text-muted-foreground">RUNTIME INFO</p>
          <dl class="mt-2 space-y-1.5 text-[10.5px]">
            <div class="flex items-center justify-between gap-2">
              <dt class="text-muted-foreground">S3</dt>
              <dd class="truncate font-mono" :title="meta.s3Endpoint">{{ meta.s3Endpoint || '—' }}</dd>
            </div>
            <div class="flex items-center justify-between gap-2">
              <dt class="text-muted-foreground">Region</dt><dd class="font-mono">{{ meta.region || '—' }}</dd>
            </div>
            <div class="flex items-center justify-between gap-2">
              <dt class="text-muted-foreground">MCP</dt>
              <dd class="truncate font-mono" :title="meta.mcpEndpoint">{{ meta.mcpEndpoint || '—' }}</dd>
            </div>
          </dl>
        </Card>

        <Card class="hidden p-3.5 lg:block">
          <p class="flex items-center gap-1.5 text-[10.5px] font-medium tracking-wide text-muted-foreground">
            <TerminalSquare class="size-3.5" />MCP 读取文档
          </p>
          <p class="mt-1.5 text-[10.5px] leading-relaxed text-muted-foreground">
            AI 客户端可通过 MCP 工具直接读取同一份文档：
          </p>
          <div class="mt-2 rounded-md border bg-muted/40 p-2 font-mono text-[9.5px] leading-relaxed text-muted-foreground">
            tools/call<br />{ "name": "plugin_docs" }<br />{ "name": "s3_docs" }<br />{ "name": "installation_docs" }
          </div>
          <p class="mt-2 text-[10px] leading-relaxed text-muted-foreground">也可用 resources/read 读取 docs://s3-api 等资源。</p>
        </Card>
      </div>

      <Card class="min-h-[26rem] overflow-hidden">
        <div class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/20 px-4 py-2.5 sm:px-6">
          <div class="flex min-w-0 items-center gap-2">
            <FileText class="size-3.5 shrink-0 text-muted-foreground" />
            <h2 class="truncate text-[12.5px] font-medium">{{ title || '文档' }}</h2>
          </div>
          <span class="font-mono text-[10px] text-muted-foreground">{{ currentSize(selected) }}</span>
        </div>
        <div class="p-4 sm:p-6">
          <div v-if="loading || docLoading" class="space-y-3">
            <Skeleton class="h-6 w-2/5" />
            <Skeleton class="h-3.5 w-full" />
            <Skeleton class="h-3.5 w-11/12" />
            <Skeleton class="h-3.5 w-4/5" />
            <Skeleton class="mt-6 h-40 w-full" />
          </div>
          <article v-else class="doc-prose" v-html="html"></article>
          <div v-if="!loading && !docLoading && !content" class="flex flex-col items-center gap-2 py-16 text-center text-muted-foreground">
            <Server class="size-6 opacity-40" />
            <p class="text-[11.5px]">该文档为空。</p>
          </div>
        </div>
      </Card>
    </div>

    <div class="mt-4 grid gap-3 sm:grid-cols-2 lg:hidden">
      <Card class="p-3.5">
        <p class="text-[10.5px] font-medium tracking-wide text-muted-foreground">RUNTIME INFO</p>
        <p class="mt-1.5 break-all font-mono text-[10.5px]">{{ meta.s3Endpoint }} · {{ meta.region }}</p>
        <p class="mt-0.5 break-all font-mono text-[10.5px]">{{ meta.mcpEndpoint }}</p>
      </Card>
      <Card class="p-3.5">
        <p class="text-[10.5px] font-medium tracking-wide text-muted-foreground">MCP 读取文档</p>
        <p class="mt-1.5 text-[10.5px] leading-relaxed text-muted-foreground">tools/call → plugin_docs / s3_docs / installation_docs，或 resources/read。</p>
      </Card>
    </div>
  </div>
</template>
