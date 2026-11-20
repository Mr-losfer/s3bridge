<script setup>
import {
  LayoutDashboard, Bell, Blocks, Upload, Images, Database, KeyRound, SquareTerminal,
  BookOpen, Users, Settings, ArrowUpCircle, Boxes,
} from 'lucide-vue-next'
import { route, go, navGroups } from '@/router'
import { session, can } from '@/session'
import { update, openUpdateModal } from '@/update'
import { computed } from 'vue'
import Badge from './ui/Badge.vue'

const emit = defineEmits(['navigate'])

const icons = {
  dashboard: LayoutDashboard,
  bell: Bell,
  blocks: Blocks,
  upload: Upload,
  images: Images,
  database: Database,
  key: KeyRound,
  terminal: SquareTerminal,
  book: BookOpen,
  users: Users,
  settings: Settings,
}

const groups = computed(() =>
  navGroups
    .map((g) => ({ ...g, items: g.items.filter((i) => !i.perm || can(i.perm)) }))
    .filter((g) => g.items.length),
)

const version = computed(() => {
  const v = session.version || 'dev'
  return /^v/i.test(v) ? v : 'v' + v
})

function active(name) {
  return route.value.name === name || (name === 'plugins' && route.value.name === 'plugin')
}
function jump(name) {
  go(name)
  emit('navigate')
}
</script>

<template>
  <aside class="flex h-full flex-col bg-sidebar text-sidebar-foreground">
    <div class="flex h-14 shrink-0 items-center gap-2.5 px-4">
      <div class="grid size-8 shrink-0 place-items-center rounded-lg bg-foreground text-background shadow-sm">
        <Boxes class="size-[18px]" :stroke-width="1.8" />
      </div>
      <div class="min-w-0 leading-tight">
        <div class="truncate text-[13.5px] font-semibold tracking-tight">S3Bridge</div>
        <div class="truncate text-[10px] tracking-[0.08em] text-muted-foreground">UPLOAD FABRIC</div>
      </div>
    </div>

    <nav class="flex-1 space-y-5 overflow-y-auto px-2.5 py-3">
      <div v-for="g in groups" :key="g.label">
        <p class="px-2 pb-1.5 text-[10.5px] font-medium tracking-[0.06em] text-muted-foreground/80">{{ g.label }}</p>
        <button
          v-for="i in g.items"
          :key="i.name"
          class="group flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-[13px] transition-colors"
          :class="active(i.name)
            ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
            : 'text-sidebar-foreground/85 hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground'"
          @click="jump(i.name)"
        >
          <component
            :is="icons[i.icon]"
            class="size-4 shrink-0 transition-colors"
            :class="active(i.name) ? 'text-primary' : 'text-muted-foreground group-hover:text-foreground'"
            :stroke-width="1.8"
          />
          <span class="truncate">{{ i.label }}</span>
          <Badge
            v-if="i.name === 'alerts' && session.openAlerts"
            variant="destructive"
            class="ml-auto h-5 min-w-5 justify-center px-1 tabular-nums"
          >{{ session.openAlerts }}</Badge>
        </button>
      </div>
    </nav>

    <div class="shrink-0 border-t border-sidebar-border px-4 py-3">
      <div class="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <span class="font-mono">{{ version }} · us-east-1</span>
        <button
          v-if="update.status?.updateAvailable"
          class="inline-flex items-center gap-1 rounded-md bg-primary/10 px-1.5 py-0.5 text-[10.5px] font-medium text-primary transition-colors hover:bg-primary/15"
          @click="openUpdateModal"
        >
          <ArrowUpCircle class="size-3" />新版本
        </button>
      </div>
      <p class="mt-1 text-[10px] tracking-[0.06em] text-muted-foreground/60">S3 · MCP · MULTI-TENANT</p>
    </div>
  </aside>
</template>
