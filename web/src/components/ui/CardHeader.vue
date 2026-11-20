<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: [String, Array, Object], default: '' } })
</script>

<template>
  <div :class="cn('flex flex-col gap-1 px-5 pt-5', $props.class)">
    <slot />
  </div>
</template>
