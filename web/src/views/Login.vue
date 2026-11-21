<script setup>
import { ref } from 'vue'
import { Boxes, ArrowRight, Layers, Database, SquareTerminal, CircleAlert, Loader2 } from 'lucide-vue-next'
import { login } from '../session'
import { notifyError } from '../toast'
import Button from '../components/ui/Button.vue'
import Input from '../components/ui/Input.vue'
import Label from '../components/ui/Label.vue'

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
  <div class="grid min-h-dvh bg-background lg:grid-cols-[1.02fr_1fr]">
    <section class="relative hidden overflow-hidden bg-[oklch(0.185_0.008_55)] p-12 text-white lg:flex lg:flex-col lg:justify-between">
      <div
        class="pointer-events-none absolute inset-0 opacity-[0.16]"
        style="background-image: linear-gradient(to right, rgba(255,255,255,.16) 1px, transparent 1px), linear-gradient(to bottom, rgba(255,255,255,.16) 1px, transparent 1px); background-size: 56px 56px"
      />
      <div class="pointer-events-none absolute -right-40 -top-32 size-[34rem] rounded-full bg-primary/25 blur-[110px]" />
      <div class="pointer-events-none absolute -bottom-48 -left-24 size-[28rem] rounded-full bg-[oklch(0.7_0.08_70)]/10 blur-[100px]" />

      <div class="relative flex items-center gap-2.5">
        <div class="grid size-8 place-items-center rounded-lg bg-white/10 ring-1 ring-white/15">
          <Boxes class="size-[18px]" :stroke-width="1.8" />
        </div>
        <div class="leading-tight">
          <div class="text-[13.5px] font-semibold tracking-tight">S3Bridge</div>
          <div class="text-[10px] tracking-[0.1em] text-white/45">UPLOAD FABRIC</div>
        </div>
      </div>

      <div class="relative max-w-[30rem]">
        <p class="mb-5 font-mono text-[11px] tracking-[0.14em] text-white/40">PLUGIN · S3 · MCP</p>
        <h1 class="text-[clamp(2.4rem,3.6vw,3.5rem)] font-semibold leading-[1.08] tracking-[-0.035em]">
          把杂乱的上传接口，<br />
          <span class="text-primary">收敛成一套标准。</span>
        </h1>
        <p class="mt-6 max-w-[24rem] text-[13.5px] leading-relaxed text-white/55">
          插件化适配第三方开放上传能力，统一转换成 S3 对象存储接口。多租户、按桶归档、按类型检索、异常可见。
        </p>

        <div class="mt-10 grid gap-3">
          <div v-for="f in [
            { icon: Layers, title: 'Go 插件', desc: '独立文件夹 + 运行时编译，任意协议自由实现' },
            { icon: Database, title: 'S3 标准接口', desc: 'AWS SigV4，现有客户端零改造接入' },
            { icon: SquareTerminal, title: 'MCP 全掌控', desc: '53 个工具覆盖插件开发与运维' },
          ]" :key="f.title" class="flex items-center gap-3 text-white/75">
            <div class="grid size-8 shrink-0 place-items-center rounded-md bg-white/[0.06] ring-1 ring-white/10">
              <component :is="f.icon" class="size-4" :stroke-width="1.6" />
            </div>
            <div class="min-w-0">
              <p class="text-[12.5px] font-medium text-white/90">{{ f.title }}</p>
              <p class="text-[11.5px] text-white/40">{{ f.desc }}</p>
            </div>
          </div>
        </div>
      </div>

      <p class="relative font-mono text-[10px] tracking-[0.14em] text-white/30">MULTI-TENANT · US-EAST-1</p>
    </section>

    <section class="flex flex-col">
      <div class="flex h-14 items-center gap-2.5 border-b px-5 lg:hidden">
        <div class="grid size-7 place-items-center rounded-lg bg-foreground text-background">
          <Boxes class="size-4" :stroke-width="1.8" />
        </div>
        <div class="leading-tight">
          <div class="text-[13px] font-semibold tracking-tight">S3Bridge</div>
          <div class="text-[9.5px] tracking-[0.1em] text-muted-foreground">UPLOAD FABRIC</div>
        </div>
      </div>

      <div class="flex flex-1 items-center justify-center px-5 py-10 sm:px-8">
        <div class="w-full max-w-[23rem]">
          <p class="text-[11px] font-medium tracking-[0.08em] text-muted-foreground">CONSOLE ACCESS</p>
          <h2 class="mt-2 text-2xl font-semibold tracking-tight">登录管理后台</h2>
          <p class="mt-1.5 text-[13px] text-muted-foreground">使用管理员账号或分配的子账号登录。</p>

          <form class="mt-7 flex flex-col gap-4" @submit.prevent="submit">
            <div class="grid gap-1.5">
              <Label for="username">用户名</Label>
              <Input id="username" v-model="username" autocomplete="username" autofocus required placeholder="admin" />
            </div>
            <div class="grid gap-1.5">
              <Label for="password">密码</Label>
              <Input
                id="password"
                v-model="password"
                type="password"
                autocomplete="current-password"
                required
                placeholder="请输入密码"
              />
            </div>

            <div v-if="error" class="flex items-start gap-2 rounded-md border border-destructive/25 bg-destructive/[0.06] px-3 py-2.5">
              <CircleAlert class="mt-px size-4 shrink-0 text-destructive" />
              <p class="text-[12px] leading-relaxed text-destructive">{{ error }}</p>
            </div>

            <Button type="submit" size="lg" class="mt-1 w-full" :disabled="busy">
              <Loader2 v-if="busy" class="size-4 animate-spin" />
              {{ busy ? '正在验证…' : '进入后台' }}
              <ArrowRight v-if="!busy" class="size-4" />
            </Button>
          </form>

          <p class="mt-7 border-t pt-4 text-[11.5px] leading-relaxed text-muted-foreground">
            首次启动若未设置 <code class="rounded bg-muted px-1 py-0.5 font-mono text-[10.5px]">S3BRIDGE_BOOTSTRAP_PASSWORD</code>，
            初始密码会在服务启动日志中输出一次。
          </p>
        </div>
      </div>
    </section>
  </div>
</template>
