<script setup>
import { cn } from '@/lib/utils'

defineProps({
  label: { type: String, required: true },
  value: { type: [String, Number], default: '—' },
  unit: { type: String, default: '' },
  hint: { type: String, default: '' },
  icon: { type: [Object, Function], default: null },
  tone: { type: String, default: 'default' },
  valueClass: { type: String, default: '' },
  class: { type: [String, Array, Object], default: '' },
})
</script>

<template>
  <div :class="cn('rounded-xl border bg-card p-4 sm:p-5', $props.class)">
    <div class="flex items-start justify-between gap-3">
      <p class="text-xs font-medium text-muted-foreground">{{ label }}</p>
      <component
        :is="icon"
        v-if="icon"
        :class="cn('size-4 shrink-0', tone === 'danger' ? 'text-destructive' : tone === 'success' ? 'text-success' : 'text-muted-foreground/70')"
      />
    </div>
    <div class="mt-2 flex items-baseline gap-1.5">
      <span
        :class="cn('text-2xl font-semibold tracking-tight tabular-nums sm:text-[28px]', tone === 'danger' && 'text-destructive', tone === 'success' && 'text-success', valueClass)"
      >{{ value }}</span>
      <span v-if="unit" class="text-xs text-muted-foreground">{{ unit }}</span>
    </div>
    <p v-if="hint" class="mt-1.5 line-clamp-2 text-[11.5px] leading-relaxed text-muted-foreground">{{ hint }}</p>
  </div>
</template>
