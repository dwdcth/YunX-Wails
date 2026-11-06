// 核心调用封装：统一经生成的 bindings 走 CoreService（JSON 命令协议见 dll 模块 DllFacade）。
// 前端一律用这里的 call/kv 帮手，不直接摸 bindings。
import { CoreService } from '../bindings/yunxwailsdemo/index.js'

export interface CallResult<T = Record<string, unknown>> {
  ok: boolean
  error?: string
  [k: string]: unknown
}

export async function call<T = CallResult>(cmd: string, extra: Record<string, unknown> = {}): Promise<T> {
  const raw = await CoreService.Call(JSON.stringify({ cmd, ...extra }))
  return JSON.parse(raw) as T
}

export async function kvGet(key: string): Promise<string | null> {
  const r = await call<{ ok: boolean; value?: string }>('kv_get', { key })
  return r.ok && r.value !== undefined ? r.value : null
}

export async function kvPut(key: string, value: string): Promise<boolean> {
  const r = await call<{ ok: boolean }>('kv_put', { key, value })
  return r.ok
}

export interface ParseShareResult {
  ok: boolean
  found: boolean
  platform?: string
  shareId?: string
  pwd?: string
  error?: string
}

export async function parseShare(text: string): Promise<ParseShareResult> {
  return JSON.parse(await CoreService.ParseShare(text))
}

export async function coreVersion(): Promise<{ version: string; preview: boolean }> {
  return JSON.parse(await CoreService.Version())
}

// ---- 下载引擎（内置 DownloadManager，命令面见 dll 模块 DllFacade）----

export interface DownloadTask {
  taskId: number
  fileName: string
  status: number // 0等待 1下载中 2已暂停 3已完成 4失败
  statusText: string
  total: number
  downloaded: number
  speed: number // B/s（内存实时）
  remainMillis: number
  avgSpeed: number
  savePath?: string | null
  platform?: string
  error?: string | null
}

export async function downloadList(): Promise<DownloadTask[]> {
  const r = await call<{ ok: boolean; tasks?: DownloadTask[] }>('download_list')
  return r.tasks ?? []
}

export async function downloadSubmit(url: string, fileName: string, saveDir = '') {
  return call<{ ok: boolean; taskId: number }>('download_submit', { url, fileName, saveDir })
}

export function downloadPause(taskId: number) {
  return call('download_pause', { taskId })
}

export function downloadResume(taskId: number) {
  return call('download_resume', { taskId })
}

export function downloadRemove(taskId: number, deleteLocal = false) {
  return call('download_remove', { taskId, deleteLocal })
}

// ---- 全局批量与长尾操作（对齐原版 DownloadScreen 的批量栏 + 行菜单）----

export function downloadPauseAll() {
  return call<{ ok: boolean; paused: number; error?: string }>('download_pause_all')
}

export function downloadResumeAll() {
  return call<{ ok: boolean; resumed: number; error?: string }>('download_resume_all')
}

export function downloadRemoveAll(deleteLocal = false) {
  return call<{ ok: boolean; removed: number; error?: string }>('download_remove_all', { deleteLocal })
}

export function downloadRedownload(taskId: number) {
  return call<{ ok: boolean; error?: string }>('download_redownload', { taskId })
}

/** 取任务直链（行菜单「复制直链」用，核心侧现取现回） */
export async function downloadGetLink(taskId: number):
  Promise<{ ok: boolean; url: string; fileName: string; error?: string }> {
  return call('download_get_link', { taskId })
}

// ---- 普通下载（NormalDownload* bindings：HTTP/磁力/BT/ed2k/Metalink → aria2-next；FTP → Go 内置）----
// 任务存独立库 <dataDir>/downloads.db（与网盘任务分家）；网盘直链仍走上面的内置分片引擎。

