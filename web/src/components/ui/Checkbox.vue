<script setup>
import { CheckboxRoot, CheckboxIndicator } from 'reka-ui'
import { Check } from 'lucide-vue-next'
import { cn } from '@/lib/utils'

defineProps({
  modelValue: { type: [Boolean, String], default: false },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['update:modelValue'])
</script>

<template>
  <CheckboxRoot
    :model-value="modelValue === true || modelValue === 'true'"
    :class="cn('peer size-4 shrink-0 rounded-[4px] border border-input bg-background shadow-xs transition-colors data-[state=checked]:border-primary data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50', $props.class)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <CheckboxIndicator class="flex items-center justify-center">
      <Check class="size-3" :stroke-width="3" />
    </CheckboxIndicator>
  </CheckboxRoot>
</template>
