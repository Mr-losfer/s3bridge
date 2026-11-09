<script setup>
import { computed } from 'vue'
import { can } from '../session'
import { fmtTime, relTime } from '../utils'
import {
  update, phaseStep, closeUpdateModal, dismissUpdate,
  startUpdate, closeProgress,
} from '../update'
import UpdateNotes from './UpdateNotes.vue'

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

function onDismiss() { if (release.value) dismissUpdate(release.value.tag) }
function copyPersist() {
  navigator.clipboard?.writeText(status.value?.persistCommand || '').then(
    () => window.dispatchEvent(new CustomEvent('s3bridge:toast', { detail: '命令已复制' })),
    () => {},
  )
}
</script>

<template>
  <!-- 更新进度 -->
  <div v-if="update.progress" class="modal-backdrop">
    <div class="modal modal--progress" role="dialog" aria-modal="true" aria-label="更新进度">
      <div class="modal-head">
        <div>
          <div class="eyebrow" style="margin-bottom:7px">System Update / 自动更新</div>
          <h2 class="modal-title">
            <template v-if="update.finished">更新完成</template>
            <template v-else-if="apply.phase === 'failed'">更新失败</template>
            <template v-else>正在更新 S3Bridge</template>
          </h2>
        </div>
        <button class="modal-close" :disabled="busy && !update.finished" title="关闭" @click="closeProgress">×</button>
      </div>

      <div class="modal-body">
        <div class="update-version-line">
          <span class="mono">{{ apply.from || update.applyMeta.from || status?.current }}</span>
          <span class="update-arrow">→</span>
          <span class="mono update-version-new">{{ apply.to || update.applyMeta.to || release?.tag }}</span>
        </div>

        <div v-if="update.finished" class="update-result is-ok">
          <div class="update-result-mark">✓</div>
          <div>
            <div class="update-result-title">新版本已生效，服务已自动重启</div>
            <p class="muted" style="margin:6px 0 0;font-size:12.5px">页面将在 2 秒后自动刷新，无需任何手动操作。</p>
          </div>
        </div>

        <div v-else-if="apply.phase === 'failed'" class="update-result is-fail">
          <div class="update-result-mark">!</div>
          <div>
            <div class="update-result-title">{{ apply.error || update.error || '更新失败' }}</div>
            <p class="muted" style="margin:6px 0 0;font-size:12.5px">已自动回滚到原版本，服务保持可用。可查看下方日志定位原因后重试。</p>
          </div>
        </div>

        <template v-else>
          <div class="update-steps">
            <div
              v-for="(s, i) in steps" :key="s.label"
              class="update-step"
              :class="{ 'is-active': step === i + 1, 'is-done': step > i + 1 }"
            >
              <div class="update-step-num">{{ step > i + 1 ? '✓' : i + 1 }}</div>
              <div class="update-step-label">{{ s.label }}</div>
              <div class="update-step-hint">{{ s.hint }}</div>
            </div>
          </div>

          <div class="update-progress-head">
            <span>{{ apply.message || '准备中…' }}</span>
            <span class="mono">{{ apply.percent || 0 }}%</span>
          </div>
          <div class="update-progress"><div class="update-progress-fill" :style="{ width: (apply.percent || 0) + '%' }"></div></div>

          <div v-if="update.restarting" class="update-offline">
            <span class="spinner"></span> 服务正在重启，连接会短暂中断，正在等待新版本恢复…
          </div>
        </template>

        <div v-if="logs.length" class="update-log">
          <div class="update-log-head">执行日志</div>
          <pre>{{ logs.join('\n') }}</pre>
        </div>

        <div v-if="showPersist" class="update-persist">
          <div class="update-persist-title">容器镜像持久化（可选）</div>
          <p class="muted tiny" style="margin:4px 0 8px">
            新版本已写入数据卷并在当前容器生效。若希望拉取官方新镜像，可执行：
          </p>
          <div class="update-persist-cmd">
            <code class="mono">{{ status.persistCommand }}</code>
            <button class="btn btn--quiet btn--sm" @click="copyPersist">复制</button>
          </div>
        </div>

        <div v-if="update.error && apply.phase !== 'failed' && !update.finished" class="alert-strip" style="margin:14px 0 0">
          <div style="font-family:var(--serif);font-size:20px;color:var(--accent)">!</div>
          <div style="flex:1"><div class="alert-strip-body">{{ update.error }}</div></div>
        </div>
      </div>

      <div class="modal-foot">
        <button v-if="apply.phase === 'failed'" class="btn" @click="startUpdate">重试更新</button>
        <button class="btn btn--primary" :disabled="busy && !update.finished" @click="closeProgress">
          {{ update.finished ? '立即刷新' : '关闭' }}
        </button>
      </div>
    </div>
  </div>

  <!-- 新版本提示 -->
  <div v-else-if="update.modal && release" class="modal-backdrop" @click.self="closeUpdateModal">
    <div class="modal" role="dialog" aria-modal="true" aria-label="发现新版本">
      <div class="modal-head">
        <div>
          <div class="eyebrow" style="margin-bottom:7px">Release / 版本更新</div>
          <h2 class="modal-title">发现新版本 <span class="update-version-new">{{ release.tag }}</span></h2>
          <p class="modal-sub">
            当前运行 <span class="mono">{{ status?.current }}</span> ·
            发布于 {{ fmtTime(release.publishedAt) }}（{{ relTime(release.publishedAt) }}）
            <span v-if="release.highlightCount"> · <strong style="color:var(--accent)">{{ release.highlightCount }} 项重点更新</strong></span>
          </p>
        </div>
        <button class="modal-close" title="稍后提醒" @click="closeUpdateModal">×</button>
      </div>

      <div class="modal-body update-modal-body">
        <div class="update-modal-section-head">
          <span>更新内容</span>
          <a v-if="release.url" :href="release.url" target="_blank" rel="noopener noreferrer" class="update-ext-link">
            在 GitHub 查看完整 Release ↗
          </a>
        </div>
        <UpdateNotes :sections="release.notes || []" />
        <div v-if="status?.strategyNote" class="update-strategy-note">
          <strong>更新方式</strong>：{{ status.strategyNote }}
        </div>
        <div v-if="!canApply" class="update-hint" style="margin-top:12px">
          当前账号无 system:manage 权限，自动更新需管理员操作；更新内容仍可正常查看。
        </div>
      </div>

      <div class="modal-foot">
        <button class="btn btn--quiet btn--sm" @click="onDismiss">忽略此版本</button>
        <button class="btn" @click="closeUpdateModal">稍后提醒</button>
        <button
          class="btn btn--primary" :disabled="!canApply || !status?.restartConfigured"
          :title="canApply ? '自动完成下载、校验、替换与重启' : '仅管理员可执行更新'"
          @click="startUpdate"
        >立即更新 ↑</button>
      </div>
    </div>
  </div>
</template>
