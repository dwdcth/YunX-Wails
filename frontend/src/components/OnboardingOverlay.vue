<script setup lang="ts">
// 首启引导覆盖层：4 步（欢迎 → 核心功能 → 数据安全 → 开始使用）。
// 自查 settingsList 的 onboardingShown 决定是否浮出（App.vue 脚本保持零改动），
// 最后一步「开始使用」写 onboardingShown=true 并关闭；样式沿用 AnnouncementPanel 的 mask/dlg。
import { onMounted, ref } from 'vue'
import { settingsList, settingsSet } from '../core'

const visible = ref(false)
const step = ref(0)
const busy = ref(false)

// 核心功能四项（对齐原版）：解析 / 网盘 / 下载 / 视频
const FEATURES = [
  { icon: '🔗', title: '解析', text: '粘贴夸克 / UC / 迅雷 / 百度等分享链接，一键列出文件' },
  { icon: '☁️', title: '网盘', text: '多账号管理自己的云盘：浏览、转存、创建分享' },
  { icon: '⬇️', title: '下载', text: '内置多线程引擎：分片、断点续传、限速、批量任务' },
  { icon: '🎬', title: '视频', text: 'B 站 / 小红书 / 快手等视频与图集解析下载' },
]

// 数据安全三要点（对齐原版第 3 屏）
const SAFETY = [
  { icon: '⚖️', title: 'AGPL-3.0 开源', text: '代码公开可审计，无隐藏上传行为' },
  { icon: '📁', title: '数据留在本机', text: '设置、账号与任务保存在本机应用数据目录（yunx/）' },
  { icon: '🤝', title: '兼容原版数据', text: '与 Compose 原版同一份数据：互通设置 / 账号 / 历史书签' },
]

onMounted(async () => {
  try {
    const s = await settingsList()
    if (s && !s.onboardingShown) visible.value = true
  } catch { /* 核心未就绪就不打扰 */ }
})

async function finish() {
  busy.value = true
  try {
    await settingsSet('onboardingShown', true)
  } catch { /* 写入失败也不阻塞进入应用 */ }
  busy.value = false
  visible.value = false
}
</script>

<template>
  <div v-if="visible" class="mask">
    <div class="dlg">
      <template v-if="step === 0">
        <div class="hero">
          <div class="logo">云</div>
          <h2 style="margin: 14px 0 4px">欢迎使用云析 YunX</h2>
          <p class="sub">网盘分享链接解析与高速下载工具</p>
        </div>
      </template>

      <template v-else-if="step === 1">
        <h3 style="margin: 0 0 12px">核心功能</h3>
        <div v-for="f in FEATURES" :key="f.title" class="feat">
          <span class="fico">{{ f.icon }}</span>
          <span><b>{{ f.title }}</b> · {{ f.text }}</span>
        </div>
      </template>

      <template v-else-if="step === 2">
        <h3 style="margin: 0 0 12px">数据安全</h3>
        <div v-for="f in SAFETY" :key="f.title" class="feat">
          <span class="fico">{{ f.icon }}</span>
          <span><b>{{ f.title }}</b> · {{ f.text }}</span>
        </div>
      </template>

      <template v-else>
        <div class="hero">
          <div class="logo">云</div>
          <h2 style="margin: 14px 0 4px">一切就绪</h2>
          <p class="sub">从解析页粘贴一条分享链接开始吧</p>
          <button class="btn start-btn" :disabled="busy" @click="finish">
            {{ busy ? '保存中…' : '开始使用' }}
          </button>
        </div>
      </template>

      <div class="row" style="justify-content: space-between; margin-top: 18px">
        <span class="dots">
          <i v-for="i in 4" :key="i" :class="{ on: step === i - 1 }" />
        </span>
        <span class="row">
          <button v-if="step > 0" class="btn text small" :disabled="busy" @click="step--">上一步</button>
          <button v-if="step < 3" class="btn" @click="step++">下一步</button>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask { position: fixed; inset: 0; background: rgba(0,0,0,0.42); display: flex;
  align-items: center; justify-content: center; z-index: 60; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 26px 28px;
  width: min(480px, 92vw); border: 1px solid var(--outline-variant); }
.hero { text-align: center; padding: 10px 0; }
.logo { width: 64px; height: 64px; border-radius: 50%; margin: 0 auto;
  background: var(--primary); color: var(--on-primary);
  display: flex; align-items: center; justify-content: center;
  font-size: 30px; font-weight: 600; user-select: none; }
.sub { color: var(--on-bg-variant); font-size: 13.5px; margin: 0; }
.feat { display: flex; align-items: flex-start; gap: 10px; padding: 9px 4px;
  border-bottom: 1px solid var(--outline-variant); font-size: 13.5px; }
.feat:last-child { border-bottom: none; }
.fico { flex: none; }
.start-btn { margin-top: 16px; padding: 12px 46px; font-size: 15px; }
.dots { display: inline-flex; gap: 6px; align-items: center; }
.dots i { width: 8px; height: 8px; border-radius: 50%; background: var(--outline-variant); }
.dots i.on { background: var(--primary); }
</style>
