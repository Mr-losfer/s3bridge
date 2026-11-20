<script setup>
import { cn } from '@/lib/utils'
defineOptions({ inheritAttrs: false })
defineProps({
  modelValue: { type: [String, Number], default: '' },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['update:modelValue'])
</script>

<template>
  <textarea
    :value="modelValue"
    :class="cn('flex min-h-20 w-full resize-y rounded-md border border-input bg-background px-3 py-2 text-[13px] leading-relaxed text-foreground shadow-xs transition-colors placeholder:text-muted-foreground/70 focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25 disabled:cursor-not-allowed disabled:opacity-50', $props.class)"
    v-bind="$attrs"
    @input="emit('update:modelValue', $event.target.value)"
  />
</template>
