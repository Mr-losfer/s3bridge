<script setup>
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent } from 'reka-ui'
import { cn } from '@/lib/utils'

defineProps({
  align: { type: String, default: 'end' },
  class: { type: [String, Array, Object], default: '' },
})
</script>

<template>
  <DropdownMenuRoot>
    <DropdownMenuTrigger as-child>
      <slot name="trigger" />
    </DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent
        :align="align"
        :side-offset="6"
        :class="cn('z-50 min-w-44 overflow-hidden rounded-lg border bg-popover p-1 text-popover-foreground shadow-lg data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95', $props.class)"
      >
        <slot />
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>
