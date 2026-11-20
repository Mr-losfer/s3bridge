<script setup>
import Badge from './ui/Badge.vue'

const props = defineProps({
  sections: { type: Array, default: () => [] },
  emptyText: { type: String, default: '该版本未提供结构化更新说明。' },
})

const kindMeta = {
  breaking: { label: '重要变更', variant: 'destructive' },
  security: { label: '安全', variant: 'destructive' },
  important: { label: '重点', variant: 'warning' },
  feature: { label: '新功能', variant: 'success' },
  fix: { label: '修复', variant: 'default' },
  other: { label: '更新', variant: 'default' },
}
const meta = (k) => kindMeta[k] || kindMeta.other
</script>

<template>
  <div class="text-[12.5px]">
    <p v-if="!props.sections.length" class="text-xs text-muted-foreground">{{ props.emptyText }}</p>
    <section v-for="(sec, si) in props.sections" :key="si" :class="si > 0 && 'mt-4'">
      <div class="mb-2 flex items-center gap-2">
        <span class="text-[12.5px] font-semibold">{{ sec.title }}</span>
        <Badge :variant="meta(sec.kind).variant">{{ meta(sec.kind).label }}</Badge>
      </div>
      <ul class="space-y-1.5">
        <li
          v-for="(item, i) in sec.items"
          :key="i"
          class="flex items-start gap-2 leading-relaxed"
          :class="item.important ? 'rounded-md border-l-2 border-primary bg-primary/[0.05] px-2.5 py-1.5' : 'text-muted-foreground'"
        >
          <span
            aria-hidden="true"
            :class="['mt-[7px] size-1 shrink-0 rounded-full', item.important ? 'bg-primary' : 'bg-muted-foreground/40']"
          />
          <span class="min-w-0 break-words" :class="item.important && 'text-foreground'">
            <template v-for="(seg, j) in item.segments" :key="j">
              <mark v-if="seg.bold" class="rounded bg-primary/10 px-1 font-medium text-primary">{{ seg.text }}</mark>
              <code v-else-if="seg.code" class="rounded border bg-muted px-1 py-px font-mono text-[11px]">{{ seg.text }}</code>
              <template v-else>{{ seg.text }}</template>
            </template>
          </span>
        </li>
      </ul>
    </section>
  </div>
</template>
