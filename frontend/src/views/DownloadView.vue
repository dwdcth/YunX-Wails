<script setup lang="ts">
// 下载页：双引擎合并任务面板（对齐原版 DownloadScreen 的核心能力面）。
// 云盘任务：download_submit / download_list 800ms 轮询（核心侧 500ms 落盘节流 + 内存实时速度）。
// 普通下载任务：NormalDownloadSubmit / NormalDownloadList 同频轮询（独立库 downloads.db；
// 磁力/BT/ed2k/Metalink/HTTP → aria2-next，FTP → Go 内置引擎）→ 两库合并一张列表，按来源路由行内操作。
// 扩展：行复选多选批量操作 / 工具栏全选（三态，只作用于当前页签+搜索的可见行）/ 全局批量栏（全部暂停/开始/删除，同时作用两个引擎）/ 按目录分组 /
// 行内 ⋯ 菜单（复制链接/重新下载/打开文件）/ ffmpeg 合轨进度（download_list stats.mergePercent 橙色条）/
// 文件夹批量任务（share_folder_download 的 batch_list 轮询）/ 下载调试弹窗（仅云盘任务；对齐原版 DownloadDebugScreen）/
// 速度详情弹窗（会话内 800ms 速度采样 → 手写 SVG 双轴曲线：速度折线+面积 / 已下载阶梯线）。
// 行副行速度口径对齐迅雷：下载中=实时速度、排队/暂停=状态文案（不显示 0 速度）、已完成=平均速度
// （云盘 avgSpeed、普通任务 downloaded÷任务耗时）、失败不显示。
// 列表工具栏（迅雷式）：状态页签（全部/下载中/失败/完成，带计数）+ 文件名搜索 + 排序（时间/文件名/大小·升降序），
// 管线顺序 = 页签过滤 → 搜索 → 排序 → 分组（组内按当前排序，组间按组内最新任务时间倒序）。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  batchList, downloadDebugExport, downloadDebugInfo, downloadGetLink, downloadList,
  downloadListFull, downloadPause, downloadPauseAll, downloadDebugPreempt, downloadRedownload,
  downloadRemove, downloadRemoveAll, downloadResume, downloadResumeAll, downloadSubmit,
  fmtBytes, fmtRemain, fmtSpeed, isNormalDownloadLink, NORMAL_STATUS_TEXT,
  normalDownloadList, normalDownloadPause, normalDownloadPauseAll, normalDownloadRemove,
  normalDownloadRemoveAll, normalDownloadResume, normalDownloadResumeAll, normalDownloadSubmit,
  normalStatusCode, settingsList, settingsSet, shutdownNow,
  type BatchTask, type DebugChunk, type DebugInfo, type DownloadStats,
  type DownloadTask, type NormalOpResult, type NormalTask,
} from '../core'
import AppIcon from '../components/AppIcon.vue'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'

const url = ref('')
const fileName = ref('')
const saveDir = ref('')
const submitting = ref(false)
const submitError = ref('')
const tasks = ref<DownloadTask[]>([])
const listError = ref('')
// 普通下载任务（aria2-next / FTP 引擎，独立任务库；与云盘任务同频轮询合并展示）
const normalTasks = ref<NormalTask[]>([])
const normalListError = ref('')
let timer: number | undefined

// download_list 的 stats（mergePercent>0 且 <100 = 有任务在 ffmpeg 合轨阶段）
const stats = ref<DownloadStats | null>(null)
const merging = computed(() => {
  const p = stats.value?.mergePercent ?? 0
  return p > 0 && p < 100 ? p : 0
})

// 多选批量（与网盘页共用 .ck 复选样式；key 用合并行 key，跨两库不撞 id）
const selected = ref(new Set<string>())

// 按目录分组
const grouped = ref(false)

// 文件夹批量任务（batch_list 与任务轮询同频）
const batches = ref<BatchTask[]>([])

let prevTaskCount = 0
let prevVisibleCount = 0
async function refresh() {
  try {
    const r = await downloadListFull()
    tasks.value = r.tasks ?? []
    stats.value = r.stats ?? null
    listError.value = ''
  } catch (e) {
    listError.value = String(e)
  }
  // 普通下载任务（独立库 downloads.db）：同频轮询，失败只提示不阻塞云盘列表
  try {
    const r = await normalDownloadList()
    normalTasks.value = r.tasks ?? []
    normalListError.value = r.ok ? '' : (r.error ?? '读取普通下载列表失败')
  } catch (e) {
    normalListError.value = String(e)
  }
  // 两库列表都到位后做本轮速度采样（会话内曲线数据；Map O(1) 追加）
  sampleSpeeds()
  try {
    batches.value = await batchList()
  } catch { /* 批量接口失败不影响任务列表展示 */ }
  // WebKitGTK 在内容高度骤减时可能不自动钳制滚动位置 → 手动重置。
  // 双口径：任务总数减少，或「过滤后可见行数减少」（任务离开当前页签 / 搜索命中变化）都触发
  const count = tasks.value.length + normalTasks.value.length
  const visible = visibleRows.value.length
  if (count < prevTaskCount || visible < prevVisibleCount) {
    requestAnimationFrame(() => {
      const el = document.querySelector('.content')
      if (el) el.scrollTop = 0
    })
  }
  prevTaskCount = count
  prevVisibleCount = visible
}

// ---- 双引擎合并行模型：云盘任务与普通任务归一成一套字段，行内操作按来源路由 ----

/** 合并行：cloud=true 走 download_*（id=taskId），false 走 NormalDownload*（id=任务库自增 id） */
interface MergedRow {
  key: string // 'c<taskId>' / 'n<id>'（两库 id 独立，key 加前缀防撞）
  cloud: boolean
  id: number
  fileName: string
  statusCode: number // 统一成云盘口径：0等待 1下载中 2已暂停 3已完成 4失败
  statusText: string
  total: number
  downloaded: number
  speed: number
  avgSpeed: number // 普通任务无均速，展示时跳过
  remainMillis: number
  savePath: string
  platform: string // 云盘任务来源平台
  engine: string // 普通任务引擎标签：aria2-next / FTP
  error: string
  url: string // 普通任务原始链接（复制链接用；磁力/种子即其本体）
  createdAt: number // Unix 秒（云盘 = download_list createdAt ?? 0；普通 = createdAt）——完成态均速/用时口径用
  updatedAt: number // Unix 秒（仅普通任务有 updatedAt；云盘恒 0）——普通任务完成态均速 = downloaded ÷ (updatedAt-createdAt)
  sortTime: number // 排序时间戳（Unix 秒：云盘 = download_list createdAt；普通 = createdAt；缺失回退 0，
  // 由后端 createTime 倒序 + 稳定排序保住云盘任务内部「最新在前」的相对顺序）
}

function cloudRow(t: DownloadTask): MergedRow {
  return {
    key: `c${t.taskId}`, cloud: true, id: t.taskId, fileName: t.fileName,
    statusCode: t.status, statusText: t.statusText, total: t.total, downloaded: t.downloaded,
    speed: t.speed, avgSpeed: t.avgSpeed, remainMillis: t.remainMillis,
    savePath: t.savePath ?? '', platform: t.platform ?? '', engine: '', error: t.error ?? '', url: '',
    createdAt: t.createdAt ?? 0, updatedAt: 0,
    sortTime: t.createdAt ?? 0,
  }
}

function normalRow(t: NormalTask): MergedRow {
  const remain = t.speed > 0 && t.size > t.downloaded
    ? Math.round(((t.size - t.downloaded) / t.speed) * 1000) : 0
  return {
    key: `n${t.id}`, cloud: false, id: t.id,
    fileName: t.name || t.url || `任务 #${t.id}`,
    statusCode: normalStatusCode(t.status), statusText: NORMAL_STATUS_TEXT[t.status] ?? t.status,
    total: t.size, downloaded: t.downloaded, speed: t.speed, avgSpeed: 0, remainMillis: remain,
    savePath: t.savePath ?? '', platform: '', engine: t.engine === 'ftp' ? 'FTP' : 'aria2-next',
    error: t.err ?? '', url: t.url, createdAt: t.createdAt, updatedAt: t.updatedAt,
    sortTime: t.createdAt,
  }
}

/** 合并列表：云盘任务在前（原顺序），普通任务在后（createdAt 倒序，Go 侧已排好） */
const rows = computed<MergedRow[]>(() => [
  ...tasks.value.map(cloudRow),
  ...normalTasks.value.map(normalRow),
])

// ---- 会话内速度采样（速度详情弹窗的曲线数据源）----
// 每次轮询 tick（800ms）对「下载中」页签口径的任务（状态 0/1/2，两引擎统一按 MergedRow key）各追一个样本；
// 每任务上限 SPEED_SAMPLE_CAP 点（800ms×1200 ≈ 16 分钟），超出丢最旧；完成/失败停止采样但保留已采数据（详情仍可看）。
// speedHistory 不入响应式（Map 追加 O(1)，不拖累轮询），弹窗刷新靠 speedTick 版本号驱动。

interface SpeedSample { t: number; speed: number; downloaded: number }