/** 普通下载任务（字段名与 Go 侧 downloadd.Task 的 json tag 一一对应） */
export interface NormalTask {
  id: number
  engine: string // "aria2" | "ftp"
  gid: string
  url: string
  name: string
  saveDir: string
  savePath: string
  size: number
  downloaded: number
  progress: number // 0-100（Go 侧算好）
  speed: number // B/s
  status: string // pending | active | paused | done | error
  err: string
  createdAt: number
  updatedAt: number
}

/** 普通任务 status 文案（对齐云盘任务 statusText 口径） */
export const NORMAL_STATUS_TEXT: Record<string, string> = {
  pending: '排队中', active: '下载中', paused: '已暂停', done: '已完成', error: '失败',
}

/** 普通任务 status → 云盘任务状态码（DownloadTask.status 0等待 1下载中 2已暂停 3已完成 4失败），
 *  下载页合并列表复用同一套状态样式 */
export function normalStatusCode(s: string): number {
  if (s === 'active') return 1
  if (s === 'paused') return 2
  if (s === 'done') return 3
  if (s === 'error') return 4
  return 0 // pending / 未知
}

export interface NormalSubmitResult {
  ok: boolean
  taskId?: number
  taskIds?: number[] // metalink 多文件展开时有多条
  engine?: string
  name?: string
  error?: string
}

export async function normalDownloadSubmit(source: string, saveDir = ''): Promise<NormalSubmitResult> {
  return JSON.parse(await CoreService.NormalDownloadSubmit(source, saveDir))
}

export interface NormalListResult { ok: boolean; tasks?: NormalTask[]; error?: string }

export async function normalDownloadList(): Promise<NormalListResult> {
  return JSON.parse(await CoreService.NormalDownloadList())
}

export interface NormalOpResult { ok: boolean; error?: string }

export function normalDownloadPause(id: number): Promise<NormalOpResult> {
  return JSON.parse(CoreService.NormalDownloadPause(id))
}

export function normalDownloadResume(id: number): Promise<NormalOpResult> {
  return JSON.parse(CoreService.NormalDownloadResume(id))
}

/** 移除任务（不删已下载文件——对齐云盘 remove 语义） */
export function normalDownloadRemove(id: number): Promise<NormalOpResult> {
  return JSON.parse(CoreService.NormalDownloadRemove(id))
}

export function normalDownloadPauseAll(): Promise<NormalOpResult> {
  return JSON.parse(CoreService.NormalDownloadPauseAll())
}

export function normalDownloadResumeAll(): Promise<NormalOpResult> {
  return JSON.parse(CoreService.NormalDownloadResumeAll())
}

export function normalDownloadRemoveAll(): Promise<NormalOpResult> {
  return JSON.parse(CoreService.NormalDownloadRemoveAll())
}

/** aria2 引擎状态（found=false 表示本机找不到 aria2-next，设置页引导配置 aria2Path） */
export interface Aria2StatusInfo {
  ok: boolean
  found?: boolean
  running?: boolean
  version?: string
  port?: number
  binPath?: string
  error?: string
}

export async function aria2Status(): Promise<Aria2StatusInfo> {
  return JSON.parse(await CoreService.Aria2Status())
}

/** aria2Path 的核心 kv 键（后端读取优先级 kv > env YUNX_ARIA2_BIN > PATH > tools 目录） */
export const ARIA2_PATH_KEY = 'settings.aria2Path'

/**
 * 普通下载链接识别（解析页/下载页分流用，规则对齐后端 downloadd.Classify）：
 * ftp(s)/magnet/ed2k/bc1 前缀，或 http(s) URL / 本地路径以 .torrent/.metalink/.meta4 结尾。
 * 纯 http(s) 直链不在内（解析页对那种先问用户，不自动提交）。
 */
