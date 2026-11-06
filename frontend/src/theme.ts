// 主题：浅/深/跟随系统，对齐原版 ThemeController 语义（0 跟随 / 1 浅色 / 2 深色）。
// 原版色板：light primary #415F91，dark primary #AAC7FF，背景 #F9F9FF / #111318（见 ui/theme/Color.kt）。
import { reactive, watchEffect } from 'vue'
import { settingsList } from './core'

export type ThemeMode = 'system' | 'light' | 'dark'

const KEY = 'yunx.theme'

export const theme = reactive({
  mode: (localStorage.getItem(KEY) as ThemeMode | null) ?? 'system',
  reducedMotion: false,
})

const media = window.matchMedia('(prefers-color-scheme: dark)')

export function setThemeMode(mode: ThemeMode) {
  theme.mode = mode
  localStorage.setItem(KEY, mode)
}

export function cycleThemeMode() {
  setThemeMode(theme.mode === 'system' ? 'light' : theme.mode === 'light' ? 'dark' : 'system')
}

function resolved(): 'light' | 'dark' {
  if (theme.mode === 'system') return media.matches ? 'dark' : 'light'
  return theme.mode
}

export function initTheme() {
  const apply = () => {
    document.documentElement.dataset.theme = resolved()
  }
  media.addEventListener('change', () => theme.mode === 'system' && apply())
  watchEffect(apply)
  // 自定义主题色的启动恢复（正式设置 theme_color_mode==1 才覆盖，失败静默）
  void restoreSeedColor()
}

// ---- 种子色覆盖（themeColorMode==1：用户自定义主题色，写正式设置 theme_seed_color）----

const SEED_KEY = 'yunx.seed'

/** 设置里的十进制色值 → #rrggbb（themeSeedColor 存的是 0xRRGGBB 的十进制） */
export function seedNumToHex(n: number): string {
  return '#' + (n & 0xffffff).toString(16).padStart(6, '0')
}

/** localStorage 秒开缓存（正式设置拉取成功后以设置为准） */
export function cachedSeedColor(): string | null {
  const v = localStorage.getItem(SEED_KEY)
  return v && /^#[0-9a-fA-F]{6}$/.test(v) ? v : null
}

/**
 * 应用/清除自定义种子色：直接覆盖 --primary；--primary-container 用其 20% 透明度 color-mix
 * （深浅色共用同一份，简化处理；inline style 优先级高于 :root 与 [data-theme] 规则）
 */
export function applySeedColor(hex: string | null) {
  const root = document.documentElement.style
  if (hex) {
    localStorage.setItem(SEED_KEY, hex)
    root.setProperty('--primary', hex)
    root.setProperty('--primary-container', `color-mix(in srgb, ${hex} 20%, transparent)`)
  } else {
    localStorage.removeItem(SEED_KEY)
    root.removeProperty('--primary')
    root.removeProperty('--primary-container')
  }
}

/** 启动恢复：读正式设置，仅 themeColorMode==1 且 seedColor 有效时覆盖默认色板 */
export async function restoreSeedColor() {
  try {
    const s = await settingsList()
    if (s && s.themeColorMode === 1 && s.themeSeedColor > 0) {
      applySeedColor(seedNumToHex(s.themeSeedColor))
    }
  } catch { /* 核心未就绪时保持默认色板 */ }
}
