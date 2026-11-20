<script setup>
import { computed } from 'vue'
import { can } from '../session'
import { fmtTime, relTime } from '../utils'
import { notify } from '../toast'
import {
  update, phaseStep, closeUpdateModal, dismissUpdate,
  startUpdate, closeProgress,
} from '../update'
import { CircleAlert, CircleCheck, Loader2, ExternalLink, RotateCw, ArrowUpCircle } from 'lucide-vue-next'
import UpdateNotes from './UpdateNotes.vue'
import Dialog from './ui/Dialog.vue'
import Button from './ui/Button.vue'
import Badge from './ui/Badge.vue'

const canApply = computed(() => can('system:manage'))
const status = computed(() => update.status)
const release = computed(() => update.status?.latest || null)
const apply = computed(() => update.status?.apply || {})
const step = computed(() => phaseStep(apply.value.phase))

const steps = [
  { label: '下载', hint: '获取发布资产' },
  { label: '校验', hint: 'SHA256 与解包' },
  { label: '替换', hint: '备份与原子替换' },
  { label: '重启', hint: '优雅重启服务' },
  { label: '完成', hint: '使用新版本' },
]

const logs = computed(() => (apply.value.log || []).slice(-7))
const busy = computed(() => ['downloading', 'verifying', 'replacing', 'restarting'].includes(apply.value.phase))
const showPersist = computed(() => !!status.value?.persistCommand && (update.finished || apply.value.phase === 'done'))
const progressTitle = computed(() =>
  update.finished ? '更新完成' : apply.value.phase === 'failed' ? '更新失败' : '正在更新 S3Bridge')

function onDismiss() { if (release.value) dismissUpdate(release.value.tag) }
function copyPersist() {
  navigator.clipboard?.writeText(status.value?.persistCommand || '').then(
    () => notify('命令已复制'),
    () => notify('浏览器未授权剪贴板', { type: 'error' }),
  )
}
</script>

