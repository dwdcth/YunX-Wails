<script setup lang="ts">
// 解析页：对齐原版 ResolveScreen + ShareDetailScreen 的主流程。
// share_resolve 建会话并返回根目录 → share_list 翻目录 → share_download 取直链入队下载（带平台请求头）。
// GitHub releases 链接走 github_resolve 分支；目录行支持「下载整个文件夹」（share_folder_download 批量入队）。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  bookmarkAdd, downloadSubmit, fmtBytes, githubDownload, githubResolve, isNormalDownloadLink,
  linkHistoryAdd, linkHistoryClear, linkHistoryDelete, linkHistoryList, normalDownloadSubmit,
  shareDownload, shareFolderDownload, shareLink, shareList, shareResolve, shareTransfer,
  shareTransferBatch,
  type GithubAsset, type LinkHistoryItem, type ShareDetail, type ShareFile,
} from '../core'
import AppIcon from '../components/AppIcon.vue'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'
import DirPickerModal from '../components/DirPickerModal.vue'
import { Events } from '/wails/runtime.js'

const link = ref('')
const pwd = ref('')
const loading = ref(false)
const error = ref('')

const router = useRouter()

const detail = ref<ShareDetail | null>(null)
const dirStack = ref<{ fid: string; name: string }[]>([])
const listLoading = ref(false)
const toast = ref('')

const history = ref<LinkHistoryItem[]>([])
const transferring = ref<ShareFile | null>(null)

// GitHub Release 详情（命中 github.com/<owner>/<repo>/releases 时替代网盘分享详情）
const gh = ref<{ url: string; repo: string; tag: string; assets: GithubAsset[] } | null>(null)

// ---- 普通下载分流（ftp/磁力/ed2k/bc1/种子/Metalink → NormalDownloadSubmit，aria2-next / 内置 FTP）----

// 纯 http(s) 直链的「不是网盘分享，是否作为普通下载？」建议（不自动提交，用户点按钮才入队）
const normalSuggest = ref('')
// 上面场景里分享解析给出的失败原因（卡片里附带展示，便于分辨「不是分享」与「解析出错」）
const normalSuggestErr = ref('')
// 提交成功后的顶部提示（带「去下载页」跳转，30 秒自动消失）
const normalNotice = ref('')
const normalBusy = ref(false)

/** 同名文件确认弹窗状态 */
const existsConfirm = ref<{ source: string; name: string; path: string } | null>(null)

async function onExistsChoice(choice: 'overwrite' | 'rename' | 'skip') {
  const c = existsConfirm.value
  if (!c) return
  existsConfirm.value = null
  if (choice === 'skip') { flash('已跳过'); return }
  normalBusy.value = true
  try {
    const r = await normalDownloadSubmit(c.source, '', choice)
    if (r.ok && !r.exists) {
      const which = r.taskIds && r.taskIds.length > 1 ? `${r.taskIds.length} 个任务` : `任务 #${r.taskId ?? ''}`
      flash(`已加入普通下载（${which}），进度见下载页`)
      normalNotice.value = r.name || c.source
      setTimeout(() => (normalNotice.value = ''), 30_000)
    } else {
      error.value = r.error ?? '提交失败'
    }
  } catch (e) {
    error.value = String(e)
  } finally {
    normalBusy.value = false
  }
}

/** 提交普通下载：成功 toast + 顶部可跳下载页提示 */
async function submitNormal(source: string) {
  if (normalBusy.value) return
  normalBusy.value = true
  error.value = ''
  try {
    const r = await normalDownloadSubmit(source, '')
    if (r.exists && r.existingPath) {
      existsConfirm.value = { source, name: r.name || source, path: r.existingPath }
      return
    }
    if (r.skipped) { flash('文件已存在，已跳过'); return }
    if (r.ok) {
      const which = r.taskIds && r.taskIds.length > 1
        ? `${r.taskIds.length} 个任务` : `任务 #${r.taskId ?? ''}`
      flash(`已加入普通下载（${which}），进度见下载页`)
      normalNotice.value = r.name || source
      setTimeout(() => (normalNotice.value = ''), 30_000)
    } else {
      error.value = r.error ?? '提交普通下载失败'
    }
  } catch (e) {
    error.value = String(e)
  } finally {
    normalBusy.value = false
  }
}

// ---- 磁力下载（迅雷式）：先取元数据 → 用户选文件/确认文件名 → 再开始 ----

interface MagnetFile { index: number; path: string; length: number }
interface MagnetInfo { resolveId: string; name: string; files: MagnetFile[]; totalLength: number; singleFile: boolean }

const magnetOpen = ref(false)
const magnetPhase = ref<'resolving' | 'ready' | 'error'>('resolving')
const magnetUrl = ref('')
const magnetInfo = ref<MagnetInfo | null>(null)
const magnetError = ref('')
const magnetSel = ref<Set<number>>(new Set())
const magnetName = ref('') // 单文件可改（aria2 out）；多文件只读（aria2 改不了根目录名）
const magnetSaveDir = ref('')
const magnetBusy = ref(false)

