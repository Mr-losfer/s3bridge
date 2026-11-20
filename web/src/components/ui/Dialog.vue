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
import { cn } from '@/lib/utils'

defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '' },
  description: { type: String, default: '' },
  closable: { type: Boolean, default: true },
  dismissable: { type: Boolean, default: true },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['update:open'])

function block(e) {
  e.preventDefault()
}
</script>

<template>
  <DialogRoot :open="open" @update:open="emit('update:open', $event)">
    <DialogPortal>
      <DialogOverlay
        class="fixed inset-0 z-50 bg-black/45 backdrop-blur-[2px] data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0"
      />
      <DialogContent
        :class="cn('fixed left-1/2 top-1/2 z-50 flex max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border bg-background shadow-xl duration-200 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95', $props.class)"
        @pointer-down-outside="dismissable ? undefined : block"
        @escape-key-down="dismissable ? undefined : block"
        @interact-outside="dismissable ? undefined : block"
      >
        <div class="flex items-start justify-between gap-4 border-b px-5 py-4">
          <div class="min-w-0">
            <DialogTitle class="truncate text-base font-semibold tracking-tight">{{ title }}</DialogTitle>
            <DialogDescription v-if="description" class="mt-1 text-xs leading-relaxed text-muted-foreground">
              {{ description }}
            </DialogDescription>
            <DialogDescription v-else class="sr-only">对话框</DialogDescription>
          </div>
          <DialogClose
            v-if="closable"
            class="-mr-1 -mt-1 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            aria-label="关闭"
          >
            <X class="size-4" />
          </DialogClose>
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <slot />
        </div>
        <div v-if="$slots.footer" class="flex flex-wrap items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
          <slot name="footer" />
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
