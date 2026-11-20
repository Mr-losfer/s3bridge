<script setup>
import { TabsContent } from 'reka-ui'
defineProps({ value: { type: String, required: true } })
</script>

<template>
  <TabsContent :value="value" class="mt-5 focus-visible:outline-none data-[state=active]:animate-in data-[state=active]:fade-in-0">
    <slot />
  </TabsContent>
</template>
