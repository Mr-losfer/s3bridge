<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { go } from '../router'
import { notifyError } from '../toast'
import { formatBytes, relTime, statusText, categoryLabel, fmtTime } from '../utils'
import UpdateCard from '../components/UpdateCard.vue'

const info = ref(null)
const stats = ref(null)
const plugins = ref([])
const alerts = ref([])
const uploads = ref([])
const loading = ref(true)

const badPlugins = computed(() => plugins.value.filter((p) => p.status === 'unhealthy'))

async function load() {
  loading.value = true
  try {
    const [i, s, p, a, u] = await Promise.all([
      api('/admin/site'), api('/admin/stats'), api('/plugins'),
      api('/alerts?limit=6'), api('/uploads?limit=6'),
    ])
    info.value = i; stats.value = s
    plugins.value = p || []
    alerts.value = a || []
    uploads.value = u?.items || []
  } catch (e) { notifyError(e, '加载概览失败') }
  finally { loading.value = false }
}
onMounted(load)
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Overview / 站点总览</div>
        <h1 class="page-title">运行态势</h1>
        <p class="page-desc">插件健康、上传归档与多租户接入的一屏总览。异常插件会置顶标红，无需进入二级页面确认。</p>
      </div>
      <div class="btn-group">
        <button class="btn" @click="go('uploadtest')">↑ 上传测试</button>
        <button class="btn btn--primary" @click="go('plugins')">管理插件 →</button>
      </div>
    </div>

    <UpdateCard />

    <div v-if="badPlugins.length" class="alert-strip">
      <div style="font-family:var(--serif);font-size:22px;line-height:1;color:var(--accent)">!</div>
      <div style="flex:1">
        <div class="alert-strip-title">{{ badPlugins.length }} 个插件处于异常状态</div>
        <div class="alert-strip-body">
          {{ badPlugins.map((p) => p.name + (p.statusMessage ? '（' + p.statusMessage + '）' : '')).join('；') }}
        </div>
      </div>
      <button class="btn btn--danger btn--sm" @click="go('plugins')">立即处理</button>
    </div>

    <div class="metrics">
      <div class="metric">
        <div class="metric-label">插件 / 异常</div>
        <div class="metric-value" :class="{ 'is-alert': badPlugins.length }">
          {{ info?.plugins ?? '—' }}<span class="metric-unit">/ {{ info?.unhealthyPlugins ?? 0 }} 异常</span>
        </div>
        <div class="metric-foot">{{ info?.healthyPlugins ?? 0 }} 个健康 · 定时检测已启用 {{ Object.keys(info?.scheduler || {}).length }} 项</div>
      </div>
      <div class="metric">
        <div class="metric-label">已归档对象</div>
        <div class="metric-value">{{ info?.objects ?? '—' }}<span class="metric-unit">files</span></div>
        <div class="metric-foot">合计 {{ formatBytes(info?.totalBytes) }}</div>
      </div>
      <div class="metric">
        <div class="metric-label">存储桶 / 用户</div>
        <div class="metric-value">{{ info?.buckets ?? '—' }}<span class="metric-unit">/ {{ info?.users ?? 0 }} 用户</span></div>
        <div class="metric-foot">按 bucket 与文件类型双维度归档</div>
      </div>
      <div class="metric">
        <div class="metric-label">未处理告警</div>
        <div class="metric-value" :class="{ 'is-alert': info?.openAlerts }">{{ info?.openAlerts ?? '—' }}</div>
        <div class="metric-foot">运行时长 {{ info?.uptime || '—' }}</div>
      </div>
    </div>

    <div class="section">
      <div class="section-head">
        <div>
          <h2 class="section-title">插件健康</h2>
          <p class="section-note">按异常优先排序，点击行进入参数配置</p>
        </div>
        <button class="btn btn--quiet btn--sm" @click="load">↻ 刷新</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>插件</th><th>状态</th><th>版本</th><th>检测间隔</th><th>上次检测</th><th>提示</th><th></th></tr></thead>
          <tbody>
            <tr v-for="p in plugins" :key="p.id" :class="{ 'is-unhealthy': p.status === 'unhealthy', 'is-clickable': true }" @click="go('plugin', p.id)">
              <td>
                <div class="cell-main">{{ p.name }}</div>
                <div class="cell-sub">{{ p.id }} · {{ p.kind }}</div>
              </td>
              <td><span class="status" :class="'status--' + p.status">{{ statusText(p.status) }}</span></td>
              <td class="mono">{{ p.version }}</td>
              <td class="mono">{{ p.health?.intervalSecs }}s</td>
              <td class="muted tiny">{{ relTime(p.lastCheckedAt) }}</td>
              <td class="tiny" style="max-width:280px">
                <span v-if="p.warning" style="color:var(--warn)">{{ p.warning }}</span>
                <span v-else class="muted">{{ p.statusMessage || '—' }}</span>
              </td>
              <td class="right"><span class="muted">→</span></td>
            </tr>
            <tr v-if="!plugins.length && !loading"><td colspan="7"><div class="empty"><div class="empty-title">尚无插件</div><p>内置插件会在服务启动时自动注册。</p></div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="section">
      <div class="section-head">
        <div>
          <h2 class="section-title">最近上传</h2>
          <p class="section-note">统一标准输出，按 bucket 与文件类型归档</p>
        </div>
        <button class="btn btn--quiet btn--sm" @click="go('uploads')">查看全部 →</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>文件名</th><th>类型</th><th>存储桶</th><th>来源插件</th><th>大小</th><th>时间</th></tr></thead>
          <tbody>
            <tr v-for="o in uploads" :key="o.id">
              <td class="cell-main">{{ o.fileName }}</td>
              <td><span class="tag">{{ categoryLabel(o.category) }}</span></td>
              <td class="mono">{{ o.bucket }}</td>
              <td class="mono">{{ o.provider }}</td>
              <td class="num">{{ formatBytes(o.size) }}</td>
              <td class="muted tiny">{{ relTime(o.createdAt) }}</td>
            </tr>
            <tr v-if="!uploads.length"><td colspan="6"><div class="empty"><div class="empty-title">还没有上传记录</div><p>去「上传测试」用一张图片验证插件是否配置正确。</p><button class="btn btn--primary btn--sm" @click="go('uploadtest')">开始测试</button></div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">站点信息</h2><p class="section-note">与 MCP 的 site_info 工具返回一致</p></div></div>
      <div class="panel" style="margin-top:18px">
        <dl class="kv">
          <dt>名称 / 版本</dt><dd>{{ info?.siteName || 'S3Bridge' }} · {{ info?.version }}</dd>
          <dt>启动时间</dt><dd>{{ fmtTime(info?.startedAt) }}（已运行 {{ info?.uptime }}）</dd>
          <dt>S3 接入点</dt><dd><code class="code">{{ info?.s3Endpoint }}</code> · region <code class="code">{{ info?.s3Region }}</code></dd>
          <dt>MCP 端点</dt><dd><code class="code">POST /mcp</code> · 工具清单见「MCP 能力」</dd>
          <dt>定时检测</dt><dd class="mono">{{ JSON.stringify(info?.scheduler || {}) }}</dd>
          <dt>文件类型分布</dt><dd class="mono">{{ JSON.stringify(stats?.byCategory || {}) }}</dd>
        </dl>
      </div>
    </div>
  </div>
</template>