const SPEED_SAMPLE_CAP = 1200
const speedHistory = new Map<string, SpeedSample[]>()
const speedTick = ref(0) // 每轮采样 +1：弹窗打开期间曲线随轮询刷新（未打开时 computed 惰性、零开销）

function sampleSpeeds() {
  const now = Date.now()
  for (const row of rows.value) {
    if (row.statusCode === 3 || row.statusCode === 4) continue // 完成/失败：停止采样，保留历史
    const sample: SpeedSample = { t: now, speed: row.speed, downloaded: row.downloaded }
    const list = speedHistory.get(row.key)
    if (list) list.push(sample)
    else speedHistory.set(row.key, [sample])
  }
  // 超上限丢最旧（稳态下每 tick 至多丢 1 点，摊还 O(1)）
  for (const list of speedHistory.values()) {
    if (list.length > SPEED_SAMPLE_CAP) list.splice(0, list.length - SPEED_SAMPLE_CAP)
  }
  speedTick.value++
}

// ---- 列表工具栏（迅雷式多页签）：状态页签 → 文件名搜索 → 排序，依次过滤后进分组 ----

type TabKey = 'all' | 'active' | 'error' | 'done'
type SortKey = 'time' | 'name' | 'size'

/** 下载中 = 等待 + 下载中 + 已暂停（含暂停便于恢复）；失败 / 完成 各自独立页签 */
const TABS: { key: TabKey; label: string; match: (s: number) => boolean }[] = [
  { key: 'all', label: '全部', match: () => true },
  { key: 'active', label: '下载中', match: (s) => s === 0 || s === 1 || s === 2 },
  { key: 'error', label: '下载失败', match: (s) => s === 4 },
  { key: 'done', label: '下载完成', match: (s) => s === 3 },
]

const activeTab = ref<TabKey>('all')
const search = ref('')
const sortKey = ref<SortKey>('time') // 默认时间·降序（最新在前）
const sortDesc = ref(true)

function tabCount(key: TabKey): number {
  const tab = TABS.find((t) => t.key === key)
  return tab ? rows.value.filter((r) => tab.match(r.statusCode)).length : 0
}

/** 排序比较器：时间用 sortTime（缺失回退 0，稳定排序保持后端倒序）；文件名 localeCompare('zh')；大小取 total */
function cmpRow(a: MergedRow, b: MergedRow): number {
  let d = 0
  if (sortKey.value === 'name') d = a.fileName.localeCompare(b.fileName, 'zh')
  else if (sortKey.value === 'size') d = a.total - b.total
  else d = a.sortTime - b.sortTime
  return sortDesc.value ? -d : d
}

/** 可见行管线：页签状态过滤 → 文件名模糊搜索（大小写不敏感）→ 排序 */
const visibleRows = computed<MergedRow[]>(() => {
  const tab = TABS.find((t) => t.key === activeTab.value) ?? TABS[0]
  const q = search.value.trim().toLowerCase()
  return rows.value
    .filter((r) => tab.match(r.statusCode) && (!q || r.fileName.toLowerCase().includes(q)))
    .sort(cmpRow)
})

/** 分页（迅雷式）：页大小 50，页签/搜索/排序变化自动回第 1 页 */
const PAGE_SIZE = 50
const page = ref(1)
const pageCount = computed(() => Math.max(1, Math.ceil(visibleRows.value.length / PAGE_SIZE)))
const pageRows = computed<MergedRow[]>(() =>
  visibleRows.value.slice((page.value - 1) * PAGE_SIZE, page.value * PAGE_SIZE))
watch([activeTab, search, sortKey, sortDesc], () => { page.value = 1 })
watch(page, () => {
  const el = document.querySelector('.content')
  if (el) requestAnimationFrame(() => { el.scrollTop = 0 })
})

function onSortKeyChange(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  sortKey.value = v === 'name' || v === 'size' ? v : 'time'
}

/** 页签各自空文案（与「暂无下载任务」的全局空态区分；有搜索词时优先提示搜索无结果） */
const emptyText = computed(() => {
  const q = search.value.trim()
  if (q) return `没有匹配「${q}」的任务`
  return {
    all: '当前筛选下没有任务',
    active: '没有下载中的任务',
    error: '没有下载失败的任务',
    done: '没有已完成的任务',
  }[activeTab.value]
})

/** 行内操作按来源路由（暂停/继续/删除；删除对已完成云盘任务连带删本地文件，普通任务一律不删） */
function pauseRow(row: MergedRow) {
  return row.cloud ? downloadPause(row.id) : normalDownloadPause(row.id)
}

function resumeRow(row: MergedRow) {
  return row.cloud ? downloadResume(row.id) : normalDownloadResume(row.id)
}

function removeRow(row: MergedRow) {
  return row.cloud
    ? downloadRemove(row.id, row.statusCode === 3)
    : normalDownloadRemove(row.id)
}

async function submit() {
  const u = url.value.trim()
  if (!u || submitting.value) return
  submitting.value = true
  submitError.value = ''
  try {
    // 普通链接（磁力/FTP/种子/Metalink）：内置引擎接不住，转普通下载（aria2-next / FTP）
    if (isNormalDownloadLink(u)) {
      const r = await normalDownloadSubmit(u, saveDir.value.trim())
      if (!r.ok) {
        submitError.value = r.error ?? '提交失败'
      } else {
        url.value = ''
        fileName.value = ''
        cancelShutdown() // 加新任务时自动取消关机
        await refresh()
      }
      return
    }
    const r = await downloadSubmit(u, fileName.value.trim(), saveDir.value.trim())
    if (!r.ok) {
      submitError.value = r.error ?? '提交失败'
    } else {
      url.value = ''
      fileName.value = ''
      cancelShutdown() // 加新任务时自动取消关机
      await refresh()
    }
  } catch (e) {
    submitError.value = String(e)
  } finally {
    submitting.value = false
  }
}

async function act(fn: () => Promise<unknown>) {
  try { await fn() } catch (e) { listError.value = String(e) }
  setTimeout(refresh, 150)
}

function toggleSel(key: string) {
  if (selected.value.has(key)) selected.value.delete(key)
  else selected.value.add(key)
}

/** 全选三态（按当前页签+搜索过滤后的「可见行」判定）：true=全选 / null=半选 / false=可见行无选中。
 *  被过滤隐藏的已选行不参与判定但保持选中（批量栏始终按整个已选集合生效） */
const allVisibleState = computed<boolean | null>(() => {
  const vis = visibleRows.value
  if (vis.length === 0) return false
  let n = 0
  for (const r of vis) if (selected.value.has(r.key)) n++
  return n === 0 ? false : n === vis.length ? true : null
})

/** 全选开关：全或无切换当前可见行（页签+搜索结果）；被过滤隐藏的行选中态不动 */
function toggleSelAll() {
  const allOn = allVisibleState.value === true
  for (const r of visibleRows.value) {
    if (allOn) selected.value.delete(r.key)
    else selected.value.add(r.key)
  }
}

/** 批量操作：按行来源路由到对应引擎（单个失败继续处理其余），完成后刷新并清空选择 */
async function batchAct(kind: 'pause' | 'resume' | 'remove') {
  const keys = [...selected.value]
  for (const key of keys) {
    const row = rows.value.find((r) => r.key === key)
    if (!row) continue
    try {
      if (kind === 'pause') await pauseRow(row)
      else if (kind === 'resume') await resumeRow(row)
      else await removeRow(row)
    } catch { /* 保持批量语义，不中断 */ }
  }
  selected.value.clear()
  setTimeout(refresh, 150)
}

// ---- 全局批量栏（与勾选无关，任务存在即显示）+ 轻提示 ----

const notice = ref('')

function showNotice(msg: string) {
  notice.value = msg
  setTimeout(() => (notice.value = ''), 2600)
}

/** 全局批量兜底：普通引擎无任务时跳过其操作，避免无谓提示 */
async function normalAll(fn: () => Promise<NormalOpResult>): Promise<string> {
  if (normalTasks.value.length === 0) return ''
  try {
    const r = await fn()
    return r.ok ? '' : (r.error ?? '普通任务操作失败')
  } catch (e) { return `普通任务：${e}` }
}

async function pauseAll() {
  const parts: string[] = []
  try {
    const r = await downloadPauseAll()
    parts.push(r.ok ? `云盘已暂停 ${r.paused} 个` : r.error ?? '云盘操作失败')
  } catch (e) { parts.push(`云盘：${e}`) }
  const nErr = await normalAll(normalDownloadPauseAll)
  if (nErr) parts.push(nErr)
  showNotice(parts.join('；'))
  setTimeout(refresh, 150)
}

async function resumeAll() {
  const parts: string[] = []
  try {
    const r = await downloadResumeAll()
    parts.push(r.ok ? `云盘已开始 ${r.resumed} 个` : r.error ?? '云盘操作失败')
  } catch (e) { parts.push(`云盘：${e}`) }
  const nErr = await normalAll(normalDownloadResumeAll)
  if (nErr) parts.push(nErr)
  showNotice(parts.join('；'))
  setTimeout(refresh, 150)
}

// 全部删除确认弹窗（含「同时删除本地文件」——仅云盘引擎支持，普通任务一律不删文件）
const removeAllOpen = ref(false)
const removeAllDeleteLocal = ref(false)
const removeAllBusy = ref(false)

