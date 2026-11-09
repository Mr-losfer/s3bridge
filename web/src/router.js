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
    label: '总览',
    items: [
      { name: 'overview', label: '概览', icon: '◈', perm: null },
      { name: 'alerts', label: '告警中心', icon: '!', perm: 'plugin:read', badge: 'alerts' },
    ],
  },
  {
    label: '插件与上传',
    items: [
      { name: 'plugins', label: '插件管理', icon: '⊞', perm: 'plugin:read' },
      { name: 'uploadtest', label: '上传测试', icon: '↑', perm: 'upload:write' },
      { name: 'uploads', label: '上传列表', icon: '≡', perm: 'upload:read' },
      { name: 'buckets', label: '存储桶', icon: '▤', perm: 'bucket:read' },
    ],
  },
  {
    label: '接入与管理',
    items: [
      { name: 'keys', label: 'API 密钥', icon: '⌘', perm: null },
      { name: 'mcp', label: 'MCP 能力', icon: '◇', perm: null },
      { name: 'docs', label: '文档中心', icon: '¶', perm: null },
      { name: 'users', label: '用户与权限', icon: '◎', perm: 'user:manage' },
      { name: 'settings', label: '系统设置', icon: '⚙', perm: 'system:manage' },
    ],
  },
]
