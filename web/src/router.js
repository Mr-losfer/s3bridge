import { ref, computed } from 'vue'

function parseHash() {
  const raw = window.location.hash.replace(/^#\/?/, '')
  const [name, ...rest] = raw.split('/')
  return {
    name: name || 'overview',
    id: rest.length ? decodeURIComponent(rest[0]) : '',
  }
}

const state = ref(parseHash())
window.addEventListener('hashchange', () => { state.value = parseHash() })

export const route = computed(() => state.value)

export function go(name, id) {
  const next = '#/' + name + (id ? '/' + encodeURIComponent(id) : '')
  if (window.location.hash === next) return
  window.location.hash = next
  window.scrollTo({ top: 0, behavior: 'instant' })
}

export const navGroups = [
  {
    label: '工作台',
    items: [
      { name: 'overview', label: '概览', icon: 'dashboard', perm: null },
      { name: 'monitor', label: '监控中心', icon: 'activity', perm: 'system:manage' },
      { name: 'alerts', label: '告警中心', icon: 'bell', perm: 'plugin:read', badge: 'alerts' },
    ],
  },
  {
    label: '上传链路',
    items: [
      { name: 'plugins', label: '插件管理', icon: 'blocks', perm: 'plugin:read' },
      { name: 'uploadtest', label: '上传测试', icon: 'upload', perm: 'upload:write' },
      { name: 'uploads', label: '上传列表', icon: 'images', perm: 'upload:read' },
      { name: 'gallery', label: '图片相册', icon: 'gallery', perm: 'upload:read' },
      { name: 'buckets', label: '存储桶', icon: 'database', perm: 'bucket:read' },
    ],
  },
  {
    label: '接入',
    items: [
      { name: 'keys', label: 'API 密钥', icon: 'key', perm: null },
      { name: 'mcp', label: 'MCP 能力', icon: 'terminal', perm: null },
      { name: 'docs', label: '文档中心', icon: 'book', perm: null },
    ],
  },
  {
    label: '系统',
    items: [
      { name: 'users', label: '用户与权限', icon: 'users', perm: 'user:manage' },
      { name: 'settings', label: '系统设置', icon: 'settings', perm: 'system:manage' },
    ],
  },
]
