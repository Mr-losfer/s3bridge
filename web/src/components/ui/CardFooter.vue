<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: [String, Array, Object], default: '' } })
</script>

<template>
  <div :class="cn('flex items-center gap-2 border-t bg-muted/40 px-5 py-3', $props.class)">
    <slot />
  </div>
</template>
