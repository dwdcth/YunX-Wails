package com.yunx.dll

import com.yunx.app.APP_IS_PREVIEW
import com.yunx.app.APP_VERSION
import com.yunx.app.AppContext
import com.yunx.app.data.backup.AuthBackupManager
import com.yunx.app.data.db.AppDatabase
import com.yunx.app.data.db.DownloadTaskEntity
import com.yunx.app.data.download.ChunkDebug
import com.yunx.app.data.download.ChunkDownloader
import com.yunx.app.data.download.DownloadDebugLog
import com.yunx.app.data.download.DownloadManager
import com.yunx.app.data.download.DownloadResourceDebug
import com.yunx.app.data.download.DownloadStats
import com.yunx.app.data.download.DownloadTuning
import com.yunx.app.data.host.HostServices
import com.yunx.app.data.host.HostStores
import com.yunx.app.data.host.KeyValueStore
import com.yunx.app.data.host.MachineProtector
import com.yunx.app.data.db.BookmarkEntity
import com.yunx.app.data.db.LinkHistoryEntity
import com.yunx.app.data.network.GitHubLinkParser
import com.yunx.app.data.network.GitHubLinkType
import com.yunx.app.data.network.GitHubMirror
import com.yunx.app.data.network.HttpClients
import com.yunx.app.data.network.ShareLinkParser
import com.yunx.app.data.network.SharePlatform
import com.yunx.app.data.network.model.ShareExpire
import com.yunx.app.data.network.model.ShareFile
import com.yunx.app.data.network.video.VideoPlatform
import com.yunx.app.data.prefs.SettingsRepository
import com.yunx.app.data.preview.FfmpegLocator
import com.yunx.app.data.repository.AccountDirectory
import com.yunx.app.data.security.DbEncryptionLevel
import com.yunx.app.data.security.DbEncryptionManager
import com.yunx.app.data.security.DbKeyStore
import com.yunx.app.data.storage.StorageMigrator
import com.yunx.app.data.update.UpdateChecker
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.prefs.Preferences

/**
 * DLL 门面：Go/Wails 壳与核心之间的唯一通道。
 *
 * 协议：请求/响应都是 UTF-8 JSON 字符串（经 C 导出层的调用方缓冲区传递）。
 * 请求：{"cmd": "<命令>", ...参数}
 * 响应：{"ok": true, ...结果} 或 {"ok": false, "error": "..."}
 *
 * 命令表：
 * - version        → {version, preview}
 * - parse_share    → {found, platform?, shareId?, pwd?}（ShareLinkParser，纯本地）
 * - kv_put/kv_get  → 通用键值（宿主 KeyValueStore；随下方工厂同源到 java.util.prefs）
 * - key_roundtrip  → DbKeyStore 宽松档建钥/重读一致性
 *
 * 宿主注入（Go 壳启动/设置时调用，见仓库根 hostenv）：
 * - host_set_machine_id → 注册 DLL 侧 MachineProtector（普通档密钥解包）
 * - host_set_proxy / host_set_ffmpeg
 *
 * 内置下载引擎（DownloadManager，库内协程异步执行）：
 * - download_submit / download_list / download_pause / download_resume / download_remove
 *
 * 设置（正式 SettingsRepository，与 Compose 原版/Linux 移植版同一份 yunx/settings）：
 * - settings_list       → 全量快照（含 misc 的 onboarding_shown；另附储存目录「当前生效」实际路径
 *                          与数据库加密**实际**档位，供 UI 区分「重启前/重启后生效」）
 * - settings_set        → {key, value}（typed，见 [SettingsKeys]；含运行时联动）
 *
 * 储存目录（对齐原版 StorageDirectoryScreen；目录改动一律**重启应用生效**——AppContext 启动时只解析一次）：
 * - settings_set storageCacheDir / storageDbDir / dbEncryptionLevel（空串 = 恢复默认/清 null）
 * - storage_migrate_db {targetDir} → 搬迁数据库（yunx.db/-wal/-shm + credential.key 拷贝到新目录、
 *   记「旧位置待清理」、写 storageDbDir；失败一律不写配置），源 = 当前 AppContext 实际目录
 * - storage_apply_encryption {level} → 切换数据库加密等级（解包主密钥 → 按新档重新包裹 → 原子替换，
 *   立即生效无需重启；与原版 DbEncryptionManager.switchTo 同一动作）
 *
 * 账号（AccountDirectory，SQLite 明文列，不暴露凭证）：
 * - accounts_list / account_set_primary / account_set_remark / account_remove
 *
 * 认证备份（AuthBackupManager，与原版同格式，跨设备导入导出；文件字节不跨界，只传路径）：
 * - auth_backup_name → 预填文件名（.yunx 加密 / .json 明文）
 * - auth_export {path, password?, onlyLoggedIn?} → 核心直接写文件
 * - auth_import {path, password?} → {imported: 恢复的平台数}
 *
 * 分享解析（ResolveGateway，桌面 ResolveViewModel 的 DLL 等价物；网络命令，单会话模型）：
 * - share_resolve {url, pwd?} → {platform, title, files[]}（建会话 + 根目录列表 + 记历史）
 * - share_list {dirFid} → 子目录列表
 * - share_link {fid,...} → 直链 + 平台下载头（游客/登录态自动分流，夸克 CDN 优选）
 * - share_download {fid,..., saveDir?} → 取链并入队下载 → {taskId}
 * - link_history_list/add/delete/clear；bookmark_list/add/delete/categories
 *
 * 云盘文件操作（CloudGateway，自己网盘；网络命令，未登录返回明确错误）：
 * - cloud_list {platform, dirFid?}（空 dirFid = 根目录）→ {files, rootFid}
 * - cloud_mkdir {platform, dirFid?, name} / cloud_rename {..., fid, newName}
 * - cloud_move {platform, files[], targetFid, targetPath?} / cloud_delete {platform, files[]}
 * - cloud_quota → 9 平台配额并发快照（蓝奏云官方无接口为 null）
 * - cloud_download {platform, fid, fname, saveDir?} → 个人盘直链入队（夸克/UC/蓝奏）
 * - share_transfer {fid,...,toDirFid} → 当前分享会话转存到自己云盘目标目录
 *
 * 登录（批次D：粘贴 Cookie/网页凭据类；网络校验后落库，与原版同一账号表）：
 * - account_login_cookie {platform, credential, newAccount?}（quark/uc/baidu/c139/pan115/lanzou=Cookie，
 *   xunlei=网页凭据原始串；guangya/ilanzou/pan123 为密码登录，待后续批次）
 *
 * 批次G1 扩展（实现分散在 ExtraGateway，DllFacade 只留分支）：
 * - share_folder_download {fid, fname, saveDir?} → {batchId}：后台协程递归展开分享目录逐文件入队
 * - batch_list → {batches:[{id,title,total,done,failed,lastError}]}（内存态，最近 20 个）
 * - cloud_share {platform, fids, title?, passcode?, expiredType?} → {shareUrl}（8 平台，蓝奏两家无接口）
 * - account_login_password {platform, account, password} → {loggedIn}（pan123/guangya/ilanzou/xunlei）
 * - github_resolve {url} → {repo, tag, assets[]}；github_download {url, fileName, saveDir?} → {taskId}
 * - video_accounts_list → {accounts[]}；video_login_cookie {platform, cookie} → {loggedIn}
 * - video_parse {url} / video_parse_quality {url, qualityId} → 全量解析结果
 *   （元信息 + qualities 清晰度列表 + medias 媒体清单；B 站切档重解析，其它平台忽略档位。
 *   抖音缺网页引擎暂拒，见 ExtraGateway）
 * - video_download {url, qualityId?, saveDir?, mode?} → 视频取链入队
 *   （mode: merge=合并音视频（默认）/ audio=仅音频 / video=仅视频 / separate=两轨分别保存）
 *
 * 下载长尾命令（任务卡批量操作，实现在 ExtraGateway）：
 * - download_redownload {taskId} → {ok}（直链已失效返回 error）
 * - download_get_link {taskId} → {url, fileName, headers}（前端复制直链用）
 * - download_pause_all → {paused}；download_resume_all → {resumed}
 * - download_remove_all {deleteLocal?} → {removed}
 *
 * 下载调试（对齐原版 DownloadDebugScreen；先在设置开 downloadDebug，行菜单才出现入口）：
 * - download_debug_info {taskId, limit?} → {task, stats, chunks[], resources, sourceCount,
 *   logs[](最近 N 条), logTotal, enabled, env, snapshot}（拉取式只读快照，前端 5 秒轮询）
 * - download_debug_export {taskId} → {path, fileName}（完整日志写 <缓存目录>/logs，core 同源导出 API）
 * - download_debug_preempt {taskId, key} → {preempted}（手动强制重连某路分片；任务/分片不存在优雅 ok:false）
 *
 * 公告与更新（网络命令）：
 * - announcements_list {page?} → 列表（含已读标记）；announcement_detail {id}；announcement_mark_read {id}
 * - update_check → 最新 Release 与当前版本比较；github_mirror_refresh → 镜像测速
 */
object DllFacade {

