<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import {
  Activity, AlertTriangle, ArrowUpRight, Clock, Database, Gauge,
  HardDrive, Loader2, RefreshCw, Search, ShieldAlert, Stethoscope,
  Timer, TriangleAlert, Upload, Waves, Boxes, FileWarning, Copy,
} from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { notifyError, notify } from '../toast'
import { formatBytes, relTime, fmtTime } from '../utils'
import PageHeader from '../components/PageHeader.vue'
import StatCard from '../components/StatCard.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import CardHeader from '../components/ui/CardHeader.vue'
import CardTitle from '../components/ui/CardTitle.vue'
import CardDescription from '../components/ui/CardDescription.vue'
import CardContent from '../components/ui/CardContent.vue'
import Tabs from '../components/ui/Tabs.vue'
import TabContent from '../components/ui/TabContent.vue'
import Select from '../components/ui/Select.vue'
import Input from '../components/ui/Input.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'
import TrendChart from '../components/charts/TrendChart.vue'
import BarList from '../components/charts/BarList.vue'
import Donut from '../components/charts/Donut.vue'

const tab = ref('overview')
const range = ref('24h')
const loading = ref(true)
const refreshing = ref(false)
const data = ref(null)
const storage = ref(null)
const scanning = ref(false)

const events = ref({ items: [], total: 0, categories: {} })
const evFilter = ref({ level: '', category: '', q: '' })
const evLoading = ref(false)
const evOffset = ref(0)
const evLimit = 40
const evHasMore = computed(() => events.value.items.length < (events.value.total || 0))

let timer = null

const tabItems = [
  { value: 'overview', label: '总览', icon: Activity },
  { value: 'storage', label: '存储', icon: Database },
  { value: 'api', label: '接口', icon: Timer },
  { value: 'events', label: '异常日志', icon: ShieldAlert },
]

const rangeItems = [
  { value: '1h', label: '1 小时' },
  { value: '6h', label: '6 小时' },
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
]

const summary = computed(() => data.value?.summary || null)
const totals = computed(() => summary.value?.totals || {})
const health = computed(() => summary.value?.health || {})
const disk = computed(() => health.value.disk || {})

const points = computed(() => {
  const raw = (summary.value?.series || []).map((p) => ({
    ...p,
    err: (p.err4 || 0) + (p.err5 || 0),
  }))
  // 服务刚启动时只有当前桶一个采样点，补一个零值起点，
  // 让曲线从 0 爬升而不是显示空白，语义上仍是「本区间从无流量开始」。
  if (raw.length === 1) {
    const step = summary.value?.stepSecs || 60
    return [{ ...raw[0], t: raw[0].t - step, req: 0, err: 0, upload: 0, p50: 0, p95: 0, p99: 0, avgMs: 0, slow: 0, bytesIn: 0, bytesOut: 0 }, raw[0]]
  }
  return raw
})
const reqSeries = [
  { key: 'req', label: '请求', color: 'var(--primary)' },
  { key: 'err', label: '错误', color: 'var(--destructive)' },
]
const latencySeries = [
  { key: 'p95', label: 'P95', color: 'var(--info)' },
  { key: 'avgMs', label: '均值', color: 'var(--success)' },
]

const statusDist = computed(() => [
  { label: '2xx 成功', value: totals.value.ok2 || 0, color: 'var(--success)' },
  { label: '3xx 重定向', value: totals.value.ok3 || 0, color: 'var(--info)' },
  { label: '4xx 客户端错误', value: totals.value.err4 || 0, color: 'var(--warning)' },
  { label: '5xx 服务端错误', value: totals.value.err5 || 0, color: 'var(--destructive)' },
])

const slowest = computed(() => (summary.value?.slowest || []).filter((r) => r.count > 0))
const errRoutes = computed(() => (summary.value?.errRoutes || []).filter((r) => r.errors > 0))

const fast = computed(() => storage.value?.summary || null)
const deep = computed(() => storage.value?.deep || null)
const buckets = computed(() => fast.value?.buckets || [])
const categories = computed(() => (fast.value?.categories || []).filter((c) => c.objects > 0))

