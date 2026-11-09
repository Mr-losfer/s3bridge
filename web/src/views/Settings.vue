<script setup>
import { ref, onMounted, computed } from 'vue'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { can } from '../session'
import { fmtTime, formatBytes, statusText } from '../utils'

const settings = ref(null)
const site = ref(null)
const plugins = ref([])
const form = ref({})
const loading = ref(true)
const saving = ref(false)
const pwdSaving = ref(false)
const pwd = ref({ oldPassword: '', newPassword: '', confirm: '' })

const canManage = computed(() => can('system:manage'))

async function load() {
  loading.value = true
  try {
    const [s, i, p] = await Promise.all([api('/admin/settings'), api('/admin/site'), api('/plugins')])
    settings.value = s
    site.value = i
    plugins.value = p || []
    form.value = {
      siteName: s?.siteName || '', defaultPlugin: s?.defaultPlugin || '',
      healthEnabled: !!s?.healthEnabled, healthIntervalSecs: s?.healthIntervalSecs || 300,
      alertOnFailure: !!s?.alertOnFailure, announcement: s?.announcement || '',
    }
  } catch (e) { notifyError(e, '加载系统设置失败') }
  finally { loading.value = false }
}
onMounted(load)

async function save() {
  saving.value = true
  try {
    const r = await api('/admin/settings', {
      method: 'PUT',
      body: {
        siteName: form.value.siteName, defaultPlugin: form.value.defaultPlugin,
        healthEnabled: form.value.healthEnabled,
        healthIntervalSecs: Number(form.value.healthIntervalSecs),
        alertOnFailure: form.value.alertOnFailure, announcement: form.value.announcement,
      },
    })
    settings.value = r
    notify('系统设置已保存，健康检测调度器已重建', { title: '保存成功' })
    await load()
  } catch (e) { notifyError(e, '保存失败') } finally { saving.value = false }
}

