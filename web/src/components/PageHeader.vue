<script setup>
import { ChevronLeft } from 'lucide-vue-next'
import { go } from '@/router'

defineProps({
  title: { type: String, required: true },
  description: { type: String, default: '' },
  back: { type: String, default: '' },
  backLabel: { type: String, default: '返回' },
})
</script>

<template>
  <div class="mb-6 flex flex-col gap-4 sm:mb-8 sm:flex-row sm:items-start sm:justify-between">
    <div class="min-w-0">
      <button
        v-if="back"
        class="mb-2 inline-flex items-center gap-0.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
        @click="go(back)"
      >
        <ChevronLeft class="size-3.5" />{{ backLabel }}
      </button>
      <h1 class="text-xl font-semibold tracking-tight sm:text-2xl">{{ title }}</h1>
      <p v-if="description" class="mt-1.5 max-w-3xl text-[13px] leading-relaxed text-muted-foreground">
        {{ description }}
      </p>
    </div>
    <div v-if="$slots.actions" class="flex flex-wrap items-center gap-2 sm:shrink-0">
      <slot name="actions" />
    </div>
  </div>
</template>
