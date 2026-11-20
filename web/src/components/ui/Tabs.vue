<script setup>
import { TabsRoot, TabsList, TabsTrigger } from 'reka-ui'
import { cn } from '@/lib/utils'

defineProps({
  modelValue: { type: String, default: '' },
  items: { type: Array, default: () => [] },
  class: { type: [String, Array, Object], default: '' },
})
const emit = defineEmits(['update:modelValue'])
</script>

<template>
  <TabsRoot :model-value="modelValue" :class="cn('flex flex-col', $props.class)" @update:model-value="emit('update:modelValue', $event)">
    <div class="-mx-1 overflow-x-auto px-1 no-scrollbar">
      <TabsList class="inline-flex h-9 w-max min-w-full items-center gap-1 rounded-lg bg-muted p-1 text-muted-foreground">
        <TabsTrigger
          v-for="item in items"
          :key="item.value"
          :value="item.value"
          class="inline-flex h-7 items-center justify-center gap-1.5 whitespace-nowrap rounded-md px-3 text-[13px] font-medium transition-all data-[state=active]:bg-background data-[state=active]:text-foreground data-[state=active]:shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:pointer-events-none disabled:opacity-50"
        >
          <component :is="item.icon" v-if="item.icon" class="size-3.5" />
          {{ item.label }}
        </TabsTrigger>
      </TabsList>
    </div>
    <slot />
  </TabsRoot>
</template>
