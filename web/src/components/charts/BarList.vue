<script setup>
import { computed } from 'vue'

/** 横向条形排行：用于桶容量、慢接口、错误分布等「Top N」场景。 */
const props = defineProps({
  items: { type: Array, default: () => [] }, // [{ label, value, sub, hint, color, onClick }]
  formatValue: { type: Function, default: (v) => String(v) },
  color: { type: String, default: 'var(--primary)' },
})

const max = computed(() => props.items.reduce((n, it) => Math.max(n, Number(it.value || 0)), 0) || 1)
function width(v) {
  const n = Number(v || 0)
  if (n <= 0) return '0%'
  return Math.max(2, (n / max.value) * 100) + '%'
}
</script>

<template>
  <ul class="space-y-2.5">
    <li v-for="(it, i) in items" :key="it.label + i" class="group">
      <div class="mb-1 flex items-center gap-2 text-[11.5px]">
        <span class="min-w-0 flex-1 truncate font-medium">{{ it.label }}</span>
        <span v-if="it.hint" class="shrink-0 text-[10.5px] text-muted-foreground">{{ it.hint }}</span>
        <span class="shrink-0 font-mono tabular-nums text-muted-foreground">{{ formatValue(it.value) }}</span>
      </div>
      <div class="h-1.5 overflow-hidden rounded-full bg-muted">
        <div
          class="h-full rounded-full transition-[width] duration-500"
          :style="{ width: width(it.value), background: it.color || color }"
        />
      </div>
      <p v-if="it.sub" class="mt-1 truncate text-[10.5px] text-muted-foreground/80">{{ it.sub }}</p>
    </li>
  </ul>
</template>
