<script setup>
import { computed } from 'vue'
import { can } from '../session'
import { notifyError } from '../toast'
import { relTime } from '../utils'
import {
  update, checkUpdate, openUpdateModal, closeUpdateModal,
  dismissUpdate, startUpdate,
} from '../update'
import UpdateNotes from './UpdateNotes.vue'

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
  <div v-if="status?.updateAvailable && release" class="update-card is-new">
    <div class="update-card-main">
      <div class="update-card-left">
        <div class="eyebrow" style="margin-bottom:8px">Release / 发现新版本</div>
        <div class="update-card-title">
          <span class="update-version-new">{{ release.tag }}</span>
          <span class="update-arrow">←</span>
          <span class="update-version-old">{{ status.current }}</span>
          <span v-if="release.highlightCount" class="tag tag--accent">{{ release.highlightCount }} 项重点</span>
        </div>
        <p class="update-card-desc">
          发布于 {{ relTime(release.publishedAt) }} · 共 {{ totalItems }} 条更新内容 ·
          <template v-if="proxyLabel">经 <span class="mono">{{ proxyLabel }}</span> 加速 · </template>
          点击「立即更新」将自动完成下载、校验、替换与重启。
        </p>
      </div>
      <div class="btn-group update-card-actions">
        <button class="btn btn--sm" @click="openUpdateModal">查看更新内容</button>
        <button class="btn btn--sm btn--quiet" @click="onLater">稍后提醒</button>
        <button
          class="btn btn--sm btn--primary" :disabled="!canApply || !status.restartConfigured"
          :title="canApply ? '自动下载并安装新版本' : '仅管理员可执行更新'"
          @click="startUpdate"
        >立即更新 ↑</button>
        <button class="btn btn--sm btn--quiet" @click="onDismiss">忽略此版本</button>
      </div>
    </div>
    <div v-if="briefSections.length" class="update-card-notes">
      <UpdateNotes :sections="briefSections" empty-text="该版本未提供更新说明。" />
      <button v-if="totalItems > 4" class="btn btn--quiet btn--sm" style="margin-top:8px" @click="openUpdateModal">
        展开全部 {{ totalItems }} 条更新内容 →
      </button>
    </div>
    <div v-if="!canApply" class="update-hint">当前账号无 system:manage 权限，更新需管理员操作。</div>
    <div v-else-if="!status.restartConfigured" class="update-hint">当前运行方式未启用自重启，无法自动更新（常见于独立 MCP 进程）。</div>
  </div>

  <!-- 已是最新 / 未检测 / 检测失败 -->
  <div v-else class="update-card" :class="{ 'is-error': !!status?.checkError || !!update.error }">
    <div class="update-card-inline">
      <span class="update-card-mark" :class="{ 'is-error': !!status?.checkError || !!update.error }">
        {{ status?.checkError || update.error ? '!' : status ? '✓' : '·' }}
      </span>
      <div style="flex:1;min-width:0">
        <div class="update-inline-title">
          <template v-if="update.checking">正在从 GitHub Releases 检测新版本…</template>
          <template v-else-if="status?.checkError || update.error">
            更新检测失败：{{ status?.checkError || update.error }}
          </template>
          <template v-else-if="status && !status.updateAvailable">
            已是最新版本 <span class="mono">{{ status.current }}</span>
            <span v-if="status.checkedAt" class="muted tiny"> · 上次检测 {{ relTime(status.checkedAt) }}</span>
            <span v-if="status.latest?.tag && status.latest.tag !== status.current" class="muted tiny"> · 仓库最新 {{ status.latest.tag }}</span>
          </template>
          <template v-else>正在获取版本更新状态… 也可点击右侧按钮立即检查 GitHub Releases。</template>
        </div>
        <div class="update-inline-sub muted tiny">
          仓库 <span class="mono">{{ status?.repo || 'Mr-losfer/s3bridge' }}</span>
          <span v-if="status?.autoCheck"> · {{ intervalLabel(status.checkIntervalSecs) }}</span>
          <span v-else> · 自动检测已关闭</span>
          <span v-if="status?.strategy"> · {{ status.strategy === 'container' ? '容器部署' : '二进制部署' }}</span>
          <span v-if="proxyLabel"> · 经 <span class="mono">{{ proxyLabel }}</span> 加速</span>
        </div>
      </div>
      <button class="btn btn--sm" :disabled="update.checking" @click="onCheck">
        <span v-if="update.checking" class="spinner"></span>
        <span v-else>↻</span>
        {{ update.checking ? '检测中' : '检测更新' }}
      </button>
    </div>
  </div>
</template>
