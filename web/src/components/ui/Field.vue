<script setup>
import { cn } from '@/lib/utils'
defineProps({
  label: { type: String, default: '' },
  hint: { type: String, default: '' },
  required: Boolean,
  class: { type: [String, Array, Object], default: '' },
  for: { type: String, default: '' },
})
</script>

<template>
  <div :class="cn('flex min-w-0 flex-col gap-1.5', $props.class)">
    <label v-if="label" :for="$props.for" class="flex items-center gap-1.5 text-xs font-medium text-foreground/80">
      {{ label }}<span v-if="required" class="text-destructive">*</span>
    </label>
    <slot />
    <p v-if="hint" class="text-[11.5px] leading-relaxed text-muted-foreground">{{ hint }}</p>
  </div>
</template>