function openMagnetDialog(url: string) {
  magnetUrl.value = url
  magnetPhase.value = 'resolving'
  magnetError.value = ''
  magnetInfo.value = null
  magnetOpen.value = true
  void resolveMagnet()
}

async function resolveMagnet() {
  magnetPhase.value = 'resolving'
  magnetError.value = ''
  try {
    const r = JSON.parse(await CoreService.MagnetResolve(magnetUrl.value))
    if (!r.ok) {
      magnetError.value = r.error ?? '元数据获取失败'
      magnetPhase.value = 'error'
      return
    }
    magnetInfo.value = r
    magnetSel.value = new Set(r.files.map((f: MagnetFile) => f.index))
    magnetName.value = r.name
    magnetPhase.value = 'ready'
  } catch (e) {
    magnetError.value = String(e)
    magnetPhase.value = 'error'
  }
}

const magnetSelTotal = computed(() =>
  magnetInfo.value?.files.filter((f) => magnetSel.value.has(f.index)).reduce((a, f) => a + f.length, 0) ?? 0)

function toggleMagnetFile(i: number) {
  const next = new Set(magnetSel.value)
  if (next.has(i)) next.delete(i)
  else next.add(i)
  magnetSel.value = next
}

async function confirmMagnet() {
  if (!magnetInfo.value || magnetBusy.value) return
  const selected = [...magnetSel.value].sort((a, b) => a - b)
  if (selected.length === 0) { flash('请至少选择一个文件'); return }
  magnetBusy.value = true
  try {
    const r = JSON.parse(await CoreService.MagnetSubmit(
      magnetInfo.value.resolveId, magnetSaveDir.value.trim(), selected,
      magnetInfo.value.singleFile ? magnetName.value.trim() : ''))
    if (r.ok) {
      flash(`「${r.name}」已加入下载（任务 #${r.taskId}），进度见下载页`)
      normalNotice.value = r.name || magnetUrl.value
      setTimeout(() => (normalNotice.value = ''), 30_000)
      magnetOpen.value = false
    } else {
      magnetError.value = r.error ?? '提交失败'
      magnetPhase.value = 'error'
    }
  } catch (e) {
    magnetError.value = String(e)
    magnetPhase.value = 'error'
  } finally {
    magnetBusy.value = false
  }
}

/** 本地种子/Metalink 文件：WebKit 拿不到原生路径 → 读内容 base64 以 `base64:文件名|<b64>` 提交 */
function pickLocalTorrent(ev: Event) {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  input.value = '' // 允许重复选同一文件
  if (!f) return
  const reader = new FileReader()
  reader.onload = () => {
    const b64 = arrayBufferToBase64(reader.result as ArrayBuffer)
    submitNormal(`base64:${f.name}|${b64}`)
  }
  reader.onerror = () => (error.value = `读取文件失败：${f.name}`)
  reader.readAsArrayBuffer(f)
}

function arrayBufferToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let bin = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(bin)
}

// 剪贴板分享链接建议（Go 宿主事件，对齐原版 ClipboardLinkPopup）
const clipboardLink = ref('')

// GitHub releases 页链接（owner/repo 两段路径）
const GITHUB_RE = /^https?:\/\/github\.com\/[^/\s]+\/[^/\s]+\/releases/i

// 视频链接分流（对齐原版 VideoLinkParser.looksLikeVideoLink）：命中即跳视频解析页
const VIDEO_DOMAINS = ['bilibili.com', 'b23.tv', 'xiaohongshu.com', 'xhslink.com', 'kuaishou.com', 'weibo.com', 'weibo.cn', 'acfun.cn', 'pixiv.net']

function isVideoLink(v: string): boolean {
  return VIDEO_DOMAINS.some((d) => v.includes(d))
}

function useClipboardLink() {
  const v = clipboardLink.value
  clipboardLink.value = ''
  link.value = v
  parse(v)
}

/** 剪贴板命中视频域名：跳视频页接力解析（Go 侧剪贴板事件不区分类型，前端补偿分流） */
function useClipboardVideo() {
  const v = clipboardLink.value
  clipboardLink.value = ''
  localStorage.setItem('yunx.pendingVideo', v)
  router.push('/video')
}


async function doTransfer(targetFid: string) {
  const f = transferring.value
  if (!f || !detail.value) return
  transferring.value = null
  try {
    const r = await shareTransfer(f, targetFid)
    flash(r.ok ? `「${f.fname}」已转存` : `转存失败：${r.error}`)
  } catch (e) {
    flash(`转存失败：${e}`)
  }
}

// ---- 直链弹窗（share_link 现取直链，可手动选目录后 download_submit 入队）----

const linkInfo = ref<{ f: ShareFile; url: string; filename: string; size: number; isHls: boolean } | null>(null)
const linkSaveDir = ref('')
const linkBusy = ref(false)
const linkError = ref('')

async function openLinkDialog(f: ShareFile) {
  if (linkBusy.value) return
  linkBusy.value = true
  linkError.value = ''
  try {
    const r = await shareLink(f)
    if (r.ok && r.url) {
      linkInfo.value = {
        f, url: r.url, filename: r.filename || f.fname,
        size: r.size || f.fsize, isHls: !!r.isHls,
      }
      linkSaveDir.value = ''
    } else {
      error.value = r.error ?? '取直链失败'
    }
  } catch (e) {
    error.value = String(e)
  } finally {
    linkBusy.value = false
  }
}