    init {
        // prefs 隔离（本应用数据目录已与原版分家，prefs 节点同样分家，两个环境变量）：
        // - YUNX_PREFS_USER_ROOT：非空则设为 java.util.prefs.userRoot 系统属性（prefs 根目录整体
        //   搬家，native 冒烟/测试用；替代过去只有 JVM -D 才能指定的办法）。必须在任何
        //   Preferences 访问前生效，放 init 最前。
        // - YUNX_PREFS_NODE：节点前缀（见 prefsStoreFactory），默认 yunx-wails ⇒ 与原版 yunx 分离。
        System.getenv("YUNX_PREFS_USER_ROOT")?.takeIf { it.isNotBlank() }?.let {
            System.setProperty("java.util.prefs.userRoot", it)
        }
        // 与桌面形态同源：KeyValueStore 走 java.util.prefs（Linux: ~/.java/.userPrefs/<节点前缀>/*，
        // Windows: HKCU\Software\JavaSoft\Prefs\<节点前缀>\*）。默认前缀 yunx-wails（本应用独立，
        // 与原版 yunx 互不干扰）；YUNX_PREFS_NODE 可覆盖（设回 "yunx" 可恢复与原版互通/共享）。
        // 必须在任何 AppContext/HostStores.named 触碰前生效（named 结果会被缓存）。
        // native 环境若不可用则退回默认 FileKeyValueStore（仅损失设置互通）。
        runCatching { HostStores.factory = ::prefsStoreFactory }
        // 启动装配（对齐桌面 Main.kt 的 AppContext.init()）：建目录 + 旧数据目录迁移 +
        // 「储存目录」搬迁后旧位置残留清理（storage_migrate_db 记下的待清理标记在这里消费）。
        // 失败不阻断（目录不可写等场景核心仍能响应纯设置命令）。
        runCatching { AppContext.init() }
        // 迅雷设备指纹加载（对齐桌面 Main.kt:124）：从 xunlei_fp prefs 恢复持久指纹，
        // 不接的话每次进程重启指纹都会重新生成，登录风控更严
        runCatching { com.yunx.app.data.network.XunleiDeviceFingerprint.init() }
        // 光鸭设备指纹（对齐桌面 MainScreen:321）：guangyaApi↔guangyaAccount 互相 lazy，
        // 在两侧都构造后接线，避免循环初始化
        runCatching {
            ResolveGateway.guangyaApi.deviceIdProvider = { ResolveGateway.guangyaAccount.cachedDeviceId() }
        }
        // 迅雷 access_token 过期自动刷新（对齐桌面 MainScreen:487）：401 时用 refresh_token
        // 换新并落库。★ 之前 DLL 漏接这条线，导入的账号 token 一过期就「帐号认证失败」。
        runCatching {
            ResolveGateway.xunleiApi.refreshTokenProvider = { deviceId ->
                kotlinx.coroutines.runBlocking {
                    val acc = ResolveGateway.xunleiAccount.getAccount()
                    if (acc == null || acc.refreshToken.isBlank()) null
                    else ResolveGateway.xunleiApi.refreshToken(acc.refreshToken, deviceId, acc.authType)
                        ?.also { (at, nrt) ->
                            ResolveGateway.xunleiAccount.updateTokens(at, nrt)
                            println("迅雷 token 已自动刷新（deviceId=${deviceId.take(8)}…）")
                        }
                }
            }
            // 迅雷网页账号 captcha 刷新（对齐桌面 MainScreen:495）：本地 9 盐签名优先，被拒回退网页会话
            ResolveGateway.xunleiApi.webCaptchaRefresher = { deviceId, action ->
                kotlinx.coroutines.runBlocking {
                    ResolveGateway.xunleiAccount.refreshWebCaptcha(deviceId, action)
                }
            }
        }
    }

    /** 下载引擎单例：首次使用时惰性构建（AppDatabase 单例 + 内置 ChunkDownloader，无外部引擎）。 */
    @Volatile
    private var manager: DownloadManager? = null

    private fun downloadManager(): DownloadManager = manager ?: synchronized(this) {
        manager ?: run {
            AppContext.init()
            // 与桌面 Main.kt 启动装配对齐（HTTP2 + 下载调优 + 下载调试日志按持久化开关装上），DLL 内引擎读同一份设置
            runCatching {
                val settings = SettingsRepository()
                HttpClients.setHttp2Enabled(settings.http2Enabled)
                DownloadTuning.applyFrom(settings)
            }
            // 必须在任何下载开始前调用：按持久化开关决定是否记录调试日志（对齐桌面 Main.kt）
            runCatching { DownloadDebugLog.install() }
            DownloadManager(
                dao = AppDatabase.get().downloadTaskDao(),
                downloader = ChunkDownloader(clientProvider = { HttpClients.downloadClient() }),
                // 各 provider 是闭包：每次调用时新建 SettingsRepository 读**最新值**，设置改动即时生效
                //（对齐桌面 MainScreen 的 provider 注入模式；惰性单例只保证引擎本身只建一次）
                threadProvider = { platform -> SettingsRepository().downloadThreadsFor(platform) },
                saveDirProvider = { SettingsRepository().downloadDirUri },
                concurrencyProvider = { SettingsRepository().maxConcurrentDownloads },
                speedLimitProvider = { SettingsRepository().downloadSpeedLimit },
                retryCountProvider = { SettingsRepository().downloadRetryCount },
                keepAwakeProvider = { SettingsRepository().keepAwakeWhileDownloading },
                showSpeedProvider = { SettingsRepository().notificationShowSpeed },
            )
        }.also { manager = it }
    }

    private val accountDirectory: AccountDirectory by lazy { AccountDirectory(AppDatabase.get()) }

    private val announcementApi = com.yunx.app.data.announcement.AnnouncementApi
    private val announcementReadStore by lazy { com.yunx.app.data.announcement.AnnouncementReadStore() }

    // 公告列表缓存 + 后台刷新：announcements_list 绝不同步走网络——
    // 命令通道是串行的（isolate 线程绑定），公告服务器不可达时一次 fetch 会把
    // connect 15s/read 30s/call 45s 的超时整个压在通道上，accounts/download 等全部排队，
    // 界面表现为「全空」（2026-11-06 实测事故）。缓存立即回，刷新丢给内部线程
    //（native image 里核心自建线程安全，下载协程同机制），前端稍后再取即得新数据。
    private val announceCache = java.util.concurrent.ConcurrentHashMap<Int, JSONObject>()
    private val announceFetching = java.util.concurrent.atomic.AtomicBoolean(false)

    private fun refreshAnnounceAsync(page: Int) {
        if (!announceFetching.compareAndSet(false, true)) return
        Thread {
            try {
                val r = kotlinx.coroutines.runBlocking { announcementApi.fetchPage(page) }
                if (r is com.yunx.app.data.announcement.AnnouncementApi.Result.Success) {
                    val arr = JSONArray()
                    for (a in r.data.list) {
                        arr.put(JSONObject()
                            .put("id", a.id).put("title", a.title).put("summary", a.summary)
                            .put("publishAt", a.publishAt ?: JSONObject.NULL)
                            .put("isPinned", a.isPinned))
                    }
                    announceCache[page] = JSONObject()
                        .put("total", r.data.total).put("page", r.data.page)
                        .put("hasMore", r.data.hasMore).put("announcements", arr)
                }
            } catch (_: Throwable) {
                // 刷新失败保缓存；下次调用再试
            } finally {
                announceFetching.set(false)
            }
        }.apply { isDaemon = true; name = "announce-refresh" }.start()
    }
    // GitHub API 带 token（对齐桌面 MainScreen:277）：有 token 限流 5000/h（匿名 60/h）
    private val githubApi by lazy {
        com.yunx.app.data.network.GitHubApi(
            tokenProvider = { runCatching { com.yunx.app.data.network.GitHubTokenStore.getToken() }.getOrNull() })
    }

    /**
     * 批量下载（share_folder_download）的后台作用域：SupervisorJob 保证单个批次协程失败不波及其他批次，
     * Dispatchers.Default 覆盖递归展开（网络 IO 走 OkHttp 自身线程）与入队的 CPU 开销。
     * 批次状态存在 ExtraGateway 内存里，进程重启即丢。
     */
    private val batchScope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    /** 认证备份器（11 个 DAO 全量，与原版同格式）。 */
    private val authBackup: AuthBackupManager by lazy {
        val db = AppDatabase.get()
        AuthBackupManager(
            quarkDao = db.quarkAccountDao(),
            ucDao = db.ucAccountDao(),
            xunleiDao = db.xunleiAccountDao(),
            baiduDao = db.baiduAccountDao(),
            c139Dao = db.c139AccountDao(),
            pan123Dao = db.pan123AccountDao(),
            pan115Dao = db.pan115AccountDao(),
            guangyaDao = db.guangyaAccountDao(),
            ilanzouDao = db.ilanzouAccountDao(),
            lanzouDao = db.lanzouAccountDao(),
            videoAccountDao = db.videoAccountDao(),
        )
    }

