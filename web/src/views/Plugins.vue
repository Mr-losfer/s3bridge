<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { go } from '../router'
import { notify, notifyError } from '../toast'
import { relTime, statusText, truncate } from '../utils'

const plugins = ref([])
const loading = ref(true)
const busyId = ref('')
const healthTarget = ref(null)
const healthForm = ref({})
const saving = ref(false)

const unhealthyCount = computed(() => plugins.value.filter((p) => p.status === 'unhealthy').length)

async function load() {
  loading.value = true
  try { plugins.value = (await api('/plugins')) || [] }
  catch (e) { notifyError(e, '加载插件失败') }
  finally { loading.value = false }
}
onMounted(load)

async function toggle(p) {
  busyId.value = p.id
  try {
    await api(`/plugins/${encodeURIComponent(p.id)}/toggle`, { method: 'POST', body: { enabled: !p.enabled } })
    notify(`${p.name} 已${p.enabled ? '停用' : '启用'}`)
    await load()
  } catch (e) { notifyError(e) } finally { busyId.value = '' }
}

async function check(p) {
  busyId.value = p.id
  try {
    const v = await api(`/plugins/${encodeURIComponent(p.id)}/check`, { method: 'POST', body: {} })
    notify(`${p.name} 检测完成：${v.status}`)
    await load()
  } catch (e) { notifyError(e) } finally { busyId.value = '' }
}

async function checkAll() {
  for (const p of plugins.value.filter((x) => x.enabled)) {
    try { await api(`/plugins/${encodeURIComponent(p.id)}/check`, { method: 'POST', body: {} }) } catch { /* 单个失败继续 */ }
  }
  notify('全部插件检测完成')
  await load()
}

