<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { getToken } from '../api'

// 缩略图按需加载：进入视口才拉取对象数据，避免一次性请求几百张图。
// 加载完成后把 blob URL 抛给父组件，灯箱可直接复用，不必二次下载。
const props = defineProps({
  bucket: { type: String, required: true },
  objectKey: { type: String, required: true },
  alt: { type: String, default: '' },
})
const emit = defineEmits(['loaded'])

const el = ref(null)
const src = ref('')
const failed = ref(false)
const loading = ref(false)
let observer = null

async function fetchImage() {
  if (src.value || loading.value) return
  loading.value = true
  try {
    const q = new URLSearchParams({ bucket: props.bucket, key: props.objectKey })
    const res = await fetch('/api/objects/download?' + q.toString(), {
      headers: { Authorization: 'Bearer ' + getToken() },
    })
    if (!res.ok) throw new Error('HTTP ' + res.status)
    const blob = await res.blob()
    src.value = URL.createObjectURL(blob)
    emit('loaded', src.value)
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  if (!('IntersectionObserver' in window)) { fetchImage(); return }
  observer = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (e.isIntersecting) {
        observer.disconnect()
        observer = null
        fetchImage()
        return
      }
    }
  }, { rootMargin: '360px 0px' })
  if (el.value) observer.observe(el.value)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  if (src.value) URL.revokeObjectURL(src.value)
})
</script>

<template>
  <div ref="el" class="media-thumb">
    <img v-if="src" :src="src" :alt="alt" loading="lazy" />
    <div v-else-if="failed" class="media-icon" title="缩略图加载失败">⚠</div>
    <span v-else class="spinner"></span>
  </div>
</template>
