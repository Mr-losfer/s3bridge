<script setup>
import { computed } from 'vue'
import Badge from './ui/Badge.vue'
import { statusMeta } from '@/utils'

const props = defineProps({
  status: { type: String, default: 'unknown' },
  showText: { type: Boolean, default: true },
})
const meta = computed(() => statusMeta[props.status] || statusMeta.unknown)
const variant = computed(() => ({
  healthy: 'success',
  unhealthy: 'destructive',
  degraded: 'warning',
  unknown: 'outline',
  disabled: 'outline',
}[props.status] || 'outline'))
const dot = computed(() => ({
  healthy: 'bg-success',
  unhealthy: 'bg-destructive',
  degraded: 'bg-warning',
  unknown: 'bg-muted-foreground/50',
  disabled: 'bg-muted-foreground/40',
}[props.status] || 'bg-muted-foreground/50'))
</script>

<template>
  <Badge :variant="variant">
    <span :class="['size-1.5 rounded-full', dot]" />
    <span v-if="showText">{{ meta.label }}</span>
  </Badge>
</template>
