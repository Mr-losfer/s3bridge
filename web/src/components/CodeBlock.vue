<script setup>
import { ref } from 'vue'
import { Check, Copy } from 'lucide-vue-next'
import { notifyError } from '@/toast'
import { cn } from '@/lib/utils'

const props = defineProps({
  code: { type: [String, Number, Object, Array], default: '' },
  label: { type: String, default: '' },
  maxHeight: { type: String, default: 'none' },
  class: { type: [String, Array, Object], default: '' },
})
const copied = ref(false)

async function copy() {
  try {
    await navigator.clipboard.writeText(String(props.code ?? ''))
    copied.value = true
    setTimeout(() => { copied.value = false }, 1400)
  } catch {
    notifyError(new Error('浏览器未授权剪贴板'))
  }
}
</script>

<template>
  <div :class="cn('relative min-w-0 overflow-hidden rounded-lg border border-white/12 bg-[oklch(0.158_0.006_60)] text-[oklch(0.93_0.004_80)]', $props.class)">
    <div v-if="label || $slots.actions" class="flex items-center justify-between gap-3 border-b border-white/12 px-3 py-1.5">
      <span class="text-[10.5px] font-medium uppercase tracking-[0.08em] text-white/45">{{ label }}</span>
      <div class="flex items-center gap-1">
        <slot name="actions" />
        <button
          class="inline-flex items-center gap-1 rounded px-1.5 py-1 text-[11px] text-white/55 transition-colors hover:bg-white/10 hover:text-white"
          @click="copy"
        >
          <component :is="copied ? Check : Copy" class="size-3" />{{ copied ? '已复制' : '复制' }}
        </button>
      </div>
    </div>
    <div v-else class="absolute right-2 top-2">
      <button
        class="inline-flex size-7 items-center justify-center rounded text-white/50 transition-colors hover:bg-white/10 hover:text-white"
        @click="copy"
        aria-label="复制"
      >
        <component :is="copied ? Check : Copy" class="size-3.5" />
      </button>
    </div>
    <pre
      class="overflow-x-auto p-3.5 font-mono text-[11.5px] leading-[1.75] whitespace-pre"
      :style="{ maxHeight }"
    ><code>{{ code }}</code></pre>
  </div>
</template>
