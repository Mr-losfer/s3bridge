<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { fmtTime, relTime } from '../utils'
import { session } from '../session'

const users = ref([])
const allPerms = ref([])
const defaults = ref({})
const loading = ref(true)
const drawer = ref(false)
const editing = ref(false)
const saving = ref(false)
const busy = ref('')
const pwdTarget = ref(null)
const pwd = ref('')
const form = ref({})

const permLabels = {
  'plugin:read': '插件查看', 'plugin:write': '插件配置', 'plugin:debug': '插件调试',
  'upload:read': '上传查看', 'upload:write': '上传写入',
  'bucket:read': '桶查看', 'bucket:write': '桶管理',
  'user:manage': '用户管理', 'system:manage': '系统设置', 's3:access': 'S3 接入',
}
const roleLabels = { admin: '管理员', editor: '编辑', viewer: '只读' }
const roleNote = {
  admin: '全部权限，含用户管理与系统设置',
  editor: '可配置插件、上传文件、管理存储桶',
  viewer: '只读，可查看插件、上传与桶',
}
function permLabel(p) { return permLabels[p] || p }

const effectivePerms = computed(() => {
  const base = new Set(defaults.value[form.value.role] || [])
  const extra = new Set(form.value.extraPerms || [])
  const revoked = new Set(form.value.revokedPerms || [])
  return allPerms.value.filter((p) => (base.has(p) || extra.has(p)) && !revoked.has(p))
})

async function load() {
  loading.value = true
  try {
    const [u, p] = await Promise.all([api('/users'), api('/permissions')])
    users.value = u || []
    allPerms.value = p?.permissions || []
    defaults.value = p?.defaults || {}
  } catch (e) { notifyError(e, '加载用户失败') }
  finally { loading.value = false }
}
onMounted(load)

function openCreate() {
  editing.value = false
  form.value = { username: '', displayName: '', password: '', role: 'viewer', extraPerms: [], revokedPerms: [] }
  drawer.value = true
}
function openEdit(u) {
  editing.value = true
  const base = new Set(defaults.value[u.role] || [])
  form.value = {
    id: u.id, username: u.username, displayName: u.displayName, role: u.role,
    password: '',
    extraPerms: (u.permissions || []).filter((p) => !base.has(p)),
    revokedPerms: [...(u.revokedPerms || [])],
    disabled: !!u.disabled,
  }
  // 已撤销但不在最终权限里的项也要保留勾选态
  const basePerms = defaults.value[u.role] || []
  for (const p of basePerms) {
    if (!(u.permissions || []).includes(p) && !form.value.revokedPerms.includes(p)) form.value.revokedPerms.push(p)
  }
  drawer.value = true
}

function togglePerm(p, kind) {
  const key = kind === 'extra' ? 'extraPerms' : 'revokedPerms'
  const other = kind === 'extra' ? 'revokedPerms' : 'extraPerms'
  const arr = form.value[key] || []
  const i = arr.indexOf(p)
  if (i >= 0) arr.splice(i, 1)
  else {
    arr.push(p)
    const o = form.value[other] || []
    const oi = o.indexOf(p)
    if (oi >= 0) o.splice(oi, 1)
  }
}

async function save() {
  saving.value = true
  try {
    if (editing.value) {
      await api('/users/' + encodeURIComponent(form.value.id), {
        method: 'PATCH',
        body: {
          role: form.value.role, displayName: form.value.displayName,
          extraPerms: form.value.extraPerms, revokedPerms: form.value.revokedPerms,
          disabled: form.value.disabled,
        },
      })
      notify('用户权限已更新')
    } else {
      if ((form.value.password || '').length < 8) { notifyError(new Error('初始密码至少 8 位')); return }
      await api('/users', {
        method: 'POST',
        body: {
          username: form.value.username.trim(), password: form.value.password,
          displayName: form.value.displayName, role: form.value.role,
          extraPerms: form.value.extraPerms,
        },
      })
      notify(`用户 ${form.value.username} 已创建`)
    }
    drawer.value = false
    await load()
  } catch (e) { notifyError(e) } finally { saving.value = false }
}

async function disable(u) {
  if (u.id === session.user?.id) { notifyError(new Error('不能禁用当前登录账号')); return }
  if (!window.confirm(`确认禁用用户「${u.username}」？该用户将无法登录。`)) return
  busy.value = u.id
  try {
    await api('/users/' + encodeURIComponent(u.id), { method: 'DELETE' })
    notify(`用户 ${u.username} 已禁用`)
    await load()
  } catch (e) { notifyError(e, '操作失败') } finally { busy.value = '' }
}