    fun handle(requestJson: String): String {
        return try {
            val req = JSONObject(requestJson)
            val resp = JSONObject().put("ok", true)
            when (val cmd = req.optString("cmd")) {
                "version" -> resp
                    .put("version", APP_VERSION)
                    .put("preview", APP_IS_PREVIEW)

                "parse_share" -> {
                    val parsed = ShareLinkParser.parse(req.getString("text"))
                    if (parsed == null) {
                        resp.put("found", false)
                    } else {
                        resp.put("found", true)
                            .put("platform", parsed.platform.name)
                            .put("shareId", parsed.shareId)
                            .put("pwd", parsed.pwd ?: JSONObject.NULL)
                    }
                }

                "kv_put" -> {
                    HostStores.named("dll").put(req.getString("key"), req.getString("value"))
                    resp.put("stored", true)
                }

                "kv_get" -> {
                    val v = HostStores.named("dll").get(req.getString("key"), null)
                    resp.put("value", v ?: JSONObject.NULL)
                }

                "key_roundtrip" -> {
                    val dir = File(req.optString("dir", "")).takeIf { it.path.isNotBlank() }
                        ?: File(System.getProperty("java.io.tmpdir"), "yunx-dll-smoke")
                    val file = DbKeyStore.keyFile(dir)
                    val k1 = DbKeyStore.loadOrCreate(file, DbEncryptionLevel.LOOSE)
                    val k2 = DbKeyStore.loadOrCreate(file, DbEncryptionLevel.LOOSE)
                    resp.put("consistent", k1.contentEquals(k2))
                        .put("keyBytes", k1.size)
                        .put("wrapped", DbKeyStore.isWrapped(file))
                }

                "host_set_machine_id" -> {
                    val id = req.getString("id")
                    if (id.isBlank()) {
                        return error("machine id must not be blank").toString()
                    }
                    HostServices.machineProtector = DllMachineProtector(id)
                    resp.put("applied", true).put("idLength", id.length)
                }

                "host_set_proxy" -> {
                    // host 为 null/空 = 直连；端口仅在有 host 时有意义。设置后新请求即时走新代理。
                    val host = if (req.isNull("host")) null else req.optString("host").takeIf { it.isNotBlank() }
                    val port = req.optInt("port", 0)
                    HttpClients.setProxy(host, port)
                    resp.put("applied", true)
                        .put("host", host ?: JSONObject.NULL)
                        .put("port", if (host != null) port else 0)
                }

                "host_set_ffmpeg" -> {
                    val path = req.optString("path")
                    if (path.isBlank()) {
                        FfmpegLocator.externalFfmpeg = null
                        FfmpegLocator.invalidate()
                        resp.put("cleared", true)
                    } else {
                        val f = File(path)
                        if (!f.isFile) {
                            return error("ffmpeg not found: $path").toString()
                        }
                        FfmpegLocator.externalFfmpeg = f
                        FfmpegLocator.invalidate()
                        resp.put("applied", f.absolutePath)
                    }
                }

                "download_submit" -> {
                    val url = req.getString("url")
                    val headers = req.optJSONObject("headers")?.let { obj ->
                        val m = HashMap<String, String>()
                        for (k in obj.keys()) m[k] = obj.getString(k)
                        m
                    } ?: emptyMap()
                    val id = runBlocking {
                        downloadManager().enqueue(
                            url = url,
                            fileName = req.optString("fileName"),
                            headers = headers,
                            platform = req.optString("platform"),
                            saveDir = req.optString("saveDir"),
                        )
                    }
                    resp.put("taskId", id)
                }

                "download_list" -> {
                    val tasks = runBlocking { AppDatabase.get().downloadTaskDao().listAll() }
                    val stats = downloadManager().stats.value
                    val arr = JSONArray()
                    for (t in tasks.sortedByDescending { it.createTime }) {
                        val s = stats[t.id]
                        arr.put(
                            JSONObject()
                                .put("taskId", t.id)
                                .put("fileName", t.fileName)
                                .put("status", t.status)
                                .put("statusText", DownloadTaskEntity.statusText(t.status))
                                .put("total", t.totalSize)
                                .put("downloaded", t.downloadedSize)
                                .put("speed", s?.speed ?: 0L)
                                .put("remainMillis", s?.remainMillis ?: -1L)
                                .put("avgSpeed", t.avgSpeed)
                                .put("savePath", t.savePath ?: JSONObject.NULL)
                                .put("platform", t.platform ?: "")
                                .put("error", t.errorMsg ?: JSONObject.NULL)
                        )
                    }
                    resp.put("tasks", arr)
                }

                "download_pause" -> {
                    downloadManager().pause(req.getLong("taskId"))
                    resp.put("paused", true)
                }

                "download_resume" -> {
                    // 继续下载与失败重试同一入口（内部按断点续传/重探测处理）
                    downloadManager().start(req.getLong("taskId"))
                    resp.put("resumed", true)
                }

                "download_remove" -> {
                    downloadManager().remove(req.getLong("taskId"), req.optBoolean("deleteLocal"))
                    resp.put("removed", true)
                }

                "settings_list" -> {
                    val s = SettingsRepository()
                    resp.put(
                        "settings", JSONObject()
                            .put("darkMode", s.darkMode)
                            .put("themeColorMode", s.themeColorMode)
                            .put("themeSeedColor", s.themeSeedColor)
                            .put("reduceMotion", s.reduceMotion)
                            .put("downloadThreads", s.downloadThreads)
                            .put("downloadDirUri", s.downloadDirUri ?: JSONObject.NULL)
                            .put("maxConcurrentDownloads", s.maxConcurrentDownloads)
                            .put("downloadSpeedLimit", s.downloadSpeedLimit)
                            .put("downloadRetryCount", s.downloadRetryCount)
                            .put("keepAwakeWhileDownloading", s.keepAwakeWhileDownloading)
                            .put("shutdownWhenDownloadsComplete", s.shutdownWhenDownloadsComplete)
                            .put("notificationShowSpeed", s.notificationShowSpeed)
                            .put("clipboardLinkDetection", s.clipboardLinkDetection)
                            .put("closeBehavior", s.closeBehavior)
                            .put("proxyMode", s.proxyMode)
                            .put("proxyHost", s.proxyHost)
                            .put("proxyPort", s.proxyPort)
                            .put("http2Enabled", s.http2Enabled)
                            .put("downloadEngine", s.downloadEngine)
                            .put("btDhtEnabled", s.btDhtEnabled)
                            .put("btTrackers", s.btTrackers)
                            .put("quarkNoSaveDownload", s.quarkNoSaveDownload)
                            .put("multiAccountDownload", s.multiAccountDownload)
                            .put("multiSourceDownload", s.multiSourceDownload)
                            .put("downloadBufferSize", s.downloadBufferSize)
                            .put("slowPreemptEnabled", s.slowPreemptEnabled)
                            .put("slowPreemptMinBps", s.slowPreemptMinBps)
                            .put("slowPreemptMinAgeMs", s.slowPreemptMinAgeMs)
                            .put("previewEngine", s.previewEngine)
                            .put("diagnosticMode", s.diagnosticMode)
                            .put("downloadDebug", s.downloadDebug)
                            .put("fullFileSha256", s.fullFileSha256)
                            .put("ignoreSslCert", s.ignoreSslCert)
                            .put("dbEncryptionLevel", s.dbEncryptionLevel)
                            .put("storageDbDir", s.storageDbDir ?: JSONObject.NULL)
                            .put("storageCacheDir", s.storageCacheDir ?: JSONObject.NULL)
                            // ---- 储存目录只读快照（对齐原版 StorageSettingsPanel 的「当前生效」展示）----
                            // 实际生效目录：AppContext 启动时解析一次，改动重启后才变（配置值≠生效值 = 待重启）
                            .put("storageDataDir", AppContext.dataDir.absolutePath)
                            .put("storageDbDirEffective", AppContext.dbDir.absolutePath)
                            .put("storageCacheDirEffective", AppContext.cacheDir.absolutePath)
                            // 加密**实际**档位：以 credential.key 里实际的封装格式为准（偏好可能是手动改过的）
                            .put("dbEncryptionEffectiveLevel", DbEncryptionManager.effectiveLevel().id)
                            .put("onboardingShown", AppContext.miscPrefs.getBoolean("onboarding_shown", false))
                    )
                }

                "settings_set" -> {
                    val key = req.getString("key")
                    val value = req.optString("value")
                    val s = SettingsRepository()
                    when (key) {
                        "darkMode" -> s.darkMode = value.toInt()
                        "themeColorMode" -> s.themeColorMode = value.toInt()
                        "themeSeedColor" -> s.themeSeedColor = value.toLong()
                        "reduceMotion" -> s.reduceMotion = value.toBoolean()
                        "downloadThreads" -> s.downloadThreads = value.toInt()
                        "downloadThreadsFor" -> s.setDownloadThreads(
                            req.getString("platform"), value.toInt())
                        "downloadDirUri" ->
                            if (value.isBlank()) s.downloadDirUri = null else s.downloadDirUri = value
                        "maxConcurrentDownloads" -> s.maxConcurrentDownloads = value.toInt()
                        "downloadSpeedLimit" -> s.downloadSpeedLimit = value.toLong()
                        "downloadRetryCount" -> s.downloadRetryCount = value.toInt()
                        "keepAwakeWhileDownloading" -> s.keepAwakeWhileDownloading = value.toBoolean()
                        "shutdownWhenDownloadsComplete" -> s.shutdownWhenDownloadsComplete = value.toBoolean()
                        "notificationShowSpeed" -> s.notificationShowSpeed = value.toBoolean()
                        "clipboardLinkDetection" -> s.clipboardLinkDetection = value.toBoolean()
                        "closeBehavior" -> s.closeBehavior = value
                        "proxyMode" -> s.proxyMode = value
                        "proxyHost" -> s.proxyHost = value
                        "proxyPort" -> s.proxyPort = value.toInt()
                        "http2Enabled" -> s.http2Enabled = value.toBoolean()
                        "downloadEngine" -> s.downloadEngine = value
                        "btDhtEnabled" -> s.btDhtEnabled = value.toBoolean()
                        "btTrackers" -> s.btTrackers = value
                        "quarkNoSaveDownload" -> s.quarkNoSaveDownload = value.toBoolean()
                        "multiAccountDownload" -> s.multiAccountDownload = value.toBoolean()
                        "multiSourceDownload" -> s.multiSourceDownload = value.toBoolean()
                        "downloadBufferSize" -> s.downloadBufferSize = value.toInt()
                        "slowPreemptEnabled" -> s.slowPreemptEnabled = value.toBoolean()
                        "slowPreemptMinBps" -> s.slowPreemptMinBps = value.toLong()
                        "slowPreemptMinAgeMs" -> s.slowPreemptMinAgeMs = value.toLong()
                        "previewEngine" -> s.previewEngine = value
                        "diagnosticMode" -> {
                            s.diagnosticMode = value.toBoolean()
                            // 动态生效（对齐原版 SettingsScreen:672）：开启即起写线程，关闭时核心内部先落最后事件
                            runCatching { com.yunx.app.util.DiagnosticLog.setEnabled(value.toBoolean()) }
                        }
                        "downloadDebug" -> s.downloadDebug = value.toBoolean()
                        "fullFileSha256" -> s.fullFileSha256 = value.toBoolean()
                        "ignoreSslCert" -> s.ignoreSslCert = value.toBoolean()
                        // ---- 储存目录三项（改动需重启应用生效：AppContext 启动时只读一次）----
                        // 空串 = 清 null（恢复默认位置：缓存 <数据目录>/cache、数据库 <数据目录>）
                        "storageCacheDir" ->
                            if (value.isBlank()) s.storageCacheDir = null else s.storageCacheDir = value
                        "storageDbDir" ->
                            if (value.isBlank()) s.storageDbDir = null else s.storageDbDir = value
                        // 只写偏好档位（不重包裹密钥）；真正的切档动作走 storage_apply_encryption（同原版 DbEncryptionManager.switchTo）
                        "dbEncryptionLevel" -> s.dbEncryptionLevel = value
                        "onboardingShown" ->
                            AppContext.miscPrefs.putBoolean("onboarding_shown", value.toBoolean())
                        else -> return error("unknown settings key: $key").toString()
                    }
                    // 运行时联动：调优类设置即时生效（代理的应用由 Go 宿主负责注入）
                    if (key == "http2Enabled") HttpClients.setHttp2Enabled(s.http2Enabled)
                    // 下载调试：开启即时开始记录；关闭即清空全部日志（对齐原版 ThemeController.setEnabled 联动）
                    if (key == "downloadDebug") runCatching { DownloadDebugLog.setEnabled(s.downloadDebug) }
                    runCatching { DownloadTuning.applyFrom(s) }
                    resp.put("stored", key)
                }

                // ---- 储存目录（对齐原版 StorageDirectoryScreen；确认弹窗在 UI 侧，这里只做校验+执行）----

                "storage_migrate_db" -> {
                    // 原版 StorageSettingsPanel.choose()+migrate() 的 DLL 版：
                    // 校验目标 → 拷贝数据文件 → 记「旧位置待清理」→ 写 storageDbDir。
                    // 四条准则与原版 StorageMigrator 一致：只拷贝不删除 / 目标必须干净 /
                    // 失败不切配置 / 搬移源 = 当前 AppContext 实际目录（dbDir，重启后才变）。
                    val raw = req.getString("targetDir")
                    if (raw.isBlank()) return error("targetDir must not be blank").toString()
                    val target = File(raw)
                    val source = AppContext.dbDir
                    val settings = SettingsRepository()
                    val configured = settings.storageDbDir?.let { File(it) } ?: AppContext.dataDir
                    when {
                        // 目标 = 已配置目录：无事可做（原版「已经是该设置」）
                        samePath(target, configured) -> resp
                            .put("migrated", false)
                            .put("reason", "already-configured")
                            .put("target", target.absolutePath)
                        // 目标 = 当前正在用的目录：不搬数据，只把配置写回去（原版「已切回当前生效目录」）
                        samePath(target, source) -> {
                            settings.storageDbDir =
                                if (samePath(target, AppContext.dataDir)) null else target.absolutePath
                            resp.put("migrated", false)
                                .put("reason", "target-is-effective")
                                .put("target", target.absolutePath)
                        }
                        else -> {
                            // 目标已存在 yunx.db 一律拒绝（防止两套数据混在一起）+ 探针文件验可写
                            StorageMigrator.checkDatabaseTarget(target)?.let {
                                return error(it).toString()
                            }
                            val report = try {
                                StorageMigrator.migrateDatabase(source, target)
                            } catch (e: Throwable) {
                                // 拷贝失败：原样保留旧配置，宁可「没搬成」也不「搬一半就切过去」
                                return error("搬迁失败，目录设置未改动：${e.message ?: e.javaClass.simpleName}").toString()
                            }
                            // 迁移成功才记待清理 + 写配置（默认位置存 null，跟随数据根目录）
                            StorageMigrator.rememberCleanupForDatabase(source)
                            settings.storageDbDir =
                                if (samePath(target, AppContext.dataDir)) null else target.absolutePath
                            val skipped = JSONArray()
                            report.skipped.forEach { skipped.put(it) }
                            resp.put("migrated", true)
                                .put("copiedFiles", report.copiedFiles)
                                .put("copiedBytes", report.copiedBytes)
                                .put("skipped", skipped)
                                .put("skippedCount", report.skippedCount)
                                .put("source", source.absolutePath)
                                .put("target", target.absolutePath)
                                .put("restartRequired", true)
                        }
                    }
                }

                "storage_apply_encryption" -> {
                    // 原版 DbEncryptionManager.switchTo 的 DLL 版：解包主密钥 → 按新档重新包裹 → 原子替换，
                    // 不碰库里任何数据，秒级完成、立即生效（无需重启）。失败保持原档位与原密钥文件不变。
                    val target = DbEncryptionLevel.entries.firstOrNull { it.id == req.getString("level") }
                        ?: return error(
                            "未知加密等级：${req.optString("level")}（可用：${DbEncryptionLevel.entries.joinToString("/") { it.id }}）"
                        ).toString()
                    DbEncryptionManager.switchTo(target).fold(
                        onSuccess = { previous ->
                            resp.put("applied", target.id)
                                .put("label", target.label)
                                .put("effectiveLevel", DbEncryptionManager.effectiveLevel().id)
                                .put("previousEffective", previous?.id ?: JSONObject.NULL)
                        },
                        onFailure = { e ->
                            return error(
                                "切换加密等级失败，仍保持原档位：${e.message ?: e.javaClass.simpleName}"
                            ).toString()
                        },
                    )
                }

                "accounts_list" -> {
                    val arr = JSONArray()
                    for (platform in DRIVE_PLATFORMS) {
                        val accounts = runBlocking { accountDirectory.list(platform) }
                        for (a in accounts) {
                            arr.put(
                                JSONObject()
                                    .put("platform", platform)
                                    .put("id", a.id)
                                    .put("nickname", a.nickname ?: "")
                                    .put("remark", a.remark ?: "")
                                    .put("isPrimary", a.isPrimary)
                                    .put("updatedAt", a.updatedAt)
                            )
                        }
                    }
                    resp.put("accounts", arr)
                }

                "account_set_primary" -> {
                    runBlocking { accountDirectory.setPrimary(req.getString("platform"), req.getString("id")) }
                    resp.put("ok", true)
                }

                "account_set_remark" -> {
                    runBlocking {
                        accountDirectory.setRemark(req.getString("platform"), req.getString("id"), req.getString("remark"))
                    }
                    resp.put("ok", true)
                }

                "account_remove" -> {
                    runBlocking { accountDirectory.remove(req.getString("platform"), req.getString("id")) }
                    resp.put("ok", true)
                }

                "auth_backup_name" -> {
                    val encrypted = req.optBoolean("encrypted", true)
                    resp.put("name", authBackup.defaultBackupFileName(encrypted))
                }

                "auth_export" -> {
                    val path = req.getString("path")
                    if (path.isBlank()) return error("path must not be blank").toString()
                    val password = req.optString("password").takeIf { it.isNotBlank() }
                    val onlyLoggedIn = req.optBoolean("onlyLoggedIn", true)
                    val content = if (password != null) {
                        runBlocking { authBackup.export(password, onlyLoggedIn) }
                    } else {
                        runBlocking { authBackup.exportJson(onlyLoggedIn) }
                    }
                    val okSaved = runBlocking { authBackup.saveTo(content, File(path)) }
                    if (!okSaved) return error("写入备份文件失败: $path").toString()
                    resp.put("path", File(path).absolutePath)
                        .put("bytes", content.toByteArray(Charsets.UTF_8).size)
                        .put("encrypted", password != null)
                }

                "auth_import" -> {
                    val path = req.getString("path")
                    val file = File(path)
                    if (!file.isFile) return error("备份文件不存在: $path").toString()
                    val content = file.readText(Charsets.UTF_8)
                    val password = req.optString("password").takeIf { it.isNotBlank() }
                    val imported = runBlocking { authBackup.import(content, password) }
                    resp.put("imported", imported)
                }

                "share_resolve" -> {
                    val url = req.getString("url")
                    val pwd = req.optString("pwd").takeIf { it.isNotBlank() }
                    val parsed = ShareLinkParser.parse(url)
                        ?: return error("无法识别的分享链接").toString()
                    val plat = parsed.platform
                    val repo = ResolveGateway.repoFor(plat)
                    val cred = runBlocking { ResolveGateway.credentialFor(plat) }
                    val session = runBlocking { repo.createSession(url, pwd, cred) }
                        .getOrElse { return error("建会话失败：${it.message}").toString() }
                    ResolveGateway.bind(plat, session)
                    runCatching { recordLinkHistory(url, session.title, plat.name, pwd) }
                    val rootFid = ResolveGateway.rootDirFor(plat)
                    val files = runBlocking { repo.listFiles(session, rootFid, cred) }
                        .getOrElse { return error("列目录失败：${it.message}").toString() }
                    resp.put("platform", plat.name)
                        .put("title", session.title)
                        .put("shareId", session.shareId)
                        .put("guest", cred.isBlank())
                        .put("dirFid", rootFid)
                        .put("files", filesJson(files))
                }

                "share_list" -> {
                    val plat = ResolveGateway.platform
                        ?: return error("尚未解析任何分享").toString()
                    val session = ResolveGateway.session!!
                    val dirFid = req.getString("dirFid")
                    val cred = runBlocking { ResolveGateway.credentialFor(plat) }
                    val files = runBlocking { ResolveGateway.repoFor(plat).listFiles(session, dirFid, cred) }
                        .getOrElse { return error("列目录失败：${it.message}").toString() }
                    resp.put("dirFid", dirFid).put("files", filesJson(files))
                }

                "share_link", "share_download" -> {
                    val plat = ResolveGateway.platform
                        ?: return error("尚未解析任何分享").toString()
                    val session = ResolveGateway.session!!
                    val repo = ResolveGateway.repoFor(plat)
                    val cred = runBlocking { ResolveGateway.credentialFor(plat) }
                    val guest = cred.isBlank()
                    if (guest && !ResolveGateway.supportsGuestDownload(plat)) {
                        return error("该平台未登录时无法下载，请先在网盘页登录").toString()
                    }
                    val file = ShareFile(
                        fid = req.getString("fid"),
                        fname = req.optString("fname"),
                        fsize = req.optLong("fsize", -1L),
                        isdir = false,
                        pdirFid = "",
                        fidToken = req.optString("fidToken"),
                    )
                    // 夸克/UC 共用 __puus：取链前确保新鲜（直链签名绑定取链时刻的 Cookie）；
                    // fresh 提到外层供多源备选源构造复用（备选池的过滤基准就是这份主源凭证）
                    val fresh = if (guest) "" else runBlocking { ResolveGateway.freshCredential(plat, cred) }
                    val link = runBlocking {
                        if (guest) {
                            repo.getGuestShareDownloadLink(session, file)
                        } else {
                            repo.getShareDownloadLinkWithoutSave(session, file, fresh)
                                .recoverCatching {
                                    if (plat == com.yunx.app.data.network.SharePlatform.QUARK) {
                                        repo.getShareDownloadLink(session, file, fresh).getOrThrow()
                                    } else throw it
                                }
                        }
                    }.getOrElse { return error("取直链失败：${it.message}").toString() }

                    val effectiveCred = if (guest) link.guestCookie else cred
                    val finalUrl = ResolveGateway.fastestUrl(plat, link.downloadUrl, effectiveCred)
                    val headers = ResolveGateway.downloadHeaders(plat, effectiveCred, link)
                    if (cmd == "share_link") {
                        resp.put("url", finalUrl)
                            .put("filename", link.filename)
                            .put("size", link.size)
                            .put("isHls", link.isHls)
                            .put("guest", guest)
                        val h = JSONObject()
                        headers.forEach { (k, v) -> h.put(k, v) }
                        resp.put("headers", h)
                    } else {
                        val taskId = runBlocking {
                            downloadManager().enqueue(
                                url = finalUrl,
                                fileName = link.filename,
                                headers = headers,
                                size = link.size,
                                platform = plat.name.lowercase(),
                                // C1/C2 多源：两个开关同时开且平台受支持时，其他账号各自重新取
                                // 一条直链作为备选源（夸克直链绑定账号，同 URL 换 Cookie 不可靠）；
                                // 游客链路 fresh 为空串，buildAltSources 直接不建
                                altSources = ExtraGateway.buildAltSources(plat, repo, session, file, fresh),
                                saveDir = req.optString("saveDir"),
                            ) {
                                // 下载完成（或失败落定）后清理转存临时目录（对齐桌面 onComplete）
                                link.cleanupDirFid?.takeIf { it.isNotBlank() }?.let { dirFid ->
                                    runCatching { repo.cleanupTempDir(dirFid, effectiveCred) }
                                }
                            }
                        }
                        resp.put("taskId", taskId).put("filename", link.filename).put("size", link.size)
                    }
                }

                "link_history_list" -> {
                    val rows = runBlocking { AppDatabase.get().linkHistoryDao().observeAll().first() }
                    val arr = JSONArray()
                    for (h in rows.sortedByDescending { it.createTime }.take(50)) {
                        arr.put(JSONObject()
                            .put("id", h.id).put("url", h.url).put("title", h.title)
                            .put("platform", h.platform).put("pwd", h.pwd).put("createTime", h.createTime))
                    }
                    resp.put("history", arr)
                }

                "link_history_add" -> {
                    recordLinkHistory(
                        req.getString("url"), req.optString("title"),
                        req.optString("platform"), req.optString("pwd").takeIf { it.isNotBlank() },
                    )
                    resp.put("stored", true)
                }

                "link_history_delete" -> {
                    runBlocking { AppDatabase.get().linkHistoryDao().delete(req.getLong("id")) }
                    resp.put("deleted", true)
                }

                "link_history_clear" -> {
                    runBlocking { AppDatabase.get().linkHistoryDao().clear() }
                    resp.put("cleared", true)
                }

                "bookmark_list" -> {
                    val rows = runBlocking { AppDatabase.get().bookmarkDao().observeAll().first() }
                    val arr = JSONArray()
                    for (b in rows.sortedByDescending { it.createTime }) {
                        arr.put(JSONObject()
                            .put("id", b.id).put("link", b.link).put("title", b.title)
                            .put("platform", b.platform).put("pwd", b.pwd)
                            .put("category", b.category).put("createTime", b.createTime))
                    }
                    resp.put("bookmarks", arr)
                }

                "bookmark_add" -> {
                    val now = System.currentTimeMillis()
                    val id = runBlocking {
                        AppDatabase.get().bookmarkDao().insert(
                            BookmarkEntity(
                                link = req.getString("link"),
                                title = req.optString("title"),
                                platform = req.optString("platform"),
                                pwd = req.optString("pwd"),
                                category = req.optString("category", "未分类"),
                                createTime = now,
                            )
                        )
                    }
                    resp.put("id", id)
                }

                "bookmark_delete" -> {
                    runBlocking { AppDatabase.get().bookmarkDao().delete(req.getLong("id")) }
                    resp.put("deleted", true)
                }

                "cloud_list" -> {
                    val plat = platformOf(req.optString("platform"))
                        ?: return error("未知平台：${req.optString("platform")}").toString()
                    val root = CloudGateway.rootFor(plat)
                    val dirFid = req.optString("dirFid").takeIf { it.isNotBlank() } ?: root
                    val files = runBlocking { CloudGateway.list(plat, dirFid) }
                    resp.put("dirFid", dirFid).put("rootFid", root).put("files", filesJson(files))
                }

                "cloud_mkdir" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    val dirFid = req.optString("dirFid").takeIf { it.isNotBlank() }
                        ?: CloudGateway.rootFor(plat)
                    runBlocking { CloudGateway.mkdir(plat, dirFid, req.getString("name")) }
                    resp.put("created", true)
                }

                "cloud_rename" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    val file = ShareFile(
                        fid = req.getString("fid"),
                        fname = req.optString("fname"),
                        fsize = 0, isdir = req.optBoolean("isdir"),
                        pdirFid = "", fidToken = req.optString("fidToken"),
                    )
                    runBlocking { CloudGateway.rename(plat, file, req.getString("newName")) }
                    resp.put("renamed", true)
                }

                "cloud_move" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    val files = filesFromReq(req)
                    runBlocking {
                        CloudGateway.move(plat, files, req.getString("targetFid"), req.optString("targetPath").takeIf { it.isNotBlank() })
                    }
                    resp.put("moved", files.size)
                }

