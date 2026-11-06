<script setup lang="ts">
// 下载页：内置下载引擎的真实任务面板（对齐原版 DownloadScreen 的核心能力面）。
// 提交 → download_submit 立即返回；列表/进度 800ms 轮询 download_list（核心侧 500ms 落盘节流 + 内存实时速度）。
// 扩展：行复选多选批量操作 / 全局批量栏（全部暂停/开始/删除）/ 按目录分组 / 行内 ⋯ 菜单（复制直链/重新下载/打开文件）/
// ffmpeg 合轨进度（download_list stats.mergePercent 橙色条）/ 文件夹批量任务（share_folder_download 的 batch_list 轮询）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  batchList, downloadGetLink, downloadList, downloadListFull, downloadPause, downloadPauseAll,
  downloadRedownload, downloadRemove, downloadRemoveAll, downloadResume, downloadResumeAll,
  downloadSubmit, fmtBytes, fmtRemain, fmtSpeed, settingsList, settingsSet, shutdownNow,
  type BatchTask, type DownloadStats, type DownloadTask,
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
let timer: number | undefined

// download_list 的 stats（mergePercent>0 且 <100 = 有任务在 ffmpeg 合轨阶段）
const stats = ref<DownloadStats | null>(null)
const merging = computed(() => {
  const p = stats.value?.mergePercent ?? 0
  return p > 0 && p < 100 ? p : 0
})

// 多选批量（与网盘页共用 .ck 复选样式）
const selected = ref(new Set<number>())

// 按目录分组
const grouped = ref(false)

// 文件夹批量任务（batch_list 与任务轮询同频）
const batches = ref<BatchTask[]>([])

let prevTaskCount = 0
async function refresh() {
  try {
    const r = await downloadListFull()
    tasks.value = r.tasks ?? []
    stats.value = r.stats ?? null
    listError.value = ''
    // WebKitGTK 在内容高度骤减时可能不自动钳制滚动位置 → 手动重置
    if (tasks.value.length < prevTaskCount) {
      requestAnimationFrame(() => {
        const el = document.querySelector('.content')
        if (el) el.scrollTop = 0
      })
    }
    prevTaskCount = tasks.value.length
  } catch (e) {
    listError.value = String(e)
  }
  try {
    batches.value = await batchList()
  } catch { /* 批量接口失败不影响任务列表展示 */ }
}

