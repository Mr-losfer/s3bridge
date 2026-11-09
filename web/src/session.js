import { reactive } from 'vue'
import { api, getToken, clearToken, setToken } from './api'

export const session = reactive({
  user: null,
  permissions: [],
  site: null,
  openAlerts: 0,
  version: '',
  ready: false,
})

export function can(perm) {
  return session.permissions.includes(perm)
}
export function isAdmin() {
  return session.user?.role === 'admin'
}

export async function refreshSession() {
  if (!getToken()) { session.user = null; session.ready = true; return }
  try {
    const me = await api('/me')
    session.user = me.user
    session.permissions = me.permissions || []
    const site = await api('/site').catch(() => null)
    session.site = site
    session.version = site?.version || ''
    const info = await api('/admin/site').catch(() => null)
    session.openAlerts = info?.openAlerts ?? 0
  } catch {
    clearToken()
    session.user = null
  } finally {
    session.ready = true
  }
}

export async function login(username, password) {
  const data = await api('/auth/login', { method: 'POST', body: { username, password } })
  setToken(data.token)
  await refreshSession()
  return data
}

export function logout() {
  clearToken()
  session.user = null
  session.permissions = []
  window.location.hash = '#/overview'
}

export function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme)
  localStorage.setItem('s3bridge.theme', theme)
}
export function currentTheme() {
  return localStorage.getItem('s3bridge.theme') || 'light'
}