async function copyDirectLink() {
  if (!linkInfo.value) return
  // 复制失败（如非安全上下文）只影响剪贴板，URL 仍展示在弹窗输入框里
  try {
    await navigator.clipboard.writeText(linkInfo.value.url)
    flash('直链已复制')
  } catch {
    flash('复制失败，请手动选择输入框内容复制')
  }
}

async function downloadViaLink() {
  if (!linkInfo.value || linkBusy.value) return
  linkBusy.value = true
  linkError.value = ''
  try {
    const r = await downloadSubmit(linkInfo.value.url, linkInfo.value.filename, linkSaveDir.value.trim())
    if (r.ok) {
      flash(`「${linkInfo.value.filename}」已加入下载（任务 #${r.taskId}），进度见下载页`)
      linkInfo.value = null
    } else {
      linkError.value = r.error ?? '下载失败'
    }
  } catch (e) {
    linkError.value = String(e)
  } finally {
    linkBusy.value = false
  }
}

// ---- 多选批量转存（复选框与下载页/网盘页同款 .ck 样式）----

const selected = ref(new Set<string>())
const batchTransferring = ref(false)
const batchBusy = ref(false)

const selFiles = computed(() => detail.value?.files.filter((f) => selected.value.has(f.fid)) ?? [])

function toggleSel(fid: string) {
  if (selected.value.has(fid)) selected.value.delete(fid)
  else selected.value.add(fid)
}

async function doBatchTransfer(targetFid: string) {
  const files = selFiles.value
  batchTransferring.value = false
  if (!files.length || batchBusy.value) return
  batchBusy.value = true
  try {
    const r = await shareTransferBatch(files, targetFid)
    selected.value.clear()
    flash(r.failCount === 0
      ? `已转存 ${r.okCount} 个文件`
      : `转存完成：成功 ${r.okCount} · 失败 ${r.failCount}${r.lastError ? `（${r.lastError}）` : ''}`)
  } catch (e) {
    error.value = String(e) // 整批中止（如剩余空间不足）：保持勾选，用户可处理后重试
  } finally {
    batchBusy.value = false
  }
}

/** 文件夹批量下载：整目录入队为一个批次，进度见下载页「批量任务」卡 */
async function downloadFolder(f: ShareFile) {
  try {
    const r = await shareFolderDownload(f.fid, f.fname)
    if (r.ok) flash(`文件夹「${f.fname}」已创建批量任务（批次 ${r.batchId ?? ''}），进度见下载页`)
    else error.value = r.error ?? '批量下载失败'
  } catch (e) {
    error.value = String(e)
  }
}

async function refreshHistory() {
  try { history.value = await linkHistoryList() } catch { /* 网络外错误忽略 */ }
}

// ---- 解析历史弹窗（对齐原版 LinkHistoryDialog：搜索 / 重解析 / 单条删除 / 清空）----

const historyOpen = ref(false)
const historyQuery = ref('')

/** 本地实时过滤（链接 / 标题 / 平台，忽略大小写），比走 DAO search 更跟手 */
const filteredHistory = computed(() => {
  const q = historyQuery.value.trim().toLowerCase()
  if (!q) return history.value
  return history.value.filter((h) =>
    h.url.toLowerCase().includes(q) ||
    h.title.toLowerCase().includes(q) ||
    h.platform.toLowerCase().includes(q))
})

function openHistory() {
  historyQuery.value = ''
  historyOpen.value = true
  refreshHistory()
}

