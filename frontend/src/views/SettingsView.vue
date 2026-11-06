<script setup lang="ts">
// 设置页：全部经核心 settings_list/settings_set 读写**正式设置**（yunx/settings，
// 与 Compose 原版 / Linux 移植版同一份数据）；代理与 ffmpeg 由 Go 宿主探测/注入。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'
import { ARIA2_PATH_KEY, aria2Status, fmtBytes, githubMirrorRefresh, kvGet, kvPut,
         settingsList, settingsSet, settingsSetPlatform,
         storageApplyEncryption, storageMigrateDb, updateCheck,
         type AppSettings, type Aria2StatusInfo } from '../core'
import { applySeedColor, cachedSeedColor, seedNumToHex, setThemeMode, theme } from '../theme'

const router = useRouter()
const s = ref<AppSettings | null>(null)
const loadError = ref('')
const flash = ref('')
const hostFlash = ref('')

// 认证备份
const backupPwd = ref('')
const backupFlash = ref('')

async function doExport() {
  try {
    const r = JSON.parse(await CoreService.AuthBackupExport(backupPwd.value))
    backupFlash.value = r.canceled ? '已取消'
      : r.ok ? `已导出 ${r.bytes} 字节 → ${r.path}${r.encrypted ? '（加密）' : '（明文 JSON）'}`
      : `失败：${r.error}`
  } catch (e) { backupFlash.value = `失败：${e}` }
}

async function doImport() {
  try {
    const r = JSON.parse(await CoreService.AuthBackupImport(backupPwd.value))
    backupFlash.value = r.canceled ? '已取消'
      : r.ok ? `导入成功，恢复 ${r.imported} 个平台凭证`
      : `失败：${r.error}`
  } catch (e) { backupFlash.value = `失败：${e}` }
  setTimeout(() => (backupFlash.value = ''), 4000)
}

// 更新检查
const updateBusy = ref(false)
const updateFlash = ref('')
const updateInfo = ref<{ current: string; latest: string; hasUpdate: boolean; url: string } | null>(null)

async function doUpdateCheck() {
  updateBusy.value = true
  updateFlash.value = ''
  try {
    const r = await updateCheck()
    if (r.ok) {
      updateInfo.value = r
      updateFlash.value = r.hasUpdate ? '发现新版本' : '已是最新 ✓'
    } else {
      updateFlash.value = r.error ?? '检查失败'
    }
  } catch (e) {
    updateFlash.value = String(e)
  } finally {
    updateBusy.value = false
  }
}

// 代理 / ffmpeg（经 Go 宿主）
const sysHost = ref('')
const sysPort = ref(0)
const ffmpegPath = ref('')

function done(msg: string) {
  flash.value = msg
  setTimeout(() => (flash.value = ''), 1500)
}

async function save(key: string, value: string | number | boolean) {
  const r = await settingsSet(key, value)
  done(r.ok ? '已保存 ✓' : `失败：${r.error}`)
}

// ---- 按平台线程数：key 固定 downloadThreadsFor，platform 字段区分平台 ----
// 平台清单与网盘页 PLATFORMS 同源（10 平台）；settings_list 不回读该维度，输入留空 = 跟随全局线程数

const THREAD_PLATFORMS = [
  { key: 'quark', name: '夸克网盘' }, { key: 'uc', name: 'UC网盘' },
  { key: 'xunlei', name: '迅雷云盘' }, { key: 'baidu', name: '百度网盘' },
  { key: 'c139', name: '移动云盘' }, { key: 'pan123', name: '123云盘' },
  { key: 'pan115', name: '115网盘' }, { key: 'guangya', name: '光鸭云盘' },
  { key: 'ilanzou', name: '蓝奏云优享版' }, { key: 'lanzou', name: '蓝奏云' },
]
const perThreadOpen = ref(false)
const perThreads = ref<Record<string, number>>({})

async function savePlatformThreads(platform: string) {
  const v = Math.max(0, Math.floor(perThreads.value[platform] ?? 0))
  // 0 = 清除该平台的单独设置，恢复跟随全局（后端以 0 表示未单独设置）
  const r = await settingsSetPlatform('downloadThreadsFor', platform, v)
  done(r.ok ? '已保存 ✓' : `失败：${r.error}`)
}

async function onTheme(modeIdx: 0 | 1 | 2) {
  const mode = (['system', 'light', 'dark'] as const)[modeIdx]
  setThemeMode(mode)
  await save('darkMode', modeIdx)
}

// ---- 主题取色器（自定义种子色：themeColorMode=1 + themeSeedColor 十进制）----

// 预置主题色：照原版 ThemeScreen presetColors（ARGB long 去掉 alpha 位 → #rrggbb，如 0xFF415F91 → #415F91）
const PRESET_COLORS = [
  { name: '蓝色', hex: '#415F91' },
  { name: '靛蓝', hex: '#3F51B5' },
  { name: '紫色', hex: '#6750A4' },
  { name: '玫红', hex: '#C2185B' },
  { name: '红色', hex: '#B3261E' },
  { name: '橙色', hex: '#F4631C' },
  { name: '金黄', hex: '#F9A825' },
  { name: '绿色', hex: '#38761D' },
  { name: '青色', hex: '#00897B' },
  { name: '天蓝', hex: '#0288D1' },
]
const pickedSeed = ref<string>(cachedSeedColor() ?? '#415F91')

async function pickSeed(hex: string) {
  pickedSeed.value = hex
  applySeedColor(hex) // 立即生效；深浅色共用同一份覆盖（简化处理）
  if (!s.value) return
  s.value.themeColorMode = 1
  s.value.themeSeedColor = parseInt(hex.slice(1), 16)
  await save('themeColorMode', 1)
  await save('themeSeedColor', s.value.themeSeedColor)
}

