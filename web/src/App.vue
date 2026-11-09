<script setup>
import { computed, onMounted, ref, onUnmounted } from 'vue'
import { route, go, navGroups } from './router'
import { session, can, refreshSession, logout, currentTheme, applyTheme } from './session'
import { toasts } from './toast'
import { isLoggedIn } from './api'
import Login from './views/Login.vue'
import Overview from './views/Overview.vue'
import Plugins from './views/Plugins.vue'
import PluginDetail from './views/PluginDetail.vue'
import Alerts from './views/Alerts.vue'
import UploadTest from './views/UploadTest.vue'
import Uploads from './views/Uploads.vue'
import Buckets from './views/Buckets.vue'
import Users from './views/Users.vue'
import ApiKeys from './views/ApiKeys.vue'
import Settings from './views/Settings.vue'
import Docs from './views/Docs.vue'
import Mcp from './views/Mcp.vue'

const views = {
  overview: Overview, plugins: Plugins, plugin: PluginDetail, alerts: Alerts,
  uploadtest: UploadTest, uploads: Uploads, buckets: Buckets, users: Users,
  keys: ApiKeys, settings: Settings, docs: Docs, mcp: Mcp,
}
const view = computed(() => views[route.value.name] || Overview)
const theme = ref(currentTheme())
const busy = ref(false)

const visibleGroups = computed(() => navGroups.map((g) => ({
  ...g,
  items: g.items.filter((i) => !i.perm || can(i.perm)),
})).filter((g) => g.items.length))

const currentLabel = computed(() => {
  for (const g of navGroups) {
    const hit = g.items.find((i) => i.name === route.value.name)
    if (hit) return hit.label
  }
  return route.value.name === 'plugin' ? '插件详情' : '概览'
})

function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  applyTheme(theme.value)
}
async function refresh() {
  busy.value = true
  try { await refreshSession() } finally { busy.value = false }
}
function onUnauthorized() { session.user = null }
function onLoggedIn() { refreshSession() }

onMounted(() => {
  applyTheme(theme.value)
  if (isLoggedIn()) refreshSession()
  else session.ready = true
  window.addEventListener('s3bridge:unauthorized', onUnauthorized)
})
onUnmounted(() => window.removeEventListener('s3bridge:unauthorized', onUnauthorized))
</script>

<template>
  <Login v-if="!session.user" @logged-in="onLoggedIn" />
  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <div>
          <div class="brand-mark">S3Bridge</div>
          <div class="brand-sub">Upload Fabric</div>
        </div>
        <span class="brand-dot"></span>
      </div>
      <div v-for="g in visibleGroups" :key="g.label" class="nav-group">
        <div class="nav-label">{{ g.label }}</div>
        <button
          v-for="i in g.items" :key="i.name"
          class="nav-item" :class="{ 'is-active': route.name === i.name || (i.name === 'plugins' && route.name === 'plugin') }"
          @click="go(i.name)"
        >
          <span class="nav-icon">{{ i.icon }}</span>
          <span>{{ i.label }}</span>
          <span v-if="i.badge === 'alerts' && session.openAlerts" class="nav-badge">{{ session.openAlerts }}</span>
        </button>
      </div>
      <div class="sidebar-foot">
        <div>v{{ session.version || 'dev' }} · us-east-1</div>
        <div style="margin-top:4px;opacity:.7">S3 / MCP ENABLED</div>
      </div>
    </aside>

    <main class="main">
      <header class="topbar">
        <div class="topbar-path">S3Bridge <span style="opacity:.4">/</span> {{ currentLabel }}</div>
        <div class="topbar-spacer"></div>
        <button class="icon-btn" :title="'刷新数据'" @click="refresh"><span v-if="busy" class="spinner"></span><span v-else>↻</span></button>
        <button class="icon-btn" :title="theme === 'dark' ? '切换浅色' : '切换深色'" @click="toggleTheme">{{ theme === 'dark' ? '☀' : '◐' }}</button>
        <div class="topbar-user">
          <span class="avatar">{{ (session.user?.displayName || session.user?.username || 'U').slice(0, 1).toUpperCase() }}</span>
          <span>{{ session.user?.displayName || session.user?.username }}</span>
          <span class="tag">{{ session.user?.role }}</span>
        </div>
        <button class="btn btn--quiet btn--sm" @click="logout">退出</button>
      </header>
      <div class="content">
        <component :is="view" :key="route.name + ':' + route.id" />
      </div>
    </main>
  </div>

  <div class="toast-stack">
    <div v-for="t in toasts" :key="t.id" class="toast" :class="{ 'is-error': t.type === 'error' }">
      <div v-if="t.title" class="toast-title">{{ t.title }}</div>
      <div class="toast-msg">{{ t.message }}</div>
    </div>
  </div>
</template>
