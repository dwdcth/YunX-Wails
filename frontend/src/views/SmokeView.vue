<script setup lang="ts">
// 隐藏冒烟页（/#/smoke）：验证「前端 → CoreService(bindings) → C ABI → libyunxcore」链路，
// 由 20 分钟巡检与联调使用；原占位页的升级版。
import { ref } from 'vue'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'

const ping = ref('…')
const version = ref('…')
const defParse = ref('…')
const text = ref('')
const out = ref('等待输入…')

const pretty = (s: unknown) => {
  try { return JSON.stringify(JSON.parse(String(s)), null, 2) } catch { return String(s) }
}

async function run() {
  try { ping.value = (await CoreService.Ping()) === 42 ? '42 ✓' : '异常值' } catch (e) { ping.value = `失败：${e}` }
  try {
    const v = JSON.parse(await CoreService.Version())
    version.value = `${v.version}${v.preview ? ' (preview)' : ''} ✓`
  } catch (e) { version.value = `失败：${e}` }
  try {
    const r = JSON.parse(await CoreService.ParseShare('https://pan.quark.cn/s/abc123XYZ 提取码：a1b2'))
    defParse.value = r.found ? `${r.platform} ${r.shareId} ✓` : '未识别'
  } catch (e) { defParse.value = `失败：${e}` }
}
async function parse() {
  if (!text.value.trim()) { out.value = '请输入分享文案'; return }
  out.value = '解析中…'
  try { out.value = pretty(await CoreService.ParseShare(text.value.trim())) }
  catch (e) { out.value = `调用失败：${e}` }
}
run()
</script>

<template>
  <div class="page">
    <div class="card">
      <h3>链路自检</h3>
      <div class="row">
        <span class="chip plain">Ping：{{ ping }}</span>
        <span class="chip plain">Version：{{ version }}</span>
        <span class="chip plain">默认解析：{{ defParse }}</span>
      </div>
    </div>
    <div class="card">
      <h3>任意解析</h3>
      <div class="row" style="margin-bottom: 10px">
        <input v-model="text" class="input" style="flex: 1" placeholder="粘贴分享文案" @keydown.enter="parse" />
        <button class="btn" @click="parse">解析</button>
      </div>
      <pre class="code">{{ out }}</pre>
    </div>
  </div>
</template>