function openHealth(p) {
  healthTarget.value = p
  healthForm.value = {
    enabled: p.health?.enabled ?? true,
    intervalSecs: p.health?.intervalSecs ?? 300,
    timeoutSecs: p.health?.timeoutSecs ?? 8,
    failThreshold: p.health?.failThreshold ?? 2,
    probeMode: p.health?.probeMode || 'ping',
  }
}
async function saveHealth() {
  saving.value = true
  try {
    await api(`/plugins/${encodeURIComponent(healthTarget.value.id)}/health`, {
      method: 'PUT',
      body: { ...healthForm.value, intervalSecs: Number(healthForm.value.intervalSecs), timeoutSecs: Number(healthForm.value.timeoutSecs), failThreshold: Number(healthForm.value.failThreshold) },
    })
    notify('定时检测配置已保存，调度器已重建', { title: healthTarget.value.name })
    healthTarget.value = null
    await load()
  } catch (e) { notifyError(e) } finally { saving.value = false }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Plugins / 插件管理</div>
        <h1 class="page-title">插件列表</h1>
        <p class="page-desc">每个插件把一家第三方上传接口转换成统一标准输出。点击插件配置临时参数，异常插件会置顶标红。</p>
      </div>
      <div class="btn-group">
        <button class="btn" @click="checkAll">◎ 全部检测</button>
        <button class="btn" @click="load">↻ 刷新</button>
        <button class="btn btn--primary" @click="go('docs', 'plugin-development')">开发文档 →</button>
      </div>
    </div>

    <div v-if="unhealthyCount" class="alert-strip">
      <div style="font-family:var(--serif);font-size:22px;line-height:1;color:var(--accent)">!</div>
      <div style="flex:1">
        <div class="alert-strip-title">{{ unhealthyCount }} 个插件异常，已生成告警</div>
        <div class="alert-strip-body">常见原因：临时 Bearer Token 过期、必填参数缺失、上游服务不可达。进入插件详情更新参数后执行调试即可恢复。</div>
      </div>
      <button class="btn btn--danger btn--sm" @click="go('alerts')">查看告警</button>
    </div>

    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>插件</th><th>状态</th><th>版本</th><th>优先级</th><th>定时检测</th><th>上次检测</th><th>提示</th><th></th></tr>
        </thead>
        <tbody>
          <tr v-for="p in plugins" :key="p.id" :class="{ 'is-unhealthy': p.status === 'unhealthy' }">
            <td style="cursor:pointer" @click="go('plugin', p.id)">
              <div class="cell-main">{{ p.name }} <span v-if="!p.enabled" class="tag">停用</span></div>
              <div class="cell-sub">{{ p.id }} · {{ p.kind }} · {{ p.author }}</div>
            </td>
            <td><span class="status" :class="'status--' + p.status">{{ statusText(p.status) }}</span></td>
            <td class="mono">{{ p.version }}</td>
            <td class="num mono">{{ p.priority }}</td>
            <td>
              <div class="mono tiny">{{ p.health?.enabled ? p.health.intervalSecs + 's / ' + p.health.failThreshold + ' 次' : '已关闭' }}</div>
              <button class="btn btn--quiet btn--sm" style="padding:2px 4px;font-size:11px" @click="openHealth(p)">设置</button>
            </td>
            <td class="muted tiny nowrap">{{ relTime(p.lastCheckedAt) }}<div v-if="p.latencyMs" class="cell-sub">{{ p.latencyMs }}ms</div></td>
            <td class="tiny" style="max-width:260px">
              <span v-if="p.warning" style="color:var(--warn)">{{ truncate(p.warning, 72) }}</span>
              <span v-else class="muted">{{ truncate(p.statusMessage, 72) || '—' }}</span>
            </td>
            <td>
              <div class="row-actions">
                <button class="btn btn--sm" :disabled="busyId === p.id" @click="check(p)">检测</button>
                <button class="btn btn--sm" @click="go('plugin', p.id)">配置</button>
                <button class="btn btn--quiet btn--sm" :disabled="busyId === p.id" @click="toggle(p)">{{ p.enabled ? '停用' : '启用' }}</button>
              </div>
            </td>
          </tr>
          <tr v-if="!plugins.length && !loading"><td colspan="8"><div class="empty"><div class="empty-title">没有插件</div><p>内置插件类型会在启动后自动注册。</p></div></td></tr>
        </tbody>
      </table>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">参数配置说明</h2><p class="section-note">插件的差异集中在参数上，无需改代码</p></div></div>
      <div class="panel" style="margin-top:18px">
        <p style="margin:0;font-size:13px;line-height:1.8">
          在插件详情页，表单由插件清单（Manifest）的 <code class="code">Params</code> 自动生成：不同类型渲染为文本框、密码框、下拉框或开关，按 <code class="code">Group</code> 分区。
          标记为 <span class="tag tag--warn">临时参数</span> 的字段（如 WorkBuddy 的 Bearer Token）需要设置过期时间，系统会在到期前 24 小时提醒，过期后健康检测判定为异常并在列表标红。
          敏感字段在服务端以 AES-256-GCM 加密存储，列表只返回脱敏值。
        </p>
      </div>
    </div>

    <!-- 定时检测设置抽屉 -->
    <template v-if="healthTarget">
      <div class="drawer-backdrop" @click="healthTarget = null"></div>
      <aside class="drawer">
        <div class="drawer-head">
          <div>
            <div class="eyebrow">Health Check</div>
            <h2 class="section-title">定时检测 · {{ healthTarget.name }}</h2>
            <p class="section-note">每个插件独立配置，保存后调度器会重建该插件的检测任务</p>
          </div>
          <button class="icon-btn" @click="healthTarget = null">✕</button>
        </div>
        <div class="drawer-body">
          <div class="form-grid">
            <div class="field">
              <label>启用定时检测</label>
              <div class="checkbox-row"><input type="checkbox" v-model="healthForm.enabled" /><span>按下方间隔自动探活</span></div>
            </div>
            <div class="field">
              <label>检测间隔（秒）<span class="req">*</span></label>
              <input type="number" min="10" v-model.number="healthForm.intervalSecs" />
              <div class="field-help">最小 10 秒，避免对第三方造成压力</div>
            </div>
            <div class="field">
              <label>单次超时（秒）</label>
              <input type="number" min="1" max="120" v-model.number="healthForm.timeoutSecs" />
            </div>
            <div class="field">
              <label>失败阈值</label>
              <input type="number" min="1" v-model.number="healthForm.failThreshold" />
              <div class="field-help">连续失败达到该次数后标记为异常并生成告警</div>
            </div>
            <div class="field">
              <label>探活模式</label>
              <select v-model="healthForm.probeMode">
                <option value="ping">ping — 轻量连通性探测（无副作用）</option>
                <option value="upload-sample">upload-sample — 样例上传</option>
              </select>
            </div>
          </div>
          <div class="divider"></div>
          <p class="tiny muted" style="line-height:1.8">
            检测失败会累加计数；达到阈值后插件状态变为 <span class="status status--unhealthy" style="display:inline-flex">异常</span>，
            列表行标红并生成一条告警。检测恢复成功时，未确认告警会自动标记为「系统已恢复」。
          </p>
        </div>
        <div class="drawer-foot">
          <button class="btn" @click="healthTarget = null">取消</button>
          <button class="btn btn--primary" :disabled="saving" @click="saveHealth">{{ saving ? '保存中…' : '保存设置' }}</button>
        </div>
      </aside>
    </template>
  </div>
</template>