async function doRemoveAll() {
  removeAllBusy.value = true
  const parts: string[] = []
  try {
    const r = await downloadRemoveAll(removeAllDeleteLocal.value)
    if (r.ok) parts.push(`云盘已删除 ${r.removed} 个`)
    else parts.push(r.error ?? '云盘删除失败')
  } catch (e) { parts.push(`云盘：${e}`) }
  const nErr = await normalAll(normalDownloadRemoveAll)
  parts.push(nErr ? nErr : (normalTasks.value.length ? '普通任务已清空' : ''))
  showNotice(parts.filter(Boolean).join('；'))
  removeAllBusy.value = false
  removeAllOpen.value = false
  setTimeout(refresh, 150)
}

// ---- 行内 ⋯ 菜单：复制链接 / 重新下载（失败云盘任务）/ 打开文件（已完成）----

const menuFor = ref('')

function toggleMenu(key: string) {
  menuFor.value = menuFor.value === key ? '' : key
}

async function copyLink(row: MergedRow) {
  menuFor.value = ''
  try {
    // 普通任务：链接就在任务上（磁力/种子/FTP 即其本体），无需回核心现取
    if (!row.cloud) {
      if (row.url) {
        await navigator.clipboard.writeText(row.url)
        showNotice(`链接已复制：${row.fileName}`)
      } else {
        showNotice('该任务没有可复制的链接')
      }
      return
    }
    const r = await downloadGetLink(row.id)
    if (r.ok && r.url) {
      await navigator.clipboard.writeText(r.url)
      showNotice(`直链已复制：${r.fileName || row.fileName}`)
    } else {
      showNotice(r.error ?? '取直链失败')
    }
  } catch (e) { showNotice(String(e)) }
}

/** 重新下载仅云盘引擎提供（入口也只在云盘失败行出现） */
async function redownload(row: MergedRow) {
  menuFor.value = ''
  try {
    const r = await downloadRedownload(row.id)
    showNotice(r.ok ? `「${row.fileName}」已重新入队` : r.error ?? '重新下载失败')
  } catch (e) { showNotice(String(e)) }
  setTimeout(refresh, 150)
}

function openFile(row: MergedRow) {
  menuFor.value = ''
  if (row.savePath) CoreService.OpenFile(row.savePath).catch(() => console.warn('打开失败'))
}

function openDirectory(row: MergedRow) {
  menuFor.value = ''
  if (row.savePath) CoreService.OpenDirectory(row.savePath).catch(() => console.warn('打开目录失败'))
}

// 点击菜单外区域收起行菜单
function closeMenu(e: MouseEvent) {
  if (!menuFor.value) return
  if (!(e.target as HTMLElement).closest('.rowmenu-wrap')) menuFor.value = ''
}

function dirOfPath(p: string): string {
  if (!p) return ''
  const i = p.lastIndexOf('/')
  return i > 0 ? p.slice(0, i) : p
}

/** 按目录分组：组内保持当前排序（插入序即排序序），组间按组内最新任务时间倒序 */
const groups = computed(() => {
  const m = new Map<string, MergedRow[]>()
  for (const row of pageRows.value) {
    const d = dirOfPath(row.savePath) || '（未知目录）'
    const list = m.get(d) ?? []
    list.push(row)
    m.set(d, list)
  }
  return [...m.entries()]
    .map(([dir, list]) => ({ dir, list, newest: Math.max(...list.map((r) => r.sortTime)) }))
    .sort((a, b) => b.newest - a.newest)
})

/** 统一分段：分组=按目录切段（带标题），平铺=单段（无标题），行渲染只写一份 */
const sections = computed(() =>
  grouped.value ? groups.value : [{ dir: '', list: pageRows.value }])

function batchPct(b: BatchTask): number {
  return b.total > 0 ? Math.min(100, Math.round((b.done / b.total) * 100)) : 0
}

function progress(row: MergedRow): number {
  return row.total > 0 ? Math.min(100, Math.round((row.downloaded / row.total) * 100)) : 0
}

function statusClass(s: number): string {
  return s === 3 ? 'ok' : s === 4 ? 'err' : s === 1 ? 'run' : ''
}

/** 任务平均速度（B/s）：云盘 = 引擎 avgSpeed（全程口径）；普通任务 = downloaded ÷ max(1, updatedAt-createdAt)（Unix 秒差） */
function avgSpeedOf(row: MergedRow): number {
  if (row.cloud) return row.avgSpeed
  return row.downloaded / Math.max(1, row.updatedAt - row.createdAt)
}

/** 行副行速度口径（对齐迅雷）：下载中 = 实时速度；排队/暂停 = 状态文案（不显示 0 速度）；
 *  已完成 = 平均速度（云盘 avgSpeed / 普通任务前端按 downloaded÷任务耗时 计）；失败 = 不显示 */
function rowSpeedText(row: MergedRow): string {
  if (row.statusCode === 1) return fmtSpeed(row.speed)
  if (row.statusCode === 0 || row.statusCode === 2) return row.statusText
  if (row.statusCode === 3) {
    const avg = avgSpeedOf(row)
    return avg > 0 ? `均速 ${fmtSpeed(avg)}` : ''
  }
  return ''
}

/** 合轨进度挂到哪个任务上展示：优先下载中的任务，退而求任务列表第一个 */
const mergingTaskId = computed(() => {
  if (merging.value <= 0) return 0
  const active = tasks.value.find((t) => t.status === 1)
  return (active ?? tasks.value[0])?.taskId ?? 0
})

// ---- 完成后关机（对齐原版 shutdownWhenDownloadsComplete 设置）----
// 每 5 秒独立检查两个任务库（云盘 + 普通）：所有任务均不在下载中（0/2/3/4）且至少 1 个已完成
// → 弹 60 秒全屏倒计时；倒计时结束调 CoreService.Shutdown 关机；
// 取消按钮 / 新加任务 / 条件被打破（恢复任务等）都会终止倒计时。

const shutdownEnabled = ref(false)
const shutdownCountdown = ref(0)
let shutdownTimer: number | undefined
let shutdownCheckTimer: number | undefined

async function loadShutdownSetting() {
  try {
    const s = await settingsList()
    shutdownEnabled.value = s?.shutdownWhenDownloadsComplete ?? false
    downloadDebugOn.value = s?.downloadDebug ?? false
  } catch { /* 核心未就绪时保持关闭 */ }
}

async function toggleShutdown() {
  shutdownEnabled.value = !shutdownEnabled.value
  if (!shutdownEnabled.value) cancelShutdown()
  try { await settingsSet('shutdownWhenDownloadsComplete', shutdownEnabled.value) }
  catch { /* 写失败不打扰，下次进页重读 */ }
}

/** 每 5 秒的条件检查（两库合并口径）：满足且未在倒计时 → 启动倒计时；条件被打破 → 自动取消 */
async function checkShutdown() {
  if (!shutdownEnabled.value) return
  const list = await downloadList().catch(() => [] as DownloadTask[])
  const normals = (await normalDownloadList().catch(() => null))?.tasks ?? []
  const codes = [
    ...list.map((t) => t.status),
    ...normals.map((t) => normalStatusCode(t.status)),
  ]
  const settled = codes.length > 0 && codes.every((c) => [0, 2, 3, 4].includes(c))
  const hasDone = codes.includes(3)
  if (settled && hasDone) {
    if (!shutdownCountdown.value) startCountdown()
  } else if (shutdownCountdown.value) {
    cancelShutdown()
  }
}

function startCountdown() {
  shutdownCountdown.value = 60
  shutdownTimer = window.setInterval(() => {
    shutdownCountdown.value--
    if (shutdownCountdown.value <= 0) doShutdownNow()
  }, 1000)
}

function stopCountdownTimer() {
  if (shutdownTimer !== undefined) {
    window.clearInterval(shutdownTimer)
    shutdownTimer = undefined
  }
}

/** 取消关机：清倒计时（设置保持开启，下轮满足条件会再次提示） */
function cancelShutdown() {
  stopCountdownTimer()
  shutdownCountdown.value = 0
}

async function doShutdownNow() {
  stopCountdownTimer()
  shutdownCountdown.value = 0
  try {
    await shutdownNow()
  } catch (e) {
    showNotice(`关机命令失败：${e}`)
  }
}

// ---- 下载调试（对齐原版 DownloadDebugScreen：拉取式快照，5 秒轮询，弹窗关掉即停）----
// 入口只在 downloadDebug 设置开启时出现在行菜单（对齐原版：不开开关看不到入口）。
// 「疑似卡死」判定与原版同口径：跑够 30 秒、本轮采样零字节（bps==0）、还没下完 ——
// 不能用「速度低于阈值」判：高并发 + 慢链路下每路都可能长时间低速却仍在推进。

const DEBUG_REFRESH_MS = 5000
const STUCK_MIN_AGE_MS = 30_000

const downloadDebugOn = ref(false)
const debugTaskId = ref(0)
const debugInfo = ref<DebugInfo | null>(null)
const debugError = ref('')
const debugExportPath = ref('')
const debugLogEl = ref<HTMLElement | null>(null)
let debugTimer: number | undefined
let debugFollow = true // 日志实时跟随：仅当用户本来就停在底部时（往上翻看历史不拽回底部）

