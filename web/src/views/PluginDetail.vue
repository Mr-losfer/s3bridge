<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { route, go } from '../router'
import { notify, notifyError } from '../toast'
import { relTime, fmtTime, renderMarkdown, truncate } from '../utils'

const id = computed(() => route.value.id)
const view = ref(null)
const form = ref({})
const showSecret = ref({})
const tab = ref('params')
const loading = ref(true)
const saving = ref(false)
const debugReport = ref(null)
const changes = ref([])
const patchJSON = ref('{\n  "priority": 120\n}')
const fullJSON = ref('{\n  "name": "",\n  "enabled": true,\n  "priority": 100,\n  "params": {},\n  "health": { "enabled": true, "intervalSecs": 300, "timeoutSecs": 8, "failThreshold": 2, "probeMode": "ping" }\n}')
const updateSpec = ref({ version: '', syncParams: true, resetToDefault: false, note: '' })
const showManifest = ref(false)

const fields = computed(() => (view.value?.manifest?.params || []))
const volatileField = computed(() => fields.value.find((f) => f.volatile && f.expiresField))
const expiryValue = computed(() => form.value[volatileField.value?.expiresField] || '')
const expiryHint = computed(() => {
  const v = String(expiryValue.value).trim()
  if (!v) return '未设置过期时间'
  const n = Number(v)
  const t = Number.isFinite(n) && n > 0 ? new Date(n > 1e12 ? n : n * 1000) : new Date(v)
  if (isNaN(t.getTime())) return '时间格式无法识别'
  const left = t.getTime() - Date.now()
  if (left <= 0) return '已于 ' + t.toLocaleString('zh-CN') + ' 过期'
  if (left < 86400000) return '将在 ' + Math.round(left / 3600000) + ' 小时后过期'
  return '有效期至 ' + t.toLocaleString('zh-CN')
})

function initForm(v) {
  const f = {}
  for (const field of v.manifest?.params || []) {
    if (field.sensitive) f[field.key] = ''
    else {
      const cur = v.params?.[field.key]
      f[field.key] = cur === undefined || cur === null ? (field.default || '') : String(cur)
    }
  }
  form.value = f
}

async function load() {
  loading.value = true
  try {
    const v = await api('/plugins/' + encodeURIComponent(id.value))
    view.value = v
    initForm(v)
  } catch (e) { notifyError(e, '加载插件失败') }
  finally { loading.value = false }
}
onMounted(load)

async function saveParams() {
  saving.value = true
  try {
    const params = {}
    for (const f of fields.value) {
      const val = form.value[f.key]
      if (val === undefined) continue
      const str = String(val)
      if (f.sensitive && str === '') continue // 敏感字段留空 = 保持原值
      params[f.key] = str
    }
    await api(`/plugins/${encodeURIComponent(id.value)}/params`, { method: 'POST', body: { params } })
    notify('参数已保存并加密落盘', { title: view.value.name })
    await load()
  } catch (e) { notifyError(e, '保存失败') } finally { saving.value = false }
}

async function parseToken() {
  const auth = form.value.authorization || form.value.token || ''
  if (!auth.trim()) { notify('请先粘贴 Bearer Token 再解析', { title: '需要 Token', type: 'error' }); return }
  try {
    const v = await api(`/plugins/${encodeURIComponent(id.value)}/parse-token`, { method: 'POST', body: { authorization: auth } })
    if (volatileField.value?.expiresField) form.value[volatileField.value.expiresField] = String(v.unix)
    if (v.xUserId && form.value.x_user_id !== undefined && !form.value.x_user_id) form.value.x_user_id = v.xUserId
    notify(`解析成功：过期 ${v.tokenExpiresAt}${v.xUserId ? '，sub=' + v.xUserId : ''}`, { title: 'Token 已解析' })
  } catch (e) { notifyError(e, '解析失败') }
}

