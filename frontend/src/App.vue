<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import AppIcon from './components/AppIcon.vue'
import AnnouncementPanel from './components/AnnouncementPanel.vue'
import BridgeHookModal from './components/BridgeHookModal.vue'
import OnboardingOverlay from './components/OnboardingOverlay.vue'
import { CoreService } from '../bindings/yunxwailsdemo/index.js'
import { announcementsList, settingsList } from './core'
import { setThemeMode, theme, cycleThemeMode } from './theme'

const route = useRoute()
// 顶级导航：对齐原版 MainTab（解析/网盘/视频/下载/设置，解析为默认首页）
const tabs = [
  { to: '/resolve', title: '解析', icon: 'link' },
  { to: '/drive', title: '网盘', icon: 'cloud' },
  { to: '/video', title: '视频', icon: 'video' },
  { to: '/download', title: '下载', icon: 'download' },
  { to: '/settings', title: '设置', icon: 'settings' },
]
const title = computed(() => (route.meta.title as string) ?? '云析')
const themeIcon = computed(() =>
  theme.mode === 'dark' ? 'moon' : theme.mode === 'light' ? 'sun' : 'auto')
const themeLabel = computed(() =>
  ({ system: '跟随系统', light: '浅色', dark: '深色' })[theme.mode])

const showAnnouncements = ref(false)
const hasUnread = ref(false)

// ---- 首启「导入原版数据」门控 ----
// 决定之前不得挂载任何视图/触发任何核心 API（各视图 onMounted 都会 call()），保证核心的
// 惰性初始化（首次核心调用才 Open 数据目录）发生在导入决定之后、直接吃到导入的数据。
// ready=false：只渲染顶栏壳（加载态）；ready=true：挂侧栏 + router-view + 引导/公告等浮层。

const ready = ref(false)

/** LegacyImportStatus 返回（纯 Go 文件探测，不碰核心） */
interface LegacyStatus { available: boolean; asked: boolean; sourceDir: string; targetDir: string }
/** LegacyImportDecide 返回（ok=false 且 partial=true = 拷了部分后失败，可重试=覆盖续拷） */
interface LegacyDecide { ok: boolean; imported?: boolean; error?: string; partial?: boolean }

const legacy = ref<LegacyStatus | null>(null) // 非空 = 弹导入询问弹窗
const importPhase = ref<'ask' | 'busy' | 'err' | 'done'>('ask')
const importError = ref('')

onMounted(async () => {
  // 第一件事：探测原版数据（绝不先发核心调用）
  try {
    const st = JSON.parse(await CoreService.LegacyImportStatus()) as LegacyStatus
    if (st.available && !st.asked) {
      legacy.value = st
      return // 等用户决定；决定后 finishGate() 再放行
    }
  } catch (e) {
    console.warn('原版数据探测失败，跳过导入询问:', e)
  }
  finishGate()
})

// WebKitGTK 首帧合成丢失缓解：双 rAF 后给根节点一个会触发重新合成的小变换再还原。
// （与 Go 侧窗口 ±1px 抖动双保险；右键/inspector 能恢复就是这类问题的特征。）
function paintKick() {
  requestAnimationFrame(() => requestAnimationFrame(() => {
    const el = document.documentElement
    el.style.transform = 'translateZ(0)'
    requestAnimationFrame(() => requestAnimationFrame(() => { el.style.transform = '' }))
  }))
}

/** 门控放行：挂载视图（触发核心惰性初始化）+ 原有的启动加载 */
function finishGate() {
  legacy.value = null
  paintKick()
  if (ready.value) return
  ready.value = true
  // 主题与原版互通：启动时读正式设置（yunx/settings 的 dark_mode），localStorage 只作秒开缓存
  settingsList().then((s) => {
    if (s) setThemeMode((['system', 'light', 'dark'] as const)[s.darkMode] ?? 'system')
  }).catch(() => {})
  // 公告未读角标（DLL 侧立即回缓存+后台刷新：首跑常为空，12s 后补取一次）
  const pollBadge = () => announcementsList().then((r) => {
    if (r.ok) hasUnread.value = (r.announcements ?? []).some((a) => !a.read)
  }).catch(() => {})
  pollBadge()
  setTimeout(pollBadge, 12_000)
}

/** 导入 = Decide(true)：ok 短提示后放行；失败停在 err 相位，可重试（覆盖续拷）或放弃 */
async function doImport() {
  if (!legacy.value || importPhase.value === 'busy' || importPhase.value === 'done') return
  importPhase.value = 'busy'
  importError.value = ''
  try {
    const r = JSON.parse(await CoreService.LegacyImportDecide(true)) as LegacyDecide
    if (!r.ok) {
      importPhase.value = 'err'
      importError.value = r.error ?? '导入失败'
      return
    }
    importPhase.value = 'done'
    setTimeout(finishGate, 1200) // 「导入完成」短提示后进入应用
  } catch (e) {
    importPhase.value = 'err'
    importError.value = String(e)
  }
}

/** 暂不导入 / 放弃 = Decide(false)：写跳过标记（防下次启动反复询问）后放行 */
async function skipImport() {
  if (importPhase.value === 'busy' || importPhase.value === 'done') return
  importPhase.value = 'busy'
  try {
    await CoreService.LegacyImportDecide(false)
  } catch (e) {
    console.warn('写入跳过标记失败（不影响进入应用）:', e)
  }
  finishGate()
}
</script>

