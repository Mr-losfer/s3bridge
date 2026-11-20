<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: [String, Array, Object], default: '' } })
</script>

<template>
  <label :class="cn('flex items-center gap-1.5 text-xs font-medium text-foreground/80', $props.class)">
    <slot />
  </label>
</template>