async function changePassword() {
  if (pwd.value.newPassword.length < 8) { notifyError(new Error('新密码至少 8 位')); return }
  if (pwd.value.newPassword !== pwd.value.confirm) { notifyError(new Error('两次输入的新密码不一致')); return }
  pwdSaving.value = true
  try {
    await api('/auth/password', { method: 'POST', body: { oldPassword: pwd.value.oldPassword, newPassword: pwd.value.newPassword } })
    notify('密码已修改，下次登录请使用新密码')
    pwd.value = { oldPassword: '', newPassword: '', confirm: '' }
  } catch (e) { notifyError(e, '修改失败') } finally { pwdSaving.value = false }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="eyebrow">System / 系统设置</div>
        <h1 class="page-title">系统设置</h1>
        <p class="page-desc">站点标识、默认插件、健康检测总开关与告警策略。修改检测开关会立即重建调度器。</p>
      </div>
      <button class="btn" @click="load">↻ 刷新</button>
    </div>

    <div class="section">
      <div class="section-head"><div><h2 class="section-title">基本设置</h2><p class="section-note">对应 MCP 工具 system_settings_get / system_settings_update</p></div></div>
      <div class="panel" style="margin-top:18px">
        <div class="form-grid">
          <div class="field">
            <label>站点名称</label>
            <input v-model="form.siteName" :disabled="!canManage" />
          </div>
          <div class="field">
            <label>默认上传插件</label>
            <select v-model="form.defaultPlugin" :disabled="!canManage">
              <option value="">自动选择（健康优先）</option>
              <option v-for="p in plugins" :key="p.id" :value="p.id">{{ p.name }} · {{ p.id }} · {{ statusText(p.status) }}</option>
            </select>
            <div class="field-help">未指定插件的上传请求会使用该插件</div>
          </div>
          <div class="field" style="grid-column:1/-1">
            <label>站点公告</label>
            <textarea v-model="form.announcement" :disabled="!canManage" placeholder="展示给所有用户的提示（可留空）"></textarea>
          </div>
        </div>

        <div class="field-group">
          <h3 class="field-group-title">健康检测与告警</h3>
          <p class="section-note" style="margin-bottom:16px">全局总开关；每个插件还可在插件列表里单独配置间隔、超时、失败阈值与探活模式。</p>
          <div class="form-grid">
            <div class="field">
              <label>定时检测总开关</label>
              <div class="checkbox-row"><input type="checkbox" v-model="form.healthEnabled" :disabled="!canManage" /><span>启用后台自动探活</span></div>
            </div>
            <div class="field">
              <label>全局默认间隔（秒）</label>
              <input v-model.number="form.healthIntervalSecs" type="number" min="10" :disabled="!canManage" />
              <div class="field-help">最小 10 秒，插件级配置优先</div>
            </div>
            <div class="field">
              <label>异常时生成告警</label>
              <div class="checkbox-row"><input type="checkbox" v-model="form.alertOnFailure" :disabled="!canManage" /><span>连续失败达阈值时写入告警中心</span></div>
              <div class="field-help">异常插件会在列表置顶并标红</div>
            </div>
          </div>
        </div>

        <div style="display:flex;justify-content:flex-end;gap:9px;margin-top:26px;padding-top:20px;border-top:1px solid var(--line)">
          <button class="btn" @click="load">重置</button>
          <button class="btn btn--primary" :disabled="!canManage || saving || loading" @click="save">{{ saving ? '保存中…' : '保存设置' }}</button>
        </div>
      </div>
    </div>

    <div style="display:grid;grid-template-columns:minmax(0,1.2fr) minmax(300px,.8fr);gap:22px" class="section">
      <div>
        <div class="section-head"><div><h2 class="section-title">站点信息</h2><p class="section-note">只读，与 MCP 的 site_info 一致</p></div></div>
        <div class="panel" style="margin-top:18px">
          <dl class="kv">
            <dt>名称 / 版本</dt><dd>{{ site?.siteName }} · <span class="mono">{{ site?.version }}</span></dd>
            <dt>启动时间</dt><dd>{{ fmtTime(site?.startedAt) }}（已运行 {{ site?.uptime }}）</dd>
            <dt>规模</dt><dd>{{ site?.plugins }} 插件 · {{ site?.users }} 用户 · {{ site?.buckets }} 桶 · {{ site?.objects }} 对象（{{ formatBytes(site?.totalBytes) }}）</dd>
            <dt>健康状态</dt><dd><span class="tag tag--good">{{ site?.healthyPlugins }} 正常</span> <span class="tag tag--warn">{{ site?.unhealthyPlugins }} 异常</span> <span class="tag">{{ site?.openAlerts }} 未处理告警</span></dd>
            <dt>S3 接入点</dt><dd><code class="code">{{ site?.s3Endpoint }}</code> · region <code class="code">{{ site?.s3Region }}</code></dd>
            <dt>调度器</dt><dd class="mono tiny" style="word-break:break-all">{{ JSON.stringify(site?.scheduler || {}) }}</dd>
          </dl>
        </div>
      </div>

      <div>
        <div class="section-head"><div><h2 class="section-title">修改我的密码</h2><p class="section-note">仅影响当前登录账号</p></div></div>
        <div class="panel" style="margin-top:18px">
          <div class="field" style="margin-bottom:16px"><label>原密码</label><input v-model="pwd.oldPassword" type="password" autocomplete="current-password" /></div>
          <div class="field" style="margin-bottom:16px"><label>新密码</label><input v-model="pwd.newPassword" type="password" autocomplete="new-password" /><div class="field-help">至少 8 位</div></div>
          <div class="field" style="margin-bottom:20px"><label>确认新密码</label><input v-model="pwd.confirm" type="password" autocomplete="new-password" @keyup.enter="changePassword" /></div>
          <button class="btn btn--primary btn--block" :disabled="pwdSaving" @click="changePassword">{{ pwdSaving ? '提交中…' : '修改密码' }}</button>
        </div>
      </div>
    </div>
  </div>
</template>
