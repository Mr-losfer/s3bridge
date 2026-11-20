<script setup>
import { ref, onMounted, computed } from 'vue'
import { RefreshCw, CheckCheck, BellRing, TriangleAlert, Blocks, ArrowRight, Inbox } from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { notify, notifyError } from '../toast'
import { fmtTime, relTime } from '../utils'
import PageHeader from '../components/PageHeader.vue'
import StatCard from '../components/StatCard.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'

const alerts = ref([])
const loading = ref(true)
const open = computed(() => alerts.value.filter((a) => !a.ackedAt))
const criticalCount = computed(() => open.value.filter((a) => a.level === 'critical').length)

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

function levelVariant(level) {
  return level === 'critical' ? 'destructive' : 'warning'
}
</script>

<template>
  <div>
    <PageHeader
      title="异常告警"
      description="插件健康检测连续失败、上传失败或临时凭据过期时自动产生告警；插件恢复后系统会自动确认。"
    >
      <template #actions>
        <Button variant="outline" :disabled="!open.length" @click="ackAll"><CheckCheck />全部确认</Button>
        <Button variant="ghost" size="icon" aria-label="刷新" @click="load"><RefreshCw /></Button>
      </template>
    </PageHeader>

    <div class="grid grid-cols-2 gap-3 lg:grid-cols-3">
      <StatCard label="未处理" :value="open.length" hint="需要人工介入" :icon="BellRing" :tone="open.length ? 'danger' : 'default'" />
      <StatCard label="历史告警" :value="alerts.length" hint="同一插件未处理告警只保留一条" :icon="Inbox" />
      <StatCard label="异常插件" :value="new Set(open.map((a) => a.pluginId)).size" hint="插件列表中显示为红色" :icon="Blocks" :tone="open.length ? 'danger' : 'default'" class="col-span-2 lg:col-span-1" />
    </div>

    <div v-if="loading" class="mt-6 space-y-2">
      <Skeleton v-for="i in 4" :key="i" class="h-20 rounded-xl" />
    </div>

    <template v-else-if="alerts.length">
      <!-- 桌面表格 -->
      <Card class="mt-6 hidden overflow-hidden md:block">
        <div class="overflow-x-auto">
          <table class="data-table">
            <thead>
              <tr>
                <th class="w-20">级别</th><th>插件</th><th>标题</th><th class="hidden lg:table-cell">详情</th>
                <th>产生时间</th><th>状态</th><th class="w-24"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in alerts" :key="a.id" :class="!a.ackedAt && 'bg-destructive/[0.025]'">
                <td><Badge :variant="levelVariant(a.level)">{{ a.level }}</Badge></td>
                <td>
                  <button class="text-left font-medium hover:text-primary" @click="go('plugin', a.pluginId)">{{ a.pluginName }}</button>
                  <div class="mt-0.5 font-mono text-[10.5px] text-muted-foreground">{{ a.pluginId }}</div>
                </td>
                <td class="font-medium">{{ a.title }}</td>
                <td class="hidden max-w-[22rem] text-[11.5px] leading-relaxed text-muted-foreground lg:table-cell">
                  <span class="line-clamp-2">{{ a.message }}</span>
                </td>
                <td class="whitespace-nowrap text-[11.5px] text-muted-foreground">
                  {{ fmtTime(a.createdAt) }}<div class="mt-0.5 text-[10.5px]">{{ relTime(a.createdAt) }}</div>
                </td>
                <td>
                  <Badge :variant="a.ackedAt ? 'success' : 'destructive'">{{ a.ackedAt ? '已确认' : '未处理' }}</Badge>
                  <div v-if="a.ackedAt" class="mt-1 text-[10.5px] text-muted-foreground">by {{ a.ackedBy }}</div>
                </td>
                <td class="text-right">
                  <Button v-if="!a.ackedAt" size="sm" variant="outline" @click="ack(a)">确认</Button>
                  <Button v-else size="sm" variant="ghost" @click="go('plugin', a.pluginId)">查看<ArrowRight /></Button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </Card>

      <!-- 移动卡片 -->
      <div class="mt-5 space-y-2.5 md:hidden">
        <div v-for="a in alerts" :key="a.id" class="rounded-xl border bg-card p-3.5">
          <div class="flex items-center gap-2">
            <Badge :variant="levelVariant(a.level)">{{ a.level }}</Badge>
            <button class="min-w-0 truncate text-[13px] font-medium" @click="go('plugin', a.pluginId)">{{ a.pluginName }}</button>
            <Badge :variant="a.ackedAt ? 'success' : 'destructive'" class="ml-auto shrink-0">{{ a.ackedAt ? '已确认' : '未处理' }}</Badge>
          </div>
          <p class="mt-2 text-[12.5px] font-medium leading-snug">{{ a.title }}</p>
          <p class="mt-1 line-clamp-3 text-[11.5px] leading-relaxed text-muted-foreground">{{ a.message }}</p>
          <div class="mt-2.5 flex items-center justify-between gap-2">
            <span class="text-[10.5px] text-muted-foreground">{{ relTime(a.createdAt) }}</span>
            <Button v-if="!a.ackedAt" size="sm" variant="outline" @click="ack(a)">确认</Button>
            <Button v-else size="sm" variant="ghost" @click="go('plugin', a.pluginId)">查看插件<ArrowRight /></Button>
          </div>
        </div>
      </div>
    </template>

    <Card v-else class="mt-6">
      <Empty title="没有告警" description="所有插件运行正常。异常发生时这里会自动出现记录。">
        <Button size="sm" variant="outline" @click="go('plugins')">查看插件状态</Button>
      </Empty>
    </Card>

    <p v-if="criticalCount" class="mt-4 flex items-center gap-1.5 text-[11.5px] text-destructive">
      <TriangleAlert class="size-3.5" />{{ criticalCount }} 条 critical 告警需要优先处理，通常是凭据过期或上游不可达。
    </p>
  </div>
</template>
