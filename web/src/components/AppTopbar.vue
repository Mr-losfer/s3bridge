<script setup>
import { computed } from 'vue'
import { Menu, RefreshCw, Sun, Moon, ChevronRight, ChevronDown, KeyRound, Settings, LogOut } from 'lucide-vue-next'
import { route, go, navGroups } from '@/router'
import { session, logout } from '@/session'
import Button from './ui/Button.vue'
import Dropdown from './ui/Dropdown.vue'
import DropdownItem from './ui/DropdownItem.vue'
import DropdownSeparator from './ui/DropdownSeparator.vue'

defineProps({
  busy: { type: Boolean, default: false },
  theme: { type: String, default: 'light' },
})
const emit = defineEmits(['menu', 'refresh', 'toggle-theme'])

const roleLabels = { admin: '管理员', editor: '编辑', viewer: '只读' }
const currentLabel = computed(() => {
  for (const g of navGroups) {
    const hit = g.items.find((i) => i.name === route.value.name)
    if (hit) return hit.label
  }
  return route.value.name === 'plugin' ? '插件详情' : '概览'
})
const initials = computed(() =>
  (session.user?.displayName || session.user?.username || 'U').slice(0, 1).toUpperCase(),
)
const roleLabel = computed(() => roleLabels[session.user?.role] || session.user?.role || '')
</script>

<template>
  <header class="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-1.5 border-b bg-background/85 px-3 backdrop-blur-md supports-[backdrop-filter]:bg-background/70 sm:px-5 lg:px-6">
    <Button variant="ghost" size="icon-sm" class="lg:hidden" aria-label="打开导航" @click="emit('menu')">
      <Menu class="size-4.5" />
    </Button>

    <div class="hidden min-w-0 items-center gap-1.5 text-[13px] sm:flex">
      <span class="text-muted-foreground">S3Bridge</span>
      <ChevronRight class="size-3.5 shrink-0 text-muted-foreground/50" />
      <span class="truncate font-medium">{{ currentLabel }}</span>
    </div>
    <span class="truncate text-sm font-medium sm:hidden">{{ currentLabel }}</span>

    <div class="flex-1" />

    <Button variant="ghost" size="icon-sm" :aria-label="'刷新数据'" :title="'刷新数据'" @click="emit('refresh')">
      <RefreshCw :class="['size-4', busy && 'animate-spin']" />
    </Button>
    <Button
      variant="ghost"
      size="icon-sm"
      :aria-label="theme === 'dark' ? '切换到浅色' : '切换到深色'"
      :title="theme === 'dark' ? '切换到浅色' : '切换到深色'"
      @click="emit('toggle-theme')"
    >
      <component :is="theme === 'dark' ? Sun : Moon" class="size-4" />
    </Button>

    <div class="mx-1 h-5 w-px bg-border" />

    <Dropdown>
      <template #trigger>
        <button
          class="flex items-center gap-2 rounded-md p-1 pr-1 transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 sm:pr-1.5"
        >
          <span class="grid size-7 place-items-center rounded-full bg-primary/10 text-[11px] font-semibold text-primary">
            {{ initials }}
          </span>
          <span class="hidden text-left leading-tight sm:block">
            <span class="block max-w-32 truncate text-[12.5px] font-medium">{{ session.user?.displayName || session.user?.username }}</span>
            <span class="block text-[10.5px] text-muted-foreground">{{ roleLabel }}</span>
          </span>
          <ChevronDown class="hidden size-3.5 text-muted-foreground sm:block" />
        </button>
      </template>

      <div class="border-b px-2.5 py-2">
        <p class="truncate text-[13px] font-medium">{{ session.user?.displayName || session.user?.username }}</p>
        <p class="truncate text-[11px] text-muted-foreground">{{ session.user?.username }} · {{ roleLabel }}</p>
      </div>
      <div class="pt-1">
        <DropdownItem @select="go('keys')"><KeyRound />API 密钥</DropdownItem>
        <DropdownItem @select="go('settings')"><Settings />系统设置</DropdownItem>
      </div>
      <DropdownSeparator />
      <DropdownItem destructive @select="logout()"><LogOut />退出登录</DropdownItem>
    </Dropdown>
  </header>
</template>