export function isNormalDownloadLink(v: string): boolean {
  const s = v.trim()
  if (!s) return false
  const lower = s.toLowerCase()
  if (/^(ftp|ftps|magnet|ed2k|bc1):/.test(lower)) return true
  if (/^https?:\/\//.test(lower)) {
    const path = lower.replace(/^https?:\/\/[^/]+/, '').split(/[?#]/)[0]
    return /\.(torrent|metalink|meta4)$/.test(path)
  }
  // 无 scheme：按本地文件路径对待（WebKit 下 file input 拿不到原生路径，手填路径由后端 os.Stat 兜底）
  if (/^[a-z][a-z0-9+.-]*:/.test(lower)) return false // 其它 scheme（thunder:// 等）不支持
  return /\.(torrent|metalink|meta4)$/.test(lower.split(/[?#]/)[0])
}

// ---- 下载调试（对齐原版 DownloadDebugScreen；设置开 downloadDebug 后行菜单出现入口）----

/** 单个在飞分片（= 一个 worker 此刻在干什么），字段与 core ChunkDebug 一一对应 */
export interface DebugChunk {
  key: string
  start: number
  size: number
  worker: number
  received: number
  bps: number
  elapsedMs: number
  idleMs: number
  preemptCount: number
  preempting: boolean
}

/** 全局资源快照（在飞配额 / 活跃任务 / 线程池 / 堆内存） */
export interface DebugResources {
  inflightCap: number
  inflightUsed: number
  activeTasks: number
  heapUsed: number
  heapMax: number
  poolSize: number
  poolActive: number
  poolQueue: number
}

export interface DebugInfo {
  ok: boolean
  error?: string
  task: {
    id: number; fileName: string; status: number; statusText: string
    total: number; downloaded: number; platform: string; engine: string
    chunkCount: number; avgSpeed: number; error: string; sourceCount: number
  }
  stats: { speed: number; remainMillis: number; chunkCount: number; mergePercent: number } | null
  chunks: DebugChunk[]
  resources: DebugResources
  sourceCount: number
  logs: string[]
  logTotal: number
  enabled: boolean
  env: string
  snapshot: string
}

/** 一次性调试快照（前端 5 秒轮询；任务不存在返回 ok:false） */
export function downloadDebugInfo(taskId: number, limit = 200) {
  return call<DebugInfo>('download_debug_info', { taskId, limit })
}

/** 导出完整日志到 <缓存目录>/logs，返回文件路径 */
export function downloadDebugExport(taskId: number) {
  return call<{ ok: boolean; path?: string; fileName?: string; error?: string }>(
    'download_debug_export', { taskId })
}

/** 手动强制重连某一路分片（调试弹窗分片行「重连」；任务/分片不在活跃集返回 ok:false） */
export function downloadDebugPreempt(taskId: number, key: string) {
  return call<{ ok: boolean; preempted?: string; error?: string }>(
    'download_debug_preempt', { taskId, key })
}

// download_list 全量返回：tasks 之外带 stats（mergePercent>0 表示有任务处于 ffmpeg 合轨阶段）
export interface DownloadStats {
  mergePercent: number
}

export async function downloadListFull():
  Promise<{ ok: boolean; tasks?: DownloadTask[]; stats?: DownloadStats; error?: string }> {
  return call('download_list')
}

// ---- 设置（正式 SettingsRepository，与 Compose 原版同一份 yunx/settings）----

export interface AppSettings {
  darkMode: number // 0 跟随系统 / 1 浅色 / 2 深色
  themeColorMode: number
  themeSeedColor: number
  reduceMotion: boolean
  downloadThreads: number
  downloadDirUri?: string | null
  maxConcurrentDownloads: number
  downloadSpeedLimit: number
  downloadRetryCount: number
  keepAwakeWhileDownloading: boolean
  shutdownWhenDownloadsComplete: boolean
  notificationShowSpeed: boolean
  clipboardLinkDetection: boolean
  closeBehavior: string
  proxyMode: string
  proxyHost: string
  proxyPort: number
  http2Enabled: boolean
  downloadEngine: string
  acceptPrereleaseUpdate: boolean
  githubMirrorPrefix?: string | null
  btDhtEnabled: boolean
  btTrackers: string
  quarkNoSaveDownload: boolean
  multiAccountDownload: boolean
  multiSourceDownload: boolean
  previewEngine: string
  diagnosticMode: boolean
  downloadDebug: boolean
  fullFileSha256: boolean
  ignoreSslCert: boolean
  dbEncryptionLevel: string
  storageDbDir?: string | null
  storageCacheDir?: string | null
  // 只读快照（DLL settings_list 附带）：实际生效目录与加密**实际**档位（见下方储存目录封装）
  storageDataDir?: string
  storageDbDirEffective?: string
  storageCacheDirEffective?: string
  dbEncryptionEffectiveLevel?: string
  onboardingShown: boolean
}

export async function settingsList(): Promise<AppSettings | null> {
  const r = await call<{ ok: boolean; settings?: AppSettings }>('settings_list')
  return r.settings ?? null
}

export async function settingsSet(key: string, value: string | number | boolean) {
  return call<{ ok: boolean }>('settings_set', { key, value: String(value) })
}

/** 带平台维度的设置写入（如按平台线程数：key 固定 downloadThreadsFor，platform 字段区分平台） */
export function settingsSetPlatform(key: string, platform: string, value: string | number | boolean) {
  return call<{ ok: boolean; error?: string }>('settings_set', { key, platform, value: String(value) })
}

// ---- 储存目录（对齐原版 StorageDirectoryScreen；目录改动重启应用后生效）----

/** 数据库搬迁结果：migrated=false 时 reason = already-configured（已是该设置）/ target-is-effective（切回当前生效目录） */
export interface StorageMigrateResult {
  ok: boolean
  error?: string
  migrated?: boolean
  reason?: string
  copiedFiles?: number
  copiedBytes?: number
  skipped?: string[]
  skippedCount?: number
  source?: string
  target?: string
  restartRequired?: boolean
}

/**
 * 搬迁数据库（yunx.db/-wal/-shm + credential.key）到新目录：拷贝 + 记旧位置待清理 + 写 storageDbDir。
 * 失败（目标已存在库文件 / 不可写 / 拷贝失败）一律不改配置；成功后需重启应用生效。
 */
export function storageMigrateDb(targetDir: string) {
  return call<StorageMigrateResult>('storage_migrate_db', { targetDir })
}

/** 切换数据库加密等级（解包主密钥 → 按新档重新包裹 → 原子替换；立即生效，无需重启） */
export function storageApplyEncryption(level: string) {
  return call<{
    ok: boolean; error?: string
    applied?: string; label?: string; effectiveLevel?: string; previousEffective?: string | null
  }>('storage_apply_encryption', { level })
}

// ---- 账号（AccountDirectory，明文列，不含凭证）----

export interface AccountEntry {
  platform: string
  id: string
  nickname: string
  remark: string
  isPrimary: boolean
  updatedAt: number
}

export async function accountsList(): Promise<AccountEntry[]> {
  const r = await call<{ ok: boolean; accounts?: AccountEntry[] }>('accounts_list')
  return r.accounts ?? []
}

export function accountSetPrimary(platform: string, id: string) {
  return call('account_set_primary', { platform, id })
}

export function accountSetRemark(platform: string, id: string, remark: string) {
  return call('account_set_remark', { platform, id, remark })
}

export function accountRemove(platform: string, id: string) {
  return call('account_remove', { platform, id })
}

// ---- 分享解析（ResolveGateway，网络命令）----

export interface ShareFile {
  fid: string; fname: string; fsize: number; isdir: boolean
  fidToken: string; modifyTime: string
}

export interface ShareDetail {
  platform: string; title: string; shareId: string
  guest: boolean; dirFid: string; files: ShareFile[]
}

export async function shareResolve(url: string, pwd = '') {
  return call<{ ok: boolean; error?: string } & ShareDetail>('share_resolve', { url, pwd })
}

export async function shareList(dirFid: string) {
  return call<{ ok: boolean; error?: string; files?: ShareFile[] }>('share_list', { dirFid })
}

// ---- 文件夹批量下载（share_folder_download 入队一批任务，batch_list 轮询进度）----

export interface BatchTask {
  id: string
  title: string
  total: number
  done: number
  failed: number
  lastError?: string
}

export function shareFolderDownload(fid: string, fname: string, saveDir = '') {
  return call<{ ok: boolean; batchId?: string; error?: string }>('share_folder_download', { fid, fname, saveDir })
}

export async function batchList(): Promise<BatchTask[]> {
  const r = await call<{ ok: boolean; batches?: BatchTask[] }>('batch_list')
  return r.batches ?? []
}

// ---- 创建分享（自己网盘多选 → cloud_share）----
// expiredType 对齐原版 ShareExpire 常量：0 永久 / 1 天 / 2 周 / 3 月；不传 = 平台默认（永久）

export function cloudShare(platform: string, fids: string[], title = '', passcode = '', expiredType?: number) {
  return call<{ ok: boolean; shareUrl?: string; error?: string }>('cloud_share', {
    platform, fids, title, passcode,
    ...(expiredType === undefined ? {} : { expiredType }),
  })
}

export async function shareDownload(f: ShareFile, saveDir = '') {
  return call<{ ok: boolean; error?: string; taskId: number; filename: string }>('share_download', {
    fid: f.fid, fname: f.fname, fsize: f.fsize, fidToken: f.fidToken, saveDir,
  })
}

// ---- 解析历史与收藏（核心 SQLite，与原版互通）----

export interface LinkHistoryItem {
  id: number; url: string; title: string; platform: string; pwd: string; createTime: number
}

export async function linkHistoryList() {
  const r = await call<{ ok: boolean; history?: LinkHistoryItem[] }>('link_history_list')
  return r.history ?? []
}

/** 手动补记一条历史（share_resolve 解析成功时核心已自动记录；GitHub 等分支由前端补记） */
export function linkHistoryAdd(url: string, title = '', platform = '', pwd = '') {
  return call<{ ok: boolean; stored?: boolean; error?: string }>('link_history_add', {
    url, title, platform, pwd,
  })
}

export function linkHistoryDelete(id: number) { return call('link_history_delete', { id }) }
export function linkHistoryClear() { return call('link_history_clear') }

export interface BookmarkItem {
  id: number; link: string; title: string; platform: string; pwd: string
  category: string; createTime: number
}

export async function bookmarkList() {
  const r = await call<{ ok: boolean; bookmarks?: BookmarkItem[] }>('bookmark_list')
  return r.bookmarks ?? []
}

/** category 传空则由核心落默认分类「未分类」（DLL bookmark_add 的 optString 默认值） */
export function bookmarkAdd(link: string, title: string, platform: string, pwd = '', category = '') {
  return call<{ ok: boolean; id?: number; error?: string }>('bookmark_add', {
    link, title, platform, pwd,
    ...(category ? { category } : {}),
  })
}

export function bookmarkDelete(id: number) { return call('bookmark_delete', { id }) }

// ---- 云盘文件操作（CloudGateway，自己网盘；未登录返回明确错误）----

export interface CloudQuota { used: number; total: number }

export async function cloudList(platform: string, dirFid = '') {
  return call<{ ok: boolean; error?: string; files?: ShareFile[]; dirFid: string; rootFid: string }>(
    'cloud_list', { platform, dirFid })
}

export function cloudMkdir(platform: string, name: string, dirFid = '') {
  return call('cloud_mkdir', { platform, name, dirFid })
}

export function cloudRename(platform: string, f: ShareFile, newName: string) {
  return call('cloud_rename', {
    platform, fid: f.fid, fname: f.fname, isdir: f.isdir, fidToken: f.fidToken, newName,
  })
}

export function cloudMove(platform: string, files: ShareFile[], targetFid: string, targetPath = '') {
  return call('cloud_move', {
    platform, targetFid, targetPath,
    files: files.map(f => ({ fid: f.fid, fname: f.fname, isdir: f.isdir, fidToken: f.fidToken })),
  })
}

export function cloudDelete(platform: string, files: ShareFile[]) {
  return call('cloud_delete', {
    platform,
    files: files.map(f => ({ fid: f.fid, fname: f.fname, isdir: f.isdir, fidToken: f.fidToken })),
  })
}

export async function cloudQuota(): Promise<Record<string, CloudQuota | null>> {
  const r = await call<{ ok: boolean; quotas?: Record<string, CloudQuota | null> }>('cloud_quota')
  return r.quotas ?? {}
}

/** 个人盘直链下载（夸克/UC/蓝奏）→ 入队 */
export function cloudDownload(platform: string, f: ShareFile, saveDir = '') {
  return call<{ ok: boolean; error?: string; taskId: number }>('cloud_download', {
    platform, fid: f.fid, fname: f.fname, fsize: f.fsize, fidToken: f.fidToken, saveDir,
  })
}

/** 当前分享会话的文件转存到自己云盘目标目录 */
export function shareTransfer(f: ShareFile, toDirFid: string) {
  return call<{ ok: boolean; error?: string; transferred: boolean }>('share_transfer', {
    fid: f.fid, fname: f.fname, fsize: f.fsize, isdir: f.isdir, fidToken: f.fidToken, toDirFid,
  })
}

/** 取分享文件直链与请求头（解析页直链弹窗：展示 + 手动选目录入队） */
export async function shareLink(f: ShareFile): Promise<{
  ok: boolean; error?: string
  url: string; filename: string; size: number; isHls: boolean
  headers: Record<string, string>
}> {
  return call('share_link', {
    fid: f.fid, fname: f.fname, fsize: f.fsize, fidToken: f.fidToken, isdir: f.isdir,
  })
}

/** 多选批量转存：逐个调 share_transfer（单个失败不中断其余），返回成功/失败数与首个错误 */
export async function shareTransferBatch(files: ShareFile[], toDirFid: string): Promise<{
  okCount: number; failCount: number; lastError?: string
}> {
  let okCount = 0
  let failCount = 0
  let lastError: string | undefined
  for (const f of files) {
    try {
      const r = await shareTransfer(f, toDirFid)
      if (r.ok) okCount++
      else { failCount++; lastError = r.error }
    } catch (e) {
      failCount++
      lastError = String(e)
    }
  }
  return { okCount, failCount, lastError }
}

// ---- 完成后关机（下载页倒计时结束触发；Go 宿主方法由集成者落地）----

/** 触发系统关机：优先走 bindings 的 CoreService.Shutdown，未再生时兜底通用命令通道 */
export async function shutdownNow(): Promise<{ ok: boolean; error?: string }> {
  const svc = CoreService as unknown as { Shutdown?: () => Promise<string> }
  if (typeof svc.Shutdown === 'function') return JSON.parse(await svc.Shutdown())
  return call('shutdown')
}

// ---- 登录（粘贴凭据类；网络校验后落库）----

export function accountLoginCookie(platform: string, credential: string, newAccount = false) {
  return call<{ ok: boolean; error?: string; loggedIn?: boolean }>('account_login_cookie', {
    platform, credential, newAccount,
  })
}

/** 账号密码登录（123云盘/光鸭/蓝奏优享/迅雷等支持账密的平台） */
export function accountLoginPassword(platform: string, account: string, password: string) {
  return call<{ ok: boolean; loggedIn?: boolean; error?: string }>('account_login_password', {
    platform, account, password,
  })
}

// ---- GitHub Release（解析 releases 页 + 附件直链下载）----

export interface GithubAsset {
  name: string
  size: number
  url: string
}

export function githubResolve(url: string) {
  return call<{ ok: boolean; repo?: string; tag?: string; assets?: GithubAsset[]; error?: string }>(
    'github_resolve', { url })
}

export function githubDownload(url: string, fileName: string, saveDir = '') {
  return call<{ ok: boolean; taskId?: number; error?: string }>('github_download', { url, fileName, saveDir })
}

// ---- 视频解析账号（登录态管理；解析入口后续批次）----

export interface VideoAccount {
  platform: string
  id: string
  nickname: string
  isPrimary: boolean
  remark: string
}

export async function videoAccountsList(): Promise<VideoAccount[]> {
  const r = await call<{ ok: boolean; accounts?: VideoAccount[] }>('video_accounts_list')
  return r.accounts ?? []
}

export function videoLoginCookie(platform: string, cookie: string) {
  return call<{ ok: boolean; loggedIn?: boolean; error?: string }>('video_login_cookie', { platform, cookie })
}

// ---- 视频解析（短视频/视频站链接 → 清晰度/媒体列表 → 下载入队；命令面见 dll 模块 DllFacade）----

export interface VideoQuality { id: string; label: string }

export interface VideoMedia {
  type: string // video | image
  url: string
  label: string
  size: number
  isHls: boolean
  audioUrl: string // DASH/HLS 分离音轨（合并下载时核心侧取用）
  motionUrl: string // 实况/动图地址（小红书等平台）
}

export interface VideoParseResult {
  ok: boolean
  error?: string
  platform: string
  title: string
  author: string
  cover: string
  content: string
  publishTime: string
  qualities: VideoQuality[]
  medias: VideoMedia[]
}

/** 解析视频链接；qualityId 非空时走 video_parse_quality 切档（B 站多清晰度） */
export async function videoParse(url: string, qualityId = ''): Promise<VideoParseResult> {
  return qualityId
    ? call<VideoParseResult>('video_parse_quality', { url, qualityId })
    : call<VideoParseResult>('video_parse', { url })
}

/** 视频媒体下载入队：mode = merge 合并 / audio 仅音频 / video 仅视频；图片直链留空（未指定） */
export function videoDownload(url: string, qualityId = '', saveDir = '', mode = '') {
  return call<{ ok: boolean; taskId: number; fileName: string; size: number; error?: string }>(
    'video_download', { url, qualityId, saveDir, mode })
}

// ---- 公告与更新 ----

export interface AnnouncementItem {
  id: string; title: string; summary: string
  publishAt?: string | null; isPinned: boolean; read: boolean
}

export async function announcementsList(page = 1) {
  return call<{ ok: boolean; error?: string; announcements?: AnnouncementItem[]; hasMore: boolean }>(
    'announcements_list', { page })
}

export async function announcementDetail(id: string) {
  return call<{ ok: boolean; error?: string; title: string; content?: string }>('announcement_detail', { id })
}

export interface UpdateInfo {
  current: string; latest: string; hasUpdate: boolean
  prerelease: boolean; url: string; notes: string
}

export async function updateCheck() {
  return call<UpdateInfo & { ok: boolean; error?: string }>('update_check')
}

export function githubMirrorRefresh() { return call('github_mirror_refresh') }

// ---- 格式化帮手 ----

export function fmtBytes(n: number): string {
  if (!isFinite(n) || n <= 0) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

export function fmtSpeed(bps: number): string {
  return bps > 0 ? `${fmtBytes(bps)}/s` : '—'
}

export function fmtRemain(ms: number): string {
  if (ms <= 0) return '—'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} 秒`
  if (s < 3600) return `${Math.floor(s / 60)} 分 ${s % 60} 秒`
  return `${Math.floor(s / 3600)} 时 ${Math.floor((s % 3600) / 60)} 分`
}
