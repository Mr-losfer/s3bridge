<script setup>
import { ref, onMounted, computed } from 'vue'
import {
  UserPlus, RefreshCw, ShieldCheck, KeyRound, Pencil, UserX, UserCheck, Users as UsersIcon,
  Check, Loader2,
} from 'lucide-vue-next'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { fmtTime, relTime } from '../utils'
import { session } from '../session'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import Input from '../components/ui/Input.vue'
import Field from '../components/ui/Field.vue'
import Select from '../components/ui/Select.vue'
import Switch from '../components/ui/Switch.vue'
import Sheet from '../components/ui/Sheet.vue'
import Dialog from '../components/ui/Dialog.vue'
import Empty from '../components/ui/Empty.vue'
import Skeleton from '../components/ui/Skeleton.vue'

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
    <PageHeader
      title="用户与权限"
      description="基于角色的访问控制（RBAC）：角色给出默认权限，再按用户逐项授予或撤销。每个用户用独立的 API Key 访问 S3。"
    >
      <template #actions>
        <Button variant="outline" @click="load"><RefreshCw />刷新</Button>
        <Button @click="openCreate"><UserPlus />新建用户</Button>
      </template>
    </PageHeader>

    <Card class="overflow-hidden">
      <div class="flex items-center justify-between gap-3 border-b px-4 py-3">
        <div>
          <h2 class="text-sm font-semibold">账号列表</h2>
          <p class="mt-0.5 text-[11.5px] text-muted-foreground">共 {{ users.length }} 个账号 · 禁用后无法登录</p>
        </div>
        <Badge variant="outline"><UsersIcon class="size-3" />RBAC</Badge>
      </div>

      <div v-if="loading" class="space-y-2 p-4"><Skeleton v-for="i in 3" :key="i" class="h-12" /></div>

      <div v-else class="hidden overflow-x-auto md:block">
        <table class="data-table">
          <thead><tr><th>用户</th><th>角色</th><th>状态</th><th class="hidden lg:table-cell">有效权限</th><th class="hidden xl:table-cell">最近登录</th><th class="hidden xl:table-cell">创建时间</th><th class="w-44"></th></tr></thead>
          <tbody>
            <tr v-for="u in users" :key="u.id" :class="u.disabled && 'opacity-55'">
              <td>
                <div class="flex items-center gap-2 font-medium">
                  {{ u.displayName || u.username }}
                  <Badge v-if="u.id === session.user?.id" variant="primary">我</Badge>
                </div>
                <div class="mt-0.5 font-mono text-[10px] text-muted-foreground">{{ u.username }} · {{ u.id }}</div>
              </td>
              <td><Badge :variant="u.role === 'admin' ? 'primary' : 'outline'">{{ roleLabels[u.role] || u.role }}</Badge></td>
              <td><Badge :variant="u.disabled ? 'outline' : 'success'">{{ u.disabled ? '已禁用' : '正常' }}</Badge></td>
              <td class="hidden max-w-[20rem] lg:table-cell">
                <div class="flex flex-wrap gap-1">
                  <Badge v-for="p in (u.permissions || []).slice(0, 5)" :key="p" variant="outline">{{ permLabel(p) }}</Badge>
                  <Badge v-if="(u.permissions || []).length > 5" variant="outline">+{{ u.permissions.length - 5 }}</Badge>
                  <span v-if="!(u.permissions || []).length" class="text-[11px] text-muted-foreground">无权限</span>
                </div>
              </td>
              <td class="hidden whitespace-nowrap text-[11px] text-muted-foreground xl:table-cell">{{ u.lastLoginAt ? relTime(u.lastLoginAt) : '从未登录' }}</td>
              <td class="hidden whitespace-nowrap text-[11px] text-muted-foreground xl:table-cell">{{ fmtTime(u.createdAt) }}</td>
              <td>
                <div class="flex items-center justify-end gap-1">
                  <Button size="sm" variant="outline" @click="openEdit(u)"><Pencil />编辑</Button>
                  <Button size="sm" variant="ghost" @click="openPassword(u)"><KeyRound /></Button>
                  <Button v-if="u.disabled" size="icon-sm" variant="ghost" aria-label="启用" :disabled="busy === u.id" @click="enable(u)"><UserCheck /></Button>
                  <Button v-else size="icon-sm" variant="ghost" aria-label="禁用" class="text-muted-foreground hover:text-destructive" :disabled="busy === u.id" @click="disable(u)"><UserX /></Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="!loading" class="divide-y md:hidden">
        <div v-for="u in users" :key="u.id" class="p-3.5" :class="u.disabled && 'opacity-55'">
          <div class="flex items-start gap-2">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-1.5">
                <p class="truncate text-[13px] font-medium">{{ u.displayName || u.username }}</p>
                <Badge v-if="u.id === session.user?.id" variant="primary">我</Badge>
              </div>
              <p class="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">{{ u.username }} · {{ u.id }}</p>
            </div>
            <div class="flex shrink-0 gap-1">
              <Badge :variant="u.role === 'admin' ? 'primary' : 'outline'">{{ roleLabels[u.role] || u.role }}</Badge>
              <Badge :variant="u.disabled ? 'outline' : 'success'">{{ u.disabled ? '已禁用' : '正常' }}</Badge>
            </div>
          </div>
          <div class="mt-2 flex flex-wrap gap-1">
            <Badge v-for="p in (u.permissions || []).slice(0, 4)" :key="p" variant="outline">{{ permLabel(p) }}</Badge>
            <Badge v-if="(u.permissions || []).length > 4" variant="outline">+{{ u.permissions.length - 4 }}</Badge>
          </div>
          <div class="mt-2.5 flex items-center gap-1.5 border-t pt-2.5">
            <Button size="sm" variant="outline" class="flex-1" @click="openEdit(u)"><Pencil />编辑</Button>
            <Button size="sm" variant="ghost" @click="openPassword(u)"><KeyRound />密码</Button>
            <Button v-if="u.disabled" size="sm" variant="ghost" :disabled="busy === u.id" @click="enable(u)"><UserCheck />启用</Button>
            <Button v-else size="sm" variant="ghost" class="text-muted-foreground" :disabled="busy === u.id" @click="disable(u)"><UserX />禁用</Button>
          </div>
        </div>
      </div>
    </Card>

    <div class="mt-6">
      <h2 class="text-sm font-semibold">角色与默认权限</h2>
      <p class="mt-0.5 text-[11.5px] text-muted-foreground">修改角色后可在用户编辑页逐项调整。</p>
      <div class="mt-3 grid gap-3 sm:grid-cols-3">
        <Card v-for="r in ['admin', 'editor', 'viewer']" :key="r" class="p-4">
          <div class="flex items-center justify-between gap-2">
            <p class="text-[13px] font-semibold">{{ roleLabels[r] }}</p>
            <Badge variant="outline">{{ r }}</Badge>
          </div>
          <p class="mt-1 text-[11px] leading-relaxed text-muted-foreground">{{ roleNote[r] }}</p>
          <div class="mt-2.5 flex flex-wrap gap-1">
            <Badge v-for="p in (defaults[r] || [])" :key="p" variant="outline">{{ permLabel(p) }}</Badge>
          </div>
        </Card>
      </div>
    </div>

    <!-- 用户编辑 -->
    <Sheet v-model:open="drawer" :title="editing ? '编辑用户' : '新建用户'" description="最终权限 = 角色默认 + 额外授予 − 撤销。" class="max-w-xl">
      <div class="grid gap-4 sm:grid-cols-2">
        <Field label="登录名" required><Input v-model="form.username" :disabled="editing" placeholder="例如 alice" /></Field>
        <Field label="显示名"><Input v-model="form.displayName" placeholder="留空则同登录名" /></Field>
        <Field v-if="!editing" label="初始密码" required hint="至少 8 位"><Input v-model="form.password" type="password" placeholder="至少 8 位" /></Field>
        <Field label="角色" :hint="roleNote[form.role]">
          <Select v-model="form.role">
            <option value="admin">admin — 管理员</option>
            <option value="editor">editor — 编辑</option>
            <option value="viewer">viewer — 只读</option>
          </Select>
        </Field>
        <div v-if="editing" class="flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5 sm:col-span-2">
          <div>
            <p class="text-[12.5px] font-medium">禁用该账号</p>
            <p class="text-[11px] text-muted-foreground">立即无法登录，已有 API Key 同时失效</p>
          </div>
          <Switch v-model="form.disabled" />
        </div>
      </div>

      <div class="mt-5">
        <div class="flex items-center gap-2">
          <ShieldCheck class="size-4 text-muted-foreground" />
          <h3 class="text-[13px] font-semibold">权限矩阵</h3>
        </div>
        <p class="mt-1 text-[11px] leading-relaxed text-muted-foreground">「额外授予」补充角色没有的权限，「撤销」移除角色已有的权限。两项互斥。</p>
        <div class="mt-2.5 overflow-hidden rounded-lg border">
          <table class="w-full text-[11.5px]">
            <thead class="bg-muted/40 text-muted-foreground">
              <tr class="border-b">
                <th class="px-2.5 py-2 text-left font-medium">权限</th>
                <th class="w-14 px-1 py-2 text-center font-medium">授予</th>
                <th class="w-14 px-1 py-2 text-center font-medium">撤销</th>
                <th class="w-14 px-1 py-2 text-center font-medium">最终</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in allPerms" :key="p" class="border-b last:border-0">
                <td class="px-2.5 py-1.5">
                  <span class="font-mono text-[10px]">{{ p }}</span>
                  <span class="ml-1.5 text-muted-foreground">{{ permLabel(p) }}</span>
                </td>
                <td class="px-1 py-1.5 text-center">
                  <input type="checkbox" class="size-3.5 accent-[var(--primary)]" :checked="(form.extraPerms || []).includes(p)" @change="togglePerm(p, 'extra')" />
                </td>
                <td class="px-1 py-1.5 text-center">
                  <input type="checkbox" class="size-3.5 accent-[var(--primary)]" :checked="(form.revokedPerms || []).includes(p)" @change="togglePerm(p, 'revoke')" />
                </td>
                <td class="px-1 py-1.5 text-center">
                  <span :class="effectivePerms.includes(p) ? 'inline-grid size-4 place-items-center rounded-full bg-success/15 text-success' : 'text-muted-foreground/40'">
                    <Check v-if="effectivePerms.includes(p)" class="size-2.5" :stroke-width="3" />
                    <template v-else>—</template>
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-2 text-[11px] text-muted-foreground">最终生效 {{ effectivePerms.length }} 项：{{ effectivePerms.map(permLabel).join('、') || '无' }}</p>
      </div>

      <template #footer>
        <Button variant="outline" @click="drawer = false">取消</Button>
        <Button :disabled="saving" @click="save">
          <Loader2 v-if="saving" class="animate-spin" />{{ saving ? '保存中…' : editing ? '保存修改' : '创建用户' }}
        </Button>
      </template>
    </Sheet>

    <!-- 重置密码 -->
    <Dialog
      :open="!!pwdTarget" :title="`重置密码 · ${pwdTarget?.username || ''}`"
      description="新密码将立即生效，用户下次登录需使用新密码。"
      @update:open="(v) => { if (!v) pwdTarget = null }"
    >
      <Field label="新密码" required hint="至少 8 位">
        <Input v-model="pwd" type="password" placeholder="至少 8 位" @keyup.enter="resetPassword" />
      </Field>
      <template #footer>
        <Button variant="outline" @click="pwdTarget = null">取消</Button>
        <Button :disabled="saving" @click="resetPassword">{{ saving ? '提交中…' : '确认重置' }}</Button>
      </template>
    </Dialog>
  </div>
</template>