async function enable(u) {
  busy.value = u.id
  try {
    await api('/users/' + encodeURIComponent(u.id), { method: 'PATCH', body: { disabled: false } })
    notify(`用户 ${u.username} 已启用`)
    await load()
  } catch (e) { notifyError(e) } finally { busy.value = '' }
}

function openPassword(u) { pwdTarget.value = u; pwd.value = '' }
async function resetPassword() {
  if (pwd.value.length < 8) { notifyError(new Error('新密码至少 8 位')); return }
  saving.value = true
  try {
    await api('/users/' + encodeURIComponent(pwdTarget.value.id) + '/password', { method: 'POST', body: { password: pwd.value } })
    notify(`已重置 ${pwdTarget.value.username} 的密码`)
    pwdTarget.value = null
  } catch (e) { notifyError(e, '重置失败') } finally { saving.value = false }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">Access / 用户与权限</div>
        <h1 class="page-title">用户与权限</h1>
        <p class="page-desc">基于角色的访问控制（RBAC）：角色给出默认权限，再按用户逐项授予或撤销。每个用户用独立的 API Key 访问 S3。</p>
      </div>
      <div class="btn-group">
        <button class="btn" @click="load">↻ 刷新</button>
        <button class="btn btn--primary" @click="openCreate">＋ 新建用户</button>
      </div>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">账号列表</h2><p class="section-note">共 {{ users.length }} 个账号 · 禁用后无法登录</p></div></div>
      <div class="table-wrap" style="margin-top:18px">
        <table>
          <thead><tr><th>用户</th><th>角色</th><th>状态</th><th>有效权限</th><th>最近登录</th><th>创建时间</th><th></th></tr></thead>
          <tbody>
            <tr v-for="u in users" :key="u.id" :class="{ 'is-unhealthy': u.disabled }">
              <td>
                <div class="cell-main">{{ u.displayName || u.username }} <span v-if="u.id === session.user?.id" class="tag tag--accent">我</span></div>
                <div class="cell-sub mono">{{ u.username }} · {{ u.id }}</div>
              </td>
              <td><span class="tag" :class="u.role === 'admin' ? 'tag--accent' : ''">{{ roleLabels[u.role] || u.role }}</span></td>
              <td><span class="status" :class="u.disabled ? 'status--disabled' : 'status--healthy'">{{ u.disabled ? '已禁用' : '正常' }}</span></td>
              <td style="max-width:340px">
                <div class="inline-list">
                  <span v-for="p in (u.permissions || []).slice(0, 6)" :key="p" class="tag">{{ permLabel(p) }}</span>
                  <span v-if="(u.permissions || []).length > 6" class="tag">+{{ u.permissions.length - 6 }}</span>
                  <span v-if="!(u.permissions || []).length" class="muted tiny">无权限</span>
                </div>
              </td>
              <td class="muted tiny nowrap">{{ u.lastLoginAt ? relTime(u.lastLoginAt) : '从未登录' }}</td>
              <td class="muted tiny nowrap">{{ fmtTime(u.createdAt) }}</td>
              <td>
                <div class="row-actions">
                  <button class="btn btn--sm" @click="openEdit(u)">编辑</button>
                  <button class="btn btn--quiet btn--sm" @click="openPassword(u)">重置密码</button>
                  <button v-if="u.disabled" class="btn btn--quiet btn--sm" :disabled="busy === u.id" @click="enable(u)">启用</button>
                  <button v-else class="btn btn--quiet btn--sm" :disabled="busy === u.id" @click="disable(u)">禁用</button>
                </div>
              </td>
            </tr>
            <tr v-if="loading"><td colspan="7"><div class="empty"><span class="spinner"></span> 正在加载…</div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">角色与默认权限</h2><p class="section-note">修改角色后可在用户编辑页逐项调整</p></div></div>
      <div style="display:grid;grid-template-columns:repeat(3,1fr);gap:16px;margin-top:18px">
        <div v-for="r in ['admin','editor','viewer']" :key="r" class="panel">
          <div class="strong" style="font-family:var(--serif);font-size:19px">{{ roleLabels[r] }} <span class="mono tiny muted">{{ r }}</span></div>
          <p class="tiny muted" style="margin:6px 0 14px">{{ roleNote[r] }}</p>
          <div class="inline-list">
            <span v-for="p in (defaults[r] || [])" :key="p" class="tag">{{ permLabel(p) }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 用户编辑抽屉 -->
    <template v-if="drawer">
      <div class="drawer-backdrop" @click="drawer = false"></div>
      <aside class="drawer">
        <div class="drawer-head">
          <div>
            <div class="eyebrow">User</div>
            <h2 class="section-title">{{ editing ? '编辑用户' : '新建用户' }}</h2>
            <p class="section-note">最终权限 = 角色默认 + 额外授予 − 撤销</p>
          </div>
          <button class="icon-btn" @click="drawer = false">✕</button>
        </div>
        <div class="drawer-body">
          <div class="form-grid">
            <div class="field">
              <label>登录名 <span class="req">*</span></label>
              <input v-model="form.username" :disabled="editing" placeholder="例如 alice" />
            </div>
            <div class="field">
              <label>显示名</label>
              <input v-model="form.displayName" placeholder="留空则同登录名" />
            </div>
            <div v-if="!editing" class="field">
              <label>初始密码 <span class="req">*</span></label>
              <input v-model="form.password" type="password" placeholder="至少 8 位" />
            </div>
            <div class="field">
              <label>角色</label>
              <select v-model="form.role">
                <option value="admin">admin — 管理员</option>
                <option value="editor">editor — 编辑</option>
                <option value="viewer">viewer — 只读</option>
              </select>
              <div class="field-help">{{ roleNote[form.role] }}</div>
            </div>
            <div v-if="editing" class="field" style="grid-column:1/-1">
              <label>账号状态</label>
              <div class="checkbox-row"><input type="checkbox" v-model="form.disabled" /><span>禁用该账号（立即无法登录）</span></div>
            </div>
          </div>

          <div class="field-group">
            <h3 class="field-group-title">权限矩阵</h3>
            <p class="section-note" style="margin-bottom:16px">「额外授予」补充角色没有的权限，「撤销」移除角色已有的权限。两项互斥。</p>
            <div class="table-wrap">
              <table>
                <thead><tr><th>权限</th><th class="right">额外授予</th><th class="right">撤销</th><th class="right">最终</th></tr></thead>
                <tbody>
                  <tr v-for="p in allPerms" :key="p">
                    <td><span class="mono tiny">{{ p }}</span><div class="cell-sub">{{ permLabel(p) }}</div></td>
                    <td class="right"><input type="checkbox" :checked="(form.extraPerms || []).includes(p)" @change="togglePerm(p, 'extra')" style="accent-color:var(--accent)" /></td>
                    <td class="right"><input type="checkbox" :checked="(form.revokedPerms || []).includes(p)" @change="togglePerm(p, 'revoke')" style="accent-color:var(--accent)" /></td>
                    <td class="right"><span :class="effectivePerms.includes(p) ? 'tag tag--good' : 'tag'">{{ effectivePerms.includes(p) ? '✓' : '—' }}</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p class="tiny muted" style="margin-top:12px">最终生效 {{ effectivePerms.length }} 项：{{ effectivePerms.map(permLabel).join('、') || '无' }}</p>
          </div>
        </div>
        <div class="drawer-foot">
          <button class="btn" @click="drawer = false">取消</button>
          <button class="btn btn--primary" :disabled="saving" @click="save">{{ saving ? '保存中…' : editing ? '保存修改' : '创建用户' }}</button>
        </div>
      </aside>
    </template>

    <!-- 重置密码 -->
    <template v-if="pwdTarget">
      <div class="drawer-backdrop" @click="pwdTarget = null"></div>
      <div style="position:fixed;inset:0;z-index:62;display:grid;place-items:center;padding:24px;pointer-events:none">
        <div class="panel" style="pointer-events:auto;width:min(430px,100%)">
          <div class="eyebrow">Reset Password</div>
          <h2 class="section-title" style="margin:6px 0 4px">重置密码 · {{ pwdTarget.username }}</h2>
          <p class="section-note" style="margin-bottom:20px">新密码将立即生效，用户下次登录需使用新密码。</p>
          <div class="field">
            <label>新密码 <span class="req">*</span></label>
            <input v-model="pwd" type="password" placeholder="至少 8 位" @keyup.enter="resetPassword" />
          </div>
          <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:24px">
            <button class="btn" @click="pwdTarget = null">取消</button>
            <button class="btn btn--primary" :disabled="saving" @click="resetPassword">确认重置</button>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
