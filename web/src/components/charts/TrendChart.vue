<script setup>
import { computed, ref } from 'vue'

/**
 * 轻量 SVG 趋势图：支持 1~3 条序列、面积渐变、hover 竖线与数值提示。
 *
 * 不引第三方图表库：监控页只需要折线/面积这一种图，手写 SVG 体积为零，
 * 且能直接复用主题 CSS 变量，暗色模式下自动跟随。
 */
const props = defineProps({
  points: { type: Array, default: () => [] },
  series: { type: Array, default: () => [] }, // [{ key, label, color, area }]
  height: { type: Number, default: 168 },
  formatValue: { type: Function, default: (v) => String(Math.round(v)) },
  formatTime: { type: Function, default: null },
  showAxis: { type: Boolean, default: true },
})

const W = 720
const PAD = { top: 12, right: 8, bottom: 18, left: 8 }

const hover = ref(-1)

const innerH = computed(() => props.height - PAD.top - PAD.bottom)
const count = computed(() => props.points.length)

const maxValue = computed(() => {
  let max = 0
  for (const p of props.points) {
    for (const s of props.series) {
      const v = Number(p[s.key] || 0)
      if (v > max) max = v
    }
  }
  return max <= 0 ? 1 : max * 1.18
})

function xAt(i) {
  const n = count.value
  if (n <= 1) return PAD.left + (W - PAD.left - PAD.right) / 2
  return PAD.left + (i / (n - 1)) * (W - PAD.left - PAD.right)
}
function yAt(v) {
  const ratio = Math.min(1, Number(v || 0) / maxValue.value)
  return PAD.top + innerH.value * (1 - ratio)
}

function linePath(key) {
  if (!count.value) return ''
  return props.points
    .map((p, i) => `${i === 0 ? 'M' : 'L'}${xAt(i).toFixed(2)},${yAt(p[key]).toFixed(2)}`)
    .join(' ')
}
function areaPath(key) {
  if (!count.value) return ''
  const base = PAD.top + innerH.value
  return `${linePath(key)} L${xAt(count.value - 1).toFixed(2)},${base} L${xAt(0).toFixed(2)},${base} Z`
}

const ticks = computed(() => {
  // 横向网格 3 条就够，避免视觉噪音。
  return [0, 0.5, 1].map((r) => PAD.top + innerH.value * r)
})

const timeLabels = computed(() => {
  const n = count.value
  if (!n) return []
  const idx = n <= 3 ? [0, n - 1] : [0, Math.floor(n / 2), n - 1]
  return idx.map((i) => ({
    x: (xAt(i) / W) * 100,
    text: props.formatTime ? props.formatTime(props.points[i].t) : '',
  }))
})

const activePoint = computed(() => (hover.value >= 0 ? props.points[hover.value] : null))

function onMove(ev) {
  const n = count.value
  if (!n) return
  const rect = ev.currentTarget.getBoundingClientRect()
  const ratio = (ev.clientX - rect.left) / rect.width
  const idx = Math.round(ratio * (n - 1))
  hover.value = Math.max(0, Math.min(n - 1, idx))
}
function onLeave() { hover.value = -1 }
const hoverX = computed(() => (hover.value >= 0 ? (xAt(hover.value) / W) * 100 : 0))
</script>

<template>
  <div class="relative w-full" @mouseleave="onLeave">
    <div
      v-if="points.length < 2"
      class="flex flex-col items-center justify-center gap-1 rounded-lg border border-dashed bg-muted/10 text-center"
      :style="{ height: height + 'px' }"
    >
      <span class="text-[11.5px] font-medium text-muted-foreground">正在积累采样点</span>
      <span class="text-[10.5px] text-muted-foreground/70">监控数据每 20 秒刷新，稍候即可看到曲线</span>
    </div>
    <svg
      v-else
      :viewBox="`0 0 ${W} ${height}`"
      :style="{ height: height + 'px' }"
      class="w-full touch-none"
      preserveAspectRatio="none"
      @mousemove="onMove"
    >
      <defs>
        <linearGradient v-for="s in series" :id="'grad-' + s.key" :key="s.key" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" :stop-color="s.color" stop-opacity="0.28" />
          <stop offset="100%" :stop-color="s.color" stop-opacity="0" />
        </linearGradient>
      </defs>

      <line
        v-for="(y, i) in ticks" :key="'g' + i"
        :x1="PAD.left" :x2="W - PAD.right" :y1="y" :y2="y"
        :stroke="'var(--border)'" stroke-width="1" vector-effect="non-scaling-stroke"
        :stroke-dasharray="i === 2 ? '0' : '3 4'"
      />

      <g v-for="s in series" :key="s.key">
        <path v-if="s.area !== false" :d="areaPath(s.key)" :fill="'url(#grad-' + s.key + ')'" />
        <path
          :d="linePath(s.key)" fill="none" :stroke="s.color" stroke-width="1.8"
          stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"
        />
      </g>

      <line
        v-if="hover >= 0"
        :x1="(hoverX / 100) * W" :x2="(hoverX / 100) * W" :y1="PAD.top" :y2="PAD.top + innerH"
        stroke="var(--muted-foreground)" stroke-width="1" stroke-dasharray="3 3"
        vector-effect="non-scaling-stroke" opacity="0.5"
      />
      <circle
        v-for="s in series" :key="'d' + s.key"
        v-show="hover >= 0"
        :cx="(hoverX / 100) * W" :cy="yAt(activePoint ? activePoint[s.key] : 0)" r="3"
        :fill="s.color" stroke="var(--card)" stroke-width="1.5" vector-effect="non-scaling-stroke"
      />
    </svg>

    <div v-if="showAxis && timeLabels.length" class="pointer-events-none relative -mt-1 h-3">
      <span
        v-for="(l, i) in timeLabels" :key="i"
        class="absolute top-0 -translate-x-1/2 text-[10px] text-muted-foreground/80"
        :style="{ left: l.x + '%' }"
      >{{ l.text }}</span>
    </div>

    <div
      v-if="activePoint"
      class="pointer-events-none absolute top-0 z-10 min-w-[7.5rem] rounded-lg border bg-popover px-2.5 py-2 text-[11px] shadow-md"
      :style="{ left: `min(max(${hoverX}%, 0px), calc(100% - 8rem))` }"
    >
      <div v-if="formatTime" class="mb-1 text-[10px] text-muted-foreground">{{ formatTime(activePoint.t) }}</div>
      <div v-for="s in series" :key="s.key" class="flex items-center justify-between gap-3">
        <span class="flex items-center gap-1.5 text-muted-foreground">
          <span class="size-1.5 rounded-full" :style="{ background: s.color }" />{{ s.label }}
        </span>
        <span class="font-mono font-medium tabular-nums">{{ formatValue(activePoint[s.key]) }}</span>
      </div>
    </div>
  </div>
</template>
