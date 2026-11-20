<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: [String, Array, Object], default: '' } })
</script>

<template>
  <div :class="cn('p-5', $props.class)">
    <slot />
  </div>
</template>