                "cloud_delete" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    val files = filesFromReq(req)
                    runBlocking { CloudGateway.delete(plat, files) }
                    resp.put("deleted", files.size)
                }

                "cloud_quota" -> {
                    val platforms = listOf(
                        SharePlatform.QUARK, SharePlatform.UC, SharePlatform.XUNLEI, SharePlatform.BAIDU,
                        SharePlatform.C139, SharePlatform.PAN123, SharePlatform.PAN115,
                        SharePlatform.GUANGYA, SharePlatform.ILANZOU, SharePlatform.LANZOU,
                    )
                    val results = runBlocking {
                        coroutineScope { platforms.map { async { it to CloudGateway.quota(it) } }.awaitAll() }
                    }
                    val q = JSONObject()
                    for ((p, info) in results) {
                        q.put(p.name.lowercase(), if (info != null) {
                            JSONObject().put("used", info.used).put("total", info.total)
                        } else JSONObject.NULL)
                    }
                    resp.put("quotas", q)
                }

                "cloud_download" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    val file = ShareFile(
                        fid = req.getString("fid"),
                        fname = req.getString("fname"),
                        fsize = req.optLong("fsize", -1L), isdir = false,
                        pdirFid = "", fidToken = req.optString("fidToken"),
                    )
                    val link = runBlocking { CloudGateway.downloadLink(plat, file) }
                    val cred = runBlocking { ResolveGateway.credentialFor(plat) }
                    val finalUrl = ResolveGateway.fastestUrl(plat, link.downloadUrl, cred)
                    val headers = ResolveGateway.downloadHeaders(plat, cred, link)
                    val taskId = runBlocking {
                        downloadManager().enqueue(
                            url = finalUrl, fileName = link.filename, headers = headers,
                            size = link.size, platform = plat.name.lowercase(),
                            saveDir = req.optString("saveDir"),
                        )
                    }
                    resp.put("taskId", taskId).put("filename", link.filename).put("size", link.size)
                }

                "cloud_link" -> {
                    // 云盘文件直链（预览用，不入队）：与 cloud_download 同一条取链路径。
                    // 浏览器 <video> 无法带自定义请求头——夸克/UC 的 CDN 签名直链可独立播放；
                    // 需要鉴权头的平台（C139 等）浏览器可能播不了，属尽力而为。
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    val file = ShareFile(
                        fid = req.getString("fid"),
                        fname = req.optString("fname"),
                        fsize = req.optLong("fsize", -1L), isdir = false,
                        pdirFid = "", fidToken = req.optString("fidToken"),
                    )
                    val link = runBlocking { CloudGateway.downloadLink(plat, file) }
                        ?: return error("取直链失败（账号可能未登录或链接已过期）").toString()
                    val cred = runBlocking { ResolveGateway.credentialFor(plat) }
                    val finalUrl = ResolveGateway.fastestUrl(plat, link.downloadUrl, cred)
                    val headers = ResolveGateway.downloadHeaders(plat, cred, link)
                    val h = JSONObject()
                    headers.forEach { (k, v) -> h.put(k, v) }
                    resp.put("url", finalUrl).put("filename", link.filename).put("size", link.size)
                        .put("headers", h)
                }

                "share_transfer" -> {
                    val plat = ResolveGateway.platform
                        ?: return error("尚未解析任何分享").toString()
                    val session = ResolveGateway.session!!
                    val repo = ResolveGateway.repoFor(plat)
                    val cred = runBlocking { ResolveGateway.credentialFor(plat) }
                    if (cred.isBlank()) {
                        return error("转存需要登录${plat.name}，请先在网盘页登录").toString()
                    }
                    val file = ShareFile(
                        fid = req.getString("fid"),
                        fname = req.optString("fname"),
                        fsize = req.optLong("fsize", 0L),
                        isdir = req.optBoolean("isdir"),
                        pdirFid = "", fidToken = req.optString("fidToken"),
                    )
                    val toDirFid = req.getString("toDirFid")
                    val result = runBlocking {
                        if (plat == SharePlatform.QUARK) {
                            (repo as com.yunx.app.data.repository.QuarkResolveRepository)
                                .saveToCloud(session, file, toDirFid, cred)
                        } else {
                            repo.transferFile(session, file, toDirFid, cred)
                        }
                    }
                    result.fold(
                        onSuccess = { resp.put("transferredFid", it ?: JSONObject.NULL).put("transferred", true) },
                        onFailure = { return error("转存失败：${it.message}").toString() },
                    )
                }

                "share_transfer_batch" -> {
                    // 批量转存（对齐桌面 ResolveViewModel:464 批量转存入口）：
                    // ①整批空间预算只查一次（ensureBatchSpace），不够整批中止；
                    // ②逐项 skipSpaceCheck=true 免二次配额校验；
                    // ③单项失败不中断其余（对齐桌面 runCatching 口径），返回成功数+失败清单。
                    val plat = ResolveGateway.platform
                        ?: return error("尚未解析任何分享").toString()
                    val session = ResolveGateway.session!!
                    val repo = ResolveGateway.repoFor(plat)
                    val cred = runBlocking { ResolveGateway.credentialFor(plat) }
                    if (cred.isBlank()) {
                        return error("转存需要登录${plat.name}，请先在网盘页登录").toString()
                    }
                    val arr = req.optJSONArray("files") ?: JSONArray()
                    if (arr.length() == 0) return error("批量转存列表为空").toString()
                    val files = (0 until arr.length()).map { i ->
                        val o = arr.optJSONObject(i) ?: JSONObject()
                        ShareFile(
                            fid = o.getString("fid"),
                            fname = o.optString("fname"),
                            fsize = o.optLong("fsize", 0L),
                            isdir = o.optBoolean("isdir"),
                            pdirFid = "", fidToken = o.optString("fidToken"),
                        )
                    }
                    val toDirFid = req.getString("toDirFid")
                    var okCount = 0
                    val failures = JSONArray()
                    runBlocking {
                        // 不支持配额查询的平台默认返回 false 不抛错 → 视为通过（接口默认实现语义）
                        runCatching { repo.ensureBatchSpace(files.map { it.fsize }, cred) }
                            .getOrElse { e -> return@runBlocking error(e.message ?: "剩余空间不足") }
                        for (f in files) {
                            val r = if (plat == SharePlatform.QUARK) {
                                (repo as com.yunx.app.data.repository.QuarkResolveRepository)
                                    .saveToCloud(session, f, toDirFid, cred, skipSpaceCheck = true)
                            } else {
                                repo.transferFile(session, f, toDirFid, cred, skipSpaceCheck = true)
                            }
                            r.fold(
                                onSuccess = { okCount++ },
                                onFailure = {
                                    failures.put(JSONObject().put("fid", f.fid)
                                        .put("error", it.message ?: "转存失败"))
                                },
                            )
                        }
                    }?.let { return it.toString() }
                    resp.put("transferred", okCount).put("total", files.size).put("failures", failures)
                }

                "account_login_cookie" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    val credential = req.getString("credential")
                    if (credential.isBlank()) return error("凭据不能为空").toString()
                    val newAccount = req.optBoolean("newAccount", false)
                    val ok = runBlocking {
                        when (plat) {
                            SharePlatform.QUARK -> ResolveGateway.quarkAccount.saveQuarkAccount(credential, newAccount)
                            SharePlatform.UC -> ResolveGateway.ucAccount.saveUCAccount(credential, newAccount)
                            SharePlatform.BAIDU -> ResolveGateway.baiduAccount.saveBaiduAccount(credential, newAccount)
                            SharePlatform.C139 -> ResolveGateway.c139Account.saveC139Account(credential, newAccount)
                            SharePlatform.PAN115 -> ResolveGateway.pan115Account.saveCookie(credential, newAccount)
                            SharePlatform.LANZOU -> ResolveGateway.lanzouAccount.saveCookie(credential, newAccount)
                            SharePlatform.XUNLEI -> ResolveGateway.xunleiAccount.saveWebCredential(credential, newAccount)
                            else -> throw UnsupportedOperationException("${plat.name} 暂不支持粘贴凭据登录（密码登录待后续批次）")
                        }
                    }
                    if (ok) resp.put("loggedIn", true) else error("凭据校验失败（可能已过期），请检查后重试").toString()
                }

                "share_folder_download" -> {
                    // 会话/登录态校验失败同步抛出（命令层立即返回 error）；递归展开与入队走 batchScope 后台协程
                    val batchId = runCatching {
                        ExtraGateway.startFolderDownload(
                            scope = batchScope,
                            manager = downloadManager(),
                            fid = req.getString("fid"),
                            fname = req.optString("fname"),
                            saveDir = req.optString("saveDir"),
                        )
                    }.getOrElse { return error(it.message ?: "启动批量下载失败").toString() }
                    resp.put("batchId", batchId)
                }

                "batch_list" -> resp.put("batches", ExtraGateway.batchesJson())

                "cloud_share" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    // API 没有创建分享方法的平台（蓝奏两家）提前拒绝，保证错误文案不带异常类名前缀
                    if (plat !in setOf(
                            SharePlatform.QUARK, SharePlatform.UC, SharePlatform.BAIDU, SharePlatform.C139,
                            SharePlatform.PAN123, SharePlatform.PAN115, SharePlatform.GUANGYA, SharePlatform.XUNLEI,
                        )
                    ) {
                        return error("该平台暂不支持创建分享").toString()
                    }
                    val fidArr = req.optJSONArray("fids")
                    val fids = fidArr?.let { arr -> (0 until arr.length()).map { arr.optString(it) } }.orEmpty()
                    if (fids.isEmpty()) return error("fids 不能为空").toString()
                    val title = req.optString("title")
                    val passcode = req.optString("passcode")
                    // 有效期为 UI 中性码（缺省 = 永久）；各平台必须经 ShareExpire 转换，禁止直传
                    val expiredType = if (req.has("expiredType") && !req.isNull("expiredType")) {
                        req.optInt("expiredType")
                    } else {
                        ShareExpire.FOREVER
                    }
                    val shareUrl = try {
                        runBlocking {
                        when (plat) {
                            SharePlatform.QUARK -> ExtraGateway.requireQuarkCookie().let { cookie ->
                                val shareId = ResolveGateway.quarkApi.createShare(
                                    fidList = fids, title = title.ifBlank { "分享文件" },
                                    urlType = if (passcode.isBlank()) 1 else 2, passcode = passcode,
                                    expiredType = expiredType, cookie = cookie,
                                ) ?: throw IllegalStateException("创建分享失败")
                                ResolveGateway.quarkApi.getShareInfo(shareId, cookie)?.shareUrl
                                    ?: throw IllegalStateException("获取分享链接失败")
                            }
                            SharePlatform.UC -> ExtraGateway.requireUcCookie().let { cookie ->
                                val shareId = ResolveGateway.ucApi.createShare(
                                    fidList = fids, title = title.ifBlank { "分享文件" },
                                    urlType = if (passcode.isBlank()) 1 else 2, passcode = passcode,
                                    expiredType = expiredType, cookie = cookie,
                                ) ?: throw IllegalStateException("创建分享失败")
                                ResolveGateway.ucApi.getShareInfo(shareId, cookie)?.shareUrl
                                    ?: throw IllegalStateException("获取分享链接失败")
                            }
                            SharePlatform.BAIDU -> ExtraGateway.requireBaiduCookie().let { cookie ->
                                ResolveGateway.baiduApi.createShare(
                                    fsIds = fids, period = ShareExpire.baiduPeriod(expiredType),
                                    pwd = passcode, cookie = cookie,
                                ).link
                            }
                            // DLL 只拿到 fid 列表、无 isdir 信息：统一按文件（coIDLst）传
                            SharePlatform.C139 -> ExtraGateway.requireC139Cookie().let { cookie ->
                                ResolveGateway.c139Api.createShare(
                                    coIDLst = fids, caIDLst = emptyList(),
                                    period = ShareExpire.daysOrNull(expiredType),
                                    dedicatedName = title.ifBlank { "分享文件" }, cookie = cookie,
                                ).shareUrl
                            }
                            SharePlatform.PAN123 -> ExtraGateway.requirePan123Token().let { token ->
                                ResolveGateway.pan123Api.createShare(
                                    fileIds = fids, shareName = title.ifBlank { "分享文件" },
                                    expiration = ExtraGateway.pan123Expiration(ShareExpire.daysOrNull(expiredType)),
                                    sharePwd = passcode.takeIf { it.isNotBlank() }, token = token,
                                ).shareUrl
                            }
                            SharePlatform.PAN115 -> ExtraGateway.requirePan115Cookie().let { cookie ->
                                ResolveGateway.pan115Api.createShare(
                                    fileIds = fids, duration = ShareExpire.pan115Duration(expiredType), cookie = cookie,
                                ).shareUrl
                            }
                            SharePlatform.GUANGYA -> ExtraGateway.requireGuangYaToken().let { token ->
                                ResolveGateway.guangyaApi.createShare(
                                    accessToken = token, fileIds = fids, title = title.ifBlank { "分享文件" },
                                    expireDays = ShareExpire.daysOrNull(expiredType),
                                    passcode = passcode.takeIf { it.isNotBlank() },
                                ).shareUrl
                            }
                            SharePlatform.XUNLEI -> ExtraGateway.requireXunlei().let { (t, d, c) ->
                                ResolveGateway.xunleiApi.createShare(
                                    fileIds = fids, title = title.ifBlank { "分享文件" },
                                    expirationDays = ShareExpire.xunleiDays(expiredType),
                                    accessToken = t, deviceId = d, captchaToken = c,
                                    passCode = passcode,
                                )?.shareUrl ?: throw IllegalStateException("创建分享失败")
                            }
                            else -> throw UnsupportedOperationException("该平台暂不支持创建分享")
                        }
                    }
                    } catch (e: Throwable) {
                        // 保留底层原始文案（如「未登录夸克网盘」），不加异常类名前缀
                        return error(e.message ?: "创建分享失败").toString()
                    }
                    resp.put("shareUrl", shareUrl)
                }

                "account_login_password" -> {
                    val plat = platformOf(req.getString("platform"))
                        ?: return error("未知平台").toString()
                    // 只接四个有密码登录仓库方法的平台，其余提前拒绝（错误文案不带异常类名前缀）
                    if (plat !in setOf(
                            SharePlatform.PAN123, SharePlatform.GUANGYA,
                            SharePlatform.ILANZOU, SharePlatform.XUNLEI,
                        )
                    ) {
                        return error("该平台暂不支持账号密码登录").toString()
                    }
                    val account = req.getString("account")
                    val password = req.getString("password")
                    if (account.isBlank() || password.isBlank()) return error("账号或密码不能为空").toString()
                    val ok = try {
                        runBlocking {
                            when (plat) {
                                SharePlatform.PAN123 -> {
                                    // 返回 null = 成功；失败带可直接展示的原因（已做过错误映射）
                                    val msg = ResolveGateway.pan123Account.loginWithPassword(account, password)
                                    if (msg != null) throw IllegalStateException(msg)
                                    true
                                }
                                SharePlatform.GUANGYA -> {
                                    ResolveGateway.guangyaAccount.login(account, password).getOrThrow()
                                    true
                                }
                                SharePlatform.ILANZOU -> {
                                    ResolveGateway.ilanzouAccount.login(account, password).getOrThrow()
                                    true
                                }
                                else -> {
                                    // 官方密码登录几乎必然触发短信验证（review_panel）：拿不到会话就明确告知未接入
                                    val step = ResolveGateway.xunleiAccount.loginWithPassword(account, password)
                                    if (step.sessionId.isBlank()) {
                                        throw IllegalStateException("迅雷密码登录需验证流程，暂未接入")
                                    }
                                    ResolveGateway.xunleiAccount.finishLogin(step, account)
                                }
                            }
                        }
                    } catch (e: Throwable) {
                        // 保留底层原始文案（如 123 的映射错误、迅雷的「需验证流程」），不加异常类名前缀
                        return error(e.message ?: "登录失败").toString()
                    }
                    if (ok) resp.put("loggedIn", true) else return error("登录失败，请检查账号与密码").toString()
                }

                "github_resolve" -> {
                    val parsed = GitHubLinkParser.parse(req.getString("url"))
                        ?: return error("无法识别的 GitHub 链接").toString()
                    when (parsed) {
                        is GitHubLinkType.Repository -> {
                            // 与 update_check 同款口径：优先取最新正式版，没有正式版再取最新一个
                            val releases = runBlocking { githubApi.getReleases(parsed.owner, parsed.repo) }
                            val latest = releases?.firstOrNull { !it.prerelease && !it.draft }
                                ?: releases?.firstOrNull()
                                ?: return error("获取 Release 失败（网络/镜像不可用）").toString()
                            val assets = JSONArray()
                            for (a in latest.assets) {
                                assets.put(
                                    JSONObject()
                                        .put("name", a.name)
                                        .put("size", a.size)
                                        .put("url", a.downloadUrl)
                                )
                            }
                            resp.put("repo", "${parsed.owner}/${parsed.repo}")
                                .put("tag", latest.tagName)
                                .put("assets", assets)
                        }
                        else -> return error("请传入 GitHub 仓库链接（github.com/owner/repo）").toString()
                    }
                }

                "github_download" -> {
                    // 前端传 Release 页链接（与 github_resolve 同源）：先解析出附件直链，按 fileName 匹配入队
                    val pageUrl = req.getString("url")
                    val wantName = req.optString("fileName")
                    val parsedLink = GitHubLinkParser.parse(pageUrl)
                        ?: return error("无法识别的 GitHub 链接").toString()
                    val repo = (parsedLink as? GitHubLinkType.Repository)
                        ?: return error("请传入 GitHub 仓库链接（github.com/owner/repo）").toString()
                    val releases = runBlocking { githubApi.getReleases(repo.owner, repo.repo) }
                    val latest = releases?.firstOrNull { !it.prerelease && !it.draft }
                        ?: releases?.firstOrNull()
                        ?: return error("获取 Release 失败（网络/镜像不可用）").toString()
                    val asset = latest.assets.firstOrNull { it.name == wantName }
                        ?: latest.assets.firstOrNull()
                        ?: return error("该 Release 没有可下载附件").toString()
                    val taskId = runBlocking {
                        downloadManager().enqueue(
                            url = asset.downloadUrl,
                            fileName = wantName.ifBlank { asset.name },
                            platform = "github",
                            saveDir = req.optString("saveDir"),
                        )
                    }
                    resp.put("taskId", taskId).put("url", asset.downloadUrl)
                }

                "video_accounts_list" ->
                    resp.put("accounts", runBlocking { ExtraGateway.videoAccountsJson() })

                "video_login_cookie" -> {
                    val platform = VideoPlatform.fromName(req.getString("platform").trim().uppercase())
                        ?: return error("未知视频平台：${req.optString("platform")}").toString()
                    // 最简校验（Cookie 非空即落库）；落位规则在 ExtraGateway（照抄 resolveAccountSlot）
                    val ok = runBlocking { ExtraGateway.saveVideoCookie(platform, req.getString("cookie")) }
                    if (ok) resp.put("loggedIn", true) else return error("Cookie 不能为空").toString()
                }

                "video_parse", "video_parse_quality" -> {
                    // 全量解析结果在 ExtraGateway 组装（qualities/medias 的映射规则见 parseVideoJson）；
                    // qualityId 是 B 站 qn（可传数字或数字串），其它平台忽略
                    val parsed = try {
                        runBlocking {
                            ExtraGateway.parseVideoJson(
                                url = req.getString("url"),
                                qualityQn = req.optString("qualityId").takeIf { it.isNotBlank() }?.toIntOrNull(),
                            )
                        }
                    } catch (e: Throwable) {
                        // 保留 ExtraGateway 抛出的原始文案（如「抖音解析需网页引擎，待 M7」），不加前缀
                        return error(e.message ?: "视频解析失败").toString()
                    }
                    for (key in parsed.keys()) resp.put(key, parsed.get(key))
                }

                "video_download" -> {
                    // 取链（重新解析）后按 mode 入队：merge=合并音视频（默认）/ audio / video / separate
                    val result = try {
                        runBlocking {
                            ExtraGateway.enqueueVideoDownload(
                                manager = downloadManager(),
                                url = req.getString("url"),
                                qualityQn = req.optString("qualityId").takeIf { it.isNotBlank() }?.toIntOrNull(),
                                saveDir = req.optString("saveDir"),
                                mode = req.optString("mode", "merge").ifBlank { "merge" },
                            )
                        }
                    } catch (e: Throwable) {
                        return error(e.message ?: "视频下载入队失败").toString()
                    }
                    for (key in result.keys()) resp.put(key, result.get(key))
                }

                "download_redownload" -> {
                    // 重新下载 = 用原直链新建任务；false = 直链已过期（Range 探测失败）或任务不存在
                    val ok = runBlocking { downloadManager().redownload(req.getLong("taskId")) }
                    if (!ok) return error("直链已失效或任务不存在，无法重新下载").toString()
                    resp.put("redownloaded", true)
                }

                "download_pause_all" -> {
                    // 只暂停「下载中」的任务；等待中（PENDING）的任务没有协程在跑，无需暂停
                    val tasks = runBlocking { AppDatabase.get().downloadTaskDao().listAll() }
                    var paused = 0
                    for (t in tasks.filter { it.status == DownloadTaskEntity.STATUS_DOWNLOADING }) {
                        downloadManager().pause(t.id)
                        paused++
                    }
                    resp.put("paused", paused)
                }

                "download_resume_all" -> {
                    // 暂停(2)与失败(4)同一入口恢复（start 内部按断点续传/重试处理）
                    val tasks = runBlocking { AppDatabase.get().downloadTaskDao().listAll() }
                    val resumable = tasks.filter {
                        it.status == DownloadTaskEntity.STATUS_PAUSED || it.status == DownloadTaskEntity.STATUS_FAILED
                    }
                    for (t in resumable) downloadManager().start(t.id)
                    resp.put("resumed", resumable.size)
                }

                "download_remove_all" -> {
                    val deleteLocal = req.optBoolean("deleteLocal")
                    val tasks = runBlocking { AppDatabase.get().downloadTaskDao().listAll() }
                    for (t in tasks) downloadManager().remove(t.id, deleteLocal)
                    resp.put("removed", tasks.size)
                }

                "download_get_link" -> {
                    val task = runBlocking { AppDatabase.get().downloadTaskDao().get(req.getLong("taskId")) }
                        ?: return error("任务不存在：${req.optLong("taskId")}").toString()
                    val headers = JSONObject()
                    val stored = task.requestHeadersJson
                    if (stored.isNotBlank()) {
                        if (stored.startsWith(CIPHER_PREFIX)) {
                            // 加密落库：经 core 公开的 CredentialDecoder 解密（2026-11-06 暴露）
                            com.yunx.app.data.security.CredentialDecoder
                                .tryDecrypt(stored, "download.requestHeaders")?.let { json ->
                                    runCatching {
                                        val j = JSONObject(json)
                                        for (k in j.keys()) headers.put(k, j.getString(k))
                                    }
                                }
                        } else {
                            // 历史遗留明文 JSON 直接透出
                            runCatching {
                                val j = JSONObject(stored)
                                for (k in j.keys()) headers.put(k, j.getString(k))
                            }
                        }
                    }
                    resp.put("url", task.url)
                        .put("fileName", task.fileName)
                        .put("headers", headers)
                }

                "download_debug_info" -> {
                    // 下载调试一次性快照（对齐原版 DownloadDebugScreen 每秒拉取的数据面；
                    // 前端 5 秒轮询，不开调试页时零额外开销——core 侧全是拉取式只读快照）
                    val id = req.getLong("taskId")
                    val task = runBlocking { AppDatabase.get().downloadTaskDao().get(id) }
                        ?: return error("任务不存在：$id").toString()
                    val mgr = downloadManager()
                    val stats = mgr.stats.value[id]
                    val chunks = mgr.chunkDebugSnapshot(id)
                    val resources = mgr.resourceDebugSnapshot()
                    val allLogs = DownloadDebugLog.entries(id)
                    val limit = req.optInt("limit", 200).coerceIn(1, 2000)
                    val logs = if (allLogs.size > limit) allLogs.takeLast(limit) else allLogs
                    // 多源（实验性 C2）只报源数：解密 altSources 计数，绝不透出直链/Cookie
                    val altCount = countAltSources(task.altSourcesJson)
                    val taskJson = JSONObject()
                        .put("id", task.id)
                        .put("fileName", task.fileName)
                        .put("status", task.status)
                        .put("statusText", DownloadTaskEntity.statusText(task.status))
                        .put("total", task.totalSize)
                        .put("downloaded", task.downloadedSize)
                        .put("platform", task.platform.ifBlank { "" })
                        .put("engine", task.engineTaskId)
                        .put("chunkCount", task.chunkCount)
                        .put("avgSpeed", task.avgSpeed)
                        .put("error", task.errorMsg)
                        .put("sourceCount", 1 + altCount)
                    val statsJson = stats?.let {
                        JSONObject()
                            .put("speed", it.speed)
                            .put("remainMillis", it.remainMillis)
                            .put("chunkCount", it.chunkCount)
                            .put("mergePercent", it.mergePercent)
                    } ?: JSONObject.NULL
                    val chunksArr = JSONArray()
                    for (c in chunks) {
                        chunksArr.put(
                            JSONObject()
                                .put("key", c.key)
                                .put("start", c.start)
                                .put("size", c.size)
                                .put("worker", c.worker)
                                .put("received", c.received)
                                .put("bps", c.bps)
                                .put("elapsedMs", c.elapsedMs)
                                .put("idleMs", c.idleMs)
                                .put("preemptCount", c.preemptCount)
                                .put("preempting", c.preempting)
                        )
                    }
                    val logsArr = JSONArray()
                    logs.forEach { logsArr.put(it) }
                    val env = runCatching { DownloadDebugLog.environmentSnapshot() }.getOrDefault("未知")
                    resp.put("task", taskJson)
                        .put("stats", statsJson)
                        .put("chunks", chunksArr)
                        .put("resources", JSONObject()
                            .put("inflightCap", resources.inflightCap)
                            .put("inflightUsed", resources.inflightUsed)
                            .put("activeTasks", resources.activeTasks)
                            .put("heapUsed", resources.heapUsed)
                            .put("heapMax", resources.heapMax)
                            .put("poolSize", resources.poolSize)
                            .put("poolActive", resources.poolActive)
                            .put("poolQueue", resources.poolQueue))
                        .put("sourceCount", 1 + altCount)
                        .put("logs", logsArr)
                        .put("logTotal", allLogs.size)
                        .put("enabled", DownloadDebugLog.isEnabled())
                        .put("env", env)
                        // 调试快照文本（与原版 buildSnapshotText 同一口径；直链不出现 ⇒ 无需脱敏）
                        .put("snapshot", buildDebugSnapshotText(task, stats, chunks, resources, env))
                }

                "download_debug_export" -> {
                    // 导出完整日志：core 现成 API（与原版同一处：写 <缓存目录>/logs 后返回文件）
                    val id = req.getLong("taskId")
                    val task = runBlocking { AppDatabase.get().downloadTaskDao().get(id) }
                        ?: return error("任务不存在：$id").toString()
                    val file = DownloadDebugLog.exportToFile(task)
                        ?: return error("导出日志失败（目录不可写？稍后重试）").toString()
                    resp.put("path", file.absolutePath).put("fileName", file.name)
                }

                "download_debug_preempt" -> {
                    // 手动强制重连某一路分片（调试页的「强制重连」按钮；core DownloadManager.forcePreemptChunk）：
                    // 掐断在飞连接、换新连接从已收字节续传。任务不存在 / 分片不在活跃集 → 优雅 ok:false。
                    val id = req.getLong("taskId")
                    val key = req.optString("key")
                    if (key.isBlank()) return error("key 不能为空").toString()
                    val task = runBlocking { AppDatabase.get().downloadTaskDao().get(id) }
                        ?: return error("任务不存在：$id").toString()
                    val preempted = downloadManager().forcePreemptChunk(task.id, key)
                    if (!preempted) {
                        return error("分片不在活跃集（未在下载或 key 不匹配）：$key").toString()
                    }
                    resp.put("preempted", key)
                }

                "announcements_list" -> {
                    val page = req.optInt("page", 1)
                    val readIds = announcementReadStore.readIds.value
                    val cached = announceCache[page]
                    if (cached != null) {
                        val arr = cached.optJSONArray("announcements") ?: JSONArray()
                        for (i in 0 until arr.length()) {
                            arr.optJSONObject(i)?.put("read", readIds.contains(arr.optJSONObject(i)!!.optString("id")))
                        }
                        resp.put("total", cached.optInt("total")).put("page", cached.optInt("page"))
                            .put("hasMore", cached.optBoolean("hasMore"))
                            .put("fromCache", true).put("announcements", arr)
                    } else {
                        // 首跑无缓存：立即回空列表（不占通道），后台刷新后前端再来取
                        resp.put("total", 0).put("page", page).put("hasMore", false)
                            .put("fromCache", false).put("announcements", JSONArray())
                    }
                    refreshAnnounceAsync(page)
                }

                "announcement_detail" -> {
                    when (val r = runBlocking { announcementApi.fetchDetail(req.getString("id")) }) {
                        is com.yunx.app.data.announcement.AnnouncementApi.Result.Success -> {
                            val a = r.data
                            announcementReadStore.markRead(a.id)
                            resp.put("id", a.id).put("title", a.title)
                                .put("content", a.content ?: JSONObject.NULL)
                                .put("publishAt", a.publishAt ?: JSONObject.NULL)
                                .put("viewCount", a.viewCount)
                        }
                        is com.yunx.app.data.announcement.AnnouncementApi.Result.Failure ->
                            return error("公告详情失败：${r.message}").toString()
                    }
                }

                "announcement_mark_read" -> {
                    announcementReadStore.markRead(req.getString("id"))
                    resp.put("marked", true)
                }

                "update_check" -> {
                    val releases = runBlocking { githubApi.getReleases("tidain", "YunX-Desktop") }
                    val latest = releases?.firstOrNull { !it.prerelease && !it.draft }
                        ?: releases?.firstOrNull()
                    if (latest == null) {
                        return error("获取最新版本失败（网络/镜像不可用）").toString()
                    }
                    val current = APP_VERSION
                    val hasUpdate = UpdateChecker.compareVersions(current, latest.tagName) < 0
                    resp.put("current", current)
                        .put("latest", latest.tagName)
                        .put("hasUpdate", hasUpdate)
                        .put("prerelease", latest.prerelease)
                        .put("url", "https://github.com/tidain/YunX-Desktop/releases/tag/${latest.tagName}")
                        .put("notes", latest.name ?: "")
                }

                "github_mirror_refresh" -> {
                    GitHubMirror.refresh()
                    resp.put("triggered", true)
                }

                else -> return error("unknown cmd: $cmd").toString()
            }
            resp.toString()
        } catch (t: Throwable) {
            error("${t.javaClass.simpleName}: ${t.message}").toString()
        }
    }

    private fun error(msg: String): JSONObject =
        JSONObject().put("ok", false).put("error", msg)

    /** 解析历史写入（对齐桌面 recordLinkHistory：同 URL 1 小时内不重复记）。 */
    private fun recordLinkHistory(url: String, title: String, platform: String, pwd: String?) {
        runBlocking {
            val dao = AppDatabase.get().linkHistoryDao()
            val last = dao.search(url).first().firstOrNull()
            if (last == null || System.currentTimeMillis() - last.createTime > 3_600_000) {
                dao.insert(
                    LinkHistoryEntity(
                        url = url, title = title, platform = platform,
                        pwd = pwd.orEmpty(), createTime = System.currentTimeMillis(),
                    )
                )
            }
        }
    }

    private fun platformOf(name: String): SharePlatform? =
        runCatching { SharePlatform.valueOf(name.trim().uppercase()) }.getOrNull()

    /** 两个路径是否指向同一位置（原版 StorageDirectoryScreen.samePath 同款；取不到 canonical 时退回绝对路径比较） */
    private fun samePath(a: File, b: File): Boolean =
        runCatching { a.canonicalPath == b.canonicalPath }.getOrDefault(a.absolutePath == b.absolutePath)

    /** cloud_move/cloud_delete 的 files:[{fid,fname?,fsize?,isdir?,fidToken?}] 解析。 */
    private fun filesFromReq(req: JSONObject): List<ShareFile> {
        val arr = req.optJSONArray("files") ?: return emptyList()
        return (0 until arr.length()).mapNotNull { i ->
            val o = arr.optJSONObject(i) ?: return@mapNotNull null
            ShareFile(
                fid = o.getString("fid"),
                fname = o.optString("fname"),
                fsize = o.optLong("fsize", 0L),
                isdir = o.optBoolean("isdir"),
                pdirFid = o.optString("pdirFid"),
                fidToken = o.optString("fidToken"),
            )
        }
    }

    private fun filesJson(files: List<ShareFile>): JSONArray {
        val arr = JSONArray()
        for (f in files.sortedWith(compareByDescending<ShareFile> { it.isdir }.thenBy { it.fname.lowercase() })) {
            arr.put(JSONObject()
                .put("fid", f.fid)
                .put("fname", f.fname)
                .put("fsize", f.fsize)
                .put("isdir", f.isdir)
                .put("fidToken", f.fidToken)
                .put("modifyTime", f.modifyTime))
        }
        return arr
    }

    /**
     * 多源备选源计数（download_debug_info 的 sourceCount）：解密 `download.altSources`
     * 只数条数，绝不透出直链/Cookie。密文损坏/解不开按 0（单源）处理——与 core
     * loadPersistedAltSources 的容错口径一致：多源只是提速手段，不影响任务本身。
     */
    private fun countAltSources(stored: String): Int {
        if (stored.isBlank()) return 0
        return runCatching {
            val text = com.yunx.app.data.security.CredentialDecoder
                .tryDecrypt(stored, "download.altSources") ?: return 0
            JSONArray(text).length()
        }.getOrDefault(0)
    }

    /**
     * 调试快照文本（原版 DownloadDebugScreen.buildSnapshotText 的 DLL 同款）：
     * 任务字段 + 实时统计 + 在飞分片表 + 全局资源 + 环境快照。复制到剪贴板用；
     * 不含直链/Cookie ⇒ 无脱敏问题。
     */
    private fun buildDebugSnapshotText(
        task: DownloadTaskEntity,
        stats: DownloadStats?,
        chunks: List<ChunkDebug>,
        resources: DownloadResourceDebug?,
        env: String,
    ): String = buildString {
        appendLine("云析 YunX-Desktop 下载调试快照")
        appendLine("任务 id：${task.id}　文件名：${task.fileName}")
        appendLine("状态：${DownloadTaskEntity.statusText(task.status)}　平台：${task.platform.ifBlank { "-" }}")
        appendLine("执行器：${task.engineTaskId.ifBlank { "内置分片下载器" }}")
        appendLine("大小：${task.downloadedSize} / ${task.totalSize} 字节")
        appendLine("环境：$env")
        if (stats != null) {
            appendLine("实时：${dbgSpeed(stats.speed)}　剩余 ${dbgRemain(stats.remainMillis)}　${stats.chunkCount} 线程")
        }
        if (task.errorMsg.isNotBlank()) appendLine("失败原因：${task.errorMsg}")
        appendLine()
        appendLine("---------- 在飞分片（${chunks.size}）----------")
        if (chunks.isEmpty()) {
            appendLine("（无）")
        } else {
            chunks.forEach {
                appendLine(
                    "${it.key} | worker#${it.worker} | 区间 ${it.start}-${it.start + it.size - 1} | " +
                        "已收 ${it.received}/${it.size} | 瞬时 ${it.bps}B/s | 已跑 ${it.elapsedMs}ms | " +
                        "抢占 ${it.preemptCount} 次${if (it.preempting) "（待换连接）" else ""}"
                )
            }
        }
        appendLine()
        appendLine("---------- 全局资源 ----------")
        if (resources == null) {
            appendLine("（读取中）")
        } else {
            appendLine("在飞分片：${resources.inflightUsed}/${resources.inflightCap}")
            appendLine("活跃任务：${resources.activeTasks}")
            appendLine("分片 IO 线程池：线程 ${resources.poolSize} · 忙 ${resources.poolActive} · 队列 ${resources.poolQueue}")
            appendLine("堆内存：${resources.heapUsed / 1024 / 1024}MB / ${resources.heapMax / 1024 / 1024}MB")
        }
    }

    private fun dbgSize(bytes: Long): String {
        if (bytes <= 0) return "0 B"
        val units = arrayOf("B", "KB", "MB", "GB", "TB")
        var value = bytes.toDouble()
        var i = 0
        while (value >= 1024 && i < units.size - 1) {
            value /= 1024
            i++
        }
        return String.format("%.1f %s", value, units[i])
    }

    private fun dbgSpeed(bytesPerSec: Long): String = if (bytesPerSec > 0) {
        "${dbgSize(bytesPerSec)}/s"
    } else {
        "0 B/s"
    }

    private fun dbgRemain(millis: Long): String = when {
        millis < 0 -> "计算中"
        else -> {
            val sec = millis / 1000
            when {
                sec < 60 -> "${sec}秒"
                sec < 3600 -> "${sec / 60}分${sec % 60}秒"
                else -> "${sec / 3600}时${(sec % 3600) / 60}分"
            }
        }
    }

    /** 节点前缀：YUNX_PREFS_NODE 可覆盖（默认 yunx-wails，与原版 yunx 分家、可双开互不干扰）。 */
    private fun prefsNodePrefix(): String =
        System.getenv("YUNX_PREFS_NODE")?.takeIf { it.isNotBlank() } ?: "yunx-wails"

    /** 与桌面 prefsStoreFactory 完全同源的节点映射（desktop/.../host/PrefsKeyValueStore.kt），
     *  仅节点前缀可经 YUNX_PREFS_NODE 覆盖。
     *  ★ 前缀在函数内现取（不能做成 object 属性）：本 init 块里的 AppContext.init() 就会触发
     *    HostStores.named → 本工厂，若读 object 属性则其初始化器尚未执行（声明序在 init 之后），
     *    会拿到 null 前缀写出 "null/misc" 节点。 */
    private fun prefsStoreFactory(name: String): KeyValueStore {
        val prefix = prefsNodePrefix()
        val node = when (name) {
            "misc" -> "$prefix/misc"
            "settings" -> "$prefix/settings"
            "xunlei_fp" -> "$prefix/xunlei_fp"
            "announcement" -> "$prefix/announcement"
            else -> "$prefix/$name"
        }
        return DllPrefsStore(Preferences.userRoot().node(node))
    }

    /** java.util.prefs 版 KeyValueStore（桌面 PrefsKeyValueStore 的 DLL 内副本，逐方法直通）。 */
    private class DllPrefsStore(private val prefs: Preferences) : KeyValueStore {
        override fun get(key: String, def: String?): String? = prefs.get(key, def)
        override fun put(key: String, value: String) = prefs.put(key, value)
        override fun getBoolean(key: String, def: Boolean): Boolean = prefs.getBoolean(key, def)
        override fun putBoolean(key: String, value: Boolean) = prefs.putBoolean(key, value)
        override fun getInt(key: String, def: Int): Int = prefs.getInt(key, def)
        override fun putInt(key: String, value: Int) = prefs.putInt(key, value)
        override fun getLong(key: String, def: Long): Long = prefs.getLong(key, def)
        override fun putLong(key: String, value: Long) = prefs.putLong(key, value)
        override fun remove(key: String) = prefs.remove(key)
    }

    /** 宿主注入 machineGuid 的 MachineProtector：普通档（MachineGuid 包裹密钥）可用；严格档 DPAPI 未提供。 */
    private class DllMachineProtector(private val guid: String) : MachineProtector {
        override fun dpapiProtect(data: ByteArray): ByteArray =
            throw UnsupportedOperationException("DPAPI 未由宿主提供（严格档待 M8，见 HANDOFF 待办 2）")

        override fun dpapiUnprotect(data: ByteArray): ByteArray =
            throw UnsupportedOperationException("DPAPI 未由宿主提供（严格档待 M8，见 HANDOFF 待办 2）")

        override fun machineGuid(): String = guid
    }

    /** 网盘账号平台（与 DriveScreen 卡片一致；视频/GitHub 账号另表，后续批接入）。 */
    private val DRIVE_PLATFORMS = listOf(
        "quark", "uc", "xunlei", "baidu", "c139", "pan123", "pan115", "guangya", "ilanzou", "lanzou"
    )

    /**
     * core 凭证加密（FileCredentialCipher，internal）的密文前缀：`yunx:v1:<iv>:<ct>`。
     * download_get_link 只用它区分「明文遗留 JSON」与「已加密落库」（后者 DLL 无法解密）。
     */
    private const val CIPHER_PREFIX = "yunx:v1:"
}
