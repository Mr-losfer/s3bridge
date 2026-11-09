<script setup>
const props = defineProps({
  sections: { type: Array, default: () => [] },
  emptyText: { type: String, default: '该版本未提供结构化更新说明。' },
})

const kindMeta = {
  breaking: { label: '重要变更', cls: 'tag--accent' },
  security: { label: '安全', cls: 'tag--accent' },
  important: { label: '重点', cls: 'tag--warn' },
  feature: { label: '新功能', cls: 'tag--good' },
  fix: { label: '修复', cls: '' },
  other: { label: '更新', cls: '' },
}
const meta = (k) => kindMeta[k] || kindMeta.other
</script>

<template>
  <div class="update-notes">
    <div v-if="!props.sections.length" class="muted tiny">{{ props.emptyText }}</div>
    <section v-for="(sec, si) in props.sections" :key="si" class="update-notes-section">
      <div class="update-notes-title">
        <span>{{ sec.title }}</span>
        <span class="tag" :class="meta(sec.kind).cls">{{ meta(sec.kind).label }}</span>
      </div>
      <ul class="update-notes-list">
        <li
          v-for="(item, i) in sec.items" :key="i"
          class="update-note"
          :class="{ 'is-important': item.important, 'is-critical': item.kind === 'breaking' || item.kind === 'security' }"
        >
          <span class="update-note-dot" aria-hidden="true"></span>
          <span class="update-note-text">
            <template v-for="(seg, j) in item.segments" :key="j">
              <mark v-if="seg.bold" class="hl">{{ seg.text }}</mark>
              <code v-else-if="seg.code" class="code">{{ seg.text }}</code>
              <template v-else>{{ seg.text }}</template>
            </template>
          </span>
        </li>
      </ul>
    </section>
  </div>
</template>
