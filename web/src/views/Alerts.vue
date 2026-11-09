<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { go } from '../router'
import { notify, notifyError } from '../toast'
import { fmtTime, relTime } from '../utils'

const alerts = ref([])
const loading = ref(true)
const open = computed(() => alerts.value.filter((a) => !a.ackedAt))

async function load() {
  loading.value = true
  try { alerts.value = (await api('/alerts?limit=200')) || [] }
  catch (e) { notifyError(e, '加载告警失败') }
  finally { loading.value = false }
}
onMounted(load)

async function ack(a) {
  try {
    await api(`/alerts/${encodeURIComponent(a.id)}/ack`, { method: 'POST', body: {} })
    notify('告警已确认', { title: a.pluginName })
    await load()
  } catch (e) { notifyError(e) }
}
async function ackAll() {
  for (const a of open.value) {
    try { await api(`/alerts/${encodeURIComponent(a.id)}/ack`, { method: 'POST', body: {} }) } catch { /* continue */ }
  }
  notify(`已确认 ${open.value.length} 条告警`)
  await load()
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Alerts / 告警中心</div>
        <h1 class="page-title">异常告警</h1>
        <p class="page-desc">插件健康检测连续失败、上传失败或临时凭据过期时自动产生告警；插件恢复后系统会自动确认。</p>
      </div>
      <div class="btn-group">
        <button class="btn" :disabled="!open.length" @click="ackAll">✓ 全部确认</button>
        <button class="btn" @click="load">↻ 刷新</button>
      </div>
    </div>

    <div class="metrics" style="margin-bottom:30px">
      <div class="metric"><div class="metric-label">未处理</div><div class="metric-value" :class="{ 'is-alert': open.length }">{{ open.length }}</div><div class="metric-foot">需要人工介入</div></div>
      <div class="metric"><div class="metric-label">历史告警</div><div class="metric-value">{{ alerts.length }}</div><div class="metric-foot">同一插件未处理告警只保留一条</div></div>
      <div class="metric"><div class="metric-label">异常插件</div><div class="metric-value is-alert">{{ new Set(open.map(a => a.pluginId)).size }}</div><div class="metric-foot">插件列表中显示为红色</div></div>
    </div>

    <div class="table-wrap">
      <table>
        <thead><tr><th>级别</th><th>插件</th><th>标题</th><th>详情</th><th>产生时间</th><th>状态</th><th></th></tr></thead>
        <tbody>
          <tr v-for="a in alerts" :key="a.id" :class="{ 'is-unhealthy': !a.ackedAt }">
            <td><span class="tag" :class="a.level === 'critical' ? 'tag--accent' : 'tag--warn'">{{ a.level }}</span></td>
            <td class="cell-main" style="cursor:pointer" @click="go('plugin', a.pluginId)">{{ a.pluginName }}<div class="cell-sub">{{ a.pluginId }}</div></td>
            <td class="cell-main">{{ a.title }}</td>
            <td class="tiny" style="max-width:380px;word-break:break-word">{{ a.message }}</td>
            <td class="tiny muted nowrap">{{ fmtTime(a.createdAt) }}<div class="cell-sub">{{ relTime(a.createdAt) }}</div></td>
            <td>
              <span v-if="a.ackedAt" class="status status--healthy">已确认</span>
              <span v-else class="status status--unhealthy">未处理</span>
              <div v-if="a.ackedAt" class="cell-sub">by {{ a.ackedBy }}</div>
            </td>
            <td class="right">
              <button v-if="!a.ackedAt" class="btn btn--sm" @click="ack(a)">确认</button>
              <button v-else class="btn btn--quiet btn--sm" @click="go('plugin', a.pluginId)">查看插件</button>
            </td>
          </tr>
          <tr v-if="!alerts.length && !loading"><td colspan="7"><div class="empty"><div class="empty-title">没有告警</div><p>所有插件运行正常。异常发生时这里会自动出现记录。</p><button class="btn btn--sm" @click="go('plugins')">查看插件状态</button></div></td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
