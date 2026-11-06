<script setup lang="ts">
// 关于页：核心版本（CoreService.Version）+ GOAL 迁移说明 + 开源地址 + 致谢。
// 入口在设置页底部「关于」按钮（隐藏路由，不进侧栏）。
import { onMounted, ref } from 'vue'
import { coreVersion } from '../core'

const version = ref('…')
const preview = ref(false)

onMounted(async () => {
  try {
    const v = await coreVersion()
    version.value = v.version
    preview.value = v.preview
  } catch { version.value = '未知' }
})
</script>

<template>
  <div class="page">
    <div class="card">
      <h3>云析 YunX</h3>
      <p class="hint">网盘分享链接解析与高速下载工具</p>
      <div class="row">
        <span class="chip">核心版本 {{ version }}<template v-if="preview">（preview）</template></span>
        <span class="chip plain">GOAL 迁移版</span>
      </div>
      <p class="hint" style="margin: 12px 0 0">
        本项目由 Compose Desktop 原版 GOAL 迁移而来：设置、账号、解析历史与书签与原版同一份数据互通。
      </p>
    </div>

    <div class="card">
      <h3>开源地址</h3>
      <p class="hint">问题反馈与版本发布均在 GitHub</p>
      <a href="https://github.com/tidain/YunX-Desktop" target="_blank" style="color: var(--primary); font-size: 14px">
        github.com/tidain/YunX-Desktop
      </a>
    </div>

    <div class="card">
      <h3>支持开发</h3>
      <p class="hint">感谢使用云析 YunX —— 你的每一次使用、反馈与贡献都在帮助它变得更好。</p>
      <p class="hint" style="margin: 0">
        本项目以 <b>AGPL-3.0</b> 许可开源，源码见
        <a href="https://github.com/tidain/YunX-Desktop" target="_blank" style="color: var(--primary)">
          github.com/tidain/YunX-Desktop
        </a>，欢迎 Star、提 Issue 与贡献代码。
      </p>
    </div>

    <div class="card">
      <h3>致谢</h3>
      <p class="hint" style="margin: 0">
        感谢原版 GOAL 项目与 Wails / Vue / Vite / Go 等开源社区作者们的付出。
      </p>
    </div>
  </div>
</template>
