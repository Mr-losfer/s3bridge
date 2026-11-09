import { reactive } from 'vue'
import { api } from './api'
import { notify } from './toast'

const IGNORE_KEY = 's3bridge.update.ignored'
const SHOWN_KEY = 's3bridge.update.shown'

export const update = reactive({
  status: null,
  checking: false,
  modal: false,      // 新版本提示弹窗
  progress: false,   // 更新进度弹窗
  restarting: false, // 服务重启中（网络短暂中断）
  error: '',
  finished: false,
  // 更新前的版本对：新进程刚重启时 latest 还没检测出来，用这份快照兜底展示。
  applyMeta: { from: '', to: '' },
})

export const hasUpdate = () => !!update.status?.updateAvailable
export const latestRelease = () => update.status?.latest || null

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const ignoredTag = () => localStorage.getItem(IGNORE_KEY) || ''

export function ignoreVersion(tag) {
  localStorage.setItem(IGNORE_KEY, tag)
}

function maybeAutoOpen(s) {
  if (!s?.updateAvailable || !s.latest) return
  if (s.dismissed || ignoredTag() === s.latest.tag) return
  if (sessionStorage.getItem(SHOWN_KEY) === s.latest.tag) return
  if (update.modal || update.progress) return
  update.modal = true
  sessionStorage.setItem(SHOWN_KEY, s.latest.tag)
}

/** 读取缓存状态（不触发 GitHub 请求），登录后与首页加载时调用。 */
export async function loadUpdateStatus() {
  try {
    const s = await api('/update/status')
    update.status = s
    maybeAutoOpen(s)
    return s
  } catch (e) {
    update.error = e.message || '无法获取更新状态'
    return null
  }
}

/** 立即从 GitHub Releases 检测新版本。 */
export async function checkUpdate(force = true) {
  update.checking = true
  update.error = ''
  try {
    const s = await api('/update/check', { method: 'POST', body: { force } })
    update.status = s
    maybeAutoOpen(s)
    if (s.checkError) update.error = s.checkError
    return s
  } catch (e) {
    update.error = e.message || '检测更新失败'
    throw e
  } finally {
    update.checking = false
  }
}

export async function dismissUpdate(tag) {
  ignoreVersion(tag)
  update.modal = false
  try {
    update.status = await api('/update/dismiss', { method: 'POST', body: { tag } })
  } catch { /* 弹窗已关闭，忽略 */ }
}

export function openUpdateModal() {
  update.error = ''
  update.modal = true
}

/** 「稍后提醒」：本次会话不再自动弹出，但首页卡片与手动入口仍然可用。 */
export function closeUpdateModal() {
  update.modal = false
  const tag = update.status?.latest?.tag
  if (tag) sessionStorage.setItem(SHOWN_KEY, tag)
}

export function closeProgress() {
  update.progress = false
  update.finished = false
  update.error = ''
}

/** 点击「立即更新」：后端独立完成下载、校验、替换与重启，前端只负责轮询进度。 */
export async function startUpdate() {
  update.modal = false
  update.progress = true
  update.finished = false
  update.error = ''
  update.restarting = false
  update.applyMeta = {
    from: update.status?.current || '',
    to: update.status?.latest?.tag || '',
  }
  try {
    const a = await api('/update/apply', { method: 'POST', body: {} })
    // 立刻呈现后端返回的初始状态，避免进度弹窗先闪一帧旧的 idle。
    if (update.status) update.status.apply = a
  } catch (e) {
    update.error = e.message || '启动更新失败'
    return
  }
  await pollApply()
}

async function pollApply() {
  const fromVersion = update.status?.current
  const deadline = Date.now() + 3 * 60 * 1000
  let offline = 0

  while (Date.now() < deadline) {
    await sleep(650)
    try {
      const s = await api('/update/status')
      update.status = s
      offline = 0
      const a = s.apply || {}
      if (a.phase === 'restarting') update.restarting = true
      if (a.phase === 'failed') {
        update.restarting = false
        update.error = a.error || '更新失败，已回滚到原版本'
        return
      }
      // 完成判定以「版本号变化」为准：进程被 syscall.Exec 替换后，新进程的
      // apply 状态机是全新的 idle，不能依赖是否观察到中间的 busy 阶段。
      const versionChanged = !!fromVersion && !!s.current && s.current !== fromVersion
      if ((a.phase === 'done' && !s.updateAvailable) || (versionChanged && !s.updateAvailable)) {
        update.restarting = false
        update.finished = true
        // 通过响应式代理赋值，确保进度弹窗立即重渲染为完成态。
        update.status.apply = { ...a, phase: 'done', percent: 100, message: '更新完成，服务已使用新版本重启' }
        notify(`已更新到 ${s.current}，页面即将刷新`, { title: '更新完成' })
        setTimeout(() => window.location.reload(), 2000)
        return
      }
    } catch (e) {
      if (e?.status === 401) {
        update.restarting = false
        update.error = '登录状态已失效，请重新登录后确认当前版本'
        return
      }
      // 重启窗口内连接中断是预期行为，继续重试直到服务恢复。
      offline++
      update.restarting = true
      if (offline > 60) {
        update.restarting = false
        update.error = '服务重启后长时间未恢复，请手动刷新页面或检查进程状态'
        return
      }
    }
  }
  update.error = '更新超时，请刷新页面确认当前版本'
}

/** 当前阶段在步骤条中的序号，用于高亮进度。 */
export function phaseStep(phase) {
  switch (phase) {
    case 'downloading': return 1
    case 'verifying': return 2
    case 'replacing': return 3
    case 'restarting': return 4
    case 'done': return 5
    default: return 0
  }
}
