<script setup>
import { ref, onMounted, computed } from 'vue'
import { RefreshCw, Save, ShieldCheck, Info, KeyRound, Loader2, Images, Network, FolderTree, Terminal, Link2 } from 'lucide-vue-next'
import { api } from '../api'
import { notify, notifyError } from '../toast'
import { can } from '../session'
import { fmtTime, formatBytes } from '../utils'
import PageHeader from '../components/PageHeader.vue'
import Badge from '../components/ui/Badge.vue'
import Button from '../components/ui/Button.vue'
import Card from '../components/ui/Card.vue'
import CardHeader from '../components/ui/CardHeader.vue'
import CardTitle from '../components/ui/CardTitle.vue'
import CardDescription from '../components/ui/CardDescription.vue'
import CardContent from '../components/ui/CardContent.vue'
import Input from '../components/ui/Input.vue'
import Field from '../components/ui/Field.vue'
import Select from '../components/ui/Select.vue'
import Switch from '../components/ui/Switch.vue'

const settings = ref(null)
const site = ref(null)
const plugins = ref([])
const form = ref({})
const loading = ref(true)
const saving = ref(false)
const pwdSaving = ref(false)
const pwd = ref({ oldPassword: '', newPassword: '', confirm: '' })

const canManage = computed(() => can('system:manage'))
const origin = window.location.origin

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
      imageValidityEnabled: s?.imageValidityEnabled !== false,
      imageValidityIntervalHours: s?.imageValidityIntervalHours || 72,
      imageValidityFailThreshold: s?.imageValidityFailThreshold || 2,
      webdavEnabled: s?.webdavEnabled !== false,
      ftpEnabled: !!s?.ftpEnabled,
      ftpAddr: s?.ftpAddr || ':2121',
      ftpPassivePorts: s?.ftpPassivePorts || '',
      ftpPublicIp: s?.ftpPublicIp || '',
      openUploadBucket: s?.openUploadBucket || 'default',
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
        imageValidityEnabled: form.value.imageValidityEnabled,
        imageValidityIntervalHours: Number(form.value.imageValidityIntervalHours),
        imageValidityFailThreshold: Number(form.value.imageValidityFailThreshold),
        webdavEnabled: form.value.webdavEnabled,
        ftpEnabled: form.value.ftpEnabled,
        ftpAddr: form.value.ftpAddr,
        ftpPassivePorts: form.value.ftpPassivePorts,
        ftpPublicIp: form.value.ftpPublicIp,
        openUploadBucket: form.value.openUploadBucket,
      },
    })
    settings.value = r
    notify('系统设置已保存；FTP/WebDAV 开关即时生效，FTP 监听地址变更会自动重启服务', { title: '保存成功' })
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
    <PageHeader
      title="系统设置"
      description="站点标识、默认插件、健康检测、图片巡检与接入方式（WebDAV / FTP / 开放上传）。修改后立即生效，无需重启。"
    >
      <template #actions>
        <Button variant="outline" @click="load"><RefreshCw />刷新</Button>
      </template>
    </PageHeader>

    <Card>
      <CardHeader class="pb-4">
        <div class="flex items-center gap-2">
          <ShieldCheck class="size-4 text-muted-foreground" />
          <CardTitle>基本设置</CardTitle>
        </div>
        <CardDescription>对应 MCP 工具 system_settings_get / system_settings_update</CardDescription>
      </CardHeader>
      <CardContent class="border-t pt-4">
        <div class="grid gap-4 sm:grid-cols-2">
          <Field label="站点名称"><Input v-model="form.siteName" :disabled="!canManage" /></Field>
          <Field label="默认上传插件" hint="未指定插件的上传请求会使用该插件">
            <Select v-model="form.defaultPlugin" :disabled="!canManage">
              <option value="">自动选择（健康优先）</option>
              <option v-for="p in plugins" :key="p.id" :value="p.id">{{ p.name }} · {{ p.id }} · {{ p.status }}</option>
            </Select>
          </Field>
          <Field label="站点公告" class="sm:col-span-2">
            <textarea
              v-model="form.announcement" :disabled="!canManage" placeholder="展示给所有用户的提示（可留空）"
              class="flex min-h-20 w-full resize-y rounded-md border border-input bg-background px-3 py-2 text-[13px] shadow-xs placeholder:text-muted-foreground/70 focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/25 disabled:opacity-50"
            />
          </Field>
        </div>

        <div class="mt-5 border-t pt-4">
          <p class="text-[13px] font-semibold">健康检测与告警</p>
          <p class="mt-0.5 text-[11.5px] text-muted-foreground">全局总开关；每个插件还可在插件列表里单独配置间隔、超时、失败阈值与探活模式。</p>
          <div class="mt-3 grid gap-3 sm:grid-cols-3">
            <div class="flex items-start justify-between gap-3 rounded-lg border px-3 py-3">
              <div>
                <p class="text-[12px] font-medium">定时检测</p>
                <p class="mt-0.5 text-[11px] text-muted-foreground">启用后台自动探活</p>
              </div>
              <Switch v-model="form.healthEnabled" :disabled="!canManage" class="mt-0.5 shrink-0" />
            </div>
            <Field label="全局默认间隔（秒）" hint="最小 10 秒，插件级配置优先">
              <Input v-model.number="form.healthIntervalSecs" type="number" min="10" :disabled="!canManage" />
            </Field>
            <div class="flex items-start justify-between gap-3 rounded-lg border px-3 py-3">
              <div>
                <p class="text-[12px] font-medium">异常时生成告警</p>
                <p class="mt-0.5 text-[11px] text-muted-foreground">连续失败达阈值时写入告警中心</p>
              </div>
              <Switch v-model="form.alertOnFailure" :disabled="!canManage" class="mt-0.5 shrink-0" />
            </div>
          </div>
        </div>

        <div class="mt-5 border-t pt-4">
          <div class="flex items-center gap-2">
            <Images class="size-4 text-muted-foreground" />
            <p class="text-[13px] font-semibold">图片有效性巡检</p>
          </div>
          <p class="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">
            只针对本站归档的图片：按周期向本机回环发 HEAD（不支持时退回带 Range 的 GET），
            判定 200 且返回 <code class="rounded border bg-muted px-1 py-px font-mono text-[10.5px]">image/*</code> 即为正常，
            <strong class="text-foreground">不会下载正文</strong>。连续失败达到阈值才标记失效并生成告警。
          </p>
          <div class="mt-3 grid gap-3 sm:grid-cols-3">
            <div class="flex items-start justify-between gap-3 rounded-lg border px-3 py-3">
              <div>
                <p class="text-[12px] font-medium">启用巡检</p>
                <p class="mt-0.5 text-[11px] text-muted-foreground">关闭后不再检查任何图片</p>
              </div>
              <Switch v-model="form.imageValidityEnabled" :disabled="!canManage" class="mt-0.5 shrink-0" />
            </div>
            <Field label="巡检间隔（小时）" hint="默认 72 小时（3 天）；插件清单可单独覆盖">
              <Input v-model.number="form.imageValidityIntervalHours" type="number" min="1" max="720" :disabled="!canManage" />
            </Field>
            <Field label="失败阈值（次）" hint="连续失败达到该次数才判定失效，避免单次抖动误报">
              <Input v-model.number="form.imageValidityFailThreshold" type="number" min="1" max="10" :disabled="!canManage" />
            </Field>
          </div>
        </div>

        <div class="mt-5 flex justify-end gap-2 border-t pt-4">
          <Button variant="outline" @click="load">重置</Button>
          <Button :disabled="!canManage || saving || loading" @click="save">
            <Loader2 v-if="saving" class="animate-spin" /><Save v-else />{{ saving ? '保存中…' : '保存设置' }}
          </Button>
        </div>
      </CardContent>
    </Card>

    <!-- 接入方式：WebDAV / FTP / 开放上传，统一用 API Key 认证 -->
    <Card class="mt-5">
      <CardHeader class="pb-4">
        <div class="flex items-center gap-2">
          <Network class="size-4 text-muted-foreground" />
          <CardTitle>接入方式</CardTitle>
        </div>
        <CardDescription>
          三种方式都只认 API Key（<code class="rounded border bg-muted px-1 py-px font-mono text-[10.5px]">sk_…</code>），
          与后台登录会话相互独立；在「API 密钥」页创建与撤销。
        </CardDescription>
      </CardHeader>
      <CardContent class="border-t pt-4">
        <div class="grid gap-4 lg:grid-cols-3">
          <div class="rounded-lg border p-3.5">
            <div class="flex items-start justify-between gap-3">
              <div class="flex items-center gap-2">
                <FolderTree class="size-4 text-muted-foreground" />
                <p class="text-[12.5px] font-semibold">WebDAV</p>
              </div>
              <Switch v-model="form.webdavEnabled" :disabled="!canManage" class="mt-0.5 shrink-0" />
            </div>
            <p class="mt-2 text-[11px] leading-relaxed text-muted-foreground">
              挂在主服务同端口，不需要额外开放端口。路径 <code class="font-mono">/dav/{bucket}/{key}</code>，
              根目录列出全部存储桶；支持 PROPFIND / GET / PUT / DELETE / MKCOL / MOVE / COPY。
            </p>
            <div class="mt-2 space-y-1 rounded-md bg-muted/50 px-2.5 py-2 font-mono text-[10.5px] leading-relaxed">
              <p>{{ origin }}/dav/</p>
              <p class="text-muted-foreground">用户名任意 · 密码填 API Key</p>
            </div>
          </div>

          <div class="rounded-lg border p-3.5">
            <div class="flex items-start justify-between gap-3">
              <div class="flex items-center gap-2">
                <Terminal class="size-4 text-muted-foreground" />
                <p class="text-[12.5px] font-semibold">FTP</p>
              </div>
              <Switch v-model="form.ftpEnabled" :disabled="!canManage" class="mt-0.5 shrink-0" />
            </div>
            <p class="mt-2 text-[11px] leading-relaxed text-muted-foreground">
              独立端口，仅被动模式。目录结构与存储桶一一对应，适合 Finder、资源管理器、FileZilla、rclone。
              明文传输，建议只在内网或 SSH 隧道内使用。
            </p>
            <Field label="监听地址" class="mt-2">
              <Input v-model="form.ftpAddr" :disabled="!canManage" placeholder=":2121" />
            </Field>
            <Field label="被动端口范围" class="mt-2" hint="Docker/NAT 需固定并映射，如 30000-30010；留空用随机端口">
              <Input v-model="form.ftpPassivePorts" :disabled="!canManage" placeholder="留空用随机端口" />
            </Field>
            <Field label="对外通告地址" class="mt-2" hint="PASV 回复给客户端的 IPv4；留空用控制连接的内网地址">
              <Input v-model="form.ftpPublicIp" :disabled="!canManage" placeholder="203.0.113.10" />
            </Field>
          </div>

          <div class="rounded-lg border p-3.5">
            <div class="flex items-start justify-between gap-3">
              <div class="flex items-center gap-2">
                <Link2 class="size-4 text-muted-foreground" />
                <p class="text-[12.5px] font-semibold">开放上传</p>
              </div>
              <Badge variant="success">常开</Badge>
            </div>
            <p class="mt-2 text-[11px] leading-relaxed text-muted-foreground">
              一个 API Key 直接投递文件，返回可嵌入的直链，无需登录。支持 multipart 与原始 body。
            </p>
            <div class="mt-2 rounded-md bg-muted/50 px-2.5 py-2 font-mono text-[10px] leading-relaxed break-all">
              <p>POST /api/open/upload</p>
              <p>X-API-Key: sk_…</p>
              <p class="text-muted-foreground">→ url: /f/{bucket}/{key}?e=&amp;s=</p>
            </div>
            <Field label="默认存储桶" class="mt-2" hint="请求未指定 bucket 时的落点">
              <Input v-model="form.openUploadBucket" :disabled="!canManage" placeholder="default" />
            </Field>
          </div>
        </div>
      </CardContent>
    </Card>

    <div class="mt-5 grid gap-4 lg:grid-cols-[1.2fr_0.8fr]">
      <Card>
        <CardHeader class="pb-3">
          <div class="flex items-center gap-2"><Info class="size-4 text-muted-foreground" /><CardTitle>站点信息</CardTitle></div>
          <CardDescription>只读，与 MCP 的 site_info 一致</CardDescription>
        </CardHeader>
        <CardContent class="space-y-2.5 border-t pt-4 text-[11.5px]">
          <div v-for="row in [
            { k: '名称 / 版本', v: (site?.siteName || '—') + ' · ' + (site?.version || '—'), mono: false },
            { k: '启动时间', v: fmtTime(site?.startedAt) + '（已运行 ' + (site?.uptime || '—') + '）', mono: false },
            { k: '规模', v: `${site?.plugins} 插件 · ${site?.users} 用户 · ${site?.buckets} 桶 · ${site?.objects} 对象（${formatBytes(site?.totalBytes)}）`, mono: false },
            { k: '健康状态', v: '', mono: false },
            { k: 'S3 接入点', v: (site?.s3Endpoint || '—') + ' · ' + (site?.s3Region || '—'), mono: true },
            { k: '调度器', v: JSON.stringify(site?.scheduler || {}), mono: true },
          ]" :key="row.k" class="grid gap-0.5 sm:grid-cols-[6.5rem_1fr] sm:gap-3">
            <span class="text-muted-foreground">{{ row.k }}</span>
            <div v-if="row.k === '健康状态'" class="flex flex-wrap gap-1">
              <Badge variant="success">{{ site?.healthyPlugins }} 正常</Badge>
              <Badge variant="warning">{{ site?.unhealthyPlugins }} 异常</Badge>
              <Badge variant="outline">{{ site?.openAlerts }} 未处理告警</Badge>
            </div>
            <span v-else :class="['break-words', row.mono && 'font-mono text-[10.5px]']">{{ row.v }}</span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="pb-3">
          <div class="flex items-center gap-2"><KeyRound class="size-4 text-muted-foreground" /><CardTitle>修改我的密码</CardTitle></div>
          <CardDescription>仅影响当前登录账号</CardDescription>
        </CardHeader>
        <CardContent class="border-t pt-4">
          <div class="grid gap-3">
            <Field label="原密码"><Input v-model="pwd.oldPassword" type="password" autocomplete="current-password" /></Field>
            <Field label="新密码" hint="至少 8 位"><Input v-model="pwd.newPassword" type="password" autocomplete="new-password" /></Field>
            <Field label="确认新密码"><Input v-model="pwd.confirm" type="password" autocomplete="new-password" @keyup.enter="changePassword" /></Field>
            <Button class="mt-1 w-full" :disabled="pwdSaving" @click="changePassword">
              <Loader2 v-if="pwdSaving" class="animate-spin" />{{ pwdSaving ? '提交中…' : '修改密码' }}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
