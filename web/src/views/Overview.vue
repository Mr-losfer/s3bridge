<script setup>
import { ref, onMounted, computed } from 'vue'
import {
  Blocks, Files, Database, BellRing, ArrowRight, ArrowUpRight, RefreshCw,
  TriangleAlert, Upload, Settings2, CircleCheck,
} from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { notifyError } from '../toast'
import { formatBytes, relTime, categoryLabel, fmtTime } from '../utils'
import UpdateCard from '../components/UpdateCard.vue'
import PageHeader from '../components/PageHeader.vue'
import StatCard from '../components/StatCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import CardHeader from '../components/ui/CardHeader.vue'
import CardTitle from '../components/ui/CardTitle.vue'
import CardDescription from '../components/ui/CardDescription.vue'
import CardContent from '../components/ui/CardContent.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'

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
    <PageHeader
      title="运行态势"
      description="插件健康、上传归档与多租户接入的一屏总览。异常插件会置顶标红，无需进入二级页面确认。"
    >
      <template #actions>
        <Button variant="outline" @click="go('uploadtest')"><Upload />上传测试</Button>
        <Button @click="go('plugins')"><Settings2 />管理插件</Button>
      </template>
    </PageHeader>

    <UpdateCard />

    <div v-if="badPlugins.length" class="mb-5 flex flex-col gap-3 rounded-xl border border-destructive/25 bg-destructive/[0.05] p-3.5 sm:flex-row sm:items-center">
      <div class="flex min-w-0 flex-1 items-start gap-2.5">
        <TriangleAlert class="mt-0.5 size-4 shrink-0 text-destructive" />
        <div class="min-w-0">
          <p class="text-[13px] font-medium text-destructive">{{ badPlugins.length }} 个插件处于异常状态</p>
          <p class="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">
            {{ badPlugins.map((p) => p.name + (p.statusMessage ? '（' + p.statusMessage + '）' : '')).join('；') }}
          </p>
        </div>
      </div>
      <Button size="sm" variant="outline" class="shrink-0 border-destructive/30 text-destructive hover:bg-destructive/10" @click="go('plugins')">
        立即处理<ArrowRight />
      </Button>
    </div>

    <div v-if="loading && !info" class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <Skeleton v-for="i in 4" :key="i" class="h-[7.25rem] rounded-xl" />
    </div>
    <div v-else class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard
        label="插件 / 异常" :value="info?.plugins ?? '—'" :unit="`/ ${info?.unhealthyPlugins ?? 0} 异常`"
        :hint="`${info?.healthyPlugins ?? 0} 个健康 · 定时检测已启用 ${Object.keys(info?.scheduler || {}).length} 项`"
        :icon="Blocks" :tone="badPlugins.length ? 'danger' : 'default'"
      />
      <StatCard
        label="已归档对象" :value="info?.objects ?? '—'" unit="files"
        :hint="`合计 ${formatBytes(info?.totalBytes)}`" :icon="Files"
      />
      <StatCard
        label="存储桶 / 用户" :value="info?.buckets ?? '—'" :unit="`/ ${info?.users ?? 0} 用户`"
        hint="按 bucket 与文件类型双维度归档" :icon="Database"
      />
      <StatCard
        label="未处理告警" :value="info?.openAlerts ?? '—'"
        :hint="`运行时长 ${info?.uptime || '—'}`" :icon="BellRing"
        :tone="info?.openAlerts ? 'danger' : 'default'"
      />
    </div>

    <Card class="mt-6 overflow-hidden">
      <CardHeader class="flex-row items-center justify-between gap-4 space-y-0 pb-4">
        <div class="min-w-0">
          <CardTitle>插件健康</CardTitle>
          <CardDescription class="mt-1">按异常优先排序，点击行进入参数配置</CardDescription>
        </div>
        <Button variant="ghost" size="sm" class="shrink-0" @click="load"><RefreshCw />刷新</Button>
      </CardHeader>
      <div class="overflow-x-auto border-t">
        <table class="data-table">
          <thead>
            <tr>
              <th>插件</th><th>状态</th><th class="hidden sm:table-cell">版本</th>
              <th class="hidden lg:table-cell">检测间隔</th><th class="hidden md:table-cell">上次检测</th>
              <th class="hidden xl:table-cell">提示</th><th class="w-8"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in plugins" :key="p.id" class="cursor-pointer" @click="go('plugin', p.id)">
              <td>
                <div class="font-medium">{{ p.name }}</div>
                <div class="mt-0.5 font-mono text-[10.5px] text-muted-foreground">{{ p.id }} · {{ p.kind }}</div>
              </td>
              <td><StatusBadge :status="p.status" /></td>
              <td class="hidden font-mono text-xs sm:table-cell">{{ p.version }}</td>
              <td class="hidden font-mono text-xs lg:table-cell">{{ p.health?.intervalSecs }}s</td>
              <td class="hidden whitespace-nowrap text-[11.5px] text-muted-foreground md:table-cell">{{ relTime(p.lastCheckedAt) }}</td>
              <td class="hidden max-w-[18rem] truncate text-[11.5px] xl:table-cell">
                <span v-if="p.warning" class="text-warning">{{ p.warning }}</span>
                <span v-else class="text-muted-foreground">{{ p.statusMessage || '—' }}</span>
              </td>
              <td class="text-right text-muted-foreground"><ArrowRight class="ml-auto size-3.5" /></td>
            </tr>
            <tr v-if="!plugins.length && !loading">
              <td colspan="7">
                <Empty title="尚无插件" description="内置插件会在服务启动时自动注册。" />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <Card class="mt-6 overflow-hidden">
      <CardHeader class="flex-row items-center justify-between gap-4 space-y-0 pb-4">
        <div class="min-w-0">
          <CardTitle>最近上传</CardTitle>
          <CardDescription class="mt-1">统一标准输出，按 bucket 与文件类型归档</CardDescription>
        </div>
        <Button variant="ghost" size="sm" class="shrink-0" @click="go('uploads')">查看全部<ArrowUpRight /></Button>
      </CardHeader>
      <div class="overflow-x-auto border-t">
        <table class="data-table">
          <thead>
            <tr>
              <th>文件名</th><th>类型</th><th class="hidden sm:table-cell">存储桶</th>
              <th class="hidden lg:table-cell">来源插件</th><th>大小</th><th class="hidden sm:table-cell">时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="o in uploads" :key="o.id">
              <td class="max-w-[16rem] truncate font-medium">{{ o.fileName }}</td>
              <td><Badge variant="outline">{{ categoryLabel(o.category) }}</Badge></td>
              <td class="hidden font-mono text-xs sm:table-cell">{{ o.bucket }}</td>
              <td class="hidden font-mono text-xs lg:table-cell">{{ o.provider }}</td>
              <td class="whitespace-nowrap font-mono text-xs tabular-nums">{{ formatBytes(o.size) }}</td>
              <td class="hidden whitespace-nowrap text-[11.5px] text-muted-foreground sm:table-cell">{{ relTime(o.createdAt) }}</td>
            </tr>
            <tr v-if="!uploads.length && !loading">
              <td colspan="6">
                <Empty
                  title="还没有上传记录"
                  description="去「上传测试」用一张图片验证插件是否配置正确。"
                >
                  <Button size="sm" @click="go('uploadtest')"><Upload />开始测试</Button>
                </Empty>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <Card class="mt-6">
      <CardHeader class="pb-3">
        <CardTitle>站点信息</CardTitle>
        <CardDescription>与 MCP 的 site_info 工具返回一致</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3 border-t pt-4 text-[12.5px]">
        <div v-for="row in [
          { k: '名称 / 版本', v: (info?.siteName || 'S3Bridge') + ' · ' + (info?.version || '—'), mono: false },
          { k: '启动时间', v: fmtTime(info?.startedAt) + '（已运行 ' + (info?.uptime || '—') + '）', mono: false },
          { k: 'S3 接入点', v: (info?.s3Endpoint || '—') + ' · region ' + (info?.s3Region || '—'), mono: true },
          { k: 'MCP 端点', v: 'POST /mcp · 工具清单见「MCP 能力」', mono: true },
          { k: '定时检测', v: JSON.stringify(info?.scheduler || {}), mono: true },
          { k: '文件类型分布', v: JSON.stringify(stats?.byCategory || {}), mono: true },
        ]" :key="row.k" class="grid gap-0.5 sm:grid-cols-[9rem_1fr] sm:gap-4">
          <span class="text-[11.5px] text-muted-foreground">{{ row.k }}</span>
          <span :class="['break-words', row.mono && 'font-mono text-[11.5px]']">{{ row.v }}</span>
        </div>
      </CardContent>
    </Card>

    <div v-if="alerts.length" class="mt-4 flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
      <CircleCheck class="size-3.5" />
      最近 {{ alerts.length }} 条告警已归档，可在「告警中心」查看完整记录。
    </div>
  </div>
</template>