/** 历史时间显示（对齐原版 SimpleDateFormat("MM-dd HH:mm")） */
function fmtHistoryTime(ts: number): string {
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

async function deleteHistory(h: LinkHistoryItem) {
  try {
    await linkHistoryDelete(h.id)
    await refreshHistory()
  } catch { /* 删除失败下次刷新自然可见 */ }
}

async function clearHistory() {
  if (!confirm('确定清空全部解析历史？')) return
  try {
    await linkHistoryClear()
    await refreshHistory()
  } catch { /* 同上 */ }
}

function flash(msg: string) {
  toast.value = msg
  setTimeout(() => (toast.value = ''), 2600)
}

/** GitHub 分支：github_resolve 列出 Release 附件，跳过 share_resolve */
async function parseGithub(url: string) {
  gh.value = null
  try {
    const r = await githubResolve(url)
    if (r.ok) {
      gh.value = { url, repo: r.repo ?? '', tag: r.tag ?? '', assets: r.assets ?? [] }
      flash(`GitHub Release：${gh.value.repo} ${gh.value.tag} · ${gh.value.assets.length} 个附件`)
      recordGithubHistory(url, gh.value.repo)
    } else {
      error.value = r.error ?? 'GitHub 解析失败'
    }
  } catch (e) {
    error.value = String(e)
  }
}

/** GitHub 解析核心不自动记历史（share_resolve 才记），前端补记（同 URL 1 小时内不重复，对齐核心去重口径） */
async function recordGithubHistory(url: string, title: string) {
  try {
    const last = history.value.find((h) => h.url === url)
    if (last && Date.now() - last.createTime < 3_600_000) return
    await linkHistoryAdd(url, title, 'GITHUB')
    refreshHistory()
  } catch { /* 记历史失败不影响解析 */ }
}

async function parse(text?: string, pwdOverride?: string) {
  const url = (text ?? link.value).trim()
  // 自动从文案提取提取码（对齐原版 ResolveScreen:192-196）
  if (!pwd.value.trim()) {
    const m = url.match(/提取码[:：\s]*([a-zA-Z0-9]{4})/)
    if (m) pwd.value = m[1]
  }
  const p = (pwdOverride ?? pwd.value).trim()
  if (!url || loading.value) return
  // 视频链接分流（对齐原版 VideoLinkParser.looksLikeVideoLink）：视频页经 pendingVideo 接力自动解析
  if (isVideoLink(url)) {
    localStorage.setItem('yunx.pendingVideo', url)
    router.push('/video')
    return
  }
  loading.value = true
  error.value = ''
  detail.value = null
  gh.value = null
  normalSuggest.value = ''
  normalSuggestErr.value = ''
  try {
    // GitHub releases 链接：走 github_resolve，不走网盘分享会话
    if (GITHUB_RE.test(url)) {
      await parseGithub(url)
      return
    }
    const r = await shareResolve(url, p)
    if (!r.ok) {
      // 分享解析没接住（found=false）→ 普通链接兜底（不打架：只在解析失败分支出现）。
      // ftp/磁力/ed2k/bc1 与 .torrent/.metalink 后缀是明确普通链接 → 直接提交；
      // 纯 http(s) 直链 → 只给「直接下载」按钮，不自动提交。
      if (/^magnet:/i.test(url)) {
        openMagnetDialog(url) // 迅雷式：先解析元数据让用户选文件，不再直接入队
        return
      }
      if (isNormalDownloadLink(url)) {
        await submitNormal(url)
      } else if (/^https?:\/\//i.test(url)) {
        normalSuggest.value = url
        normalSuggestErr.value = r.error ?? ''
      } else {
        error.value = r.error ?? '解析失败'
      }
    } else {
      detail.value = r
      dirStack.value = [{ fid: r.dirFid, name: r.title || '根目录' }]
      refreshHistory()
    }
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
}

/** GitHub 附件下载：url 传 Release 页链接（与 github_resolve 同参），fileName 取附件名 */
async function ghDownload(a: GithubAsset) {
  if (!gh.value) return
  try {
    const r = await githubDownload(gh.value.url, a.name)
    if (r.ok) flash(`「${a.name}」已加入下载（任务 #${r.taskId}），进度见下载页`)
    else error.value = r.error ?? '下载失败'
  } catch (e) {
    error.value = String(e)
  }
}

async function openDir(f: ShareFile) {
  if (!detail.value || listLoading.value) return
  listLoading.value = true
  selected.value.clear() // 换目录后旧选择失效
  try {
    const r = await shareList(f.fid)
    if (r.ok && r.files) {
      if (detail.value) detail.value.files = r.files
      dirStack.value.push({ fid: f.fid, name: f.fname })
    } else {
      error.value = r.error ?? '进入目录失败'
    }
  } finally {
    listLoading.value = false
  }
}

async function backTo(level: number) {
  // level = 目标层（0=根）；与原版 goBack/navigateToLevel 对应
  if (!detail.value || listLoading.value || level >= dirStack.value.length - 1) {
    if (level >= dirStack.value.length - 1) return
  }
  listLoading.value = true
  selected.value.clear() // 换目录后旧选择失效
  try {
    const target = dirStack.value[level]
    const r = await shareList(target.fid)
    if (r.ok && r.files) {
      if (detail.value) detail.value.files = r.files
      dirStack.value = dirStack.value.slice(0, level + 1)
    }
  } finally {
    listLoading.value = false
  }
}

async function download(f: ShareFile) {
  try {
    const r = await shareDownload(f)
    if (r.ok) {
      flash(`「${r.filename}」已加入下载（任务 #${r.taskId}），进度见下载页`)
    } else {
      error.value = r.error ?? '下载失败'
    }
  } catch (e) {
    error.value = String(e)
  }
}

async function saveBookmark() {
  if (!detail.value) return
  try {
    await bookmarkAdd(link.value.trim() || location.hash, detail.value.title, detail.value.platform, pwd.value.trim())
    flash('已收藏')
  } catch (e) {
    flash(`收藏失败：${e}`)
  }
}

async function useHistory(h: LinkHistoryItem) {
  historyOpen.value = false
  link.value = h.url
  pwd.value = h.pwd
  parse(h.url, h.pwd)
}

// 书签页「重解析」的接力：取 pendingResolve 自动填链并解析
async function checkPendingResolve() {
  const pending = localStorage.getItem('yunx.pendingResolve')
  if (!pending) return
  localStorage.removeItem('yunx.pendingResolve')
  link.value = pending
  parse(pending)
}

onMounted(() => {
  refreshHistory()
  checkPendingResolve()
})

// 剪贴板事件订阅（同一链接只提示一次）
let lastClip = ''
Events.On('clipboard:link', (data: unknown) => {
  const link = typeof data === 'string' ? data : String((data as { data?: string })?.data ?? '')
  if (!link || link === lastClip) return
  lastClip = link
  clipboardLink.value = link
  setTimeout(() => (clipboardLink.value = ''), 30_000)
})
</script>

<template>
  <div class="page">
    <div class="card">
      <h3>解析分享链接</h3>
      <p class="hint">支持夸克 / UC / 迅雷 / 百度 / 139 / 123 / 115 / 光鸭 / 蓝奏云（优享版），未登录也可解析与部分平台下载；
        磁力 / BT 种子 / ed2k / Metalink / FTP 及 .torrent 链接自动转入普通下载（aria2-next）</p>
      <div style="display: flex; flex-direction: column; gap: 10px">
        <div class="row">
          <div class="field" style="flex: 1; min-width: 260px">
            <label>分享链接</label>
            <input v-model="link" class="input" placeholder="https://pan.quark.cn/s/xxxxxxxx"
                   @keydown.enter="parse()" />
          </div>
          <label class="btn tonal" style="align-self: flex-end; cursor: pointer" title="选择本地 .torrent / .metalink / .meta4 文件">
            本地种子/Metalink
            <input type="file" accept=".torrent,.metalink,.meta4,.metalink3"
                   style="display: none" @change="pickLocalTorrent" />
          </label>
          <button class="btn" style="align-self: flex-end" :disabled="!link.trim() || loading"
                  @click="parse()">
            {{ loading ? '解析中…' : '解析' }}
          </button>
          <button class="btn tonal" style="align-self: flex-end" title="查看解析历史"
                  @click="openHistory">
            <AppIcon name="history" :size="15" /> 历史
          </button>
        </div>
        <div class="row">
          <div class="field" style="flex: 1; min-width: 160px">
            <label>提取码（文案里自带则不用填）</label>
            <input v-model="pwd" class="input" placeholder="如 a1b2" @keydown.enter="parse()" />
          </div>
        </div>
      </div>
      <div v-if="toast" class="banner" style="margin-top: 10px">{{ toast }}</div>
      <!-- 普通下载提交成功：顶部提示可跳下载页（30 秒自动消失） -->
      <div v-if="normalNotice" class="row banner" style="margin-top: 10px; gap: 8px">
        <span style="flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap"
              :title="normalNotice">⬇️ 已加入普通下载：{{ normalNotice }}</span>
        <button class="btn tonal small" style="flex: none" @click="router.push('/download')">去下载页</button>
        <button class="btn text small" style="flex: none" @click="normalNotice = ''">关闭</button>
      </div>
      <div v-if="clipboardLink && isVideoLink(clipboardLink)" class="row banner" style="margin-top: 10px; gap: 8px">
        <span style="flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap"
              :title="clipboardLink">📋 剪贴板检测到视频链接：{{ clipboardLink }}</span>
        <button class="btn tonal small" @click="useClipboardVideo">去视频页解析</button>
        <button class="btn text small" @click="clipboardLink = ''">忽略</button>
      </div>
      <div v-else-if="clipboardLink" class="row banner" style="margin-top: 10px; gap: 8px">
        <span style="flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap"
              :title="clipboardLink">📋 剪贴板检测到分享链接：{{ clipboardLink }}</span>
        <button class="btn tonal small" @click="useClipboardLink">解析它</button>
        <button class="btn text small" @click="clipboardLink = ''">忽略</button>
      </div>
    </div>

    <div v-if="error" class="banner error">{{ error }}</div>

    <!-- 纯 http(s) 直链兜底：分享解析没接住时问一句，不自动提交 -->
    <div v-if="normalSuggest" class="card">
      <div class="row" style="justify-content: space-between; gap: 10px">
        <div style="min-width: 0; flex: 1">
          <b style="font-size: 14px">不是网盘分享，是否作为普通下载？</b>
          <div class="hint" style="margin-top: 4px; overflow: hidden; text-overflow: ellipsis;
                                   white-space: nowrap" :title="normalSuggest">{{ normalSuggest }}</div>
          <div v-if="normalSuggestErr" class="hint" style="margin-top: 2px">
            分享解析：{{ normalSuggestErr }}（若这其实是个文件直链，可用普通下载）
          </div>
        </div>
        <span class="row" style="flex: none">
          <button class="btn text small" @click="normalSuggest = ''">忽略</button>
          <button class="btn tonal small" :disabled="normalBusy"
                  @click="submitNormal(normalSuggest); normalSuggest = ''">
            {{ normalBusy ? '提交中…' : '直接下载' }}
          </button>
        </span>
      </div>
      <p class="hint" style="margin: 8px 0 0">经 aria2-next 引擎下载（HTTP 直链），与网盘任务并列显示在下载页</p>
    </div>

    <!-- GitHub Release 详情（github_resolve 分支，替代网盘分享详情） -->
    <div v-if="gh" class="card">
      <div class="row" style="justify-content: space-between; margin-bottom: 6px">
        <div class="row" style="gap: 8px">
          <span class="chip">github</span>
          <b style="font-size: 15px">{{ gh.repo }}</b>
          <span class="chip plain">{{ gh.tag }}</span>
        </div>
        <button class="btn text small" @click="gh = null">关闭</button>
      </div>
      <div v-if="gh.assets.length === 0" class="empty"><p>该 Release 没有附件</p></div>
      <div v-for="a in gh.assets" :key="a.url" class="filerow">
        <span class="ficon"><AppIcon name="file" /></span>
        <span class="fname" :title="a.name">{{ a.name }}</span>
        <span class="fmeta">{{ fmtBytes(a.size) }}</span>
        <button class="btn tonal small" @click="ghDownload(a)">下载</button>
      </div>
    </div>

    <div v-if="detail" class="card">
      <div class="row" style="justify-content: space-between; margin-bottom: 6px">
        <div class="row" style="gap: 8px">
          <span class="chip">{{ detail.platform }}</span>
          <b style="font-size: 15px">{{ detail.title }}</b>
          <span v-if="detail.guest" class="chip plain">游客模式</span>
        </div>
        <span class="row">
          <button class="btn text small" @click="saveBookmark">收藏</button>
          <button class="btn text small" @click="detail = null">关闭</button>
        </span>
      </div>

      <div class="row crumbs" style="margin: 6px 0 10px">
        <template v-for="(d, i) in dirStack" :key="d.fid + i">
          <button class="crumb" :disabled="i === dirStack.length - 1" @click="backTo(i)">
            {{ d.name || '根目录' }}
          </button>
          <span v-if="i < dirStack.length - 1" class="sep">/</span>
        </template>
      </div>

      <!-- 多选批量栏：勾选任意文件后出现 -->
      <div v-if="selected.size > 0" class="row" style="justify-content: space-between; margin-bottom: 10px">
        <span style="font-weight: 600">已选 {{ selected.size }} 项</span>
        <span class="row">
          <button class="btn tonal small" :disabled="batchBusy" @click="batchTransferring = true">批量转存</button>
          <button class="btn text small" @click="selected.clear()">取消选择</button>
        </span>
      </div>

      <div v-if="listLoading" class="banner">加载目录…</div>
      <div v-else-if="detail.files.length === 0" class="empty">
        <p>目录为空</p>
      </div>
      <div v-for="f in detail.files" :key="f.fid" class="filerow">
        <span class="ck" :class="{ on: selected.has(f.fid) }" title="选中" @click="toggleSel(f.fid)">
          <AppIcon v-if="selected.has(f.fid)" name="check" :size="13" />
        </span>
        <span class="ficon"><AppIcon :name="f.isdir ? 'cloud' : 'file'" /></span>
        <span class="fname" :title="f.fname" @click="f.isdir && openDir(f)">{{ f.fname }}</span>
        <span class="fmeta">{{ f.isdir ? '目录' : fmtBytes(f.fsize) }}</span>
        <template v-if="f.isdir">
          <button class="btn text small" @click="openDir(f)">打开</button>
          <button class="btn tonal small" @click="downloadFolder(f)">下载整个文件夹</button>
        </template>
        <template v-else>
          <button class="btn tonal small" @click="download(f)">下载</button>
          <button class="btn text small" :disabled="linkBusy" title="取直链（可复制或手动选目录下载）"
                  @click="openLinkDialog(f)">直链</button>
          <button class="btn text small" @click="transferring = f">转存</button>
        </template>
      </div>
    </div>

    <DirPickerModal v-if="transferring && detail" :platform="detail.platform.toLowerCase()"
                    title="转存到哪个文件夹？" @confirm="doTransfer" @cancel="transferring = null" />
    <DirPickerModal v-else-if="batchTransferring && detail" :platform="detail.platform.toLowerCase()"
                    title="批量转存到哪个文件夹？" @confirm="doBatchTransfer" @cancel="batchTransferring = false" />

    <!-- 直链弹窗：share_link 现取直链，可复制或手动选目录入队 -->
    <div v-if="linkInfo" class="mask" @click.self="linkInfo = null">
      <div class="dlg">
        <div class="row" style="justify-content: space-between; margin-bottom: 6px">
          <h3 style="margin: 0">直链下载</h3>
          <button class="btn text small" @click="linkInfo = null">关闭</button>
        </div>
        <p class="hint" style="margin-bottom: 10px">
          {{ linkInfo.filename }} · {{ fmtBytes(linkInfo.size) }}
          <span v-if="linkInfo.isHls" class="chip plain" style="margin-left: 6px">HLS</span>
        </p>
        <div class="row" style="margin-bottom: 12px; gap: 8px">
          <input class="input" style="flex: 1; min-width: 0" :value="linkInfo.url" readonly
                 @focus="($event.target as HTMLInputElement).select()" />
          <button class="btn tonal small" @click="copyDirectLink">复制</button>
        </div>
        <div class="field" style="margin-bottom: 12px">
          <label>保存目录（空 = 默认下载目录）</label>
          <input v-model="linkSaveDir" class="input" placeholder="/home/xxx/Downloads"
                 @keydown.enter="downloadViaLink" />
        </div>
        <div v-if="linkError" class="banner error" style="margin-bottom: 10px">{{ linkError }}</div>
        <div class="row end">
          <button class="btn" :disabled="linkBusy" @click="downloadViaLink">
            {{ linkBusy ? '提交中…' : '加入下载' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 解析历史弹窗（对齐原版 LinkHistoryDialog：搜索 / 重解析 / 单条删除 / 清空） -->
    <div v-if="historyOpen" class="mask" @click.self="historyOpen = false">
      <div class="dlg dlg-wide">
        <div class="row" style="justify-content: space-between; margin-bottom: 10px">
          <h3 style="margin: 0"><AppIcon name="history" :size="16" /> 解析历史</h3>
          <button class="btn text small" @click="historyOpen = false">关闭</button>
        </div>

        <!-- 搜索框（本地过滤链接 / 标题 / 平台） -->
        <div class="row" style="gap: 8px; margin-bottom: 8px">
          <input v-model="historyQuery" class="input" style="flex: 1; min-width: 0"
                 placeholder="搜索链接 / 标题 / 平台" />
          <button v-if="historyQuery" class="btn text small" title="清空搜索"
                  @click="historyQuery = ''">✕</button>
        </div>

        <div class="row" style="justify-content: space-between; margin-bottom: 8px">
          <span class="hint">共 {{ filteredHistory.length }} 条（与原版互通）</span>
          <button v-if="history.length" class="btn text small" style="color: var(--error)"
                  @click="clearHistory">清空</button>
        </div>

        <div v-if="filteredHistory.length === 0" class="empty" style="padding: 24px 0">
          <p style="margin: 0">{{ history.length === 0 ? '暂无历史记录' : '无匹配结果' }}</p>
        </div>
        <div v-else class="hlist">
          <div v-for="h in filteredHistory" :key="h.id" class="hrow" title="点击重新解析"
               @click="useHistory(h)">
            <div class="hmain">
              <div class="htitle">{{ h.title || h.url }}</div>
              <div class="hurl" :title="h.url">{{ h.url }}</div>
              <div class="row" style="gap: 8px; flex: none">
                <span class="chip plain">{{ h.platform }}</span>
                <span class="hmeta">{{ fmtHistoryTime(h.createTime) }}</span>
                <span v-if="h.pwd" class="hmeta">提取码 {{ h.pwd }}</span>
              </div>
            </div>
            <button class="btn tonal small" @click.stop="useHistory(h)">重解析</button>
            <button class="btn text small" title="删除" @click.stop="deleteHistory(h)">✕</button>
          </div>
        </div>
      </div>
    </div>
  
    <!-- 同名文件确认弹窗 -->
    <div v-if="existsConfirm" class="mask" @click.self="existsConfirm = null">
      <div class="dlg" style="width: min(480px, 92vw)">
        <h3 style="margin: 0 0 8px">文件已存在</h3>
        <p class="hint" style="word-break: break-all; margin: 0 0 6px">{{ existsConfirm.path }}</p>
        <p class="hint" style="margin: 0 0 16px">要重新下载「{{ existsConfirm.name }}」吗？请选择处理方式：</p>
        <div class="row" style="justify-content: flex-end; gap: 8px">
          <button class="btn text small" @click="onExistsChoice('skip')">跳过</button>
          <button class="btn tonal small" @click="onExistsChoice('rename')">重命名下载</button>
          <button class="btn small" @click="onExistsChoice('overwrite')">覆盖下载</button>
        </div>
      </div>
    </div>

    <!-- 磁力下载对话框（迅雷式：元数据解析 → 选文件/改名 → 开始） -->
    <div v-if="magnetOpen" class="mask" @click.self="magnetOpen = false">
      <div class="dlg" style="width: min(560px, 92vw); max-height: 80vh; overflow: auto">
        <div class="row" style="justify-content: space-between; margin-bottom: 6px">
          <h3 style="margin: 0">磁力下载</h3>
          <button class="btn text small" @click="magnetOpen = false">关闭</button>
        </div>

        <div v-if="magnetPhase === 'resolving'" class="magnet-resolving">
          <span class="spin"></span>
          <p>正在获取磁力元数据（DHT 冷启动可能需要 1-3 分钟）…</p>
          <p class="hint" style="word-break: break-all">{{ magnetUrl }}</p>
        </div>

        <div v-else-if="magnetPhase === 'error'">
          <p class="banner error">{{ magnetError }}</p>
          <div class="row" style="justify-content: flex-end">
            <button class="btn text small" @click="magnetOpen = false">取消</button>
            <button class="btn tonal small" @click="resolveMagnet">重试</button>
          </div>
        </div>

        <template v-else-if="magnetInfo">
          <div class="field" style="margin-bottom: 8px">
            <label>保存文件名{{ magnetInfo.singleFile ? '' : '（种子目录名，多文件不可改）' }}</label>
            <input v-if="magnetInfo.singleFile" v-model="magnetName" class="input" />
            <input v-else :value="magnetName" class="input" disabled title="aria2 无法重命名种子的根目录" />
          </div>
          <div class="field" style="margin-bottom: 8px">
            <label>保存目录（空 = 默认下载目录）</label>
            <input v-model="magnetSaveDir" class="input" placeholder="~/Downloads/…" />
          </div>
          <p class="hint" style="margin: 0 0 6px">
            文件清单（已选 {{ magnetSel.size }}/{{ magnetInfo.files.length }} · {{ fmtBytes(magnetSelTotal) }} / {{ fmtBytes(magnetInfo.totalLength) }}）
            <a style="cursor: pointer" @click="magnetSel = new Set(magnetInfo.files.map(f => f.index))">全选</a> ·
            <a style="cursor: pointer" @click="magnetSel = new Set()">全不选</a>
          </p>
          <div class="magnet-files">
            <label v-for="f in magnetInfo.files" :key="f.index" class="magnet-file">
              <input type="checkbox" :checked="magnetSel.has(f.index)" @change="toggleMagnetFile(f.index)" />
              <span class="mfp" :title="f.path">{{ f.path }}</span>
              <span class="mfs">{{ fmtBytes(f.length) }}</span>
            </label>
          </div>
          <div class="row" style="justify-content: flex-end; margin-top: 10px">
            <button class="btn text small" @click="magnetOpen = false">取消</button>
            <button class="btn" :disabled="magnetBusy || magnetSel.size === 0" @click="confirmMagnet">
              {{ magnetBusy ? '提交中…' : `开始下载（${magnetSel.size} 个文件）` }}
            </button>
          </div>
        </template>
      </div>
    </div>
</div>
</template>

<style scoped>
.filerow { display: flex; align-items: center; gap: 10px; padding: 8px 2px;
  border-bottom: 1px solid var(--outline-variant); }
.filerow:last-child { border-bottom: none; }
.ficon { color: var(--on-bg-variant); display: flex; }
.fname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; cursor: default; }
.filerow .fname { cursor: pointer; }
.fmeta { color: var(--on-bg-variant); font-size: 12.5px; flex: none; min-width: 64px; text-align: right; }
.crumbs { flex-wrap: wrap; }
.crumb { border: none; background: transparent; color: var(--primary); cursor: pointer;
  padding: 2px 6px; border-radius: 6px; font-size: 13px; }
.crumb:disabled { color: var(--on-bg); font-weight: 600; cursor: default; }
.crumb:not(:disabled):hover { background: var(--surface-container-high); }
.sep { color: var(--outline); }

/* 多选复选框（与下载页/网盘页同款） */
.ck { width: 18px; height: 18px; border-radius: 4px; border: 2px solid var(--outline);
  cursor: pointer; flex: none; display: inline-flex; align-items: center; justify-content: center;
  color: var(--on-primary); }
.ck.on { background: var(--primary); border-color: var(--primary); }

/* 直链弹窗（.mask/.dlg 对齐 DirPickerModal） */
.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.42); display: flex;
  align-items: center; justify-content: center; z-index: 40; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(480px, 92vw); border: 1px solid var(--outline-variant); }

/* 解析历史弹窗（行样式对齐原版 HistoryItem：标题 + 链接 + 平台/时间/提取码 + 操作） */
.dlg-wide { width: min(640px, 92vw); }
.hint { font-size: 12.5px; color: var(--on-bg-variant); }
.hlist { max-height: 360px; overflow: auto; display: flex; flex-direction: column; gap: 6px; }
.hrow { display: flex; align-items: center; gap: 10px; padding: 9px 12px;
  border-radius: var(--radius); background: var(--surface-container); cursor: pointer; }
.hrow:hover { background: var(--surface-container-high); }
.hmain { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; }
.htitle { font-size: 13.5px; font-weight: 600; overflow: hidden; text-overflow: ellipsis;
  white-space: nowrap; }
.hurl { font-size: 12px; color: var(--on-bg-variant); overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap; }
.hmeta { font-size: 12px; color: var(--on-bg-variant); }
/* 磁力对话框 */
.magnet-resolving { text-align: center; padding: 24px 8px; }
.magnet-resolving .spin { display: inline-block; width: 22px; height: 22px; border-radius: 50%;
  border: 2.5px solid var(--outline-variant); border-top-color: var(--primary);
  animation: mspin 0.9s linear infinite; margin-bottom: 10px; }
@keyframes mspin { to { transform: rotate(360deg); } }
.magnet-files { max-height: 260px; overflow: auto; border: 1px solid var(--outline-variant);
  border-radius: var(--radius); }
.magnet-file { display: flex; align-items: center; gap: 8px; padding: 6px 10px;
  border-bottom: 1px solid var(--outline-variant); font-size: 13px; cursor: pointer; }
.magnet-file:last-child { border-bottom: none; }
.magnet-file:hover { background: var(--surface-container); }
.magnet-file .mfp { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.magnet-file .mfs { color: var(--on-bg-variant); font-size: 12px; flex: none; }
</style>