async function runDebug() {
  try { debugReport.value = await api(`/plugins/${encodeURIComponent(id.value)}/debug`, { method: 'POST', body: {} }) }
  catch (e) { notifyError(e, '调试失败') }
}
async function runCheck() {
  try {
    const v = await api(`/plugins/${encodeURIComponent(id.value)}/check`, { method: 'POST', body: {} })
    notify('当前状态：' + v.status, { title: '检测完成' })
    await load()
  } catch (e) { notifyError(e) }
}
async function loadChanges() {
  try { changes.value = (await api(`/plugins/${encodeURIComponent(id.value)}/changes?limit=50`)) || [] }
  catch (e) { notifyError(e) }
}
async function applyPatch() {
  try {
    await api(`/plugins/${encodeURIComponent(id.value)}`, { method: 'PATCH', body: JSON.parse(patchJSON.value) })
    notify('差量修改已应用', { title: 'plugin_patch' }); await load()
  } catch (e) { notifyError(e, '差量修改失败') }
}
async function applyReplace() {
  if (!window.confirm('全量替换会重置未提供的参数为默认值，确认继续？')) return
  try {
    await api(`/plugins/${encodeURIComponent(id.value)}`, { method: 'PUT', body: JSON.parse(fullJSON.value) })
    notify('全量配置已替换', { title: 'plugin_replace' }); await load()
  } catch (e) { notifyError(e, '全量修改失败') }
}
async function applyUpdate() {
  try {
    await api(`/plugins/${encodeURIComponent(id.value)}/update`, { method: 'POST', body: updateSpec.value })
    notify('插件已更新', { title: 'plugin_update' }); await load(); await loadChanges()
  } catch (e) { notifyError(e, '更新失败') }
}

