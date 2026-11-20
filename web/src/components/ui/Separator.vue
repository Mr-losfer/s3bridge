<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: [String, Array, Object], default: '' }, vertical: Boolean })
</script>

<template>
  <div :class="cn('shrink-0 bg-border', vertical ? 'h-full w-px' : 'h-px w-full', $props.class)" role="separator" />
</template>
