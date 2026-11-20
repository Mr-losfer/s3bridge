<script setup>
import { computed } from 'vue'
import { cn } from '@/lib/utils'

const props = defineProps({
  variant: { type: String, default: 'default' },
  class: { type: [String, Array, Object], default: '' },
})

const variants = {
  default: 'border-transparent bg-secondary text-secondary-foreground',
  primary: 'border-transparent bg-primary/12 text-primary',
  success: 'border-transparent bg-success/12 text-success',
  warning: 'border-transparent bg-warning/15 text-warning',
  destructive: 'border-transparent bg-destructive/12 text-destructive',
  info: 'border-transparent bg-info/12 text-info',
  outline: 'border-border text-muted-foreground',
}
const cls = computed(() =>
  cn(
    'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px] font-medium leading-5',
    variants[props.variant] || variants.default,
    props.class,
  ),
)
</script>

<template>
  <span :class="cls"><slot /></span>
</template>
