<script setup>
import { SwitchRoot, SwitchThumb } from 'reka-ui'
import { cn } from '@/lib/utils'

defineProps({
  modelValue: { type: Boolean, default: false },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['update:modelValue'])
</script>

<template>
  <SwitchRoot
    :model-value="modelValue"
    :class="cn('peer inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent bg-input transition-colors data-[state=checked]:bg-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50', $props.class)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <SwitchThumb
      class="pointer-events-none block size-4 rounded-full bg-background shadow-sm ring-0 transition-transform data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0.5"
    />
  </SwitchRoot>
</template>