<template>
  <!-- 更新进度 -->
  <Dialog
    :open="update.progress"
    :title="progressTitle"
    :closable="!busy || update.finished"
    :dismissable="!busy"
    class="max-w-xl"
    @update:open="(v) => { if (!v) closeProgress() }"
  >
    <div class="flex items-baseline gap-2 font-mono text-[13px]">
      <span class="text-muted-foreground">{{ apply.from || update.applyMeta.from || status?.current }}</span>
      <span class="text-muted-foreground">→</span>
      <span class="font-semibold text-primary">{{ apply.to || update.applyMeta.to || release?.tag }}</span>
    </div>

    <div v-if="update.finished" class="mt-4 flex items-start gap-3 rounded-lg border border-success/25 bg-success/[0.07] p-3.5">
      <CircleCheck class="mt-0.5 size-4 shrink-0 text-success" />
      <div>
        <p class="text-[13px] font-medium">新版本已生效，服务已自动重启</p>
        <p class="mt-1 text-[11.5px] text-muted-foreground">页面将在 2 秒后自动刷新，无需任何手动操作。</p>
      </div>
    </div>

    <div v-else-if="apply.phase === 'failed'" class="mt-4 flex items-start gap-3 rounded-lg border border-destructive/25 bg-destructive/[0.06] p-3.5">
      <CircleAlert class="mt-0.5 size-4 shrink-0 text-destructive" />
      <div>
        <p class="text-[13px] font-medium text-destructive">{{ apply.error || update.error || '更新失败' }}</p>
        <p class="mt-1 text-[11.5px] text-muted-foreground">已自动回滚到原版本，服务保持可用。可查看下方日志定位原因后重试。</p>
      </div>
    </div>

    <template v-else>
      <div class="relative mt-6">
        <div class="absolute left-[10%] right-[10%] top-3 h-px bg-border" />
        <div class="relative grid grid-cols-5 gap-1">
          <div v-for="(s, i) in steps" :key="s.label" class="flex flex-col items-center text-center">
            <div
              class="grid size-6 place-items-center rounded-full border bg-background font-mono text-[10.5px] transition-colors"
              :class="step === i + 1
                ? 'border-primary bg-primary font-semibold text-primary-foreground'
                : step > i + 1
                  ? 'border-success text-success'
                  : 'border-border text-muted-foreground'"
            >
              <CircleCheck v-if="step > i + 1" class="size-3.5" />
              <template v-else>{{ i + 1 }}</template>
            </div>
            <p class="mt-2 text-[11.5px] font-medium">{{ s.label }}</p>
            <p class="mt-0.5 hidden text-[10px] text-muted-foreground sm:block">{{ s.hint }}</p>
          </div>
        </div>
      </div>

      <div class="mt-5 flex items-center justify-between text-[11.5px] text-muted-foreground">
        <span class="truncate">{{ apply.message || '准备中…' }}</span>
        <span class="font-mono">{{ apply.percent || 0 }}%</span>
      </div>
      <div class="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
        <div class="h-full rounded-full bg-primary transition-[width] duration-300" :style="{ width: (apply.percent || 0) + '%' }" />
      </div>

      <div v-if="update.restarting" class="mt-4 flex items-center gap-2 rounded-md border border-warning/25 bg-warning/[0.08] px-3 py-2 text-[11.5px] text-warning">
        <Loader2 class="size-3.5 animate-spin" />
        服务正在重启，连接会短暂中断，正在等待新版本恢复…
      </div>
    </template>

    <div v-if="logs.length" class="mt-5 overflow-hidden rounded-lg border bg-muted/30">
      <div class="border-b px-3 py-1.5 text-[10.5px] font-medium tracking-wide text-muted-foreground">执行日志</div>
      <pre class="max-h-32 overflow-auto p-3 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap break-all text-muted-foreground">{{ logs.join('\n') }}</pre>
    </div>

    <div v-if="showPersist" class="mt-4 rounded-lg border border-dashed p-3.5">
      <p class="text-[12px] font-medium">容器镜像持久化（可选）</p>
      <p class="mt-1 text-[11.5px] text-muted-foreground">新版本已写入数据卷并在当前容器生效。若希望拉取官方新镜像，可执行：</p>
      <div class="mt-2 flex items-center gap-2 rounded-md bg-[oklch(0.19_0.006_60)] px-2.5 py-2 text-[oklch(0.93_0.004_80)]">
        <code class="min-w-0 flex-1 break-all font-mono text-[10.5px]">{{ status.persistCommand }}</code>
        <Button size="sm" variant="ghost" class="h-6 shrink-0 px-1.5 text-[10.5px] text-white/70 hover:bg-white/10 hover:text-white" @click="copyPersist">复制</Button>
      </div>
    </div>

    <p v-if="update.error && apply.phase !== 'failed' && !update.finished" class="mt-4 rounded-md border border-destructive/25 bg-destructive/[0.06] px-3 py-2 text-[11.5px] text-destructive">
      {{ update.error }}
    </p>

    <template #footer>
      <Button v-if="apply.phase === 'failed'" variant="outline" @click="startUpdate"><RotateCw />重试更新</Button>
      <Button :disabled="busy && !update.finished" @click="closeProgress">
        {{ update.finished ? '立即刷新' : '关闭' }}
      </Button>
    </template>
  </Dialog>

  <!-- 新版本提示 -->
  <Dialog
    :open="update.modal"
    :title="`发现新版本 ${release?.tag || ''}`"
    :description="`当前运行 ${status?.current || ''} · 发布于 ${fmtTime(release?.publishedAt)}（${relTime(release?.publishedAt)}）`"
    class="max-w-2xl"
    @update:open="(v) => { if (!v) closeUpdateModal() }"
  >
    <div class="flex items-center justify-between gap-3 border-b pb-2.5">
      <div class="flex items-center gap-2">
        <span class="text-[13px] font-medium">更新内容</span>
        <Badge v-if="release?.highlightCount" variant="primary">{{ release.highlightCount }} 项重点</Badge>
      </div>
      <a
        v-if="release?.url" :href="release.url" target="_blank" rel="noopener noreferrer"
        class="inline-flex items-center gap-1 text-[11.5px] font-medium text-info hover:underline"
      >
        在 GitHub 查看<ExternalLink class="size-3" />
      </a>
    </div>

    <div class="pt-3">
      <UpdateNotes :sections="release?.notes || []" />
    </div>

    <div v-if="status?.strategyNote" class="mt-4 rounded-lg border bg-muted/35 p-3 text-[11.5px] leading-relaxed text-muted-foreground">
      <p><span class="font-medium text-foreground">更新方式</span>：{{ status.strategyNote }}</p>
      <p v-if="status?.proxy" class="mt-1"><span class="font-medium text-foreground">加速代理</span>：<span class="font-mono">{{ status.proxy }}</span>（失败自动回退直连）</p>
    </div>

    <p v-if="!canApply" class="mt-3 rounded-md border border-warning/25 bg-warning/[0.08] px-3 py-2 text-[11.5px] text-warning">
      当前账号无 system:manage 权限，自动更新需管理员操作；更新内容仍可正常查看。
    </p>

    <template #footer>
      <Button variant="ghost" size="sm" class="mr-auto" @click="onDismiss">忽略此版本</Button>
      <Button variant="outline" @click="closeUpdateModal">稍后提醒</Button>
      <Button :disabled="!canApply || !status?.restartConfigured" @click="startUpdate">
        <ArrowUpCircle />立即更新
      </Button>
    </template>
  </Dialog>
</template>
