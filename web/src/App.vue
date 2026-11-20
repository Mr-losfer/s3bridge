<script setup>
import { computed, onMounted, ref, onUnmounted, watch } from 'vue'
import { route } from './router'
import { session, refreshSession, currentTheme, applyTheme } from './session'
import { toasts } from './toast'
import { isLoggedIn } from './api'
import { loadUpdateStatus } from './update'
import { CircleCheck, CircleAlert } from 'lucide-vue-next'
import UpdateModal from './components/UpdateModal.vue'
import AppSidebar from './components/AppSidebar.vue'
import AppTopbar from './components/AppTopbar.vue'
import Sheet from './components/ui/Sheet.vue'
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
const navOpen = ref(false)

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

// 用登录态变化统一驱动更新检测：无论是首次登录、刷新页面还是重新登录，
// 都只会走这一条路径，避免依赖 Login 组件的事件时机。
watch(
  () => !!session.user,
  (loggedIn) => { if (loggedIn) loadUpdateStatus() },
  { immediate: true },
)

// 路由变化时关闭移动端抽屉，避免返回键后遮罩残留。
watch(() => route.value.name + route.value.id, () => { navOpen.value = false })

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
  <div v-else class="min-h-dvh bg-background">
    <AppSidebar class="fixed inset-y-0 left-0 z-40 hidden w-[15.5rem] border-r border-sidebar-border lg:flex" />

    <Sheet v-model:open="navOpen" side="left" bare class="w-[17.5rem] max-w-[86%]">
      <AppSidebar @navigate="navOpen = false" />
    </Sheet>

    <div class="flex min-h-dvh flex-col lg:pl-[15.5rem]">
      <AppTopbar
        :busy="busy"
        :theme="theme"
        @menu="navOpen = true"
        @refresh="refresh"
        @toggle-theme="toggleTheme"
      />
      <main class="mx-auto w-full max-w-[1440px] flex-1 px-4 py-6 sm:px-6 lg:px-8 lg:py-8">
        <component :is="view" :key="route.name + ':' + route.id" />
      </main>
    </div>
  </div>

  <UpdateModal v-if="session.user" />

  <div
    class="pointer-events-none fixed inset-x-4 bottom-4 z-[100] flex flex-col items-end gap-2 sm:inset-x-auto sm:right-6 sm:bottom-6"
    aria-live="polite"
  >
    <div
      v-for="t in toasts"
      :key="t.id"
      class="pointer-events-auto flex w-full max-w-sm items-start gap-2.5 rounded-lg border bg-popover p-3.5 text-popover-foreground shadow-lg animate-in slide-in-from-bottom-2 fade-in-0 duration-200"
    >
      <component
        :is="t.type === 'error' ? CircleAlert : CircleCheck"
        :class="['mt-0.5 size-4 shrink-0', t.type === 'error' ? 'text-destructive' : 'text-success']"
      />
      <div class="min-w-0 flex-1">
        <p v-if="t.title" class="text-[12.5px] font-medium leading-snug">{{ t.title }}</p>
        <p :class="['text-[12px] leading-relaxed', t.title ? 'mt-0.5 text-muted-foreground' : 'text-foreground']">
          {{ t.message }}
        </p>
      </div>
    </div>
  </div>
</template>
