<script setup lang="ts">
// 登录弹窗：网页登录（WebKitGTK 磁盘 Cookie 库）+ 粘贴凭据 + 账号密码 三通道。
import { onUnmounted, ref } from 'vue'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'
import { accountLoginCookie, accountLoginPassword } from '../core'
import { Events } from '/wails/runtime.js'

const props = defineProps<{ platform: string; platformName: string; credentialHint: string }>()
const emit = defineEmits<{ (e: 'done', ok: boolean): void; (e: 'cancel'): void }>()

// 密码登录支持的平台；其余平台只有「粘贴凭据」tab
const PASSWORD_PLATFORMS = ['pan123', 'guangya', 'ilanzou', 'xunlei']
const hasPwdTab = PASSWORD_PLATFORMS.includes(props.platform)

// 网页登录支持的平台（Go 侧 webLogins 同源：quark/uc/baidu/c139/pan115）
const WEB_PLATFORMS = ['quark', 'uc', 'baidu', 'c139', 'pan115']
const hasWebLogin = WEB_PLATFORMS.includes(props.platform)

const tab = ref<'paste' | 'password'>('paste')
const credential = ref('')
const account = ref('')
const password = ref('')
const newAccount = ref(false)
const busy = ref(false)
const message = ref('')

async function submitPaste() {
  if (!credential.value.trim() || busy.value) return
  busy.value = true
  message.value = '校验中…'
  try {
    const r = await accountLoginCookie(props.platform, credential.value.trim(), newAccount.value)
    if (r.ok && r.loggedIn) {
      message.value = '登录成功 ✓'
      setTimeout(() => emit('done', true), 500)
    } else {
      message.value = r.error ?? '凭据校验失败'
    }
  } catch (e) {
    message.value = String(e)
  } finally {
    busy.value = false
  }
}

// 网页登录：开登录窗口，Go 轮询 Cookie 命中后经 weblogin:done 事件回报
async function startWebLogin() {
  if (busy.value) return
  busy.value = true
  message.value = '已打开登录窗口，请在其中完成登录（命中后自动保存）…'
  try {
    const r = JSON.parse(await CoreService.WebLogin(props.platform))
    if (!r.ok) {
      message.value = r.error ?? '无法打开登录窗口'
      busy.value = false
    }
  } catch (e) {
    message.value = String(e)
    busy.value = false
  }
}

const offWebLogin = Events.On('weblogin:done', (raw: unknown) => {
  const d = (typeof raw === 'object' ? raw : {}) as { platform?: string; ok?: boolean; error?: string }
  if (d.platform !== props.platform) return
  busy.value = false
  if (d.ok) {
    message.value = '网页登录成功，已保存 ✓'
    setTimeout(() => emit('done', true), 600)
  } else {
    message.value = `网页登录未完成：${d.error ?? '未知'}`
  }
})
onUnmounted(() => { try { offWebLogin() } catch { /* runtime 取消器签名差异忽略 */ } })

async function submitPassword() {
  if (!account.value.trim() || !password.value || busy.value) return
  busy.value = true
  message.value = '校验中…'
  try {
    const r = await accountLoginPassword(props.platform, account.value.trim(), password.value)
    if (r.ok && r.loggedIn) {
      message.value = '登录成功 ✓'
      setTimeout(() => emit('done', true), 500)
    } else {
      message.value = r.error ?? '账号或密码错误'
    }
  } catch (e) {
    message.value = String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="mask" @click.self="emit('cancel')">
    <div class="dlg">
      <div class="row" style="justify-content: space-between; margin-bottom: 6px">
        <h3 style="margin: 0">登录 {{ platformName }}</h3>
        <button class="btn text small" @click="emit('cancel')">关闭</button>
      </div>

      <div v-if="hasPwdTab" class="seg" style="margin-bottom: 12px">
        <button :class="{ active: tab === 'paste' }" @click="tab = 'paste'; message = ''">粘贴凭据</button>
        <button :class="{ active: tab === 'password' }" @click="tab = 'password'; message = ''">密码登录</button>
      </div>

      <!-- 粘贴凭据 tab -->
      <template v-if="tab === 'paste'">
        <div v-if="hasWebLogin" class="row" style="justify-content: space-between; margin-bottom: 10px; gap: 8px">
          <span class="hint" style="flex: 1">推荐：在应用内打开{{ platformName }}登录页，登录后自动抓取 Cookie（含 HttpOnly）</span>
          <button class="btn tonal small" :disabled="busy" @click="startWebLogin">网页登录…</button>
        </div>
        <p v-if="credentialHint" class="hint">或从浏览器已登录的{{ platformName }}复制 {{ credentialHint }}，粘贴到下面；核心会联网校验后保存</p>
        <p v-else class="hint">该平台无 Cookie 型登录，试试「密码登录」tab</p>
        <textarea v-model="credential" class="input" rows="4" style="font-family: ui-monospace, monospace; font-size: 12.5px"
                  :placeholder="credentialHint" />
        <div class="row" style="justify-content: space-between; margin-top: 10px">
          <label class="row" style="gap: 6px; font-size: 13px; color: var(--on-bg-variant)">
            <i class="switch sm" :class="{ on: newAccount }" @click="newAccount = !newAccount" />
            添加为新账号（多账号）
          </label>
          <button class="btn" :disabled="!credential.trim() || busy" @click="submitPaste">
            {{ busy ? '校验中…' : '登录' }}
          </button>
        </div>
      </template>

      <!-- 密码登录 tab -->
      <template v-else>
        <p class="hint">输入{{ platformName }}的账号与密码，核心会联网校验后保存凭据</p>
        <div style="display: flex; flex-direction: column; gap: 10px">
          <div class="field">
            <label>账号</label>
            <input v-model="account" class="input" placeholder="手机号 / 邮箱 / 用户名"
                   @keydown.enter="submitPassword" />
          </div>
          <div class="field">
            <label>密码</label>
            <input v-model="password" class="input" type="password" placeholder="登录密码"
                   @keydown.enter="submitPassword" />
          </div>
        </div>
        <div class="row end" style="margin-top: 10px">
          <button class="btn" :disabled="!account.trim() || !password || busy" @click="submitPassword">
            {{ busy ? '校验中…' : '登录' }}
          </button>
        </div>
      </template>

      <div v-if="message" class="banner" :class="{ error: message.includes('失败') }" style="margin-top: 10px">
        {{ message }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask { position: fixed; inset: 0; background: rgba(0,0,0,0.42); display: flex;
  align-items: center; justify-content: center; z-index: 40; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(560px, 92vw); border: 1px solid var(--outline-variant); }
.hint { font-size: 12.5px; color: var(--on-bg-variant); }
.switch.sm { width: 34px; height: 20px; border-width: 2px; }
.switch.sm::after { width: 12px; height: 12px; }
.switch.sm.on::after { left: 17px; }
</style>