const catColors = {
  image: 'var(--primary)',
  video: 'var(--info)',
  audio: 'var(--success)',
  document: 'var(--warning)',
  archive: 'oklch(0.6 0.09 300)',
  other: 'var(--muted-foreground)',
}
const donutItems = computed(() =>
  categories.value.map((c) => ({ label: c.label || c.key, value: c.bytes, color: catColors[c.key] || 'var(--muted-foreground)' })),
)

const issues = computed(() => {
  const d = deep.value
  if (!d) return []
  return [
    { key: 'orphan', label: '孤儿文件', count: (d.orphans || []).length, bytes: d.orphanBytes || 0, hint: '磁盘上有文件，元数据里没有记录', tone: 'warning' },
    { key: 'missing', label: '断链对象', count: (d.missing || []).length, bytes: 0, hint: '元数据存在但文件已丢失', tone: 'destructive' },
    { key: 'dup', label: '重复内容', count: (d.duplicates || []).length, bytes: (d.duplicates || []).reduce((n, g) => n + (g.wastedBytes || 0), 0), hint: '相同 SHA-256 多份存储', tone: 'info' },
    { key: 'stale', label: '超期对象', count: (d.stale || []).length, bytes: 0, hint: '超过保留天数的历史文件', tone: 'muted' },
  ].filter((i) => i.count > 0)
})

const diskPercent = computed(() => Math.min(100, Math.max(0, disk.value.usedPercent || 0)))