/** 弹窗头部的任务概要行：状态 / 大小 / 实时速度（口径与原版 DebugTaskCard 一致） */
const debugSummary = computed(() => {
  const info = debugInfo.value
  if (!info) return ''
  const t = info.task
  const parts: string[] = [t.statusText]
  if (t.total > 0) parts.push(`${fmtBytes(Math.min(t.downloaded, t.total))} / ${fmtBytes(t.total)}`)
  if (info.stats && info.stats.speed > 0) {
    parts.push(`${fmtSpeed(info.stats.speed)} · 剩余 ${fmtRemain(info.stats.remainMillis)} · ${info.stats.chunkCount} 线程`)
  } else if (t.avgSpeed > 0) {
    parts.push(`均速 ${fmtSpeed(t.avgSpeed)}`)
  }
  if (t.sourceCount > 1) parts.push(`${t.sourceCount} 源`)
  parts.push(t.engine ? `执行器 ${t.engine}` : '内置分片下载器')
  if (t.platform) parts.push(t.platform)
  return parts.join(' · ')
})

/** 进度条百分比：合并阶段（mergePercent>=0）优先显示合并进度（与下载页同一口径） */
const debugProgress = computed(() => {
  const info = debugInfo.value
  if (!info) return 0
  const merge = info.stats?.mergePercent ?? -1
  if (merge >= 0) return merge
  const t = info.task
  return t.total > 0 ? Math.min(100, Math.round((Math.min(t.downloaded, t.total) / t.total) * 100)) : 0
})

const debugMerging = computed(() => (debugInfo.value?.stats?.mergePercent ?? -1) >= 0)

const debugWorkerSummary = computed(() => {
  const info = debugInfo.value
  if (!info) return ''
  const totalWorkers = info.stats?.chunkCount ?? 0
  if (info.chunks.length === 0) {
    return '当前没有在飞分片（未开始 / 已暂停 / 已完成，或该任务走的是单流 / 引擎下载）。'
  }
  return totalWorkers > 0
    ? `在飞 ${info.chunks.length} 路 · 空闲 ${Math.max(0, totalWorkers - info.chunks.length)} 路（共 ${totalWorkers} 个 worker）`
    : `在飞 ${info.chunks.length} 路`
})

const debugResourceRows = computed(() => {
  const r = debugInfo.value?.resources
  if (!r) return [] as [string, string][]
  return [
    ['在飞分片', `${r.inflightUsed} / ${r.inflightCap}`],
    ['活跃任务', String(r.activeTasks)],
    ['线程池', `线程 ${r.poolSize} · 忙 ${r.poolActive} · 队列 ${r.poolQueue}`],
    ['堆内存', `${Math.floor(r.heapUsed / 1048576)} MB / ${Math.floor(r.heapMax / 1048576)} MB`],
  ] as [string, string][]
})

function debugChunkPct(c: DebugChunk): number {
  return c.size > 0 ? Math.min(100, Math.floor((c.received / c.size) * 100)) : 0
}

function debugChunkSpeed(c: DebugChunk): string {
  return c.bps > 0 ? fmtSpeed(c.bps) : '0 B/s'
}

function debugStuck(c: DebugChunk): boolean {
  return c.elapsedMs >= STUCK_MIN_AGE_MS && c.bps <= 0 && c.received < c.size
}

async function pollDebug() {
  if (!debugTaskId.value) return
  try {
    const r = await downloadDebugInfo(debugTaskId.value)
    if (!r.ok) {
      // 任务已删除（或核心拒绝）：停轮询并提示，绝不让弹窗崩掉
      stopDebugPoll()
      debugError.value = r.error ?? '读取调试信息失败'
      return
    }
    debugInfo.value = r
    debugError.value = ''
    // 日志跟随：只在用户本来就停在底部时滚到底（原版 LogsColumn 同款语义）
    if (debugFollow && r.logs.length > 0) {
      nextTick(() => {
        const el = debugLogEl.value
        if (el) el.scrollTop = el.scrollHeight
      })
    }
  } catch (e) {
    debugError.value = String(e) // 瞬时异常保留轮询，下一轮自愈
  }
}

/** 调试信息仅云盘任务有（入口也只在云盘行出现） */
function openDebug(row: MergedRow) {
  menuFor.value = ''
  debugTaskId.value = row.id
  debugInfo.value = null
  debugError.value = ''
  debugExportPath.value = ''
  debugFollow = true
  pollDebug()
  debugTimer = window.setInterval(pollDebug, DEBUG_REFRESH_MS)
}

function stopDebugPoll() {
  if (debugTimer !== undefined) {
    window.clearInterval(debugTimer)
    debugTimer = undefined
  }
}

function closeDebug() {
  stopDebugPoll()
  debugTaskId.value = 0
  debugInfo.value = null
  debugExportPath.value = ''
}

function onDebugLogScroll() {
  const el = debugLogEl.value
  if (!el) return
  debugFollow = el.scrollHeight - el.scrollTop - el.clientHeight < 48
}

async function copyDebugSnapshot() {
  if (!debugInfo.value) return
  try {
    await navigator.clipboard.writeText(debugInfo.value.snapshot)
    showNotice('调试快照已复制')
  } catch (e) {
    showNotice(`复制失败：${e}`)
  }
}

async function exportDebugLogs() {
  if (!debugTaskId.value) return
  try {
    const r = await downloadDebugExport(debugTaskId.value)
    if (r.ok && r.path) {
      debugExportPath.value = r.path
      showNotice(`已导出完整下载日志（${r.fileName}）`)
    } else {
      showNotice(r.error ?? '导出失败，请稍后重试')
    }
  } catch (e) {
    showNotice(String(e))
  }
}

function openDebugDir() {
  if (debugExportPath.value) {
    CoreService.OpenDirectory(debugExportPath.value).catch(() => console.warn('打开目录失败'))
  }
}

/** 手动强制重连某路分片（对齐原版 ChunkRow「重连」）：掐断在飞连接、换新连接从已收字节续传；
 *  成功行内提示并立即手动刷一次调试数据（不等 5 秒轮询），失败显示 error（分片已结束/不在活跃集） */
const preemptNote = ref<{ key: string; msg: string; err: boolean } | null>(null)
const preemptBusy = ref('')

async function preemptChunk(c: DebugChunk) {
  if (!debugTaskId.value || preemptBusy.value) return
  preemptBusy.value = c.key
  try {
    const r = await downloadDebugPreempt(debugTaskId.value, c.key)
    preemptNote.value = {
      key: c.key,
      msg: r.ok ? `已标记 ${c.key} 断开换连接续传` : (r.error ?? '该分片已结束，无法重连'),
      err: !r.ok,
    }
    if (r.ok) pollDebug()
  } catch (e) {
    preemptNote.value = { key: c.key, msg: String(e), err: true }
  } finally {
    preemptBusy.value = ''
    setTimeout(() => {
      if (preemptNote.value?.key === c.key) preemptNote.value = null
    }, 3000)
  }
}

// ---- 速度详情弹窗（会话内采样 → 手写 SVG 双轴曲线：速度折线+面积 / 已下载阶梯线；不引图表库）----

const detailKey = ref('')
let detailFallback: MergedRow | null = null // 打开瞬间的行快照：任务被删后弹窗仍能展示最后已知数据

function openDetail(row: MergedRow) {
  menuFor.value = ''
  detailFallback = row
  detailKey.value = row.key
}

function closeDetail() {
  detailKey.value = ''
}

/** Esc 关闭速度详情弹窗（挂在 document 上，随组件卸载移除） */
function onDetailKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && detailKey.value) closeDetail()
}

/** 弹窗当前行：优先实时行（指标随轮询更新），任务被删则回退到打开时的快照 */
const detailRow = computed<MergedRow | null>(() => {
  if (!detailKey.value) return null
  return rows.value.find((r) => r.key === detailKey.value) ?? detailFallback
})

/** 弹窗当前样本序列（speedTick 依赖：每轮采样后失效重算；弹窗关闭时 computed 惰性不求值） */
const detailSamples = computed<SpeedSample[]>(() => {
  if (speedTick.value < 0) return [] // 不可达分支：仅建立对采样版本号的响应式依赖
  const row = detailRow.value
  return row ? speedHistory.get(row.key) ?? [] : []
})

/** 峰值速度（样本 max）+ 样本首尾时间差 + 样本数 */
const detailStats = computed(() => {
  const s = detailSamples.value
  let peak = 0
  let spanMs = 0
  if (s.length > 0) {
    for (const p of s) if (p.speed > peak) peak = p.speed
    if (s.length >= 2) spanMs = s[s.length - 1].t - s[0].t
  }
  return { peak, spanMs, count: s.length }
})

/** 用时：样本首尾时间差 与 任务自建耗时（updatedAt-createdAt，仅普通任务两字段齐全）取大者（会话内口径） */
const detailDurationMs = computed(() => {
  const row = detailRow.value
  if (!row) return 0
  let ms = detailStats.value.spanMs
  if (!row.cloud && row.createdAt > 0 && row.updatedAt > row.createdAt) {
    ms = Math.max(ms, (row.updatedAt - row.createdAt) * 1000)
  }
  return ms
})

