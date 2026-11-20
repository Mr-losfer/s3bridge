<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: [String, Array, Object], default: '' } })
</script>

<template>
  <p :class="cn('text-xs leading-relaxed text-muted-foreground', $props.class)">
    <slot />
  </p>
</template>