function fmtTick(t) {
  const d = new Date(t * 1000)
  if (range.value === '7d' || range.value === '30d') return `${d.getMonth() + 1}/${d.getDate()}`
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}
function fmtMs(v) {
  if (!v) return '0'
  return v < 10 ? v.toFixed(1) : String(Math.round(v))
}
function fmtUptime(secs) {
  const s = Math.max(0, Number(secs) || 0)
  if (s < 60) return s + ' 秒'
  if (s < 3600) return Math.floor(s / 60) + ' 分钟'
  if (s < 86400) return (s / 3600).toFixed(1) + ' 小时'
  return Math.floor(s / 86400) + ' 天'
}
function fmtClock(iso) {
  const d = new Date(iso)
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
  return sameDay ? hm : `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}
const levelMeta = {
  error: { label: '错误', variant: 'destructive' },
  warn: { label: '警告', variant: 'warning' },
  info: { label: '提示', variant: 'info' },
}
const catList = computed(() => Object.entries(events.value.categories || data.value?.categoryLabels || {}))

async function loadSummary() {
  try {
    const r = await api(`/monitor/summary?range=${range.value}`)
    data.value = r
  } catch (e) {
    if (!data.value) notifyError(e, '加载监控数据失败')
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

async function loadStorage() {
  try {
    storage.value = await api('/monitor/storage')
    scanning.value = !!storage.value.scanning
    if (scanning.value) pollScan()
  } catch (e) {
    if (!storage.value) notifyError(e, '加载存储画像失败')
  }
}

async function loadEvents(reset = true) {
  if (evLoading.value) return
  evLoading.value = true
  if (reset) evOffset.value = 0
  try {
    const q = new URLSearchParams({ limit: String(evLimit), offset: String(evOffset.value) })
    if (evFilter.value.level) q.set('level', evFilter.value.level)
    if (evFilter.value.category) q.set('category', evFilter.value.category)
    if (evFilter.value.q) q.set('q', evFilter.value.q)
    if (range.value !== '30d') q.set('seconds', String(rangeSecs[range.value] || 86400))
    const r = await api('/monitor/events?' + q.toString())
    if (reset) events.value = { ...r, items: r.items || [] }
    else events.value = { ...r, items: [...events.value.items, ...(r.items || [])] }
  } catch (e) {
    notifyError(e, '加载异常日志失败')
  } finally {
    evLoading.value = false
  }
}

const rangeSecs = { '1h': 3600, '6h': 21600, '24h': 86400, '7d': 604800, '30d': 2592000 }

function loadMore() {
  evOffset.value = events.value.items.length
  loadEvents(false)
}

async function runScan() {
  if (scanning.value) return
  try {
    await api('/monitor/storage/scan', { method: 'POST' })
    scanning.value = true
    notify('正在扫描磁盘与元数据，通常几秒内完成', { title: '深度体检已开始' })
    pollScan()
  } catch (e) {
    notifyError(e, '触发体检失败')
  }
}

let pollTimer = null
function pollScan() {
  clearTimeout(pollTimer)
  pollTimer = setTimeout(async () => {
    try {
      const r = await api('/monitor/storage/scan')
      scanning.value = !!r.scanning
      if (!r.scanning) {
        await loadStorage()
        return
      }
      pollScan()
    } catch { scanning.value = false }
  }, 1200)
}

function refreshAll() {
  refreshing.value = true
  loadSummary()
  if (tab.value === 'storage' || tab.value === 'overview') loadStorage()
  if (tab.value === 'events') loadEvents()
}

watch(range, () => { loadSummary(); if (tab.value === 'events') loadEvents() })
watch(evFilter, () => loadEvents(), { deep: true })
watch(tab, (v) => {
  if (v === 'storage' && !storage.value) loadStorage()
  if (v === 'events' && !events.value.items.length) loadEvents()
})

onMounted(async () => {
  await Promise.all([loadSummary(), loadStorage()])
  timer = setInterval(() => {
    if (document.hidden) return
    loadSummary()
    if (tab.value === 'storage') loadStorage()
  }, 20000)
})
onUnmounted(() => {
  clearInterval(timer)
  clearTimeout(pollTimer)
})
</script>

<template>
  <div>
    <PageHeader
      title="监控中心"
      description="请求吞吐、延迟分位、异常分类与存储体检的一体化视图；数据在内存中聚合，落盘节流，不拖慢上传与 S3 主链路。"
    >
      <template #actions>
        <Select v-model="range" class="h-9 w-32 flex-none">
          <option v-for="r in rangeItems" :key="r.value" :value="r.value">{{ r.label }}</option>
        </Select>
        <Button variant="outline" :disabled="refreshing" @click="refreshAll">
          <RefreshCw :class="refreshing ? 'animate-spin' : ''" />刷新
        </Button>
      </template>
    </PageHeader>

    <div v-if="loading" class="space-y-4">
      <div class="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">
        <Skeleton v-for="i in 6" :key="i" class="h-24 rounded-xl" />
      </div>
      <Skeleton class="h-64 rounded-xl" />
    </div>

    <template v-else>
      <Tabs v-model="tab" :items="tabItems">
        <!-- ---------- 总览 ---------- -->
        <TabContent value="overview" class="mt-5 space-y-4">
          <div class="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">
            <StatCard label="请求数" :value="totals.req || 0" hint="含 REST / S3 / MCP" :icon="Activity" />
            <StatCard
              label="错误率" :value="((totals.errRate || 0) * 100).toFixed(2)" unit="%"
              :hint="`4xx ${totals.err4 || 0} · 5xx ${totals.err5 || 0}`" :icon="TriangleAlert"
              :tone="(totals.errRate || 0) > 0.05 ? 'danger' : 'default'"
            />
            <StatCard label="P95 延迟" :value="fmtMs(totals.p95)" unit="ms" :hint="`P50 ${fmtMs(totals.p50)} · P99 ${fmtMs(totals.p99)}`" :icon="Gauge" />
            <StatCard label="上传次数" :value="totals.upload || 0" :hint="`入流量 ${formatBytes(totals.bytesIn || 0)}`" :icon="Upload" />
            <StatCard label="出流量" :value="formatBytes(totals.bytesOut || 0)" hint="响应体字节数" :icon="Waves" value-class="text-lg sm:text-2xl" />
            <StatCard label="慢请求" :value="totals.slow || 0" unit="次" hint="耗时超过 1s" :icon="Clock" />
          </div>

          <div class="grid gap-4 xl:grid-cols-3">
            <Card class="xl:col-span-2">
              <CardHeader class="pb-2">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <CardTitle>请求与错误</CardTitle>
                    <CardDescription>按 {{ summary?.stepSecs >= 3600 ? '小时' : summary?.stepSecs >= 300 ? '5 分钟' : '分钟' }} 聚合，共 {{ points.length }} 个采样点</CardDescription>
                  </div>
                  <div class="flex items-center gap-3 text-[11px]">
                    <span class="flex items-center gap-1.5"><span class="size-1.5 rounded-full bg-primary" />请求</span>
                    <span class="flex items-center gap-1.5"><span class="size-1.5 rounded-full bg-destructive" />错误</span>
                  </div>
                </div>
              </CardHeader>
              <CardContent>
                <TrendChart :points="points" :series="reqSeries" :height="180" :format-time="fmtTick" :format-value="(v) => v" />
              </CardContent>
            </Card>

            <Card>
              <CardHeader class="pb-2">
                <CardTitle>响应状态</CardTitle>
                <CardDescription>区间内状态码分层</CardDescription>
              </CardHeader>
              <CardContent>
                <BarList :items="statusDist" :format-value="(v) => String(v)" />
                <div class="mt-4 grid grid-cols-2 gap-3 border-t pt-3 text-[11.5px]">
                  <div>
                    <p class="text-muted-foreground">运行时长</p>
                    <p class="mt-0.5 font-medium">{{ fmtUptime(health.uptimeSecs) }}</p>
                  </div>
                  <div>
                    <p class="text-muted-foreground">goroutine</p>
                    <p class="mt-0.5 font-medium tabular-nums">{{ health.goroutines || 0 }}</p>
                  </div>
                  <div>
                    <p class="text-muted-foreground">堆内存</p>
                    <p class="mt-0.5 font-medium">{{ formatBytes(health.heapBytes || 0) }}</p>
                  </div>
                  <div>
                    <p class="text-muted-foreground">GC 次数</p>
                    <p class="mt-0.5 font-medium tabular-nums">{{ health.numGC || 0 }}</p>
                  </div>
                </div>
                <div v-if="disk.available" class="mt-3 border-t pt-3">
                  <div class="mb-1 flex items-center justify-between text-[11.5px]">
                    <span class="text-muted-foreground">磁盘占用</span>
                    <span class="font-mono tabular-nums">{{ diskPercent.toFixed(1) }}%</span>
                  </div>
                  <div class="h-1.5 overflow-hidden rounded-full bg-muted">
                    <div
                      class="h-full rounded-full bg-primary transition-[width]"
                      :class="diskPercent > 85 ? 'bg-destructive' : diskPercent > 70 ? 'bg-warning' : 'bg-primary'"
                      :style="{ width: diskPercent + '%' }"
                    />
                  </div>
                  <p class="mt-1 text-[10.5px] text-muted-foreground">
                    可用 {{ formatBytes(disk.freeBytes) }} / 共 {{ formatBytes(disk.totalBytes) }}
                  </p>
                </div>
              </CardContent>
            </Card>
          </div>

          <div class="grid gap-4 xl:grid-cols-3">
            <Card class="xl:col-span-2">
              <CardHeader class="pb-2">
                <CardTitle>延迟分位</CardTitle>
                <CardDescription>P95 反映尾部体验，尖刺通常来自上游插件或大文件传输</CardDescription>
              </CardHeader>
              <CardContent>
                <TrendChart :points="points" :series="latencySeries" :height="150" :format-time="fmtTick" :format-value="(v) => fmtMs(v) + ' ms'" />
              </CardContent>
            </Card>

            <Card>
              <CardHeader class="pb-2">
                <div class="flex items-center justify-between">
                  <CardTitle>最近异常</CardTitle>
                  <Button variant="link" size="sm" class="h-auto px-0 text-[11px]" @click="tab = 'events'">
                    全部<ArrowUpRight />
                  </Button>
                </div>
              </CardHeader>
              <CardContent>
                <div v-if="(data?.recent || []).length" class="space-y-2">
                  <button
                    v-for="e in data.recent" :key="e.id"
                    class="w-full rounded-lg border bg-muted/20 px-2.5 py-2 text-left transition-colors hover:bg-muted/50"
                    @click="tab = 'events'"
                  >
                    <div class="flex items-center gap-2">
                      <Badge :variant="levelMeta[e.level]?.variant || 'outline'">{{ levelMeta[e.level]?.label || e.level }}</Badge>
                      <span class="font-mono text-[10.5px] text-muted-foreground">{{ e.status }}</span>
                      <span class="ml-auto text-[10.5px] text-muted-foreground">{{ relTime(e.time) }}</span>
                    </div>
                    <p class="mt-1 line-clamp-2 text-[11.5px] leading-relaxed">{{ e.message }}</p>
                    <p class="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">{{ e.method }} {{ e.route }}</p>
                  </button>
                </div>
                <Empty v-else title="没有异常" description="最近 24 小时没有 error 级别事件。" />
              </CardContent>
            </Card>
          </div>
        </TabContent>

        <!-- ---------- 存储 ---------- -->
        <TabContent value="storage" class="mt-5 space-y-4">
          <div v-if="!fast" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Skeleton v-for="i in 4" :key="i" class="h-24 rounded-xl" />
          </div>
          <template v-else>
            <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
              <StatCard label="对象总数" :value="fast.objects" unit="个" :hint="`${fast.buckets.length} 个存储桶`" :icon="Boxes" />
              <StatCard label="存储占用" :value="formatBytes(fast.bytes)" hint="元数据统计体积" :icon="Database" value-class="text-lg sm:text-2xl" />
              <StatCard
                label="磁盘可用" :value="formatBytes(disk.freeBytes || 0)"
                :hint="disk.available ? `共 ${formatBytes(disk.totalBytes)} · 已用 ${diskPercent.toFixed(1)}%` : '当前平台不可读'"
                :icon="HardDrive" value-class="text-lg sm:text-2xl"
              />
              <StatCard
                label="体检问题" :value="issues.reduce((n, i) => n + i.count, 0)" unit="项"
                :hint="deep ? `扫描 ${deep.orphanScanned} 个文件 · ${deep.durationMs} ms` : '尚未执行深度体检'"
                :icon="Stethoscope" :tone="issues.length ? 'danger' : 'default'"
              />
            </div>

            <div class="grid gap-4 xl:grid-cols-3">
              <Card class="xl:col-span-2">
                <CardHeader class="pb-2">
                  <CardTitle>存储桶容量排行</CardTitle>
                  <CardDescription>按体积排序，健康度反映断链比例</CardDescription>
                </CardHeader>
                <CardContent>
                  <BarList
                    v-if="buckets.length"
                    :items="buckets.map((b) => ({
                      label: b.name,
                      value: b.bytes,
                      hint: `${b.objects} 个对象`,
                      sub: `健康度 ${b.health}%${b.publicRead ? ' · 公开读' : ''}`,
                    }))"
                    :format-value="formatBytes"
                  />
                  <Empty v-else title="还没有存储桶" description="创建存储桶并上传对象后，这里会显示容量分布。" />
                </CardContent>
              </Card>

              <Card>
                <CardHeader class="pb-2">
                  <CardTitle>文件类型分布</CardTitle>
                  <CardDescription>按存储体积占比</CardDescription>
                </CardHeader>
                <CardContent>
                  <Donut
                    v-if="donutItems.length"
                    :items="donutItems" center-label="总体积" :center-value="formatBytes(fast.bytes)"
                    :size="132"
                  />
                  <Empty v-else title="暂无对象" description="上传文件后展示类型构成。" />
                </CardContent>
              </Card>
            </div>

            <Card>
              <CardHeader class="pb-3">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <CardTitle>存储体检</CardTitle>
                    <CardDescription>
                      孤儿文件、断链对象、重复内容与超期数据；
                      <span v-if="deep">上次扫描 {{ relTime(deep.generatedAt) }}，耗时 {{ deep.durationMs }} ms</span>
                      <span v-else>尚未执行，点击右侧按钮扫描磁盘</span>
                    </CardDescription>
                  </div>
                  <Button variant="outline" :disabled="scanning" @click="runScan">
                    <Loader2 v-if="scanning" class="animate-spin" /><Stethoscope v-else />
                    {{ scanning ? '扫描中…' : '立即体检' }}
                  </Button>
                </div>
              </CardHeader>
              <CardContent class="space-y-4">
                <div v-if="issues.length" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  <div v-for="i in issues" :key="i.key" class="rounded-lg border bg-muted/20 p-3">
                    <div class="flex items-center justify-between">
                      <span class="text-[11.5px] text-muted-foreground">{{ i.label }}</span>
                      <Badge :variant="i.tone === 'destructive' ? 'destructive' : i.tone === 'warning' ? 'warning' : 'outline'">{{ i.count }}</Badge>
                    </div>
                    <p class="mt-1.5 text-lg font-semibold tabular-nums">{{ i.bytes ? formatBytes(i.bytes) : i.count + ' 项' }}</p>
                    <p class="mt-0.5 text-[10.5px] leading-relaxed text-muted-foreground">{{ i.hint }}</p>
                  </div>
                </div>
                <Empty v-else-if="deep" title="体检未发现问题" description="没有孤儿文件、断链、重复内容或超期对象。" />
                <div v-else class="rounded-lg border border-dashed bg-muted/10 px-4 py-6 text-center">
                  <p class="text-[12.5px] font-medium">尚未执行深度体检</p>
                  <p class="mt-1 text-[11.5px] text-muted-foreground">扫描对象目录可发现「文件在、记录丢了」与「记录在、文件没了」这两类隐蔽问题。</p>
                </div>

                <div v-if="deep && (deep.topObjects || []).length" class="grid gap-4 lg:grid-cols-2">
                  <div>
                    <p class="mb-2 text-[11.5px] font-medium text-muted-foreground">最大的对象</p>
                    <ul class="divide-y rounded-lg border">
                      <li v-for="o in deep.topObjects.slice(0, 8)" :key="o.bucket + o.key" class="flex items-center gap-2 px-3 py-2 text-[11.5px]">
                        <span class="min-w-0 flex-1 truncate">{{ o.fileName }}</span>
                        <span class="shrink-0 text-[10.5px] text-muted-foreground">{{ o.bucket }}</span>
                        <span class="shrink-0 font-mono tabular-nums">{{ formatBytes(o.size) }}</span>
                      </li>
                    </ul>
                  </div>
                  <div>
                    <p class="mb-2 text-[11.5px] font-medium text-muted-foreground">重复内容（可回收空间）</p>
                    <ul v-if="(deep.duplicates || []).length" class="divide-y rounded-lg border">
                      <li v-for="g in deep.duplicates.slice(0, 8)" :key="g.sha256" class="flex items-center gap-2 px-3 py-2 text-[11.5px]">
                        <Copy class="size-3.5 shrink-0 text-muted-foreground" />
                        <span class="min-w-0 flex-1 truncate font-mono text-[10.5px]">{{ g.sha256.slice(0, 12) }}…</span>
                        <span class="shrink-0 text-[10.5px] text-muted-foreground">{{ g.count }} 份</span>
                        <span class="shrink-0 font-mono tabular-nums text-warning">{{ formatBytes(g.wastedBytes) }}</span>
                      </li>
                    </ul>
                    <p v-else class="rounded-lg border border-dashed px-3 py-4 text-center text-[11px] text-muted-foreground">没有重复内容</p>
                  </div>
                </div>

                <div v-if="deep && (deep.orphans || []).length">
                  <p class="mb-2 text-[11.5px] font-medium text-muted-foreground">孤儿文件（磁盘存在、无元数据）</p>
                  <ul class="divide-y rounded-lg border">
                    <li v-for="o in deep.orphans.slice(0, 10)" :key="o.path" class="flex items-center gap-2 px-3 py-2 text-[11px]">
                      <FileWarning class="size-3.5 shrink-0 text-warning" />
                      <span class="min-w-0 flex-1 truncate font-mono">{{ o.path }}</span>
                      <span class="shrink-0 font-mono tabular-nums">{{ formatBytes(o.size) }}</span>
                      <span class="shrink-0 text-[10px] text-muted-foreground">{{ fmtTime(o.modTime) }}</span>
                    </li>
                  </ul>
                </div>
                <div v-if="deep && (deep.missing || []).length">
                  <p class="mb-2 text-[11.5px] font-medium text-muted-foreground">断链对象（元数据存在、文件丢失）</p>
                  <ul class="divide-y rounded-lg border">
                    <li v-for="o in deep.missing.slice(0, 10)" :key="o.bucket + o.key" class="flex items-center gap-2 px-3 py-2 text-[11px]">
                      <AlertTriangle class="size-3.5 shrink-0 text-destructive" />
                      <span class="min-w-0 flex-1 truncate">{{ o.fileName }}</span>
                      <span class="shrink-0 font-mono text-[10px] text-muted-foreground">{{ o.bucket }}</span>
                      <Button size="sm" variant="ghost" class="h-6 px-1.5 text-[10.5px]" @click="go('uploads', o.bucket)">查看</Button>
                    </li>
                  </ul>
                </div>
              </CardContent>
            </Card>
          </template>
        </TabContent>

        <!-- ---------- 接口 ---------- -->
        <TabContent value="api" class="mt-5 grid gap-4 xl:grid-cols-2">
          <Card>
            <CardHeader class="pb-2">
              <CardTitle>最慢接口</CardTitle>
              <CardDescription>按平均耗时排序，帮助定位性能瓶颈</CardDescription>
            </CardHeader>
            <CardContent>
              <BarList
                v-if="slowest.length"
                :items="slowest.map((r) => ({ label: r.route, value: r.avgMs, hint: `${r.count} 次请求` }))"
                :format-value="(v) => fmtMs(v) + ' ms'"
                color="var(--info)"
              />
              <Empty v-else title="还没有足够的样本" description="产生访问后这里会显示接口耗时排行。" />
            </CardContent>
          </Card>

          <Card>
            <CardHeader class="pb-2">
              <CardTitle>高错误接口</CardTitle>
              <CardDescription>4xx/5xx 次数最多的路由</CardDescription>
            </CardHeader>
            <CardContent>
              <BarList
                v-if="errRoutes.length"
                :items="errRoutes.map((r) => ({ label: r.route, value: r.errors, hint: `${r.count} 次请求` }))"
                :format-value="(v) => String(v) + ' 次'"
                color="var(--destructive)"
              />
              <Empty v-else title="没有错误接口" description="当前区间内所有请求都成功了。" />
            </CardContent>
          </Card>

          <Card class="xl:col-span-2">
            <CardHeader class="pb-2">
              <CardTitle>接口明细</CardTitle>
              <CardDescription>区间内出现过的路由与状态码分布</CardDescription>
            </CardHeader>
            <CardContent class="px-0 pt-0">
              <div class="overflow-x-auto">
                <table class="data-table">
                  <thead>
                    <tr>
                      <th class="pl-4">路由</th>
                      <th>请求数</th>
                      <th>错误</th>
                      <th>平均耗时</th>
                      <th>峰值</th>
                      <th class="pr-4">状态码</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="r in [...slowest, ...errRoutes].filter((v, i, a) => a.findIndex((x) => x.route === v.route) === i)" :key="r.route">
                      <td class="pl-4 font-mono text-[11px]">{{ r.route }}</td>
                      <td class="tabular-nums">{{ r.count }}</td>
                      <td class="tabular-nums" :class="r.errors ? 'text-destructive font-medium' : 'text-muted-foreground'">{{ r.errors }}</td>
                      <td class="tabular-nums">{{ fmtMs(r.avgMs) }} ms</td>
                      <td class="tabular-nums text-muted-foreground">{{ fmtMs(r.maxMs) }} ms</td>
                      <td class="pr-4">
                        <span class="flex flex-wrap gap-1">
                          <Badge v-for="(n, code) in r.status" :key="code" variant="outline" class="font-mono text-[10px]">{{ code }}×{{ n }}</Badge>
                        </span>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </CardContent>
          </Card>
        </TabContent>

        <!-- ---------- 异常日志 ---------- -->
        <TabContent value="events" class="mt-5">
          <Card>
            <CardHeader class="pb-3">
              <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <div>
                  <CardTitle>异常日志</CardTitle>
                  <CardDescription>共 {{ events.total }} 条 · 记录 4xx/5xx 与慢请求，普通 2xx 只进指标</CardDescription>
                </div>
                <div class="flex flex-wrap items-center gap-2">
                  <Select v-model="evFilter.level" class="h-8 w-28 text-xs">
                    <option value="">全部级别</option>
                    <option value="error">仅错误</option>
                    <option value="warn">仅警告</option>
                  </Select>
                  <Select v-model="evFilter.category" class="h-8 w-32 text-xs">
                    <option value="">全部分类</option>
                    <option v-for="[k, v] in catList" :key="k" :value="k">{{ v }}</option>
                  </Select>
                  <div class="relative w-full sm:w-48">
                    <Search class="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                    <Input v-model="evFilter.q" placeholder="搜索消息 / 路由 / 操作者" class="h-8 pl-8 text-xs" />
                  </div>
                </div>
              </div>
            </CardHeader>
            <CardContent class="px-0 pt-0">
              <div v-if="evLoading && !events.items.length" class="space-y-2 px-4 py-3">
                <Skeleton v-for="i in 6" :key="i" class="h-12" />
              </div>
              <template v-else-if="events.items.length">
                <!-- 桌面表格 -->
                <div class="hidden overflow-x-auto md:block">
                  <table class="data-table">
                    <thead>
                      <tr>
                        <th class="pl-4">时间</th>
                        <th>级别</th>
                        <th>分类</th>
                        <th>路由</th>
                        <th>状态</th>
                        <th>耗时</th>
                        <th>操作者</th>
                        <th class="pr-4">消息</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="e in events.items" :key="e.id">
                        <td class="pl-4 whitespace-nowrap font-mono text-[10.5px] text-muted-foreground">{{ fmtClock(e.time) }}</td>
                        <td><Badge :variant="levelMeta[e.level]?.variant || 'outline'">{{ levelMeta[e.level]?.label || e.level }}</Badge></td>
                        <td class="whitespace-nowrap text-[11.5px]">{{ events.categories[e.category] || e.category }}</td>
                        <td class="max-w-[16rem] truncate font-mono text-[10.5px]">{{ e.method }} {{ e.route }}</td>
                        <td class="font-mono tabular-nums" :class="e.status >= 500 ? 'text-destructive' : e.status >= 400 ? 'text-warning' : 'text-muted-foreground'">{{ e.status }}</td>
                        <td class="whitespace-nowrap tabular-nums text-[11px]">{{ fmtMs(e.latencyMs) }} ms</td>
                        <td class="max-w-[7rem] truncate text-[11px] text-muted-foreground">{{ e.actor || '匿名' }}</td>
                        <td class="max-w-[24rem] truncate pr-4 text-[11.5px]" :title="e.message">{{ e.message }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <!-- 移动卡片 -->
                <ul class="divide-y md:hidden">
                  <li v-for="e in events.items" :key="e.id" class="px-4 py-3">
                    <div class="flex items-center gap-2">
                      <Badge :variant="levelMeta[e.level]?.variant || 'outline'">{{ levelMeta[e.level]?.label || e.level }}</Badge>
                      <span class="text-[10.5px] text-muted-foreground">{{ events.categories[e.category] || e.category }}</span>
                      <span class="ml-auto font-mono text-[10px] text-muted-foreground">{{ fmtClock(e.time) }}</span>
                    </div>
                    <p class="mt-1.5 text-[11.5px] leading-relaxed">{{ e.message }}</p>
                    <p class="mt-1 truncate font-mono text-[10px] text-muted-foreground">
                      {{ e.method }} {{ e.route }} · {{ e.status }} · {{ fmtMs(e.latencyMs) }} ms
                    </p>
                  </li>
                </ul>
                <div class="flex items-center justify-center gap-2 border-t px-4 py-3">
                  <Button v-if="evHasMore" variant="outline" size="sm" :disabled="evLoading" @click="loadMore">
                    <Loader2 v-if="evLoading" class="animate-spin" />加载更多
                  </Button>
                  <span v-else class="text-[11px] text-muted-foreground">已加载全部 {{ events.total }} 条</span>
                </div>
              </template>
              <Empty v-else title="没有匹配的异常" description="调整筛选条件，或等待新的请求产生。" />
            </CardContent>
          </Card>
        </TabContent>
      </Tabs>
    </template>
  </div>
</template>
