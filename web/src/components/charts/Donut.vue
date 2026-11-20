<script setup>
import { computed } from 'vue'

/** 环形占比图：用 stroke-dasharray 分段，中心显示总量。 */
const props = defineProps({
  items: { type: Array, default: () => [] }, // [{ label, value, color }]
  size: { type: Number, default: 148 },
  thickness: { type: Number, default: 12 },
  centerLabel: { type: String, default: '' },
  centerValue: { type: [String, Number], default: '' },
})

const R = 42
const CIRC = 2 * Math.PI * R

const total = computed(() => props.items.reduce((n, it) => n + Number(it.value || 0), 0))

const segments = computed(() => {
  const t = total.value || 1
  let offset = 0
  return props.items
    .filter((it) => Number(it.value || 0) > 0)
    .map((it) => {
      const ratio = Number(it.value || 0) / t
      const len = ratio * CIRC
      const seg = { ...it, ratio, len, offset }
      offset += len
      return seg
    })
})
</script>

<template>
  <div class="flex items-center gap-5">
    <div class="relative shrink-0" :style="{ width: size + 'px', height: size + 'px' }">
      <svg viewBox="0 0 100 100" class="size-full -rotate-90">
        <circle cx="50" cy="50" :r="R" fill="none" stroke="var(--muted)" :stroke-width="thickness" />
        <circle
          v-for="s in segments" :key="s.label"
          cx="50" cy="50" :r="R" fill="none" :stroke="s.color" :stroke-width="thickness"
          :stroke-dasharray="`${s.len} ${CIRC - s.len}`" :stroke-dashoffset="-s.offset"
          stroke-linecap="butt"
        />
      </svg>
      <div class="absolute inset-0 flex flex-col items-center justify-center gap-0.5">
        <span class="text-lg font-semibold tabular-nums">{{ centerValue }}</span>
        <span class="text-[10px] text-muted-foreground">{{ centerLabel }}</span>
      </div>
    </div>
    <ul class="min-w-0 flex-1 space-y-1.5">
      <li v-for="s in segments" :key="s.label" class="flex items-center gap-2 text-[11.5px]">
        <span class="size-2 shrink-0 rounded-full" :style="{ background: s.color }" />
        <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ s.label }}</span>
        <span class="font-mono tabular-nums">{{ (s.ratio * 100).toFixed(0) }}%</span>
      </li>
    </ul>
  </div>
</template>
