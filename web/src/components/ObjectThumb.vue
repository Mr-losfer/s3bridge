<script setup>
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { ImageOff } from 'lucide-vue-next'
import { getToken } from '../api'

// 缩略图按需加载：进入视口才拉取对象数据，避免一次性请求几百张图。
// 加载完成后把 blob URL 抛给父组件，灯箱可直接复用，不必二次下载。
const props = defineProps({
  bucket: { type: String, required: true },
  objectKey: { type: String, required: true },
  alt: { type: String, default: '' },
  // 0 表示请求原图；大于 0 时走后端缩略图端点（JPEG，体积小一个量级）。
  thumbWidth: { type: Number, default: 0 },
  // 'auto' 时高度由图片自身比例决定，供瀑布流布局使用。
  aspect: { type: String, default: '4/3' },
  fit: { type: String, default: 'cover' },
})
const emit = defineEmits(['loaded'])

const el = ref(null)
const src = ref('')
const failed = ref(false)
const loading = ref(false)
let observer = null

const boxStyle = computed(() => (props.aspect === 'auto' ? {} : { aspectRatio: props.aspect }))
const imgClass = computed(() => [
  'w-full transition-transform duration-300 group-hover:scale-[1.02]',
  props.aspect === 'auto' ? 'h-auto' : 'size-full',
  props.fit === 'contain' ? 'object-contain' : 'object-cover',
])

async function fetchImage() {
  if (src.value || loading.value) return
  loading.value = true
  try {
    const q = new URLSearchParams({ bucket: props.bucket, key: props.objectKey })
    let url = '/api/objects/download?'
    if (props.thumbWidth > 0) {
      url = '/api/objects/thumb?'
      q.set('w', String(props.thumbWidth))
    }
    const res = await fetch(url + q.toString(), {
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
  <div
    ref="el"
    class="flex w-full items-center justify-center overflow-hidden bg-muted/60"
    :class="aspect === 'auto' && !src ? 'h-40' : ''"
    :style="boxStyle"
  >
    <img v-if="src" :src="src" :alt="alt" loading="lazy" :class="imgClass" />
    <ImageOff v-else-if="failed" class="size-5 text-muted-foreground/60" />
    <span v-else class="size-4 animate-spin rounded-full border-2 border-muted-foreground/20 border-t-muted-foreground/70" />
  </div>
</template>
