<script setup>
import { TabsContent } from 'reka-ui'
import { cn } from '@/lib/utils'
defineProps({
  value: { type: String, required: true },
  class: { type: [String, Array, Object], default: '' },
})
</script>

<template>
  <TabsContent
    :value="value"
    :class="cn('mt-5 focus-visible:outline-none data-[state=active]:animate-in data-[state=active]:fade-in-0', $props.class)"
  >
    <slot />
  </TabsContent>
</template>
