<script setup>
import {
  DialogRoot,
  DialogPortal,
  DialogOverlay,
  DialogContent,
  DialogTitle,
  DialogDescription,
  DialogClose,
} from 'reka-ui'
import { X } from 'lucide-vue-next'
import { computed } from 'vue'
import { cn } from '@/lib/utils'

const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '' },
  description: { type: String, default: '' },
  side: { type: String, default: 'right' },
  bare: { type: Boolean, default: false },
  bodyClass: { type: [String, Array, Object], default: '' },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['update:open'])

const sideCls = computed(() =>
  props.side === 'left'
    ? 'inset-y-0 left-0 h-full w-[86%] max-w-xs border-r data-[state=open]:slide-in-from-left data-[state=closed]:slide-out-to-left'
    : 'inset-y-0 right-0 h-full w-[92%] max-w-md border-l data-[state=open]:slide-in-from-right data-[state=closed]:slide-out-to-right',
)
</script>

<template>
  <DialogRoot :open="open" @update:open="emit('update:open', $event)">
    <DialogPortal>
      <DialogOverlay
        class="fixed inset-0 z-50 bg-black/45 backdrop-blur-[2px] data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0"
      />
      <DialogContent
        :class="cn('fixed z-50 flex flex-col overflow-hidden border-border bg-background shadow-xl duration-200 data-[state=open]:animate-in data-[state=closed]:animate-out', sideCls, $props.class)"
      >
        <div v-if="title || description" class="flex items-start justify-between gap-3 border-b px-5 py-4">
          <div class="min-w-0">
            <DialogTitle class="text-base font-semibold tracking-tight">{{ title }}</DialogTitle>
            <DialogDescription v-if="description" class="mt-1 text-xs leading-relaxed text-muted-foreground">
              {{ description }}
            </DialogDescription>
            <DialogDescription v-else class="sr-only">抽屉</DialogDescription>
          </div>
          <DialogClose
            class="-mr-1 -mt-1 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            aria-label="关闭"
          >
            <X class="size-4" />
          </DialogClose>
        </div>
        <DialogTitle v-else class="sr-only">导航</DialogTitle>
        <DialogDescription v-if="!description" class="sr-only">侧边抽屉面板</DialogDescription>
        <DialogClose
          v-if="!(title || description)"
          class="absolute right-3 top-3 z-10 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          aria-label="关闭"
        >
          <X class="size-4" />
        </DialogClose>
        <div :class="cn('min-h-0 flex-1 overflow-y-auto', bare ? '' : 'px-5 py-5', bodyClass)">
          <slot />
        </div>
        <div v-if="$slots.footer" class="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
          <slot name="footer" />
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
