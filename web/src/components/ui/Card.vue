<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: [String, Array, Object], default: '' } })
</script>

<template>
  <div :class="cn('rounded-xl border bg-card text-card-foreground', $props.class)">
    <slot />
  </div>
</template>