function switchTab(t) {
  tab.value = t
  if (t === 'changes') loadChanges()
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Plugins / <button class="btn btn--quiet btn--sm" style="padding:0" @click="go('plugins')">返回列表</button> / {{ id }}</div>
        <h1 class="page-title">{{ view?.name || id }}</h1>
        <p class="page-desc">{{ view?.description || '插件详情与参数配置' }}</p>
      </div>
      <div class="btn-group">
        <span class="status" :class="'status--' + (view?.status || 'unknown')">{{ view?.status || 'unknown' }}</span>
        <span class="tag">v{{ view?.version }}</span>
        <span class="tag">revision {{ view?.revision }}</span>
      </div>
    </div>

    <div v-if="view?.warning" class="alert-strip">
      <div style="font-family:var(--serif);font-size:22px;line-height:1;color:var(--accent)">!</div>
      <div><div class="alert-strip-title">临时参数提醒</div><div class="alert-strip-body">{{ view.warning }}</div></div>
    </div>
    <div v-if="view?.statusMessage" class="panel" style="margin-bottom:22px;padding:13px 18px">
      <span class="tiny muted">最近状态</span>
      <div style="font-size:13px;margin-top:3px">{{ view.statusMessage }} <span class="muted tiny">· {{ relTime(view.lastCheckedAt) }} · {{ view.latencyMs }}ms</span></div>
    </div>

    <div class="tabs">
      <button v-for="t in [
        { k: 'params', l: '参数配置' }, { k: 'debug', l: '调试与检测' },
        { k: 'advanced', l: '差量 / 全量 / 更新' }, { k: 'changes', l: '变更记录' }, { k: 'docs', l: '插件文档' },
      ]" :key="t.k" class="tab" :class="{ 'is-active': tab === t.k }" @click="switchTab(t.k)">{{ t.l }}</button>
    </div>

    <!-- 参数配置 -->
    <div v-if="tab === 'params'">
      <div v-for="g in (view?.grouped || [])" :key="g.name" class="field-group">
        <h2 class="field-group-title">{{ g.name || '通用' }}</h2>
        <p class="section-note" style="margin-bottom:20px">清单定义的配置项，按类型自动渲染</p>
        <div class="form-grid">
          <div v-for="f in g.fields" :key="f.key" class="field" :style="f.type === 'textarea' ? 'grid-column:1/-1' : ''">
            <label>
              {{ f.label }} <span v-if="f.required" class="req">*</span>
              <span v-if="f.volatile" class="tag tag--warn">临时</span>
              <span v-if="f.sensitive" class="tag">加密</span>
              <span class="mono" style="color:var(--muted);font-size:10px;text-transform:none;letter-spacing:0">{{ f.key }}</span>
            </label>

            <div v-if="f.type === 'bool'" class="checkbox-row" style="padding:8px 0">
              <input type="checkbox" :checked="form[f.key] === 'true'" @change="form[f.key] = $event.target.checked ? 'true' : 'false'" />
              <span>{{ f.help || '启用' }}</span>
            </div>
            <select v-else-if="f.type === 'select'" v-model="form[f.key]">
              <option v-for="o in f.options" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
            <textarea v-else-if="f.type === 'textarea'" v-model="form[f.key]" :placeholder="f.placeholder || f.help"></textarea>
            <div v-else class="field-inline">
              <input
                v-model="form[f.key]"
                :type="f.sensitive && !showSecret[f.key] ? 'password' : (f.type === 'number' || f.type === 'duration' ? 'number' : 'text')"
                :placeholder="f.sensitive && view?.params?.[f.key] ? '已保存：' + view.params[f.key] + '（留空表示不修改）' : (f.placeholder || f.help || '')"
                :min="f.type === 'number' ? 0 : undefined"
              />
              <button v-if="f.sensitive" class="btn btn--sm" type="button" @click="showSecret[f.key] = !showSecret[f.key]">{{ showSecret[f.key] ? '隐藏' : '显示' }}</button>
            </div>
            <div v-if="volatileField && f.key === volatileField.expiresField" class="field-help" style="color:var(--warn)">⏱ {{ expiryHint }}</div>
            <div v-else class="field-help">{{ f.help }}</div>
          </div>
        </div>
      </div>

      <div v-if="volatileField" class="panel" style="margin-top:26px;background:var(--warn-soft);border-color:var(--warn)">
        <div style="display:flex;justify-content:space-between;gap:16px;align-items:center;flex-wrap:wrap">
          <div>
            <div class="strong" style="font-size:13px">临时凭据助手</div>
            <div class="tiny muted" style="margin-top:4px">粘贴 Bearer Token 后一键解析 exp / sub，自动回填过期时间与 X-User-Id。{{ expiryHint }}</div>
          </div>
          <button class="btn btn--sm" @click="parseToken">从 Token 解析过期时间</button>
        </div>
      </div>

      <div style="margin-top:28px;display:flex;gap:9px;align-items:center;flex-wrap:wrap">
        <button class="btn btn--primary" :disabled="saving" @click="saveParams">{{ saving ? '保存中…' : '保存参数配置' }}</button>
        <button class="btn" @click="load">重新载入</button>
        <span class="tiny muted">敏感字段留空表示保持原值；服务端以 AES-256-GCM 加密存储</span>
      </div>
    </div>

    <!-- 调试与检测 -->
    <div v-else-if="tab === 'debug'">
      <div class="btn-group" style="margin-bottom:22px">
        <button class="btn btn--primary" @click="runDebug">▷ 运行插件调试</button>
        <button class="btn" @click="runCheck">◎ 立即健康检测</button>
      </div>
      <p class="section-note" style="margin-bottom:20px">调试不会上传真实文件：依次检查参数合法性、临时凭据有效期与上游连通性。</p>
      <div v-if="debugReport" class="panel panel--flush">
        <div style="padding:16px 20px;border-bottom:1px solid var(--line);display:flex;justify-content:space-between;align-items:center;gap:12px">
          <div><span class="strong">诊断结果</span> <span class="mono muted tiny" style="margin-left:8px">v{{ debugReport.version }} · {{ fmtTime(debugReport.checkedAt) }}</span></div>
          <span class="tag" :class="debugReport.healthy ? 'tag--good' : 'tag--accent'">{{ debugReport.healthy ? 'PASS' : 'FAIL' }}</span>
        </div>
        <table>
          <thead><tr><th>步骤</th><th>结论</th><th>详情</th><th class="right">耗时</th></tr></thead>
          <tbody>
            <tr v-for="(s, i) in debugReport.steps" :key="i">
              <td class="cell-main">{{ s.name }}</td>
              <td><span class="tag" :class="s.status === 'pass' ? 'tag--good' : (s.status === 'warn' ? 'tag--warn' : 'tag--accent')">{{ s.status.toUpperCase() }}</span></td>
              <td class="tiny" style="max-width:560px;word-break:break-word">{{ s.detail }}</td>
              <td class="num mono">{{ s.durationMs }}ms</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty"><div class="empty-title">尚未运行调试</div><p>点击上方按钮，三步诊断插件配置与上游链路。</p></div>
    </div>

    <!-- 差量 / 全量 / 更新 -->
    <div v-else-if="tab === 'advanced'">
      <p class="page-desc" style="margin-bottom:26px">与 MCP 的 <code class="code">plugin_patch</code> / <code class="code">plugin_replace</code> / <code class="code">plugin_update</code> 工具等价。所有写入都会记录到变更审计。</p>
      <div class="form-grid" style="grid-template-columns:repeat(auto-fit,minmax(320px,1fr));align-items:start">
        <div class="panel">
          <div class="eyebrow">plugin_patch</div>
          <h3 class="section-title">差量修改</h3>
          <p class="section-note">只覆盖传入字段，params 按键合并</p>
          <div class="field" style="margin-top:18px">
            <label>Patch JSON</label>
            <textarea v-model="patchJSON" style="min-height:150px"></textarea>
          </div>
          <button class="btn btn--primary btn--block" style="margin-top:14px" @click="applyPatch">应用差量修改</button>
        </div>
        <div class="panel">
          <div class="eyebrow">plugin_replace</div>
          <h3 class="section-title">全量修改</h3>
          <p class="section-note">未提供的参数重置为默认值</p>
          <div class="field" style="margin-top:18px">
            <label>Full JSON</label>
            <textarea v-model="fullJSON" style="min-height:150px"></textarea>
          </div>
          <button class="btn btn--danger btn--block" style="margin-top:14px" @click="applyReplace">全量替换配置</button>
        </div>
        <div class="panel">
          <div class="eyebrow">plugin_update</div>
          <h3 class="section-title">插件更新</h3>
          <p class="section-note">升级版本并同步清单新增参数</p>
          <div class="field" style="margin-top:18px"><label>目标版本</label><input v-model="updateSpec.version" placeholder="留空使用内置版本" /></div>
          <div class="checkbox-row" style="margin-top:14px"><input type="checkbox" v-model="updateSpec.syncParams" /><span>补齐清单中新增参数</span></div>
          <div class="checkbox-row" style="margin-top:8px"><input type="checkbox" v-model="updateSpec.resetToDefault" /><span>重置全部参数为默认值</span></div>
          <div class="field" style="margin-top:14px"><label>更新说明</label><input v-model="updateSpec.note" placeholder="可选" /></div>
          <button class="btn btn--primary btn--block" style="margin-top:14px" @click="applyUpdate">执行更新</button>
        </div>
      </div>
    </div>

    <!-- 变更记录 -->
    <div v-else-if="tab === 'changes'">
      <div class="table-wrap">
        <table>
          <thead><tr><th>时间</th><th>动作</th><th>操作者</th><th>说明</th><th>字段变更</th></tr></thead>
          <tbody>
            <tr v-for="c in changes" :key="c.id">
              <td class="tiny muted nowrap">{{ fmtTime(c.createdAt) }}</td>
              <td><span class="tag">{{ c.action }}</span></td>
              <td class="mono tiny">{{ c.actor }}</td>
              <td class="tiny">{{ c.message }}</td>
              <td class="mono tiny" style="max-width:340px;word-break:break-word">{{ truncate(JSON.stringify(c.diff || {}), 160) }}</td>
            </tr>
            <tr v-if="!changes.length"><td colspan="5"><div class="empty"><div class="empty-title">暂无变更记录</div><p>参数修改、更新与调试都会在此留痕。</p></div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 文档 -->
    <div v-else>
      <div class="prose" v-html="renderMarkdown(view?.manifest?.docs || '该插件未提供接入说明。')"></div>
      <div class="section">
        <div class="section-head">
          <div><h2 class="section-title">示例代码</h2><p class="section-note">实现 Uploader 接口的最小骨架</p></div>
          <button class="btn btn--sm" @click="showManifest = !showManifest">{{ showManifest ? '收起' : '查看完整 Manifest' }}</button>
        </div>
        <pre class="codeblock" style="margin-top:16px"><code>{{ view?.manifest?.sampleCode }}</code></pre>
        <pre v-if="showManifest" class="codeblock" style="margin-top:12px"><code>{{ JSON.stringify(view?.manifest, null, 2) }}</code></pre>
      </div>
    </div>
  </div>
</template>
