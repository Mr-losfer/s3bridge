<script setup>
import { cn } from '@/lib/utils'
defineProps({
  title: { type: String, default: '' },
  description: { type: String, default: '' },
  class: { type: [String, Array, Object], default: '' },
})
</script>

<template>
  <div :class="cn('flex flex-col items-center justify-center gap-2 px-6 py-14 text-center', $props.class)">
    <div v-if="$slots.icon" class="mb-1 grid size-10 place-items-center rounded-lg border bg-muted/60 text-muted-foreground [&_svg]:size-5">
      <slot name="icon" />
    </div>
    <p v-if="title" class="text-sm font-medium text-foreground">{{ title }}</p>
    <p v-if="description" class="max-w-sm text-xs leading-relaxed text-muted-foreground">{{ description }}</p>
    <div v-if="$slots.default" class="mt-2"><slot /></div>
  </div>
</template>
