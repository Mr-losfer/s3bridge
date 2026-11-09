<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { route } from '../router'
import { notifyError } from '../toast'
import { renderMarkdown, formatBytes } from '../utils'

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
    <div class="page-head">
      <div>
        <div class="eyebrow">Docs / 文档中心</div>
        <h1 class="page-title">文档中心</h1>
        <p class="page-desc">文档随服务端版本一起发布，后台、MCP 工具与仓库 <code class="code">docs/</code> 始终是同一份内容。</p>
      </div>
    </div>

    <div class="tabs">
      <button v-for="d in docs" :key="d.name" class="tab" :class="{ 'is-active': selected === d.name }" @click="select(d.name)">
        {{ d.title }}
      </button>
      <span v-if="!docs.length && !loading" class="muted tiny" style="padding:12px">文档索引为空</span>
    </div>

    <div style="display:grid;grid-template-columns:minmax(0,1fr) 250px;gap:32px;align-items:start">
      <div class="panel" style="min-height:420px">
        <div v-if="loading || docLoading" class="empty"><span class="spinner"></span> 正在加载文档…</div>
        <article v-else class="prose" v-html="html"></article>
      </div>

      <div>
        <div class="panel" style="padding:18px 20px">
          <div class="eyebrow" style="margin-bottom:12px">Runtime Info</div>
          <dl class="kv" style="grid-template-columns:78px 1fr;font-size:12px">
            <dt>S3</dt><dd><code class="code">{{ meta.s3Endpoint }}</code></dd>
            <dt>Region</dt><dd class="mono">{{ meta.region }}</dd>
            <dt>MCP</dt><dd><code class="code">{{ meta.mcpEndpoint }}</code></dd>
          </dl>
          <div class="divider" style="margin:16px 0"></div>
          <div class="eyebrow" style="margin-bottom:10px">文档列表</div>
          <div style="display:flex;flex-direction:column;gap:4px">
            <button v-for="d in docs" :key="d.name" class="btn btn--quiet" style="justify-content:space-between;width:100%;padding:7px 8px;font-size:12px"
              :class="{ 'is-active': selected === d.name }" @click="select(d.name)">
              <span>{{ d.title }}</span><span class="mono tiny muted">{{ currentSize(d.name) }}</span>
            </button>
          </div>
        </div>
        <div class="panel" style="padding:18px 20px;margin-top:16px">
          <div class="eyebrow" style="margin-bottom:10px">MCP 读取文档</div>
          <p class="tiny muted" style="line-height:1.7;margin:0 0 10px">AI 客户端可以通过 MCP 工具直接读取同一份文档：</p>
          <pre class="codeblock" style="font-size:10.5px;padding:12px"><code>tools/call
{ "name": "plugin_docs" }
{ "name": "s3_docs" }
{ "name": "installation_docs" }</code></pre>
          <p class="tiny muted" style="margin:10px 0 0">也可用 <code class="code">resources/read</code> 读取 <code class="code">docs://s3-api</code> 等资源。</p>
        </div>
      </div>
    </div>
  </div>
</template>