<template>
  <div class="app">
    <!-- 门控：导入决定之前不挂侧栏（router-link 也不出现） -->
    <aside v-if="ready" class="rail">
      <div class="rail-logo" title="云析 YunX">云</div>
      <router-link v-for="t in tabs" :key="t.to" :to="t.to" class="rail-item"
                   :class="{ active: route.path === t.to }">
        <span class="rail-pill"><AppIcon :name="t.icon" /></span>
        <span class="rail-label">{{ t.title }}</span>
      </router-link>
    </aside>

    <div class="main">
      <!-- 顶栏壳可先显示（加载态），按钮等决定后再出现 -->
      <header class="topbar">
        <h1>{{ title }}</h1>
        <div class="topbar-actions">
          <template v-if="ready">
            <button class="icon-btn" title="书签收藏" @click="$router.push('/bookmarks')">
              <AppIcon name="star" />
            </button>
            <button class="icon-btn" title="公告" @click="showAnnouncements = true">
              <AppIcon name="bell" /><i v-if="hasUnread" class="badge-dot" />
            </button>
            <button class="icon-btn" :title="`主题：${themeLabel}（点击切换）`"
                    @click="cycleThemeMode()">
              <AppIcon :name="themeIcon" />
            </button>
          </template>
          <span v-else class="boot-hint">{{ legacy ? '等待导入决定…' : '正在初始化…' }}</span>
        </div>
      </header>
      <!-- 门控：决定之前不渲染 router-view（各视图 onMounted 都会调核心 API） -->
      <main v-if="ready" class="content"><router-view /></main>
      <main v-else class="content boot">
        <div class="boot-box">
          <div class="boot-logo">云</div>
          <p>{{ legacy ? '等待导入决定…' : '正在初始化…' }}</p>
        </div>
      </main>
    </div>
    <template v-if="ready">
      <AnnouncementPanel v-if="showAnnouncements" @close="showAnnouncements = false" />
      <!-- 首启引导：组件自行查 onboardingShown，未展示过才浮出（ready 后自判，与导入弹窗不冲突） -->
      <OnboardingOverlay />
      <!-- 浏览器插件劫持决定弹窗（组件自行订阅 bridge:hook 事件） -->
      <BridgeHookModal />
    </template>

    <!-- 首启「导入原版数据」弹窗：决定之前挡在最上层（遮罩+卡片，风格同各视图 modal） -->
    <div v-if="legacy" class="mask">
      <div class="dlg">
        <template v-if="importPhase !== 'done'">
          <h3>检测到原版 YunX-Desktop 数据</h3>
          <p class="hint">
            检测到原版 YunX-Desktop 的数据目录，可一次性导入到本应用（YunX-Wails）：<br>
            <code class="codepath">{{ legacy.sourceDir }}</code><br>→<br>
            <code class="codepath">{{ legacy.targetDir }}</code>
          </p>
          <p class="hint">
            导入内容：账号、下载记录、书签、解析历史、设置、密钥。<br>
            建议先关闭原版应用；导入不会修改原版数据。
          </p>
          <div v-if="importPhase === 'err'" class="errbox">
            导入失败：{{ importError || '未知错误' }}
            <br>已拷贝的文件会保留，重试将从断点继续（覆盖同名文件）。
          </div>
          <div class="row end">
            <button class="btn text small" :disabled="importPhase === 'busy'"
                    @click="skipImport">{{ importPhase === 'err' ? '放弃导入' : '暂不导入' }}</button>
            <button v-if="importPhase === 'err'" class="btn small"
                    @click="doImport">重试导入</button>
            <button v-else class="btn small" :disabled="importPhase === 'busy'"
                    @click="doImport">{{ importPhase === 'busy' ? '正在导入…' : '导入到 YunX-Wails' }}</button>
          </div>
        </template>
        <template v-else>
          <h3>导入完成</h3>
          <p class="hint">原版数据已导入到本应用，即将进入…</p>
          <div class="row end"><button class="btn small" @click="finishGate">进入应用</button></div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 门控加载态（决定之前整个应用只显示顶栏壳 + 这块） */
.boot { display: flex; align-items: center; justify-content: center; }
.boot-box { text-align: center; opacity: 0.7; }
.boot-box p { color: var(--on-bg-variant); font-size: 13px; margin: 10px 0 0; }
.boot-logo { width: 56px; height: 56px; border-radius: 50%; margin: 0 auto;
  background: var(--primary); color: var(--on-primary);
  display: flex; align-items: center; justify-content: center;
  font-size: 26px; font-weight: 600; user-select: none; }
.boot-hint { font-size: 12.5px; color: var(--on-bg-variant); }
/* 首启导入弹窗（.mask/.dlg 对齐各视图 modal 约定） */
.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.42); display: flex;
  align-items: center; justify-content: center; z-index: 70; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(440px, 92vw); border: 1px solid var(--outline-variant); }
.dlg h3 { margin: 0 0 8px; font-size: 15px; }
.dlg .hint { color: var(--on-bg-variant); font-size: 12.5px; margin: 0 0 14px; line-height: 1.7; }
.codepath { font-family: ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace;
  font-size: 12.5px; word-break: break-all; }
.errbox { background: var(--error-container); color: var(--error); border-radius: 8px;
  padding: 8px 12px; font-size: 12.5px; line-height: 1.6; margin-bottom: 14px;
  word-break: break-all; }
</style>
