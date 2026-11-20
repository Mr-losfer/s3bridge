<script setup>
import { ChevronDown } from 'lucide-vue-next'
import { cn } from '@/lib/utils'
defineOptions({ inheritAttrs: false })
defineProps({
  modelValue: { type: [String, Number], default: '' },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['update:modelValue'])
</script>

<template>
  <div :class="cn('relative', $props.class)">
    <select
      :value="modelValue"
      class="h-9 w-full appearance-none rounded-md border border-input bg-background pl-3 pr-8 text-[13px] text-foreground shadow-xs transition-colors focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25 disabled:cursor-not-allowed disabled:opacity-50"
      v-bind="$attrs"
      @change="emit('update:modelValue', $event.target.value)"
    >
      <slot />
    </select>
    <ChevronDown class="pointer-events-none absolute right-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
  </div>
</template>
