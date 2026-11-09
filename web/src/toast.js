import { ref } from 'vue'

export const toasts = ref([])
let seq = 0

export function notify(message, { title = '', type = 'success', timeout = 4200 } = {}) {
  const id = ++seq
  toasts.value.push({ id, message, title, type })
  setTimeout(() => {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }, timeout)
}

export function notifyError(err, title = '操作失败') {
  notify(err?.message || String(err), { title, type: 'error', timeout: 6500 })
}
