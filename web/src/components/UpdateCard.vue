<script setup>
import { computed } from 'vue'
import { can } from '../session'
import { notifyError } from '../toast'
import { relTime } from '../utils'
import {
  update, checkUpdate, openUpdateModal, closeUpdateModal,
  dismissUpdate, startUpdate,
} from '../update'
import { CircleAlert, CircleCheck, Loader2, RefreshCw, ArrowUpCircle } from 'lucide-vue-next'
import UpdateNotes from './UpdateNotes.vue'
import Button from './ui/Button.vue'
import Badge from './ui/Badge.vue'

const canApply = computed(() => can('system:manage'))
const status = computed(() => update.status)
const release = computed(() => update.status?.latest || null)
const proxyLabel = computed(() => {
  const p = status.value?.proxy
  if (!p) return ''
  try { return new URL(p).host } catch { return p }
})

// 首页卡片只展示「重要内容高亮」的条目；没有重点时退化为普通更新条目。
const briefSections = computed(() => {
  const notes = release.value?.notes || []
  if (!notes.length) return []
  const pick = (predicate, limit) => {
    const out = []
    let n = 0
    for (const sec of notes) {
      const items = sec.items.filter(predicate).slice(0, limit - n)
      if (!items.length) continue
      out.push({ ...sec, items })
      n += items.length
      if (n >= limit) break
    }
    return out
  }
  const important = pick((it) => it.important, 4)
  return important.length ? important : pick(() => true, 3)
})

const totalItems = computed(() =>
  (release.value?.notes || []).reduce((n, s) => n + s.items.length, 0))

function intervalLabel(secs) {
  if (!secs) return ''
  if (secs >= 3600) return `每 ${Math.round(secs / 3600)} 小时自动检测`
  return `每 ${Math.max(1, Math.round(secs / 60))} 分钟自动检测`
}

async function onCheck() {
  try { await checkUpdate(true) } catch (e) { notifyError(e, '检测更新失败') }
}

function onLater() { closeUpdateModal() }
async function onDismiss() { if (release.value) await dismissUpdate(release.value.tag) }
</script>

<template>
  <!-- 发现新版本 -->
  <div v-if="status?.updateAvailable && release" class="mb-5 overflow-hidden rounded-xl border border-primary/30 bg-card shadow-sm">
    <div class="flex flex-col gap-4 p-4 sm:flex-row sm:items-start sm:justify-between sm:p-5">
      <div class="min-w-0 flex-1">
        <p class="text-[10.5px] font-medium tracking-[0.08em] text-primary">RELEASE / 发现新版本</p>
        <div class="mt-1.5 flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <span class="font-mono text-lg font-semibold text-primary">{{ release.tag }}</span>
          <span class="text-muted-foreground">←</span>
          <span class="font-mono text-[13px] text-muted-foreground line-through">{{ status.current }}</span>
          <Badge v-if="release.highlightCount" variant="primary">{{ release.highlightCount }} 项重点</Badge>
        </div>
        <p class="mt-2 text-[12px] leading-relaxed text-muted-foreground">
          发布于 {{ relTime(release.publishedAt) }} · 共 {{ totalItems }} 条更新内容 ·
          <template v-if="proxyLabel">经 {{ proxyLabel }} 加速 · </template>
          自动完成下载、校验、替换与重启。
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2 sm:justify-end">
        <Button size="sm" variant="outline" @click="openUpdateModal">查看内容</Button>
        <Button size="sm" variant="ghost" @click="onLater">稍后</Button>
        <Button
          size="sm" :disabled="!canApply || !status.restartConfigured"
          :title="canApply ? '自动下载并安装新版本' : '仅管理员可执行更新'"
          @click="startUpdate"
        >
          <ArrowUpCircle />立即更新
        </Button>
        <Button size="sm" variant="ghost" class="text-muted-foreground" @click="onDismiss">忽略</Button>
      </div>
    </div>

    <div v-if="briefSections.length" class="border-t bg-muted/35 px-4 py-3.5 sm:px-5">
      <UpdateNotes :sections="briefSections" empty-text="该版本未提供更新说明。" />
      <Button v-if="totalItems > 4" variant="link" size="sm" class="mt-1 h-auto px-0" @click="openUpdateModal">
        展开全部 {{ totalItems }} 条更新内容 →
      </Button>
    </div>

    <p v-if="!canApply" class="border-t bg-warning/10 px-4 py-2 text-[11.5px] text-warning sm:px-5">
      当前账号无 system:manage 权限，更新需管理员操作。
    </p>
    <p v-else-if="!status.restartConfigured" class="border-t bg-warning/10 px-4 py-2 text-[11.5px] text-warning sm:px-5">
      当前运行方式未启用自重启，无法自动更新（常见于独立 MCP 进程）。
    </p>
  </div>

  <!-- 已是最新 / 未检测 / 检测失败 -->
  <div v-else class="mb-5 rounded-xl border bg-card">
    <div class="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:px-5">
      <div
        class="grid size-8 shrink-0 place-items-center rounded-full"
        :class="status?.checkError || update.error ? 'bg-destructive/10 text-destructive' : 'bg-success/10 text-success'"
      >
        <component
          :is="status?.checkError || update.error ? CircleAlert : (status ? CircleCheck : RefreshCw)"
          class="size-4"
          :class="!status && !update.error && 'text-muted-foreground'"
        />
      </div>
      <div class="min-w-0 flex-1">
        <p class="text-[12.5px] font-medium leading-snug">
          <template v-if="update.checking">正在从 GitHub Releases 检测新版本…</template>
          <template v-else-if="status?.checkError || update.error">
            更新检测失败：{{ status?.checkError || update.error }}
          </template>
          <template v-else-if="status && !status.updateAvailable">
            已是最新版本 <span class="font-mono">{{ status.current }}</span>
            <span v-if="status.checkedAt" class="font-normal text-muted-foreground"> · 上次检测 {{ relTime(status.checkedAt) }}</span>
          </template>
          <template v-else>正在获取版本更新状态…</template>
        </p>
        <p class="mt-0.5 text-[11px] text-muted-foreground">
          仓库 <span class="font-mono">{{ status?.repo || 'Mr-losfer/s3bridge' }}</span>
          <span v-if="status?.autoCheck"> · {{ intervalLabel(status.checkIntervalSecs) }}</span>
          <span v-else> · 自动检测已关闭</span>
          <span v-if="proxyLabel"> · 经 {{ proxyLabel }} 加速</span>
        </p>
      </div>
      <Button size="sm" variant="outline" :disabled="update.checking" class="sm:shrink-0" @click="onCheck">
        <Loader2 v-if="update.checking" class="animate-spin" />
        <RefreshCw v-else />
        {{ update.checking ? '检测中' : '检测更新' }}
      </Button>
    </div>
  </div>
</template>
