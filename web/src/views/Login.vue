<script setup>
import { ref } from 'vue'
import { login } from '../session'
import { notifyError } from '../toast'

const emit = defineEmits(['logged-in'])
const username = ref('admin')
const password = ref('')
const busy = ref(false)
const error = ref('')

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await login(username.value.trim(), password.value)
    emit('logged-in')
  } catch (e) {
    error.value = e.message
    notifyError(e, '登录失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <section class="login-art">
      <div class="login-meta">S3Bridge / Upload Fabric</div>
      <div>
        <h1>把杂乱的上传接口，<br /><em>收敛成一套标准。</em></h1>
        <p>插件化适配第三方开放上传能力，统一转换成 S3 对象存储接口。多租户、按桶归档、按类型检索、异常可见。</p>
      </div>
      <div class="login-meta">PLUGIN · S3 · MCP · MULTI-TENANT</div>
    </section>
    <section class="login-form-side">
      <div class="login-box">
        <div class="eyebrow">Console Access</div>
        <h2>登录管理后台</h2>
        <p class="muted" style="font-size:13px;margin:0">使用管理员账号或分配的子账号登录。</p>
        <form @submit.prevent="submit">
          <div class="field">
            <label>用户名</label>
            <input v-model="username" autocomplete="username" autofocus required />
          </div>
          <div class="field">
            <label>密码</label>
            <input v-model="password" type="password" autocomplete="current-password" required placeholder="请输入密码" />
          </div>
          <div v-if="error" class="alert-strip" style="margin-top:18px;margin-bottom:0">
            <div>
              <div class="alert-strip-title">无法登录</div>
              <div class="alert-strip-body">{{ error }}</div>
            </div>
          </div>
          <button class="btn btn--primary btn--block" type="submit" :disabled="busy">
            <span v-if="busy" class="spinner"></span>
            {{ busy ? '正在验证…' : '进入后台' }}
          </button>
        </form>
        <p class="tiny muted" style="margin-top:26px;line-height:1.7">
          首次启动若未设置 <code class="code">S3BRIDGE_BOOTSTRAP_PASSWORD</code>，初始密码会在服务启动日志中输出一次。
        </p>
      </div>
    </section>
  </div>
</template>