function onColorInput(e: Event) {
  const hex = (e.target as HTMLInputElement).value
  if (/^#[0-9a-fA-F]{6}$/.test(hex)) pickSeed(hex)
}

// ---- 实验性功能（对齐原版 ExperimentalFeaturesScreen；键名 = SettingsRepository 属性名）----
// 数值项按原版「合理范围」钳制：读缓冲 16–1024 KB、慢连接阈值 4–1024 KB/s、慢连接时长 5–120 秒。
// 核心存原始单位：downloadBufferSize 字节 / slowPreemptMinBps 字节每秒 / slowPreemptMinAgeMs 毫秒。
const BUFFER_KB = { min: 16, max: 1024, def: 64 }
const PREEMPT_KBPS = { min: 4, max: 1024, def: 12 }
const PREEMPT_SEC = { min: 5, max: 120, def: 15 }

const expTuning = ref({ bufferKb: BUFFER_KB.def, preemptKbps: PREEMPT_KBPS.def, preemptSec: PREEMPT_SEC.def })
const preemptEnabled = ref(true) // 慢连接抢占开关（核心默认开）

function clampInt(v: number, lo: number, hi: number, fallback: number): number {
  const n = Math.floor(Number(v))
  return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : fallback
}

async function saveBufferKb() {
  expTuning.value.bufferKb = clampInt(expTuning.value.bufferKb, BUFFER_KB.min, BUFFER_KB.max, BUFFER_KB.def)
  await save('downloadBufferSize', expTuning.value.bufferKb * 1024)
}

async function savePreemptKbps() {
  expTuning.value.preemptKbps =
    clampInt(expTuning.value.preemptKbps, PREEMPT_KBPS.min, PREEMPT_KBPS.max, PREEMPT_KBPS.def)
  await save('slowPreemptMinBps', expTuning.value.preemptKbps * 1024)
}

async function savePreemptSec() {
  expTuning.value.preemptSec = clampInt(expTuning.value.preemptSec, PREEMPT_SEC.min, PREEMPT_SEC.max, PREEMPT_SEC.def)
  await save('slowPreemptMinAgeMs', expTuning.value.preemptSec * 1000)
}

async function togglePreempt() {
  preemptEnabled.value = !preemptEnabled.value
  await save('slowPreemptEnabled', preemptEnabled.value)
}

// 多账号下载（C1）/ 单文件多源分片（C2，依赖 C1）：开启前二次风险确认（文案照原版 MultiAccountRisk 枚举原文）
type RiskKind = 'multiAccount' | 'multiSource'
const RISK_TEXT: Record<RiskKind, { title: string; message: string }> = {
  multiAccount: {
    title: '开启「多账号下载」？',
    message: '请只登录你自己的账号。同一网络下频繁切换多个账号取链，部分网盘（尤其百度、夸克）' +
      '可能触发风控：出现验证码、临时限流，甚至短期限制账号下载。功能仍在实验阶段，' +
      '遇到异常可随时关闭。',
  },
  multiSource: {
    title: '开启「单文件多源分片」？',
    message: '同一个文件会被多个账号同时拉取，比多账号下载更容易触发风控；可能出现分片失败率上升、' +
      '下载变慢或账号被临时限制。目前只支持夸克 / UC / 迅雷。功能仍在实验阶段，' +
      '请自行评估风险。',
  },
}
const pendingRisk = ref<RiskKind | null>(null)
const pendingRiskText = computed(() => (pendingRisk.value ? RISK_TEXT[pendingRisk.value] : null))
// C2 读取口径 = C1 且 C2 都开（对齐原版 multiSourceActive()，避免「C1 关着、C2 还显示开启」的错觉）
const multiSourceActive = computed(() => !!s.value?.multiAccountDownload && !!s.value?.multiSourceDownload)

async function onMultiAccountToggle() {
  if (!s.value) return
  if (s.value.multiAccountDownload) {
    // 关闭 C1 直接生效（无需确认），且按原版口径连带关掉 C2（C2 依赖 C1）
    s.value.multiAccountDownload = false
    const c2WasOn = s.value.multiSourceDownload
    s.value.multiSourceDownload = false
    await save('multiAccountDownload', false)
    if (c2WasOn) {
      await save('multiSourceDownload', false)
      done('已关闭多账号下载，单文件多源分片同步关闭')
    }
  } else {
    pendingRisk.value = 'multiAccount' // 开启前二次确认；取消则什么都不写
  }
}

function onMultiSourceToggle() {
  if (!s.value || !s.value.multiAccountDownload) return // C1 未开时 C2 不可操作
  if (s.value.multiSourceDownload) {
    s.value.multiSourceDownload = false // 关闭不需要确认
    save('multiSourceDownload', false)
  } else {
    pendingRisk.value = 'multiSource'
  }
}

async function confirmRisk() {
  const kind = pendingRisk.value
  pendingRisk.value = null
  if (!kind || !s.value) return
  if (kind === 'multiAccount') {
    s.value.multiAccountDownload = true
    await save('multiAccountDownload', true)
  } else {
    s.value.multiSourceDownload = true
    await save('multiSourceDownload', true)
  }
}

// ---- GitHub 下载加速（自定义镜像前缀 + 触发测速）----

const mirrorPrefix = ref('')

async function saveMirror() {
  await save('githubMirrorPrefix', mirrorPrefix.value.trim())
}

async function doMirrorRefresh() {
  try {
    await githubMirrorRefresh()
    done('镜像测速已触发，稍后自动选最快节点 ✓')
  } catch (e) {
    done(`测速失败：${e}`)
  }
}

// ---- 下载引擎（普通下载：aria2-next + 内置 FTP；对位原版 DownloadEngineScreen，替代 Gopeed 占位）----

const aria2 = ref<Aria2StatusInfo | null>(null)
const aria2Loading = ref(false)
const aria2Path = ref('')
const aria2Saving = ref(false)
const aria2Flash = ref('')

async function loadAria2() {
  aria2Loading.value = true
  try {
    aria2.value = await aria2Status()
  } catch (e) {
    aria2.value = { ok: false, error: String(e) }
  } finally {
    aria2Loading.value = false
  }
}

async function loadAria2Path() {
  try { aria2Path.value = (await kvGet(ARIA2_PATH_KEY)) ?? '' } catch { /* 读不到按空处理 */ }
}

/** 保存 aria2-next 路径到核心 kv（settings.aria2Path；后端优先级 kv > env > PATH > tools 目录）。
 *  Aria2Status 每次现查 FindBinary，保存后立即重检反映新路径。 */
async function saveAria2Path() {
  if (aria2Saving.value) return
  aria2Saving.value = true
  try {
    const ok = await kvPut(ARIA2_PATH_KEY, aria2Path.value.trim())
    aria2Flash.value = ok ? '已保存 ✓（按 kv > 环境变量 > PATH > 工具目录 生效）' : '写入失败'
    if (ok) await loadAria2()
  } catch (e) {
    aria2Flash.value = `失败：${e}`
  } finally {
    aria2Saving.value = false
    setTimeout(() => (aria2Flash.value = ''), 4000)
  }
}

// 状态区三态：运行中（绿点+版本+端口）/ 已找到未运行（灰点，首个任务自动拉起）/ 未安装（红点+引导）
const aria2DotClass = computed(() => {
  const st = aria2.value
  if (!st || !st.ok || st.found === false) return 'off'
  return st.running ? 'on' : 'idle'
})

const aria2StateText = computed(() => {
  const st = aria2.value
  if (!st && aria2Loading.value) return '检测引擎状态…'
  if (!st || !st.ok) return '引擎状态未知'
  if (st.found === false) return '未安装 aria2-next'
  if (st.running) return 'aria2-next 运行中'
  return '已找到 aria2-next（未运行）'
})

const aria2Sub = computed(() => {
  const st = aria2.value
  if (!st || !st.ok || st.found === false) return ''
  if (st.running) {
    const parts: string[] = []
    if (st.version) parts.push(`版本 ${st.version.startsWith('v') ? st.version : `v${st.version}`}`)
    if (st.port) parts.push(`RPC 端口 ${st.port}`)
    if (st.binPath) parts.push(st.binPath)
    return parts.join(' · ')
  }
  return st.binPath
    ? `${st.binPath} · 提交首个磁力 / BT 任务时自动启动`
    : '提交首个磁力 / BT 任务时自动启动'
})

// ---- 忽略版本（kv：misc.ignored_version，更新检查核心侧据此跳过该版本）----

async function ignoreVersion() {
  if (!updateInfo.value?.latest) return
  const r = await kvPut('misc.ignored_version', updateInfo.value.latest)
  done(r ? `已忽略 ${updateInfo.value.latest}，之后检查更新不再提示 ✓` : '写入失败')
}

async function unignoreVersion() {
  const r = await kvPut('misc.ignored_version', '')
  done(r ? '已取消忽略 ✓' : '写入失败')
}

// ---- 储存目录（对齐原版 StorageDirectoryScreen 四项；目录改动重启应用后生效）----
// 说明：目录「配置值」与「当前生效值」可能不同（改完还没重启）——分别展示，不合并成一句。

const cacheDirInput = ref('')
const dbTargetDir = ref('')

// 数据库目录：配置值（空 = 默认数据根目录）与生效值（AppContext 启动时解析一次）
const dbConfigured = computed(() => s.value?.storageDbDir || s.value?.storageDataDir || '')
const dbEffective = computed(() => s.value?.storageDbDirEffective || dbConfigured.value)
const dbPendingRestart = computed(() =>
  !!s.value?.storageDataDir && dbConfigured.value !== dbEffective.value)
// 缓存目录同理（默认 = <数据目录>/cache）
const cacheDefault = computed(() =>
  s.value?.storageDataDir ? `${s.value.storageDataDir}/cache` : '')
const cacheConfigured = computed(() => s.value?.storageCacheDir || cacheDefault.value)
const cacheEffective = computed(() => s.value?.storageCacheDirEffective || cacheConfigured.value)
const cachePendingRestart = computed(() =>
  !!s.value?.storageDataDir && cacheConfigured.value !== cacheEffective.value)

async function saveCacheDir() {
  await save('storageCacheDir', cacheDirInput.value.trim())
}

/** 数据库搬迁（拷贝 yunx.db/-wal/-shm + credential.key → 记待清理 → 写配置；失败不切配置） */
const migrateConfirm = ref<{ source: string; target: string } | null>(null)
const migrateBusy = ref(false)
const migrateResult = ref('')

function askMigrate() {
  const target = dbTargetDir.value.trim()
  if (!target) { done('请先填写新目录'); return }
  migrateConfirm.value = { source: dbEffective.value, target }
}

/** 绑定本机的加密档（换机器后凭证解不开；原版在「数据库不在用户目录」时提示便携版风险） */
const encMachineBound = computed(() =>
  ['normal', 'strict'].includes(s.value?.dbEncryptionEffectiveLevel ?? 'normal'))
const dbPortableWarn = computed(() =>
  encMachineBound.value && !!s.value?.storageDataDir && dbConfigured.value !== s.value.storageDataDir)

async function confirmMigrate() {
  const req = migrateConfirm.value
  migrateConfirm.value = null
  if (!req) return
  migrateBusy.value = true
  try {
    const r = await storageMigrateDb(req.target)
    if (!r.ok) {
      done(`搬迁失败：${r.error}`)
    } else if (r.migrated) {
      migrateResult.value =
        `已搬迁 ${r.copiedFiles} 个文件（${fmtBytes(r.copiedBytes ?? 0)}）` +
        (r.skippedCount ? `，${r.skippedCount} 个文件被占用已跳过` : '') +
        ` → ${r.target}\n重启应用后生效；旧位置的数据库文件会在下次启动时删除。`
    } else if (r.reason === 'target-is-effective') {
      done('已切回当前生效目录（无需搬迁）')
    } else {
      done('已经是该设置')
    }
    await load()
  } catch (e) {
    done(`搬迁失败：${e}`)
  } finally {
    migrateBusy.value = false
  }
}

// ---- 数据（数据目录展示 + 原版数据一次性导入；Go 宿主 LegacyImport*，纯文件操作不经核心）----

interface LegacyStatus { available: boolean; asked: boolean; sourceDir: string; targetDir: string }
const legacyStatus = ref<LegacyStatus | null>(null)
const legacyBusy = ref(false)
const legacyFlash = ref('')
const legacyConfirm = ref<LegacyStatus | null>(null)

/** 按钮旁的状态小字：可用 / 已决定（导入或跳过）/ 无源或目标已有数据 */
const legacyNote = computed(() => {
  const st = legacyStatus.value
  if (!st) return '探测原版数据中…'
  if (st.available) return `检测到原版数据（${st.sourceDir}），可导入`
  if (st.asked) return '已完成导入或已选择跳过，不再重复导入'
  return '未检测到可导入的原版数据（原版未使用过，或本应用数据目录已有数据）'
})

async function loadLegacy() {
  try {
    legacyStatus.value = JSON.parse(await CoreService.LegacyImportStatus())
  } catch { legacyStatus.value = null }
}

function askLegacyImport() {
  if (!legacyStatus.value?.available || legacyBusy.value) return
  legacyConfirm.value = legacyStatus.value
}

/** 确认后执行导入：本会话核心可能已初始化（仍打开旧数据）→ 提示重启后生效 */
async function confirmLegacyImport() {
  const st = legacyConfirm.value
  legacyConfirm.value = null
  if (!st) return
  legacyBusy.value = true
  try {
    const r = JSON.parse(await CoreService.LegacyImportDecide(true)) as { ok: boolean; error?: string }
    legacyFlash.value = r.ok ? '已导入，重启应用后生效 ✓' : `导入失败：${r.error ?? '未知错误'}（可重试，覆盖同名文件续拷）`
  } catch (e) {
    legacyFlash.value = `导入失败：${e}`
  } finally {
    legacyBusy.value = false
    loadLegacy() // 决定标记已落盘 → 按钮转禁用并更新状态说明
  }
}

// ---- 下载调试开关（对齐原版 SettingsScreen：关闭会清空全部日志 → 先二次确认，取消回弹）----
// 确认关闭时连带关掉「下载完整文件校验（SHA-256 全量）」：摘要只写调试日志，与原版联动一致。
const debugOffConfirm = ref(false)

function onDownloadDebugToggle() {
  if (!s.value) return
  if (s.value.downloadDebug) {
    debugOffConfirm.value = true // 不直接改状态；取消 = 开关回弹
  } else {
    s.value.downloadDebug = true
    save('downloadDebug', true)
  }
}

async function confirmDebugOff() {
  debugOffConfirm.value = false
  if (!s.value) return
  s.value.downloadDebug = false
  await save('downloadDebug', false) // 核心侧「关闭即清空全部调试日志」
  if (s.value.fullFileSha256) {
    s.value.fullFileSha256 = false
    await save('fullFileSha256', false)
    done('下载调试已关闭，日志已清空；全量 SHA-256 已一并关闭')
  } else {
    done('下载调试已关闭，日志已清空')
  }
}

// ---- 数据库加密等级（切档 = 重新包裹 credential.key，不碰库内数据、立即生效无需重启）----
// 档位名/说明/后果提示照抄原版 DbEncryptionLevel 与 StorageDirectoryScreen 文案；
// UI 展示**实际**档位（密钥文件里的封装格式），非偏好值。
// 「较为严格」需 Windows DPAPI（当前宿主未提供）、「严格」需 SQLCipher（原版亦未支持）——如实标注并禁用。

const ENC_LEVELS = [
  { id: 'loose', label: '宽松', disabled: false, disabledNote: '',
    desc: '库明文 · 凭证列加密 · 密钥明文存放。换机器 / 便携版必须用这档',
    warning: '密钥将以明文存放在 credential.key 中：任何能读到该文件的人都能解开网盘凭证。只在需要用便携版 / 换机器时选择。' },
  { id: 'normal', label: '普通（默认）', disabled: false, disabledNote: '',
    desc: '库明文 · 凭证列加密 · 密钥用本机派生密钥包裹（绑定本机，换机器解不开）',
    warning: '密钥将用本机派生密钥包裹：绑定当前这台机器。\n之后把数据库/密钥拷到别的电脑、或重装系统，凭证将无法解密（表现为网盘账号全部未登录，需重新登录）。\n如果需要便携使用，请保持「宽松」。' },
  { id: 'strict', label: '较为严格', disabled: true, disabledNote: '需 Windows DPAPI，当前宿主暂未提供',
    desc: '库明文 · 凭证列加密 · 密钥用 Windows DPAPI 保护（绑定当前 Windows 用户）',
    warning: '' },
  { id: 'strictest', label: '严格', disabled: true, disabledNote: 'SQLCipher 暂未支持（原版同）',
    desc: '库整体加密（SQLCipher）—— 暂未支持',
    warning: '' },
]
const effectiveLevel = computed(() =>
  s.value?.dbEncryptionEffectiveLevel ?? s.value?.dbEncryptionLevel ?? 'normal')
const pendingLevel = ref<typeof ENC_LEVELS[number] | null>(null)

function onPickLevel(lv: typeof ENC_LEVELS[number]) {
  if (lv.disabled) { done(`「${lv.label}」${lv.disabledNote}`); return }
  if (lv.id !== effectiveLevel.value) pendingLevel.value = lv
}

async function confirmLevel() {
  const lv = pendingLevel.value
  pendingLevel.value = null
  if (!lv) return
  const r = await storageApplyEncryption(lv.id)
  if (r.ok) {
    done(`加密等级已切换为「${lv.label}」（立即生效）`)
    await load()
  } else {
    done(`切换失败，仍保持原档位：${r.error}`)
  }
}

async function load() {
  try {
    s.value = await settingsList()
    // 自定义主题色回显：设置优先，读不到时沿用 localStorage 缓存
    if (s.value && s.value.themeColorMode === 1 && s.value.themeSeedColor > 0) {
      pickedSeed.value = seedNumToHex(s.value.themeSeedColor)
    }
    // 实验性调优项回显：settings_list 暂未回传这四项，读不到时按核心默认值展示
    const ex = (s.value ?? {}) as unknown as Record<string, unknown>
    expTuning.value.bufferKb = clampInt(Math.round(Number(ex.downloadBufferSize ?? 65536) / 1024),
      BUFFER_KB.min, BUFFER_KB.max, BUFFER_KB.def)
    preemptEnabled.value = typeof ex.slowPreemptEnabled === 'boolean' ? ex.slowPreemptEnabled : true
    expTuning.value.preemptKbps = clampInt(Math.round(Number(ex.slowPreemptMinBps ?? 12288) / 1024),
      PREEMPT_KBPS.min, PREEMPT_KBPS.max, PREEMPT_KBPS.def)
    expTuning.value.preemptSec = clampInt(Math.round(Number(ex.slowPreemptMinAgeMs ?? 15000) / 1000),
      PREEMPT_SEC.min, PREEMPT_SEC.max, PREEMPT_SEC.def)
    mirrorPrefix.value = s.value?.githubMirrorPrefix ?? ''
    cacheDirInput.value = s.value?.storageCacheDir ?? ''
  } catch (e) {
    loadError.value = String(e)
  }
  try {
    const info = JSON.parse(await CoreService.HostStatus())
    sysHost.value = info.systemProxyHost ?? ''
    sysPort.value = info.systemProxyPort ?? 0
    ffmpegPath.value = info.ffmpegPath ?? ''
  } catch { /* 宿主状态不可用时保持默认 */ }
}

async function saveProxy() {
  if (!s.value) return
  try {
    const r = JSON.parse(await CoreService.SetProxy(s.value.proxyMode, s.value.proxyHost, s.value.proxyPort))
    hostFlash.value = r.ok ? '代理已注入核心并写入设置 ✓' : `失败：${r.error ?? '未知'}`
  } catch (e) { hostFlash.value = `失败：${e}` }
  setTimeout(() => (hostFlash.value = ''), 2500)
}

async function saveFfmpeg(auto = false) {
  try {
    const path = auto ? '' : ffmpegPath.value.trim()
    const r = JSON.parse(await CoreService.SetFfmpeg(path))
    hostFlash.value = r.ok
      ? (r.applied ? `ffmpeg 已注入：${r.applied}` : r.cleared ? '已清除注入（核心内置探测）' : '已保存 ✓')
      : `失败：${r.error ?? '未知'}`
    if (auto) ffmpegPath.value = ''
  } catch (e) { hostFlash.value = `失败：${e}` }
  setTimeout(() => (hostFlash.value = ''), 2500)
}

onMounted(() => {
  load()
  loadLegacy() // 原版数据探测（纯 Go 文件检查，不影响设置加载）
  loadAria2() // 普通下载引擎状态（Aria2Status；失败不影响设置加载）
  loadAria2Path() // aria2-next 路径回显（kv settings.aria2Path）
})
</script>

<template>
  <div class="page">
    <div v-if="loadError" class="banner error">{{ loadError }}</div>

    <div v-if="s" class="card">
      <h3>外观（与原版互通）<span class="chip plain" style="margin-left: 8px">{{ flash }}</span></h3>
      <p class="hint">主题三态写 yunx/settings 的 dark_mode，Compose 原版改这里我们同步生效，反之亦然</p>
      <div class="row">
        <div class="seg">
          <button :class="{ active: s.darkMode === 0 }" @click="onTheme(0)">跟随系统</button>
          <button :class="{ active: s.darkMode === 1 }" @click="onTheme(1)">浅色</button>
          <button :class="{ active: s.darkMode === 2 }" @click="onTheme(2)">深色</button>
        </div>
      </div>
      <div class="row" style="margin-top: 14px">
        <span style="flex: 1">减少动画</span>
        <i class="switch" :class="{ on: s.reduceMotion }"
           @click="s.reduceMotion = !s.reduceMotion; save('reduceMotion', s.reduceMotion)" />
      </div>
      <div class="row" style="margin-top: 14px; gap: 10px">
        <span style="font-size: 13px; color: var(--on-bg-variant)">主题色</span>
        <button v-for="c in PRESET_COLORS" :key="c.hex" class="swatch" :class="{ picked: pickedSeed === c.hex }"
                :style="{ background: c.hex }" :title="`${c.name} ${c.hex}`" @click="pickSeed(c.hex)" />
        <input type="color" class="colorpick" :value="pickedSeed" title="自定义主题色"
               @input="onColorInput" />
      </div>
      <p class="hint" style="margin: 8px 0 0">选择即生效并写入 theme_seed_color（自定义主题色，深浅色共用）</p>
    </div>

    <div v-if="s" class="card">
      <h3>下载（正式设置）</h3>
      <div class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 1; min-width: 150px">
          <label>单任务线程数（1-512）</label>
          <input class="input" type="number" min="1" max="512" v-model.number="s.downloadThreads"
                 @change="save('downloadThreads', s.downloadThreads)" />
        </div>
        <div class="field" style="flex: 1; min-width: 150px">
          <label>最大并发任务（1-10）</label>
          <input class="input" type="number" min="1" max="10" v-model.number="s.maxConcurrentDownloads"
                 @change="save('maxConcurrentDownloads', s.maxConcurrentDownloads)" />
        </div>
        <div class="field" style="flex: 1; min-width: 150px">
          <label>失败重试（0-10）</label>
          <input class="input" type="number" min="0" max="10" v-model.number="s.downloadRetryCount"
                 @change="save('downloadRetryCount', s.downloadRetryCount)" />
        </div>
      </div>
      <div class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 1; min-width: 150px">
          <label>全局限速（字节/秒，0 = 不限）</label>
          <input class="input" type="number" min="0" v-model.number="s.downloadSpeedLimit"
                 @change="save('downloadSpeedLimit', s.downloadSpeedLimit)" />
        </div>
        <span style="flex: 1"></span>
      </div>
      <div class="row">
        <span style="flex: 1">下载期间阻止系统休眠</span>
        <i class="switch" :class="{ on: s.keepAwakeWhileDownloading }"
           @click="s.keepAwakeWhileDownloading = !s.keepAwakeWhileDownloading; save('keepAwakeWhileDownloading', s.keepAwakeWhileDownloading)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">剪贴板分享链接检测</span>
        <i class="switch" :class="{ on: s.clipboardLinkDetection }"
           @click="s.clipboardLinkDetection = !s.clipboardLinkDetection; save('clipboardLinkDetection', s.clipboardLinkDetection)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">免转存下载（夸克分享直下，不经网盘中转）</span>
        <i class="switch" :class="{ on: s.quarkNoSaveDownload }"
           @click="s.quarkNoSaveDownload = !s.quarkNoSaveDownload; save('quarkNoSaveDownload', s.quarkNoSaveDownload)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">通知栏显示下载进度（速度 / 百分比）</span>
        <i class="switch" :class="{ on: s.notificationShowSpeed }"
           @click="s.notificationShowSpeed = !s.notificationShowSpeed; save('notificationShowSpeed', s.notificationShowSpeed)" />
      </div>
      <!-- 按平台线程数：折叠面板，10 平台各一档（key 固定 downloadThreadsFor，platform 区分） -->
      <div class="row" style="margin-top: 14px; justify-content: space-between; cursor: pointer"
           @click="perThreadOpen = !perThreadOpen">
        <span style="font-weight: 600">按平台设置线程数</span>
        <span class="chip plain">{{ perThreadOpen ? '收起' : '展开' }}</span>
      </div>
      <div v-if="perThreadOpen" class="perthreads">
        <div v-for="p in THREAD_PLATFORMS" :key="p.key" class="field">
          <label>{{ p.name }}</label>
          <input class="input" type="number" min="0" max="512" v-model.number="perThreads[p.key]"
                 placeholder="0 = 跟随全局" @change="savePlatformThreads(p.key)" />
        </div>
      </div>
    </div>

    <div v-if="s" class="card">
      <h3>储存目录（与原版互通）</h3>
      <p class="hint">目录改动需重启应用生效（核心启动时只解析一次）；切换数据库位置时会把旧位置的数据自动搬到新位置（旧文件留到下次启动再清理）</p>

      <!-- ① 下载保存目录 -->
      <div class="field" style="margin-bottom: 12px">
        <label>下载保存目录（下载完成后文件的落盘位置，空 = 系统「下载」文件夹）</label>
        <input class="input" v-model="s.downloadDirUri" placeholder="/home/xxx/Downloads"
               @change="save('downloadDirUri', s.downloadDirUri ?? '')" />
      </div>

      <!-- ② 下载缓存目录 -->
      <div class="field" style="margin-bottom: 6px">
        <label>下载缓存目录（下载分片、合并临时文件；空 = 默认 {{ cacheDefault || '…' }}）</label>
        <input class="input" v-model="cacheDirInput" :placeholder="cacheDefault"
               @change="saveCacheDir" />
      </div>
      <div class="pathrow">
        <template v-if="cachePendingRestart">
          <span class="pathlabel">重启后生效</span>
          <code class="codepath">{{ cacheConfigured }}</code>
          <span class="pathlabel" style="margin-top: 4px">当前生效（重启前）</span>
          <code class="codepath">{{ cacheEffective }}</code>
        </template>
        <template v-else>
          <span class="pathlabel">当前生效</span>
          <code class="codepath">{{ cacheConfigured }}</code>
        </template>
      </div>
      <p class="hint" style="margin: 6px 0 0">
        仅写入配置、不自动搬移旧缓存：已下载的分片留在原目录，未完成任务下次开始时从头下载
      </p>

      <!-- ③ 数据库位置 -->
      <div class="field" style="margin: 14px 0 6px">
        <label>数据库位置（任务、收藏、历史与网盘账号凭证：yunx.db + credential.key，两者必须同处一地）</label>
        <div class="row" style="flex-wrap: nowrap">
          <input class="input" style="flex: 1" v-model="dbTargetDir"
                 :placeholder="dbConfigured" @keydown.enter="askMigrate" />
          <button class="btn tonal small" :disabled="migrateBusy" @click="askMigrate">
            {{ migrateBusy ? '搬迁中…' : '搬迁…' }}
          </button>
        </div>
      </div>
      <div class="pathrow">
        <template v-if="dbPendingRestart">
          <span class="pathlabel">重启后生效</span>
          <code class="codepath">{{ dbConfigured }}</code>
          <span class="pathlabel" style="margin-top: 4px">当前生效（重启前）</span>
          <code class="codepath">{{ dbEffective }}</code>
        </template>
        <template v-else>
          <span class="pathlabel">当前生效</span>
          <code class="codepath">{{ dbConfigured }}</code>
        </template>
      </div>
      <p class="hint" style="margin: 6px 0 0">
        搬迁时连同 credential.key 一起搬走 —— 库里的凭证是用它加密的，分开会全部解不开；要搬回默认目录就填 {{ s.storageDataDir }}
      </p>
      <p class="hint" v-if="dbPortableWarn" style="margin: 6px 0 0">
        ⚠ 数据库已不在用户目录，而当前加密等级绑定本机：若要把数据当作便携版带到别的电脑使用，请把加密等级切回「宽松」
      </p>

      <!-- ④ 数据库加密等级 -->
      <div class="row" style="margin: 14px 0 8px; font-weight: 600">数据库加密等级</div>
      <div v-for="lv in ENC_LEVELS" :key="lv.id" class="optrow"
           :class="{ picked: effectiveLevel === lv.id, disabled: lv.disabled }"
           @click="onPickLevel(lv)">
        <i class="radio" :class="{ on: effectiveLevel === lv.id }" />
        <div style="flex: 1; min-width: 0">
          <div>{{ lv.label }}<span v-if="lv.disabled" class="dim">（{{ lv.disabledNote }}）</span></div>
          <div class="dim">{{ lv.desc }}</div>
        </div>
      </div>
      <p class="hint" style="margin: 10px 0 0">
        三档都只保护密钥：库内账号凭证始终是 AES-256-GCM 密文，但任务文件名 / URL / 保存路径等元数据在三档下都是明文（整库加密需要 SQLCipher，暂未支持）。切档只重新包裹密钥，立即生效、无需重启
      </p>
    </div>

    <!-- 数据：数据目录展示 + 原版数据一次性导入（决定标记落盘后按钮禁用并说明状态） -->
    <div class="card">
      <h3>数据</h3>
      <p class="hint">本应用数据目录独立于原版 YunX-Desktop（两者可同时使用、互不影响）；目录跟随用户主目录，不可更改</p>
      <div class="pathrow">
        <span class="pathlabel">当前数据目录</span>
        <code class="codepath">{{ legacyStatus?.targetDir ?? '读取中…' }}</code>
      </div>
      <div class="row" style="margin-top: 12px; justify-content: space-between; gap: 10px">
        <span class="dim" style="font-size: 12.5px; min-width: 0">{{ legacyNote }}</span>
        <button class="btn tonal small" style="flex: none" :disabled="!legacyStatus?.available || legacyBusy"
                @click="askLegacyImport">{{ legacyBusy ? '导入中…' : '导入原版数据' }}</button>
      </div>
      <p v-if="legacyFlash" class="hint" style="margin: 8px 0 0">{{ legacyFlash }}</p>
      <p class="hint" style="margin: 8px 0 0">导入内容：账号、下载记录、书签、解析历史、设置、密钥；导入不会修改原版数据。本会话若已初始化核心，导入后需重启应用才能读到导入的数据</p>
    </div>

    <div v-if="s" class="card">
      <h3>网络</h3>
      <p class="hint">代理：探测在 Go 宿主，注入核心即时生效；三模式与原版一致，选择写入正式设置（HTTP/2 已移至「实验性功能」）</p>
      <div class="row" style="margin-bottom: 12px">
        <div class="seg">
          <button :class="{ active: s.proxyMode === 'direct' }" @click="s.proxyMode = 'direct'">直连</button>
          <button :class="{ active: s.proxyMode === 'system' }" @click="s.proxyMode = 'system'">跟随系统</button>
          <button :class="{ active: s.proxyMode === 'manual' }" @click="s.proxyMode = 'manual'">手动</button>
        </div>
        <span v-if="sysHost" class="chip plain">系统探测：{{ sysHost }}:{{ sysPort }}</span>
        <span class="chip plain">{{ hostFlash }}</span>
      </div>
      <div v-if="s.proxyMode === 'manual'" class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 2; min-width: 160px">
          <label>代理服务器</label>
          <input class="input" v-model="s.proxyHost" placeholder="127.0.0.1" />
        </div>
        <div class="field" style="flex: 1; min-width: 90px">
          <label>端口</label>
          <input class="input" type="number" v-model.number="s.proxyPort" placeholder="7890" />
        </div>
      </div>
      <div class="row end"><button class="btn tonal small" @click="saveProxy">应用代理</button></div>
    </div>

    <div class="card">
      <h3>外部 ffmpeg（DASH 合轨）</h3>
      <p class="hint">路径由 Go 宿主注入核心；留空 = 自动探测 PATH</p>
      <div class="row" style="margin-bottom: 10px">
        <div class="field" style="flex: 1">
          <label>ffmpeg 可执行文件路径</label>
          <input class="input" v-model="ffmpegPath" placeholder="/usr/bin/ffmpeg" />
        </div>
      </div>
      <div class="row" style="justify-content: space-between">
        <span class="chip plain" v-if="sysHost && false"></span>
        <span class="chip plain">{{ hostFlash }}</span>
      </div>
      <div class="row end" style="margin-top: 10px">
        <button class="btn text small" @click="saveFfmpeg(true)">自动探测并注入</button>
        <button class="btn tonal small" @click="saveFfmpeg(false)">应用并保存</button>
      </div>
    </div>

    <div class="card">
      <h3>认证备份（与原版同格式，可跨设备恢复）</h3>
      <p class="hint">备份 10 网盘 + 视频解析平台 + GitHub Token 的凭证；口令 ≥8 位导出加密 .yunx，留空导出明文 .json</p>
      <div class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 1; min-width: 200px">
          <label>备份口令（导入 .yunx 时也填这里）</label>
          <input class="input" type="password" v-model="backupPwd" placeholder="至少 8 位，留空 = 明文 JSON" />
        </div>
      </div>
      <div class="row" style="justify-content: space-between">
        <span class="chip plain">{{ backupFlash }}</span>
        <span class="row">
          <button class="btn text small" @click="doImport">导入备份…</button>
          <button class="btn tonal small" @click="doExport">导出备份…</button>
        </span>
      </div>
    </div>

    <div class="card">
      <h3>更新</h3>
      <p class="hint">从 GitHub Release 检查最新版本（经镜像加速）</p>
      <div class="row" style="justify-content: space-between">
        <span class="chip plain">{{ updateFlash }}</span>
        <button class="btn tonal small" :disabled="updateBusy" @click="doUpdateCheck">
          {{ updateBusy ? '检查中…' : '检查更新' }}
        </button>
      </div>
      <div v-if="updateInfo" class="banner" style="margin-top: 10px">
        当前 {{ updateInfo.current }} · 最新 {{ updateInfo.latest }}
        <template v-if="updateInfo.hasUpdate">
          —— <a :href="updateInfo.url" target="_blank" style="color: var(--primary)">去下载</a>
        </template>
        <template v-else>（已是最新）</template>
      </div>
      <div v-if="s" class="row" style="margin-top: 12px">
        <span style="flex: 1">接受预发布版（beta / preview 也提示更新）</span>
        <i class="switch" :class="{ on: s.acceptPrereleaseUpdate }"
           @click="s.acceptPrereleaseUpdate = !s.acceptPrereleaseUpdate; save('acceptPrereleaseUpdate', s.acceptPrereleaseUpdate)" />
      </div>
      <div v-if="updateInfo" class="row end" style="margin-top: 10px">
        <button class="btn text small" @click="unignoreVersion">取消忽略</button>
        <button class="btn text small" @click="ignoreVersion">忽略此版本（{{ updateInfo.latest }}）</button>
      </div>
    </div>

    <div class="card">
      <h3>GitHub 下载加速</h3>
      <p class="hint">Release 附件直链可经自定义前缀加速（如镜像站反代前缀，留空 = 直连 GitHub）；「测速」触发核心对各公共镜像测速</p>
      <div class="row" style="margin-bottom: 10px">
        <div class="field" style="flex: 1; min-width: 220px">
          <label>下载前缀（拼接在 github.com 链接前，留空 = 直连）</label>
          <input v-model="mirrorPrefix" class="input" placeholder="https://gh-proxy.com/"
                 @keydown.enter="saveMirror" @change="saveMirror" />
        </div>
        <button class="btn tonal small" style="align-self: flex-end" @click="doMirrorRefresh">测速</button>
      </div>
      <p class="hint" style="margin: 0">更新检查与附件下载共用此设置，保存后立即生效</p>
    </div>

    <div v-if="s" class="card">
      <h3>实验性功能</h3>
      <p class="hint">以下参数影响下载行为，改动后对新任务生效（慢连接抢占在下一采样周期生效）；实验性开关默认关闭</p>
      <div class="row">
        <span style="flex: 1">启用 HTTP/2（默认仅 HTTP/1.1；理论上更快，实测差异通常不大）</span>
        <i class="switch" :class="{ on: s.http2Enabled }"
           @click="s.http2Enabled = !s.http2Enabled; save('http2Enabled', s.http2Enabled)" />
      </div>
      <div class="row" style="margin-top: 14px; margin-bottom: 12px">
        <div class="field" style="flex: 1; min-width: 150px">
          <label>下载读缓冲大小 KB（16-1024，默认 64）</label>
          <input class="input" type="number" min="16" max="1024" v-model.number="expTuning.bufferKb"
                 @change="saveBufferKb" />
        </div>
        <div class="field" style="flex: 1; min-width: 150px">
          <label>慢连接判定阈值 KB/s（4-1024，默认 12）</label>
          <input class="input" type="number" min="4" max="1024" v-model.number="expTuning.preemptKbps"
                 @change="savePreemptKbps" />
        </div>
        <div class="field" style="flex: 1; min-width: 150px">
          <label>慢连接判定时长 秒（5-120，默认 15）</label>
          <input class="input" type="number" min="5" max="120" v-model.number="expTuning.preemptSec"
                 @change="savePreemptSec" />
        </div>
      </div>
      <div class="row">
        <span style="flex: 1">慢连接抢占（把远低于同伴的慢分片换新连接续传，默认开）</span>
        <i class="switch" :class="{ on: preemptEnabled }" @click="togglePreempt" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">多账号下载（实验性）：批量下载按文件轮换不同账号取链，每个文件仍只用一个账号</span>
        <i class="switch" :class="{ on: s.multiAccountDownload }" @click="onMultiAccountToggle" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span class="dim" style="flex: 1">
          {{ s.multiAccountDownload
              ? '单文件多源分片（实验性）：同一文件多账号各取直链、分片轮流下载，目前只支持夸克 / UC / 迅雷'
              : '单文件多源分片（实验性）：需先开启「多账号下载」' }}
        </span>
        <i class="switch" :class="{ on: multiSourceActive, disabled: !s.multiAccountDownload }"
           @click="onMultiSourceToggle" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">下载完整文件校验（SHA-256 全量）</span>
        <i class="switch" :class="{ on: s.fullFileSha256 }"
           @click="s.fullFileSha256 = !s.fullFileSha256; save('fullFileSha256', s.fullFileSha256)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">下载调试
          <span class="dim" style="display: block; font-size: 12px; margin-top: 2px">
            开启后新任务记录调试日志；关闭会清空日志（开启时下载页任务菜单出现「调试信息」入口）
          </span>
        </span>
        <!-- 开→关先二次确认（关闭即清空全部日志，不可恢复）；取消则开关回弹 -->
        <i class="switch" :class="{ on: s.downloadDebug }" @click="onDownloadDebugToggle" />
      </div>
      <p class="hint" style="margin: 10px 0 0">
        多账号下载与单文件多源分片开启前有二次风险确认；默认值：HTTP/2 关、读缓冲 64 KB、慢连接抢占开（12 KB/s · 15 秒）
      </p>
    </div>

    <div v-if="s" class="card">
      <h3>BT / 磁力</h3>
      <p class="hint">磁力与种子下载的 peer 发现配置；保存后对新任务生效</p>
      <div class="row" style="margin-bottom: 12px">
        <span style="flex: 1">DHT 节点发现（无 tracker 时也能找 peer）</span>
        <i class="switch" :class="{ on: s.btDhtEnabled }"
           @click="s.btDhtEnabled = !s.btDhtEnabled; save('btDhtEnabled', s.btDhtEnabled)" />
      </div>
      <div class="field">
        <label>Tracker 列表（每行一个 tracker URL）</label>
        <textarea v-model="s.btTrackers" class="input trackers" rows="6"
                  placeholder="https://tracker.example.com/announce" @change="save('btTrackers', s.btTrackers)" />
      </div>
      <p class="hint" style="margin: 8px 0 0">每行一条，空行与 # 开头的注释行会被忽略</p>
    </div>

    <!-- 下载引擎（对位原版 DownloadEngineScreen，替代 Gopeed 占位）：aria2-next 状态 + 路径配置 -->
    <div class="card">
      <h3>下载引擎</h3>
      <p class="hint">磁力 / BT / ed2k / Metalink / 普通 HTTP 直链 → aria2-next；FTP/FTPS → 内置引擎；
        网盘直链 → 内置分片引擎（不受本卡配置影响）</p>

      <!-- 状态区：运行中（绿点+版本+端口）/ 已找到未运行 / 未安装（引导）/ 引擎不可用 -->
      <div class="pathrow" style="margin-bottom: 12px">
        <div class="row" style="gap: 8px">
          <span class="dot" :class="aria2DotClass" />
          <span style="font-weight: 600">{{ aria2Loading && !aria2 ? '检测引擎状态…' : aria2StateText }}</span>
          <button class="btn text small" title="重新检测" @click="loadAria2">刷新</button>
        </div>
        <div v-if="aria2Sub" class="hint" style="margin-top: 3px; word-break: break-all">{{ aria2Sub }}</div>
      </div>

      <div v-if="aria2 && !aria2.ok" class="banner error" style="margin-bottom: 12px">
        普通下载引擎不可用：{{ aria2.error ?? '未知错误' }}
      </div>
      <div v-else-if="aria2 && aria2.found === false" class="banner" style="margin-bottom: 12px">
        未找到 aria2-next 二进制：请安装到系统 PATH，或放到
        <code class="codepath">~/.local/share/yunx-desktop/tools/aria2-next</code>，
        或在下方路径框直接配置
      </div>

      <!-- 配置项：aria2-next 路径（kv settings.aria2Path，优先级 kv > 环境变量 > PATH > 工具目录） -->
      <div class="field" style="margin-bottom: 6px">
        <label>aria2-next 可执行文件路径（留空 = 自动探测；保存后立即生效，对之后提交的任务生效）</label>
        <div class="row" style="flex-wrap: nowrap">
          <input v-model="aria2Path" class="input" style="flex: 1" placeholder="/usr/bin/aria2-next"
                 @keydown.enter="saveAria2Path" />
          <button class="btn tonal small" :disabled="aria2Saving" @click="saveAria2Path">
            {{ aria2Saving ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
      <p v-if="aria2Flash" class="hint" style="margin: 6px 0 0">{{ aria2Flash }}</p>
    </div>

    <!-- 原版数据导入确认（设置页手动触发；成功后需重启才能读到导入的数据） -->
    <div v-if="legacyConfirm" class="mask" @click.self="legacyConfirm = null">
      <div class="dlg">
        <h3>导入原版数据？</h3>
        <p class="hint">
          将把原版数据<br><code class="codepath">{{ legacyConfirm.sourceDir }}</code><br>导入到<br><code class="codepath">{{ legacyConfirm.targetDir }}</code><br><br>
          导入内容：账号、下载记录、书签、解析历史、设置、密钥。建议先关闭原版应用；导入不会修改原版数据。<br><br>
          本会话核心可能已初始化（仍打开旧数据）：导入完成后需重启应用才能读到导入的数据。
        </p>
        <div class="row end">
          <button class="btn text small" :disabled="legacyBusy" @click="legacyConfirm = null">取消</button>
          <button class="btn small" :disabled="legacyBusy" @click="confirmLegacyImport">
            {{ legacyBusy ? '导入中…' : '开始导入' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 关闭「下载调试」二次确认（文案照原版 SettingsScreen：关闭会清空日志且不可恢复，全量 SHA-256 一并关） -->
    <div v-if="debugOffConfirm" class="mask" @click.self="debugOffConfirm = false">
      <div class="dlg">
        <h3>关闭下载调试？</h3>
        <p class="hint">
          关闭后会清空已记录的全部下载调试日志，且无法恢复；「下载完整文件校验（SHA-256 全量）」也会被一并关闭（下次开启下载调试后需手动重新开启）。确定要关闭吗？
        </p>
        <div class="row end">
          <button class="btn text small" @click="debugOffConfirm = false">取消</button>
          <button class="btn small danger" @click="confirmDebugOff">关闭并清空</button>
        </div>
      </div>
    </div>

    <!-- 多账号下载 / 单文件多源分片：开启前二次风险确认（.mask/.dlg 对齐 DownloadView 弹窗，文案照原版 MultiAccountRisk 原文） -->
    <div v-if="pendingRiskText" class="mask" @click.self="pendingRisk = null">
      <div class="dlg">
        <h3>{{ pendingRiskText.title }}</h3>
        <p class="hint">{{ pendingRiskText.message }}</p>
        <div class="row end">
          <button class="btn text small" @click="pendingRisk = null">取消</button>
          <button class="btn small" @click="confirmRisk">确认开启</button>
        </div>
      </div>
    </div>

    <!-- 数据库搬迁确认（文案对齐原版 StorageDirectoryScreen 搬迁弹窗） -->
    <div v-if="migrateConfirm" class="mask" @click.self="migrateConfirm = null">
      <div class="dlg">
        <h3>搬迁数据库？</h3>
        <p class="hint">
          数据库（含凭证密钥）将从<br><code class="codepath">{{ migrateConfirm.source }}</code><br>搬迁到<br><code class="codepath">{{ migrateConfirm.target }}</code><br><br>
          搬迁完成后需要重启应用才会生效。旧位置的数据库文件会在下次启动时删除。<template v-if="encMachineBound"><br><br>⚠ 当前加密等级绑定本机：若要把数据当作便携版带到别的电脑使用，请先把加密等级切回「宽松」。</template>
        </p>
        <div class="row end">
          <button class="btn text small" @click="migrateConfirm = null">取消</button>
          <button class="btn small" @click="confirmMigrate">开始搬迁</button>
        </div>
      </div>
    </div>

    <!-- 搬迁完成：提示重启（Wails 壳无法自行重启核心 isolate，手动重启即可） -->
    <div v-if="migrateResult" class="mask" @click.self="migrateResult = ''">
      <div class="dlg">
        <h3>搬迁完成，需重启生效</h3>
        <p class="hint" style="white-space: pre-line">{{ migrateResult }}</p>
        <div class="row end">
          <button class="btn small" @click="migrateResult = ''">稍后手动重启</button>
        </div>
      </div>
    </div>

    <!-- 加密等级切换确认（后果提示照原版 levelWarning 原文；普通档绑定本机必须让用户明确知道） -->
    <div v-if="pendingLevel" class="mask" @click.self="pendingLevel = null">
      <div class="dlg">
        <h3>切换为「{{ pendingLevel.label }}」？</h3>
        <p class="hint" style="white-space: pre-line">{{ pendingLevel.warning }}</p>
        <div class="row end">
          <button class="btn text small" @click="pendingLevel = null">取消</button>
          <button class="btn small" @click="confirmLevel">切换</button>
        </div>
      </div>
    </div>

    <div class="row end">
      <button class="btn text small" @click="router.push('/about')">关于 YunX</button>
    </div>
  </div>
</template>

<style scoped>
.swatch { width: 26px; height: 26px; border-radius: 50%; border: 2px solid var(--outline-variant);
  cursor: pointer; padding: 0; flex: none; }
.swatch.picked { border-color: var(--on-bg); box-shadow: 0 0 0 2px var(--surface), 0 0 0 4px var(--on-bg); }
.colorpick { width: 30px; height: 30px; padding: 2px; border: none; background: none; cursor: pointer; flex: none; }
.switch.disabled { opacity: 0.45; cursor: not-allowed; }
.dim { color: var(--on-bg-variant); }
/* 风险确认弹窗（.mask/.dlg 对齐 DownloadView / DirPickerModal 约定） */
.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.42); display: flex;
  align-items: center; justify-content: center; z-index: 40; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(420px, 92vw); border: 1px solid var(--outline-variant); }
.dlg h3 { margin: 0 0 8px; font-size: 15px; }
.dlg .hint { color: var(--on-bg-variant); font-size: 12.5px; margin: 0 0 14px; line-height: 1.7; }
.btn.danger { background: var(--error); color: #fff; }
.perthreads { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 10px; margin-top: 10px; padding-top: 12px; border-top: 1px dashed var(--outline-variant); }
.trackers { font-family: ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace; font-size: 12.5px;
  resize: vertical; line-height: 1.6; }
/* 储存目录：路径展示条（等宽、区分「当前生效 / 重启后生效」）与加密档位单选行 */
.pathrow { display: flex; flex-direction: column; gap: 2px; background: var(--surface-container);
  border-radius: 8px; padding: 8px 12px; }
/* 下载引擎状态圆点：运行中绿 / 已找到未运行灰 / 未安装或不可用红 */
.dot { width: 10px; height: 10px; border-radius: 50%; flex: none; display: inline-block; }
.dot.on { background: var(--success); box-shadow: 0 0 6px color-mix(in srgb, var(--success) 55%, transparent); }
.dot.idle { background: var(--outline); }
.dot.off { background: var(--error); }
.pathlabel { font-size: 11.5px; color: var(--on-bg-variant); }
.codepath { font-family: ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace;
  font-size: 12.5px; word-break: break-all; }
.optrow { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 10px;
  cursor: pointer; }
.optrow:hover { background: var(--surface-container); }
.optrow.picked { background: var(--surface-container-high); }
.optrow.disabled { opacity: 0.5; cursor: not-allowed; }
.radio { width: 16px; height: 16px; border-radius: 50%; border: 2px solid var(--outline);
  flex: none; position: relative; }
.radio.on { border-color: var(--primary); }
.radio.on::after { content: ''; position: absolute; inset: 3px; border-radius: 50%;
  background: var(--primary); }
</style>