// 曲线图几何常量：viewBox 固定 640×200，宽度随弹窗自适应缩放
const CHART_W = 640
const CHART_H = 200
const CHART_PAD = { l: 60, r: 56, t: 12, b: 22 }

/** 速度轴（左轴）上限：1/2/5×10ⁿ 里取第一个 ≥ 峰值的好看档位（如 2 MB/s、5 MB/s） */
function niceCeil(v: number): number {
  if (!isFinite(v) || v <= 0) return 1
  const base = 10 ** Math.floor(Math.log10(v))
  for (const m of [1, 2, 5, 10]) {
    if (m * base >= v - base * 1e-9) return m * base
  }
  return 10 * base
}

/** 刻度速度文案（fmtBytes 对 <1 的值会掉单位，这里兜底小数展示） */
function fmtTickSpeed(v: number): string {
  return v < 1 ? `${v.toFixed(2)} B/s` : fmtSpeed(v)
}

function fmtClock(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/** 曲线图数据：速度折线+半透明面积 / 已下载阶梯线（右轴归一化到总大小）/ 网格与刻度；无样本返回 null */
const detailChart = computed(() => {
  const row = detailRow.value
  const s = detailSamples.value
  if (!row || s.length === 0) return null
  const l = CHART_PAD.l
  const r = CHART_W - CHART_PAD.r
  const t = CHART_PAD.t
  const b = CHART_H - CHART_PAD.b
  const pw = r - l
  const ph = b - t
  let peak = 0
  for (const p of s) if (p.speed > peak) peak = p.speed
  const yMax = niceCeil(peak)
  const t0 = s[0].t
  const t1 = s[s.length - 1].t
  const span = Math.max(1, t1 - t0)
  const x = (ts: number) => l + ((ts - t0) / span) * pw
  const ySpeed = (v: number) => b - (Math.min(v, yMax) / yMax) * ph
  const yDown = (v: number) => (row.total > 0 ? b - (Math.min(v, row.total) / row.total) * ph : b)

  // 速度折线 + 半透明面积（首尾落到底边闭合）
  const speedPathD = `M${s.map((p) => `${x(p.t).toFixed(1)},${ySpeed(p.speed).toFixed(1)}`).join(' L')}`
  const areaPathD = `${speedPathD} L${x(t1).toFixed(1)},${b.toFixed(1)} L${x(t0).toFixed(1)},${b.toFixed(1)} Z`

  // 已下载量阶梯线（step-after：上一档位保持到下一时刻再跳变；总大小未知则不画）
  let dlPathD = ''
  if (row.total > 0) {
    const segs: string[] = []
    s.forEach((p, i) => {
      const px = x(p.t).toFixed(1)
      const py = yDown(p.downloaded).toFixed(1)
      if (i === 0) segs.push(`M${px},${py}`)
      else segs.push(`L${px},${yDown(s[i - 1].downloaded).toFixed(1)}`, `L${px},${py}`)
    })
    dlPathD = segs.join(' ')
  }

  // 横向网格 4 条（25/50/75/100% 高度）：左侧速度刻度 + 右侧下载量百分比刻度（右轴共用同一套网格）
  const grid = [0.25, 0.5, 0.75, 1].map((f) => ({
    y: b - f * ph,
    speed: fmtTickSpeed(yMax * f),
    pct: `${Math.round(f * 100)}%`,
  }))
  // X 轴刻度：首/中/尾三个时间点（仅 1 个样本时只画首点，避免三条同文案叠在一起）
  const xTicks = t1 === t0
    ? [{ x: l, label: fmtClock(t0) }]
    : [0, 0.5, 1].map((f) => ({ x: l + f * pw, label: fmtClock(t0 + f * (t1 - t0)) }))

  return { viewBox: `0 0 ${CHART_W} ${CHART_H}`, l, r, b, speedPathD, areaPathD, dlPathD, grid, xTicks }
})

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 800)
  document.addEventListener('click', closeMenu)
  document.addEventListener('keydown', onDetailKeydown)
  loadShutdownSetting()
  shutdownCheckTimer = window.setInterval(checkShutdown, 5000)
})
onUnmounted(() => {
  window.clearInterval(timer)
  window.clearInterval(shutdownCheckTimer)
  stopCountdownTimer()
  stopDebugPoll()
  document.removeEventListener('click', closeMenu)
  document.removeEventListener('keydown', onDetailKeydown)
  speedHistory.clear() // 会话内采样随组件卸载清空
})
</script>

