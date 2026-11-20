<script setup>
import { DropdownMenuItem } from 'reka-ui'
import { cn } from '@/lib/utils'

defineProps({
  destructive: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['select'])
</script>

<template>
  <DropdownMenuItem
    :disabled="disabled"
    :class="cn('relative flex cursor-pointer select-none items-center gap-2 rounded-md px-2 py-1.5 text-[13px] outline-none transition-colors data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0', destructive && 'text-destructive data-[highlighted]:bg-destructive/10 data-[highlighted]:text-destructive', $props.class)"
    @select="emit('select', $event)"
  >
    <slot />
  </DropdownMenuItem>
</template>