async function submit() {
  if (!url.value.trim() || submitting.value) return
  submitting.value = true
  submitError.value = ''
  try {
    const r = await downloadSubmit(url.value.trim(), fileName.value.trim(), saveDir.value.trim())
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

function toggleSel(taskId: number) {
  if (selected.value.has(taskId)) selected.value.delete(taskId)
  else selected.value.add(taskId)
}

/** 批量操作：循环调单任务命令（单个失败继续处理其余），完成后刷新并清空选择 */
async function batchAct(kind: 'pause' | 'resume' | 'remove') {
  const ids = [...selected.value]
  for (const id of ids) {
    try {
      if (kind === 'pause') await downloadPause(id)
      else if (kind === 'resume') await downloadResume(id)
      else await downloadRemove(id, tasks.value.find((t) => t.taskId === id)?.status === 3)
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

async function pauseAll() {
  try {
    const r = await downloadPauseAll()
    showNotice(r.ok ? `已暂停 ${r.paused} 个任务` : r.error ?? '操作失败')
  } catch (e) { showNotice(String(e)) }
  setTimeout(refresh, 150)
}

async function resumeAll() {
  try {
    const r = await downloadResumeAll()
    showNotice(r.ok ? `已开始 ${r.resumed} 个任务` : r.error ?? '操作失败')
  } catch (e) { showNotice(String(e)) }
  setTimeout(refresh, 150)
}

// 全部删除确认弹窗（含「同时删除本地文件」）
const removeAllOpen = ref(false)
const removeAllDeleteLocal = ref(false)
const removeAllBusy = ref(false)

async function doRemoveAll() {
  removeAllBusy.value = true
  try {
    const r = await downloadRemoveAll(removeAllDeleteLocal.value)
    if (r.ok) {
      removeAllOpen.value = false
      showNotice(`已删除 ${r.removed} 个任务`)
    } else {
      showNotice(r.error ?? '删除失败')
    }
  } catch (e) { showNotice(String(e)) } finally {
    removeAllBusy.value = false
  }
  setTimeout(refresh, 150)
}

// ---- 行内 ⋯ 菜单：复制直链 / 重新下载（失败任务）/ 打开文件（已完成）----

const menuFor = ref(0)

function toggleMenu(taskId: number) {
  menuFor.value = menuFor.value === taskId ? 0 : taskId
}

async function copyLink(t: DownloadTask) {
  menuFor.value = 0
  try {
    const r = await downloadGetLink(t.taskId)
    if (r.ok && r.url) {
      await navigator.clipboard.writeText(r.url)
      showNotice(`直链已复制：${r.fileName || t.fileName}`)
    } else {
      showNotice(r.error ?? '取直链失败')
    }
  } catch (e) { showNotice(String(e)) }
}

async function redownload(t: DownloadTask) {
  menuFor.value = 0
  try {
    const r = await downloadRedownload(t.taskId)
    showNotice(r.ok ? `「${t.fileName}」已重新入队` : r.error ?? '重新下载失败')
  } catch (e) { showNotice(String(e)) }
  setTimeout(refresh, 150)
}

function openFile(t: DownloadTask) {
  menuFor.value = 0
  if (t.savePath) CoreService.OpenFile(t.savePath).catch((e) => console.warn('打开失败:', e))
}

function openDirectory(t: DownloadTask) {
  menuFor.value = 0
  if (t.savePath) CoreService.OpenDirectory(t.savePath).catch((e) => console.warn('打开目录失败:', e))
}

// 点击菜单外区域收起行菜单
function closeMenu(e: MouseEvent) {
  if (!menuFor.value) return
  if (!(e.target as HTMLElement).closest('.rowmenu-wrap')) menuFor.value = 0
}

function dirOf(t: DownloadTask): string {
  const p = t.savePath ?? ''
  if (!p) return ''
  const i = p.lastIndexOf('/')
  return i > 0 ? p.slice(0, i) : p
}

const groups = computed(() => {
  const m = new Map<string, DownloadTask[]>()
  for (const t of tasks.value) {
    const d = dirOf(t) || '（未知目录）'
    const list = m.get(d) ?? []
    list.push(t)
    m.set(d, list)
  }
  return [...m.entries()].map(([dir, list]) => ({ dir, list }))
})

/** 统一分段：分组=按目录切段（带标题），平铺=单段（无标题），行渲染只写一份 */
const sections = computed(() =>
  grouped.value ? groups.value : [{ dir: '', list: tasks.value }])

function batchPct(b: BatchTask): number {
  return b.total > 0 ? Math.min(100, Math.round((b.done / b.total) * 100)) : 0
}

function progress(t: DownloadTask): number {
  return t.total > 0 ? Math.min(100, Math.round((t.downloaded / t.total) * 100)) : 0
}

function statusClass(s: number): string {
  return s === 3 ? 'ok' : s === 4 ? 'err' : s === 1 ? 'run' : ''
}

/** 合轨进度挂到哪个任务上展示：优先下载中的任务，退而求任务列表第一个 */
const mergingTaskId = computed(() => {
  if (merging.value <= 0) return 0
  const active = tasks.value.find((t) => t.status === 1)
  return (active ?? tasks.value[0])?.taskId ?? 0
})

// ---- 完成后关机（对齐原版 shutdownWhenDownloadsComplete 设置）----
// 每 5 秒独立检查 download_list：所有任务均不在下载中（0/2/3/4）且至少 1 个已完成 → 弹 60 秒全屏倒计时；
// 倒计时结束调 CoreService.Shutdown 关机；取消按钮 / 新加任务 / 条件被打破（恢复任务等）都会终止倒计时。

const shutdownEnabled = ref(false)
const shutdownCountdown = ref(0)
let shutdownTimer: number | undefined
let shutdownCheckTimer: number | undefined

async function loadShutdownSetting() {
  try {
    const s = await settingsList()
    shutdownEnabled.value = s?.shutdownWhenDownloadsComplete ?? false
  } catch { /* 核心未就绪时保持关闭 */ }
}

async function toggleShutdown() {
  shutdownEnabled.value = !shutdownEnabled.value
  if (!shutdownEnabled.value) cancelShutdown()
  try { await settingsSet('shutdownWhenDownloadsComplete', shutdownEnabled.value) }
  catch { /* 写失败不打扰，下次进页重读 */ }
}

/** 每 5 秒的条件检查：满足且未在倒计时 → 启动倒计时；倒计时中条件被打破 → 自动取消 */
async function checkShutdown() {
  if (!shutdownEnabled.value) return
  const list = await downloadList().catch(() => [] as DownloadTask[])
  const settled = list.length > 0 && list.every((t) => [0, 2, 3, 4].includes(t.status))
  const hasDone = list.some((t) => t.status === 3)
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

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 800)
  document.addEventListener('click', closeMenu)
  loadShutdownSetting()
  shutdownCheckTimer = window.setInterval(checkShutdown, 5000)
})
onUnmounted(() => {
  window.clearInterval(timer)
  window.clearInterval(shutdownCheckTimer)
  stopCountdownTimer()
  document.removeEventListener('click', closeMenu)
})
</script>

<template>
  <div class="page">
    <div class="card">
      <h3>添加下载任务</h3>
      <p class="hint">直链（http/https）；网盘直链请先经解析获取。留空目录 = 使用默认下载目录</p>
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

    <!-- 全局批量栏：与勾选无关，任务存在即显示 -->
    <div v-if="tasks.length > 0" class="card" style="padding: 10px 20px">
      <div class="row" style="justify-content: space-between">
        <span style="font-weight: 600">全部任务（{{ tasks.length }}）</span>
        <span class="row">
          <span class="row" style="gap: 8px; font-size: 13px" title="全部任务结束后弹出倒计时关机">
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

    <!-- 列表控制：按目录分组 -->
    <div v-if="tasks.length > 0" class="card" style="padding: 10px 20px">
      <div class="row" style="justify-content: space-between">
        <span>按目录分组（savePath 所在目录）</span>
        <i class="switch" :class="{ on: grouped }" @click="grouped = !grouped" />
      </div>
    </div>

    <div v-if="listError" class="banner error">{{ listError }}</div>

    <div v-if="tasks.length === 0" class="card">
      <div class="empty">
        <div class="big">⬇️</div>
        <h3 style="margin: 0 0 6px">暂无下载任务</h3>
        <p>内置分片下载引擎（32 线程 / Range 分片 / 断点续传）已就绪，添加一个直链试试。</p>
      </div>
    </div>

    <!-- 任务列表：分组 / 平铺共用同一套行渲染 -->
    <template v-for="g in sections" :key="g.dir || '_flat'">
      <div v-if="grouped" class="row grouphdr">
        <span class="gdir" :title="g.dir">{{ g.dir }}</span>
        <span class="chip plain">{{ g.list.length }} 个任务</span>
      </div>
      <div v-for="t in g.list" :key="t.taskId" class="card" style="padding: 14px 20px">
        <div class="row" style="justify-content: space-between; margin-bottom: 8px">
          <span class="ck" :class="{ on: selected.has(t.taskId) }" title="选中"
                @click="toggleSel(t.taskId)">
            <AppIcon v-if="selected.has(t.taskId)" name="check" :size="13" />
          </span>
          <div style="min-width: 0; flex: 1">
            <div style="font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap"
                 :title="t.savePath ?? t.fileName">{{ t.fileName }}</div>
            <div style="font-size: 12px; color: var(--on-bg-variant)">
              {{ fmtBytes(t.downloaded) }}<span v-if="t.total > 0"> / {{ fmtBytes(t.total) }}</span>
              · {{ t.status === 1 ? fmtSpeed(t.speed) : `均速 ${fmtSpeed(t.avgSpeed)}` }}
              <span v-if="t.status === 1 && t.remainMillis > 0"> · 剩余 {{ fmtRemain(t.remainMillis) }}</span>
              <span v-if="t.platform"> · {{ t.platform }}</span>
            </div>
          </div>
          <div class="row" style="flex: none">
            <span class="chip" :class="statusClass(t.status)">{{ t.statusText }}</span>
            <button v-if="t.status === 1 || t.status === 0" class="btn tonal small" @click="act(() => downloadPause(t.taskId))">暂停</button>
            <button v-if="t.status === 2 || t.status === 4" class="btn tonal small" @click="act(() => downloadResume(t.taskId))">
              {{ t.status === 4 ? '重试' : '继续' }}
            </button>
            <button class="btn text small" @click="act(() => downloadRemove(t.taskId, t.status === 3))">删除</button>
            <span class="rowmenu-wrap">
              <button class="btn text small" title="更多操作" @click.stop="toggleMenu(t.taskId)">⋯</button>
              <div v-if="menuFor === t.taskId" class="rowmenu">
                <button class="menuitem" @click="copyLink(t)">复制直链</button>
                <button v-if="t.status === 4" class="menuitem" @click="redownload(t)">重新下载</button>
                <button v-if="t.status === 3 && t.savePath" class="menuitem" @click="openDirectory(t)">打开目录</button>
                <button v-if="t.status === 3 && t.savePath" class="menuitem" @click="openFile(t)">打开文件</button>
              </div>
            </span>
          </div>
        </div>
        <div v-if="t.total > 0" class="bar" :class="{ mergebar: mergingTaskId === t.taskId }">
          <i :style="{ width: (mergingTaskId === t.taskId ? merging : progress(t)) + '%' }" />
        </div>
        <div v-if="mergingTaskId === t.taskId" class="mergelabel">合并中 {{ merging }}%</div>
        <div v-if="t.error" class="banner error" style="margin-top: 8px">{{ t.error }}</div>
      </div>
    </template>

    <!-- 全部删除确认弹窗（.mask/.dlg 模式，对齐 DirPickerModal） -->
    <div v-if="removeAllOpen" class="mask" @click.self="removeAllOpen = false">
      <div class="dlg">
        <h3 style="margin: 0 0 6px">删除全部任务？</h3>
        <p class="hint">将从下载列表移除全部 {{ tasks.length }} 个任务（不含批量任务批次）</p>
        <div class="row" style="margin-bottom: 12px">
          <span class="ck" :class="{ on: removeAllDeleteLocal }" title="同时删除本地文件"
                @click="removeAllDeleteLocal = !removeAllDeleteLocal">
            <AppIcon v-if="removeAllDeleteLocal" name="check" :size="13" />
          </span>
          <span>同时删除本地文件</span>
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
.ck { width: 18px; height: 18px; border-radius: 4px; border: 2px solid var(--outline);
  cursor: pointer; flex: none; display: inline-flex; align-items: center; justify-content: center;
  color: var(--on-primary); }
.ck.on { background: var(--primary); border-color: var(--primary); }

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
</style>
