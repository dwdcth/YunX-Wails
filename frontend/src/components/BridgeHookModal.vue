<script setup lang="ts">
// 浏览器插件劫持弹窗：插件桥 /hook（挂起等待）→ wails 事件 bridge:hook → 本弹窗决定。
// 接受 = 加入云析下载（platform=browser）并回 {action:'accept'}；取消/归还同理。
import { ref } from 'vue'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'
import { call, fmtBytes } from '../core'
import { Events } from '/wails/runtime.js'

interface HookEvent { id: string; payload: string }

const current = ref<{ id: string; url: string; fileName: string } | null>(null)
const busy = ref(false)

Events.On('bridge:hook', (raw: unknown) => {
  const d = (typeof raw === 'object' ? raw : {}) as { id?: string; payload?: string }
  if (!d.id) return
  try {
    const p = JSON.parse(d.payload ?? '{}') as { url?: string; fileName?: string }
    if (p.url) current.value = { id: d.id, url: p.url, fileName: p.fileName ?? p.url.split('/').pop() ?? '下载' }
  } catch { /* 非法载荷忽略 */ }
})

async function decide(action: 'accept' | 'cancel' | 'return') {
  const cur = current.value
  if (!cur || busy.value) return
  busy.value = true
  try {
    if (action === 'accept') {
      await call('download_submit', { url: cur.url, fileName: cur.fileName, platform: 'browser' })
    }
    await CoreService.BridgeDecide(cur.id, action, '')
  } finally {
    current.value = null
    busy.value = false
  }
}
</script>

<template>
  <div v-if="current" class="mask" @click.self="decide('return')">
    <div class="dlg">
      <h3 style="margin: 0 0 8px">浏览器请求下载</h3>
      <p class="hint">浏览器插件希望把以下下载交给云析接管：</p>
      <div class="card-lite">
        <b>{{ current.fileName }}</b>
        <div class="url" :title="current.url">{{ current.url }}</div>
      </div>
      <div class="row end" style="margin-top: 14px">
        <button class="btn text small" :disabled="busy" @click="decide('return')">归还浏览器</button>
        <button class="btn text small" :disabled="busy" @click="decide('cancel')">取消</button>
        <button class="btn" :disabled="busy" @click="decide('accept')">{{ busy ? '处理中…' : '用云析下载' }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask { position: fixed; inset: 0; background: rgba(0,0,0,0.42); display: flex;
  align-items: center; justify-content: center; z-index: 60; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(560px, 92vw); border: 1px solid var(--outline-variant); }
.hint { font-size: 13px; color: var(--on-bg-variant); margin: 0 0 10px; }
.card-lite { background: var(--surface-container); border-radius: var(--radius);
  padding: 12px 14px; font-size: 14px; word-break: break-all; }
.url { font-size: 12px; color: var(--on-bg-variant); margin-top: 6px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