<template>
  <div class="page">
    <div class="card">
      <h3>添加下载任务</h3>
      <p class="hint">直链（http/https）走内置分片引擎；磁力 / BT / ed2k / Metalink / FTP 及 .torrent 链接自动转普通下载
        （aria2-next / 内置 FTP）；网盘直链请先经解析获取。留空目录 = 使用默认下载目录</p>
      <div style="display: flex; flex-direction: column; gap: 10px">
        <div class="row">
          <div class="field" style="flex: 1; min-width: 260px">
            <label>链接</label>
            <input v-model="url" class="input" placeholder="https://..." @keydown.enter="submit" />
          </div>
          <button class="btn" style="align-self: flex-end" :disabled="!url.trim() || submitting" @click="submit">
            {{ submitting ? '提交中…' : '下载' }}
          </button>
        </div>
        <div class="row">
          <div class="field" style="flex: 1; min-width: 160px">
            <label>文件名（可选，默认从链接取）</label>
            <input v-model="fileName" class="input" placeholder="video.mp4" />
          </div>
          <div class="field" style="flex: 1; min-width: 160px">
            <label>保存目录（可选）</label>
            <input v-model="saveDir" class="input" placeholder="/home/xxx/Downloads" />
          </div>
        </div>
      </div>
      <div v-if="submitError" class="banner error" style="margin-top: 10px">{{ submitError }}</div>
    </div>

    <!-- 批量任务（文件夹下载的批次进度，与任务轮询同频 800ms） -->
    <div v-if="batches.length" class="card" style="padding: 14px 20px">
      <h3>批量任务</h3>
      <div v-for="b in batches" :key="b.id" style="padding: 6px 0">
        <div class="row" style="justify-content: space-between; margin-bottom: 6px">
          <span style="font-weight: 600; font-size: 13.5px; min-width: 0;
                       overflow: hidden; text-overflow: ellipsis; white-space: nowrap" :title="b.title">
            {{ b.title }}
          </span>
          <span class="chip plain">{{ b.done }}/{{ b.total }} · 失败 {{ b.failed }}</span>
        </div>
        <div class="bar"><i :style="{ width: batchPct(b) + '%' }" /></div>
        <div v-if="b.lastError" class="banner error" style="margin-top: 6px">{{ b.lastError }}</div>
      </div>
    </div>

    <!-- 全局批量栏：与勾选无关，任务存在即显示（同时作用云盘引擎与普通下载引擎） -->
    <div v-if="rows.length > 0" class="card" style="padding: 10px 20px">
      <div class="row" style="justify-content: space-between">
        <span style="font-weight: 600">全部任务（{{ rows.length }} · 网盘 {{ tasks.length }} / 普通 {{ normalTasks.length }}）</span>
        <span class="row">
          <span class="row" style="gap: 8px; font-size: 13px" title="两个引擎的全部任务结束后弹出倒计时关机">
            完成后关机
            <i class="switch" :class="{ on: shutdownEnabled }" @click="toggleShutdown" />
          </span>
          <button class="btn tonal small" @click="pauseAll">全部暂停</button>
          <button class="btn tonal small" @click="resumeAll">全部开始</button>
          <button class="btn text small" style="color: var(--error)" @click="removeAllOpen = true">全部删除</button>
        </span>
      </div>
      <div v-if="notice" class="banner" style="margin-top: 8px">{{ notice }}</div>
    </div>

    <!-- 已选批量栏 -->
    <div v-if="selected.size > 0" class="card" style="padding: 10px 20px">
      <div class="row" style="justify-content: space-between">
        <span style="font-weight: 600">已选 {{ selected.size }} 项</span>
        <span class="row">
          <button class="btn tonal small" @click="batchAct('pause')">批量暂停</button>
          <button class="btn tonal small" @click="batchAct('resume')">批量继续</button>
          <button class="btn text small" style="color: var(--error)" @click="batchAct('remove')">删除所选</button>
          <button class="btn text small" @click="selected.clear()">取消选择</button>
        </span>
      </div>
    </div>

    <!-- 列表工具栏：状态页签（迅雷式，带计数）+ 文件名搜索 + 排序 + 按目录分组（两库任务统一按 savePath 所在目录分组） -->
    <div v-if="rows.length > 0" class="card" style="padding: 10px 20px">
      <div class="row" style="justify-content: space-between; gap: 10px">
        <div class="row" style="gap: 12px; min-width: 0; flex-wrap: wrap">
          <!-- 全选：只作用于当前页签+搜索结果里的可见行（三态）；被过滤隐藏的已选行保持选中 -->
          <span class="selall" title="全选/取消全选当前页签+搜索结果中的可见任务（被过滤隐藏的已选任务保持选中）"
                @click="toggleSelAll">
            <span class="ck" :class="{ on: allVisibleState === true, half: allVisibleState === null }">
              <AppIcon v-if="allVisibleState === true" name="check" :size="13" />
            </span>
            <span>全选</span>
            <span v-if="selected.size > 0" class="chip plain selcnt">已选 {{ selected.size }}</span>
          </span>
          <div class="tabs">
            <button v-for="t in TABS" :key="t.key" class="tab" :class="{ on: activeTab === t.key }"
                    @click="activeTab = t.key">
              {{ t.label }}<span class="cnt">{{ tabCount(t.key) }}</span>
            </button>
          </div>
        </div>
        <span class="row" style="gap: 8px; flex: none; flex-wrap: wrap">
          <span class="searchbox">
            <input v-model="search" placeholder="搜索文件名" />
            <button v-if="search" class="clear" title="清空搜索" @click="search = ''">✕</button>
          </span>
          <select class="sortsel" :value="sortKey" title="排序方式" @change="onSortKeyChange">
            <option value="time">时间</option>
            <option value="name">文件名</option>
            <option value="size">大小</option>
          </select>
          <button class="btn tonal small" style="min-width: 34px"
                  :title="sortDesc ? '当前降序，点击切换为升序' : '当前升序，点击切换为降序'"
                  @click="sortDesc = !sortDesc">{{ sortDesc ? '↓' : '↑' }}</button>
          <span class="row" style="gap: 6px; font-size: 13px" title="按 savePath 所在目录分组（两库任务统一分组）">
            分组<i class="switch" :class="{ on: grouped }" @click="grouped = !grouped" />
          </span>
        </span>
      </div>
    </div>

    <div v-if="listError" class="banner error">{{ listError }}</div>
    <div v-if="normalListError" class="banner error">普通下载：{{ normalListError }}</div>

    <div v-if="rows.length === 0" class="card">
      <div class="empty">
        <div class="big">⬇️</div>
        <h3 style="margin: 0 0 6px">暂无下载任务</h3>
        <p>内置分片引擎（网盘直链，32 线程 / Range 分片 / 断点续传）与普通下载引擎（aria2-next：磁力 / BT / ed2k / Metalink；内置 FTP）均已就绪。</p>
      </div>
    </div>

    <!-- 页签内空态：有任务但当前页签/搜索筛不出来（与上面的全局空态区分） -->
    <div v-else-if="visibleRows.length === 0" class="card" style="padding: 10px 20px">
      <div class="empty" style="padding: 16px 0">
        <p style="margin: 0">{{ emptyText }}</p>
      </div>

    <!-- 分页栏（仅超出单页时显示） -->
    <div v-if="visibleRows.length > PAGE_SIZE" class="pagerow">
      <button class="btn text small" :disabled="page <= 1" @click="page--">‹ 上一页</button>
      <span class="dim">第 {{ page }} / {{ pageCount }} 页 · 共 {{ visibleRows.length }} 条</span>
      <button class="btn text small" :disabled="page >= pageCount" @click="page++">下一页 ›</button>
    </div>
    </div>

    <!-- 任务列表：分组 / 平铺共用同一套行渲染（云盘任务与普通任务合并，来源徽标区分） -->
    <template v-for="g in sections" :key="g.dir || '_flat'">
      <div v-if="grouped" class="row grouphdr">
        <span class="gdir" :title="g.dir">{{ g.dir }}</span>
        <span class="chip plain">{{ g.list.length }} 个任务</span>
      </div>
      <div v-for="row in g.list" :key="row.key" class="card" style="padding: 14px 20px">
        <div class="row" style="justify-content: space-between; margin-bottom: 8px">
          <span class="ck" :class="{ on: selected.has(row.key) }" title="选中"
                @click="toggleSel(row.key)">
            <AppIcon v-if="selected.has(row.key)" name="check" :size="13" />
          </span>
          <div style="min-width: 0; flex: 1">
            <div style="font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap"
                 :title="row.savePath || row.fileName">
              {{ row.fileName }}
              <!-- 普通任务来源徽标（云盘任务的 platform 在下行 meta 里以文本展示） -->
              <span v-if="!row.cloud" class="chip plain engchip">{{ row.engine }}</span>
            </div>
            <div style="font-size: 12px; color: var(--on-bg-variant)">
              {{ fmtBytes(row.downloaded) }}<span v-if="row.total > 0"> / {{ fmtBytes(row.total) }}</span>
              <span v-if="rowSpeedText(row)"> · {{ rowSpeedText(row) }}</span>
              <span v-if="row.statusCode === 1 && row.remainMillis > 0"> · 剩余 {{ fmtRemain(row.remainMillis) }}</span>
              <span v-if="row.cloud && row.platform"> · {{ row.platform }}</span>
            </div>
          </div>
          <div class="row" style="flex: none">
            <span class="chip" :class="statusClass(row.statusCode)">{{ row.statusText }}</span>
            <button v-if="row.statusCode === 1 || row.statusCode === 0" class="btn tonal small"
                    @click="act(() => pauseRow(row))">暂停</button>
            <button v-if="row.statusCode === 2 || row.statusCode === 4" class="btn tonal small"
                    @click="act(() => resumeRow(row))">
              {{ row.statusCode === 4 ? '重试' : '继续' }}
            </button>
            <button class="btn text small" @click="act(() => removeRow(row))">删除</button>
            <span class="rowmenu-wrap">
              <button class="btn text small" title="更多操作" @click.stop="toggleMenu(row.key)">⋯</button>
              <div v-if="menuFor === row.key" class="rowmenu">
                <button class="menuitem" @click="openDetail(row)">速度详情</button>
                <button class="menuitem" @click="copyLink(row)">复制链接</button>
                <button v-if="row.cloud && row.statusCode === 4" class="menuitem" @click="redownload(row)">重新下载</button>
                <button v-if="row.cloud && downloadDebugOn" class="menuitem" @click="openDebug(row)">调试信息</button>
                <button v-if="row.statusCode === 3 && row.savePath" class="menuitem" @click="openDirectory(row)">打开目录</button>
                <button v-if="row.statusCode === 3 && row.savePath" class="menuitem" @click="openFile(row)">打开文件</button>
              </div>
            </span>
          </div>
        </div>
        <div v-if="row.total > 0" class="bar" :class="{ mergebar: row.cloud && mergingTaskId === row.id }">
          <i :style="{ width: (row.cloud && mergingTaskId === row.id ? merging : progress(row)) + '%' }" />
        </div>
        <div v-if="row.cloud && mergingTaskId === row.id" class="mergelabel">合并中 {{ merging }}%</div>
        <div v-if="row.error" class="banner error" style="margin-top: 8px">{{ row.error }}</div>
      </div>
    </template>

    <!-- 全部删除确认弹窗（.mask/.dlg 模式，对齐 DirPickerModal） -->
    <div v-if="removeAllOpen" class="mask" @click.self="removeAllOpen = false">
      <div class="dlg">
        <h3 style="margin: 0 0 6px">删除全部任务？</h3>
        <p class="hint">
          将从下载列表移除全部 {{ rows.length }} 个任务（网盘 {{ tasks.length }} · 普通 {{ normalTasks.length }}，
          不含批量任务批次）；普通下载任务不删本地文件，「同时删除本地文件」仅对云盘任务生效
        </p>
        <div class="row" style="margin-bottom: 12px">
          <span class="ck" :class="{ on: removeAllDeleteLocal }" title="同时删除本地文件（仅云盘任务）"
                @click="removeAllDeleteLocal = !removeAllDeleteLocal">
            <AppIcon v-if="removeAllDeleteLocal" name="check" :size="13" />
          </span>
          <span>同时删除本地文件（仅云盘任务）</span>
        </div>
        <div class="row end">
          <button class="btn text small" @click="removeAllOpen = false">取消</button>
          <button class="btn small danger" :disabled="removeAllBusy" @click="doRemoveAll">
            {{ removeAllBusy ? '删除中…' : '全部删除' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 完成后关机：全屏 60 秒倒计时（取消关机大按钮 + 立即关机） -->
    <div v-if="shutdownCountdown > 0" class="mask shutdown-mask">
      <div class="dlg shutdown-dlg">
        <div class="big">⏻</div>
        <h3 style="margin: 0 0 6px">所有下载已完成</h3>
        <p class="hint" style="margin-bottom: 18px">系统将在 {{ shutdownCountdown }} 秒后关机</p>
        <div class="row" style="justify-content: center; gap: 14px">
          <button class="btn cancel-shutdown" @click="cancelShutdown">取消关机</button>
          <button class="btn danger cancel-shutdown" @click="doShutdownNow">立即关机</button>
        </div>
      </div>
    </div>

    <!-- 下载调试弹窗（近全屏大卡片；对齐原版 DownloadDebugScreen 的桌面分栏布局） -->
    <div v-if="debugTaskId" class="mask" @click.self="closeDebug">
      <div class="dlg dbgdlg">
        <!-- 顶部：标题 + 操作（复制快照 / 导出日志 / 关闭） -->
        <div class="row" style="justify-content: space-between; gap: 10px; margin-bottom: 10px">
          <span class="dbgtitle" :title="debugInfo?.task.fileName">
            下载调试 · {{ debugInfo?.task.fileName ?? '读取中…' }}
          </span>
          <span class="row" style="flex: none; gap: 6px">
            <button class="btn tonal small" :disabled="!debugInfo" @click="copyDebugSnapshot">复制快照</button>
            <button class="btn tonal small" @click="exportDebugLogs">导出日志</button>
            <button class="btn text small" @click="closeDebug">关闭</button>
          </span>
        </div>

        <div v-if="debugError" class="banner error" style="margin-bottom: 8px">{{ debugError }}</div>

        <template v-if="debugInfo">
          <!-- 任务概要行：状态 / 大小 / 实时速度 / 源数 / 执行器（口径同原版 DebugTaskCard） -->
          <div class="dbgsummary">
            <div class="bar" :class="{ mergebar: debugMerging }">
              <i :style="{ width: debugProgress + '%' }" />
            </div>
            <div class="hint" style="margin: 4px 0 0">
              {{ debugMerging ? `正在合并分片 · ${debugProgress}% · ` : '' }}{{ debugSummary }}
              <span v-if="debugInfo.env"> · {{ debugInfo.env }}</span>
            </div>
          </div>
          <div v-if="debugInfo.task.error" class="banner error" style="margin-top: 8px">
            {{ debugInfo.task.error }}
          </div>

          <!-- 左右分栏：左 = 全局资源 + 线程表，右 = 实时日志 -->
          <div class="dbgcols">
            <div class="dbgcol">
              <div class="dbgsec">全局资源</div>
              <div class="dbgpanel">
                <div v-for="[k, v] in debugResourceRows" :key="k" class="dbginforow">
                  <span class="k">{{ k }}</span><span>{{ v }}</span>
                </div>
              </div>
              <div class="dbgsec">线程（每个在飞分片在干什么）</div>
              <div class="hint">{{ debugWorkerSummary }}</div>
              <div class="dbgchunks">
                <div v-for="c in debugInfo.chunks" :key="c.key" class="dbgchunk" :class="{ stuck: debugStuck(c) }">
                  <div class="dbgchunk-main">
                    <div style="font-weight: 600; font-size: 12.5px">{{ c.key }}　worker #{{ c.worker }}</div>
                    <div class="hint">区间 {{ c.start }}-{{ c.start + c.size - 1 }} · 已收
                      {{ fmtBytes(c.received) }}/{{ fmtBytes(c.size) }}（{{ debugChunkPct(c) }}%）</div>
                    <div class="hint" :class="{ errtext: debugStuck(c) }">
                      瞬时 {{ debugChunkSpeed(c) }} · 已跑 {{ Math.floor(c.elapsedMs / 1000) }}s
                      <span v-if="c.idleMs >= 1000"> · 已 {{ Math.floor(c.idleMs / 1000) }}s 无数据</span>
                      <span v-if="c.preemptCount > 0"> · 已抢占 {{ c.preemptCount }} 次</span>
                      <span v-if="c.preempting"> · 待换连接</span>
                      <span v-if="debugStuck(c)"> · 疑似卡死</span>
                    </div>
                    <div v-if="preemptNote && preemptNote.key === c.key" class="hint"
                         :class="{ errtext: preemptNote.err }" style="margin-top: 3px">
                      {{ preemptNote.msg }}
                    </div>
                  </div>
                  <!-- 手动强制重连：掐断当前连接、换新连接从已收字节续传（原版 ChunkRow 同款「重连」） -->
                  <button class="btn text small dbgpreempt" :disabled="preemptBusy === c.key"
                          title="掐断该分片当前连接，换新连接从已收字节续传"
                          @click="preemptChunk(c)">
                    {{ preemptBusy === c.key ? '重连中…' : '重连' }}
                  </button>
                </div>
              </div>
            </div>
            <div class="dbgcol dbgright">
              <div class="dbgsec">实时日志（主要变更节点 / 错误节点）</div>
              <div class="hint">
                共 {{ debugInfo.logTotal }} 条 · 每 {{ DEBUG_REFRESH_MS / 1000 }} 秒刷新 · 等宽字体对齐时间戳
              </div>
              <div v-if="debugInfo.logs.length === 0" class="hint" style="margin-top: 8px">
                暂无日志：该任务创建时「下载调试」未开启，或开关刚被关闭（关闭会清空日志）。
              </div>
              <div v-else ref="debugLogEl" class="dbglogs" @scroll="onDebugLogScroll">
                <div v-for="(line, i) in debugInfo.logs" :key="i" class="dbglogline">{{ line }}</div>
              </div>
            </div>
          </div>

          <!-- 导出结果：路径 + 打开目录 -->
          <div v-if="debugExportPath" class="row" style="margin-top: 10px; gap: 8px">
            <span class="hint dbgpath" :title="debugExportPath">{{ debugExportPath }}</span>
            <button class="btn text small" style="flex: none" @click="openDebugDir">打开目录</button>
          </div>
        </template>
      </div>
    </div>

    <!-- 速度详情弹窗（会话内采样：左轴速度折线+面积 / 右轴已下载阶梯线；纯手写 SVG，不引图表库） -->
    <div v-if="detailKey" class="mask" @click.self="closeDetail">
      <div class="dlg speeddlg">
        <div class="row" style="justify-content: space-between; gap: 10px; margin-bottom: 10px">
          <span class="dbgtitle" :title="detailRow?.fileName ?? ''">
            速度详情 · {{ detailRow?.fileName ?? '' }}
            <span v-if="detailRow" class="chip" :class="statusClass(detailRow.statusCode)"
                  style="margin-left: 8px; vertical-align: 1px">{{ detailRow.statusText }}</span>
          </span>
          <button class="btn text small" style="flex: none" @click="closeDetail">关闭</button>
        </div>

        <template v-if="detailRow">
          <!-- 指标行：总大小 / 已下载 / 平均速度 / 峰值速度（样本 max）/ 用时 -->
          <div class="speedmetrics">
            <span>总大小 <b>{{ detailRow.total > 0 ? fmtBytes(detailRow.total) : '—' }}</b></span>
            <span>已下载 <b>{{ fmtBytes(detailRow.downloaded) }}</b></span>
            <span>平均速度 <b>{{ fmtSpeed(avgSpeedOf(detailRow)) }}</b></span>
            <span>峰值速度 <b>{{ detailStats.peak > 0 ? fmtSpeed(detailStats.peak) : '—' }}</b></span>
            <span>用时 <b>{{ fmtRemain(detailDurationMs) }}</b></span>
          </div>

          <div class="speedlegend">
            <span><i class="lg-line" />速度（左轴）</span>
            <span><i class="lg-dash" />已下载（右轴 · 占总大小 %）</span>
            <span v-if="detailStats.count > 0" class="hint" style="margin: 0">已采样 {{ detailStats.count }} 点</span>
          </div>

          <svg v-if="detailChart" class="speedsvg" :viewBox="detailChart.viewBox" role="img" aria-label="速度与已下载量曲线图">
            <!-- 网格线 4 条：左刻度 = 速度档位，右刻度 = 下载量百分比（两轴共用同一套高度） -->
            <g v-for="(g, i) in detailChart.grid" :key="i">
              <line class="grid" :x1="detailChart.l" :x2="detailChart.r" :y1="g.y" :y2="g.y" />
              <text class="tick tl" :x="detailChart.l - 6" :y="g.y + 3">{{ g.speed }}</text>
              <text class="tick tr" :x="detailChart.r + 6" :y="g.y + 3">{{ g.pct }}</text>
            </g>
            <!-- X 轴基线 + 首中尾时间刻度 -->
            <line class="axis" :x1="detailChart.l" :x2="detailChart.r" :y1="detailChart.b" :y2="detailChart.b" />
            <text v-for="(xt, i) in detailChart.xTicks" :key="'x' + i" class="tick tx"
                  :x="xt.x" :y="detailChart.b + 15"
                  :text-anchor="i === 0 ? 'start' : i === detailChart.xTicks.length - 1 ? 'end' : 'middle'">
              {{ xt.label }}
            </text>
            <!-- 已下载量阶梯线（虚线灰，右轴）→ 速度面积 → 速度折线（主色，左轴）-->
            <path v-if="detailChart.dlPathD" class="dl" :d="detailChart.dlPathD" />
            <path class="area" :d="detailChart.areaPathD" />
            <path class="line" :d="detailChart.speedPathD" />
          </svg>
          <div v-else class="speedempty">
            <span>本页打开后未记录到速度样本</span>
            <span class="hint" style="margin: 0">速度为会话内采样（每 0.8 秒记录一次下载中的任务），应用重启后无历史</span>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.bar { height: 6px; border-radius: 3px; background: var(--surface-container-high); overflow: hidden; }
.bar i { display: block; height: 100%; background: var(--primary); border-radius: 3px; transition: width 0.4s ease; }
.chip.run { background: var(--primary-container); color: var(--on-primary-container); }
.chip.ok { background: color-mix(in srgb, var(--success) 18%, transparent); color: var(--success); }
.chip.err { background: var(--error-container); color: var(--error); }
.grouphdr { padding: 2px 4px; gap: 8px; }
.gdir { font-size: 12.5px; color: var(--on-bg-variant); font-weight: 600;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 普通任务来源徽标（跟在文件名后，aria2-next / FTP） */
.engchip { margin-left: 6px; font-size: 11px; padding: 1px 8px; vertical-align: 1px; }
.ck { width: 18px; height: 18px; border-radius: 4px; border: 2px solid var(--outline);
  cursor: pointer; flex: none; display: inline-flex; align-items: center; justify-content: center;
  color: var(--on-primary); }
.ck.on { background: var(--primary); border-color: var(--primary); }
/* 半选态（indeterminate）：实底 + 居中横杠（AppIcon 无减号图标，纯 CSS 画） */
.ck.half { background: var(--primary); border-color: var(--primary); }
.ck.half::after { content: ''; width: 10px; height: 2px; border-radius: 1px; background: var(--on-primary); }
/* 工具栏「全选」控件：复选框 + 文案 +「已选 N」计数（chip 风格，与行复选共用 .ck） */
.selall { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; flex: none;
  font-size: 13px; color: var(--on-bg-variant); user-select: none; }
.selall:hover { color: var(--on-bg); }
.selall:hover .ck { border-color: var(--primary); }
.selcnt { font-size: 11.5px; padding: 1px 8px; }

/* 列表工具栏：状态页签（胶囊选中态，对齐设置页 .seg / 侧栏 rail-pill 风格）+ 搜索框 + 排序下拉 */
.tabs { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
.tab { border: none; border-radius: 16px; padding: 5px 13px; font-size: 13px; cursor: pointer;
  background: transparent; color: var(--on-bg-variant);
  display: inline-flex; align-items: center; gap: 6px; }
.tab:hover { background: var(--surface-container); }
.tab.on { background: var(--primary-container); color: var(--on-primary-container); font-weight: 600; }
.tab .cnt { font-size: 11.5px; line-height: 17px; padding: 0 6px; border-radius: 9px;
  background: var(--surface-container-high); color: var(--on-bg-variant); font-weight: 500; }
.tab.on .cnt { background: color-mix(in srgb, var(--on-primary-container) 16%, transparent); }
.searchbox { position: relative; display: inline-flex; align-items: center; }
.searchbox input { border: 1px solid var(--outline-variant); border-radius: 16px; outline: none;
  background: var(--surface-container); color: var(--on-bg);
  padding: 5px 30px 5px 14px; font-size: 13px; width: 168px; }
.searchbox input:focus { border-color: var(--primary); box-shadow: 0 0 0 1px var(--primary); }
.searchbox .clear { position: absolute; right: 5px; width: 20px; height: 20px; border: none;
  border-radius: 50%; background: transparent; color: var(--on-bg-variant);
  font-size: 11px; cursor: pointer; display: inline-flex; align-items: center; justify-content: center; }
.searchbox .clear:hover { background: var(--surface-container-high); color: var(--on-bg); }
.sortsel { border: 1px solid var(--outline-variant); border-radius: 16px; outline: none;
  background: var(--surface); color: var(--on-bg); padding: 5px 8px; font-size: 13px; cursor: pointer; }
.sortsel:focus { border-color: var(--primary); }

/* ffmpeg 合轨阶段：进度条变橙 + 「合并中 X%」标签 */
.bar.mergebar i { background: #E8590C; }
.mergelabel { font-size: 12px; color: #E8590C; margin-top: 4px; }

/* 行内 ⋯ 菜单（复制直链/重新下载/打开文件） */
.rowmenu-wrap { position: relative; display: inline-flex; }
.rowmenu { position: absolute; right: 0; top: 100%; margin-top: 4px; z-index: 20;
  background: var(--surface); border: 1px solid var(--outline-variant); border-radius: 10px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.18); display: flex; flex-direction: column;
  min-width: 116px; overflow: hidden; }
.menuitem { border: none; background: transparent; text-align: left; padding: 8px 14px;
  font-size: 13px; cursor: pointer; color: var(--on-bg); white-space: nowrap; }
.menuitem:hover { background: var(--surface-container); }

/* 全部删除确认弹窗（.mask/.dlg 对齐 DirPickerModal） */
.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.42); display: flex;
  align-items: center; justify-content: center; z-index: 40; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(420px, 92vw); border: 1px solid var(--outline-variant); }
.btn.danger { background: var(--error); color: #fff; }

/* 完成后关机倒计时：盖过其它弹窗的全屏层 */
.shutdown-mask { z-index: 55; }
.shutdown-dlg { width: min(380px, 92vw); text-align: center; padding: 26px 24px; }
.shutdown-dlg .big { font-size: 44px; opacity: 0.65; margin-bottom: 8px; }
.cancel-shutdown { padding: 12px 30px; font-size: 15px; }

/* 下载调试弹窗：近全屏大卡片，正文左右分栏（左=资源+线程表，右=实时日志），各自独立滚动 */
.dbgdlg { width: min(1080px, 96vw); height: min(760px, 92vh); display: flex; flex-direction: column; overflow: hidden; }
.dbgtitle { font-weight: 600; font-size: 15px; min-width: 0; flex: 1;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dbgsummary { margin-bottom: 4px; }
.dbgcols { display: flex; gap: 14px; flex: 1; min-height: 0; margin-top: 10px; }
.dbgcol { flex: 1; min-width: 0; display: flex; flex-direction: column; min-height: 0; }
.dbgright { border-left: 1px solid var(--outline-variant); padding-left: 14px; }
.dbgsec { font-weight: 600; font-size: 13px; margin: 4px 0 6px; }
.dbgpanel { background: var(--surface-container); border-radius: 10px; padding: 10px 12px;
  display: flex; flex-direction: column; gap: 4px; }
.dbginforow { display: flex; gap: 10px; font-size: 12.5px; }
.dbginforow .k { color: var(--on-bg-variant); width: 60px; flex: none; }
.dbgchunks { overflow-y: auto; flex: 1; min-height: 0; display: flex; flex-direction: column;
  gap: 8px; padding: 6px 1px 8px; }
.dbgchunk { background: var(--surface-container); border-radius: 10px; padding: 8px 12px; flex: none;
  display: flex; align-items: center; gap: 8px; }
.dbgchunk.stuck { border: 1px solid var(--error); }
.dbgchunk-main { flex: 1; min-width: 0; }
.dbgpreempt { flex: none; }
.dbgchunk .hint { margin: 2px 0 0; }
.errtext { color: var(--error) !important; }
.dbglogs { flex: 1; min-height: 0; overflow-y: auto; margin-top: 6px;
  background: var(--surface-container); border-radius: 10px; padding: 10px 12px;
  font-family: ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace;
  font-size: 12px; line-height: 1.6; color: var(--on-bg-variant); }
.dbglogline { white-space: pre-wrap; word-break: break-all; }
.dbgpath { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 速度详情弹窗：指标行 + 图例 + 手写 SVG 双轴曲线图（速度=主色折线+面积，已下载=灰虚线阶梯） */
.speeddlg { width: min(720px, 94vw); }
.speedmetrics { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 16px;
  font-size: 12.5px; color: var(--on-bg-variant); margin-bottom: 8px; }
.speedmetrics b { color: var(--on-bg); font-weight: 600; }
.speedlegend { display: flex; align-items: center; gap: 16px; font-size: 12px;
  color: var(--on-bg-variant); margin-bottom: 6px; }
.lg-line, .lg-dash { display: inline-block; width: 20px; height: 0; vertical-align: middle;
  margin-right: 6px; border-top: 2px solid var(--primary); }
.lg-dash { border-top-style: dashed; border-top-color: var(--on-bg-variant); }
.speedsvg { width: 100%; height: auto; display: block; background: var(--surface-container);
  border-radius: 10px; }
.speedsvg .grid { stroke: var(--outline-variant); stroke-width: 1; stroke-dasharray: 3 3; opacity: 0.55; }
.speedsvg .axis { stroke: var(--outline-variant); stroke-width: 1; }
.speedsvg .tick { font-size: 10px; fill: var(--on-bg-variant); }
.speedsvg .tl { text-anchor: end; }
.speedsvg .tr { text-anchor: start; }
.speedsvg .line { fill: none; stroke: var(--primary); stroke-width: 2;
  stroke-linejoin: round; stroke-linecap: round; }
.speedsvg .area { fill: var(--primary); fill-opacity: 0.13; }
.speedsvg .dl { fill: none; stroke: var(--on-bg-variant); stroke-width: 1.5;
  stroke-dasharray: 5 4; opacity: 0.85; }
.speedempty { height: 200px; display: flex; flex-direction: column; gap: 6px;
  align-items: center; justify-content: center; text-align: center; padding: 0 24px;
  background: var(--surface-container); border-radius: 10px;
  color: var(--on-bg-variant); font-size: 13px; }
.pagerow { display: flex; align-items: center; justify-content: center; gap: 14px;
  padding: 8px 0 2px; color: var(--on-bg-variant); font-size: 13px; }
</style>
