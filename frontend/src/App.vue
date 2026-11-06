<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import AppIcon from './components/AppIcon.vue'
import AnnouncementPanel from './components/AnnouncementPanel.vue'
import BridgeHookModal from './components/BridgeHookModal.vue'
import OnboardingOverlay from './components/OnboardingOverlay.vue'
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

// 主题与原版互通：启动时读正式设置（yunx/settings 的 dark_mode），localStorage 只作秒开缓存
onMounted(async () => {
  const s = await settingsList().catch(() => null)
  if (s) setThemeMode((['system', 'light', 'dark'] as const)[s.darkMode] ?? 'system')
  // 公告未读角标
  announcementsList().then((r) => {
    if (r.ok) hasUnread.value = (r.announcements ?? []).some((a) => !a.read)
  }).catch(() => {})
})
</script>

<template>
  <div class="app">
    <aside class="rail">
      <div class="rail-logo" title="云析 YunX">云</div>
      <router-link v-for="t in tabs" :key="t.to" :to="t.to" class="rail-item"
                   :class="{ active: route.path === t.to }">
        <span class="rail-pill"><AppIcon :name="t.icon" /></span>
        <span class="rail-label">{{ t.title }}</span>
      </router-link>
    </aside>

    <div class="main">
      <header class="topbar">
        <h1>{{ title }}</h1>
        <div class="topbar-actions">
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
        </div>
      </header>
      <main class="content"><router-view /></main>
    </div>
    <AnnouncementPanel v-if="showAnnouncements" @close="showAnnouncements = false" />
    <!-- 首启引导：组件自行查 onboardingShown，未展示过才浮出（避免改动本文件脚本） -->
    <OnboardingOverlay />
    <!-- 浏览器插件劫持决定弹窗（组件自行订阅 bridge:hook 事件） -->
    <BridgeHookModal />
  </div>
</template>
