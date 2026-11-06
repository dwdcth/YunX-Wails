package com.yunx.dll

import com.yunx.app.data.db.AppDatabase
import com.yunx.app.data.db.VideoAccountEntity
import com.yunx.app.data.download.DownloadManager
import com.yunx.app.data.download.DownloadPlatform
import com.yunx.app.data.download.DownloadSource
import com.yunx.app.data.network.Pan123Constants
import com.yunx.app.data.network.SharePlatform
import com.yunx.app.data.network.model.ShareFile
import com.yunx.app.data.network.model.ShareSession
import com.yunx.app.data.network.video.DetectedVideoLink
import com.yunx.app.data.network.video.QualitySelectableParser
import com.yunx.app.data.network.video.VideoHttp
import com.yunx.app.data.network.video.VideoLinkParser
import com.yunx.app.data.network.video.VideoMedia
import com.yunx.app.data.network.video.VideoMediaKind
import com.yunx.app.data.network.video.VideoParseResult
import com.yunx.app.data.network.video.VideoParsers
import com.yunx.app.data.network.video.VideoPlatform
import com.yunx.app.data.prefs.SettingsRepository
import com.yunx.app.data.repository.ShareResolveRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicInteger

/**
 * 扩展网关：批次 G1 新增命令的可复用逻辑（DllFacade 只留 when 分支，实现在这里）。
 *
 * - 批量下载执行器（share_folder_download / batch_list）：后台协程递归展开分享目录并逐文件
 *   入队下载，批次状态存内存（ConcurrentHashMap + AtomicInteger），进程重启即丢（与 DLL 定位一致）；
 * - C1/C2 多源（share_download 的 altSources）与凭证轮换（share_folder_download）：
 *   多账号下载（C1）开启时批量下载逐文件轮换账号凭证；再叠开多源分片（C2）时，
 *   单文件下载由其他账号各自重新取一条直链构造备选源（夸克直链绑定账号，同 URL 换 Cookie 不可靠）；
 * - 视频账号 DAO 直访（video_accounts_list / video_login_cookie）：core 的 VideoAccountRepository
 *   是 internal，DLL 不能引用，只能走公开的 AppDatabase.videoAccountDao()（内部已做列级加解密）；
 * - 视频解析（video_parse / video_parse_quality）：VideoLinkParser 识别 → 短链展开 → 纯 HTTP 解析器，
 *   返回全量结果（元信息 + 清晰度列表 + 媒体清单，含 DASH 分轨音轨与动图直链）；
 * - 视频下载入队（video_download）：取链（重新解析）后按 merge/audio/video/separate 分流入队，
 *   分流规则对齐桌面 enqueueVideoMedias；文件名规则复制 core 内部的 VideoDownloads/DownloadPathPolicy；
 * - 云盘分享的凭证帮手（cloud_share）：与 CloudGateway.require* 同源的登录态校验。
 */
object ExtraGateway {

    // ---- 批量下载（share_folder_download / batch_list）----

    /** 一个批量下载批次的内存态：total/done/failed 用 AtomicInteger（后台协程与 batch_list 并发读写）。 */
    class BatchState(val id: Long, val title: String) {
        val total = AtomicInteger(0)
        val done = AtomicInteger(0)
        val failed = AtomicInteger(0)

        /** 最近一次失败原因（首个失败项；null = 尚无失败）。 */
        @Volatile
        var lastError: String? = null
    }

    /** 批次表（id → 状态），只保留最近 20 个（见 [prune]）。 */
    private val batches = ConcurrentHashMap<Long, BatchState>()

    /** 批次 id 发号器（单调递增，batch_list 按 id 降序 = 新批次在前）。 */
    private val batchIdGen = AtomicInteger(0)

    /**
     * 启动分享目录批量下载：同步校验会话与登录态（失败直接抛给命令层返回 error），
     * 通过后在 [scope]（DllFacade.batchScope）里起后台协程递归展开并入队。
     *
     * @param manager DllFacade 的下载引擎单例（downloadManager 是 private，由分支传入）
     * @param fid 要下载的分享内目录 fid（列目录失败时回退按单文件下载）
     * @param fname 目录名（作为相对路径前缀与批次标题）
     * @param saveDir 保存目录（空串 = 跟随全局下载目录设置）
     */
    fun startFolderDownload(
        scope: CoroutineScope,
        manager: DownloadManager,
        fid: String,
        fname: String,
        saveDir: String
    ): Long {
        val plat = ResolveGateway.platform
            ?: throw IllegalStateException("尚未解析任何分享")
        val session = ResolveGateway.session
            ?: throw IllegalStateException("尚未解析任何分享")
        val repo = ResolveGateway.repoFor(plat)
        // 登录态规则与 share_download 一致：游客且平台不支持游客下载 → 整批拒绝（同步报错）
        val cred = runBlocking { ResolveGateway.credentialFor(plat) }
        if (cred.isBlank() && !ResolveGateway.supportsGuestDownload(plat)) {
            throw IllegalStateException("该平台未登录时无法下载，请先在网盘页登录")
        }
        val id = batchIdGen.incrementAndGet().toLong()
        batches[id] = BatchState(id = id, title = fname.ifBlank { session.title })
        prune()
        scope.launch {
            runFolderBatch(batches[id] ?: return@launch, manager, plat, session, repo, fid, fname, cred, saveDir)
        }
        return id
    }

    /** batch_list 的批次快照：按 id 降序、只取最近 20 个。 */
    fun batchesJson(): JSONArray {
        val arr = JSONArray()
        for (b in batches.values.sortedByDescending { it.id }.take(20)) {
            arr.put(
                JSONObject()
                    .put("id", b.id)
                    .put("title", b.title)
                    .put("total", b.total.get())
                    .put("done", b.done.get())
                    .put("failed", b.failed.get())
                    .put("lastError", b.lastError ?: JSONObject.NULL)
            )
        }
        return arr
    }

    /** 超过 20 个时淘汰最老的批次（id 最小者）。 */
    private fun prune() {
        while (batches.size > 20) {
            val oldest = batches.keys.minOrNull() ?: break
            batches.remove(oldest)
        }
    }

    /**
     * 批量执行体：递归展开目录 → 逐文件取直链并入队。
     * 多账号下载（C1）开启时按文件序号轮换凭证池里的账号取链（对齐桌面批量下载的
     * credentialRotation，每个文件仍只用一个账号）；单文件失败只计入 failed/lastError，
     * 不中断整批（对齐桌面批量下载的逐项 runCatching）。
     */
    private suspend fun runFolderBatch(
        state: BatchState,
        manager: DownloadManager,
        plat: SharePlatform,
        session: ShareSession,
        repo: ShareResolveRepository,
        fid: String,
        fname: String,
        cred: String,
        saveDir: String
    ) {
        try {
            // 夸克/UC 共用 __puus：整批的取链与下载必须用同一份已刷新 Cookie（对齐桌面批量入口）
            val fresh = if (cred.isBlank()) "" else ResolveGateway.freshCredential(plat, cred)
            // 多账号下载（C1）：开启时构造凭证池（主账号在前），批内逐文件轮换取链；
            // 开关关 / 游客（cred 空）时池退化为一条 fresh，行为与未开启完全一致
            //（对齐桌面 ResolveViewModel:590 的 credentialRotation 轮换语义）
            var credentialPool = if (cred.isBlank()) emptyList() else credentialRotation(plat, fresh)
            val tasks = mutableListOf<Pair<ShareFile, String>>()
            // fid 是文件时列目录必然失败：回退按单文件下载（对齐桌面 downloadSelected 的文件直入语义）
            val listed = runCatching { repo.listFiles(session, fid, fresh).getOrNull() }.getOrNull()
            if (listed != null) {
                // 根目录前缀 = 目录名（对齐桌面 collectShareFolder 的首层调用）
                collectFolder(repo, session, fid, fname, fresh, tasks, 0)
            } else {
                tasks.add(ShareFile(fid = fid, fname = fname, fsize = -1L, isdir = false, pdirFid = "", fidToken = "") to "")
            }
            state.total.set(tasks.size)
            if (tasks.isEmpty()) {
                state.lastError = "所选文件夹为空"
                return
            }
            // 整批空间预算（对齐桌面 ResolveViewModel:600）：逐凭证校验账号剩余空间，
            // 放不下的凭证剔出轮换池；全部放不下则中止批次。fsize 未知（-1）按 0 计。
            if (credentialPool.isNotEmpty()) {
                val sizes = tasks.map { it.first.fsize.coerceAtLeast(0L) }
                val usable = mutableListOf<String>()
                var firstSpaceFailure: Throwable? = null
                credentialPool.forEach { c ->
                    val checked = runCatching { repo.ensureBatchSpace(sizes, c) }
                    if (checked.isSuccess) {
                        usable += c
                    } else if (firstSpaceFailure == null) {
                        firstSpaceFailure = checked.exceptionOrNull()
                    }
                }
                if (usable.isEmpty()) {
                    state.lastError = firstSpaceFailure?.message ?: "剩余空间不足"
                    return
                }
                if (usable.size < credentialPool.size) credentialPool = usable
            }
            for ((index, task) in tasks.withIndex()) {
                val (file, relPath) = task
                // 本次文件用哪个账号：按文件序号轮换（取链与下载始终用同一份凭证，
                // 池只剩主账号一条时自然退回单账号——对齐桌面批量下载 :633 的取法）
                val fileCred = if (credentialPool.isEmpty()) fresh else credentialPool[index % credentialPool.size]
                try {
                    // 游客态走游客取链；登录态优先免转存，夸克失败时回退转存取链（与 share_download 同款）
                    val link = (if (cred.isBlank()) {
                        repo.getGuestShareDownloadLink(session, file)
                    } else {
                        repo.getShareDownloadLinkWithoutSave(session, file, fileCred)
                            .recoverCatching {
                                if (plat == SharePlatform.QUARK) {
                                    repo.getShareDownloadLink(session, file, fileCred).getOrThrow()
                                } else throw it
                            }
                    }).getOrElse { throw IllegalStateException(it.message ?: "取直链失败") }
                    val effectiveCred = if (cred.isBlank()) link.guestCookie else fileCred
                    val finalUrl = ResolveGateway.fastestUrl(plat, link.downloadUrl, effectiveCred)
                    val headers = ResolveGateway.downloadHeaders(plat, effectiveCred, link)
                    manager.enqueue(
                        url = finalUrl,
                        // 目录内文件用相对路径（保持目录结构）；根目录文件用取链返回的文件名
                        fileName = if (relPath.isBlank()) link.filename else relPath,
                        headers = headers,
                        size = link.size,
                        platform = plat.name.lowercase(),
                        saveDir = saveDir,
                    ) {
                        // 与 share_download 同款：下载成功后清理夸克转存临时目录
                        link.cleanupDirFid?.takeIf { it.isNotBlank() }?.let { dirFid ->
                            runCatching { repo.cleanupTempDir(dirFid, effectiveCred) }
                        }
                    }
                    state.done.incrementAndGet()
                } catch (e: Throwable) {
                    // 协程取消不是「失败」：原样抛出，避免记成一条假的失败原因
                    if (e is kotlinx.coroutines.CancellationException) throw e
                    state.failed.incrementAndGet()
                    if (state.lastError == null) state.lastError = e.message ?: e.javaClass.simpleName
                }
            }
        } catch (e: Throwable) {
            if (e is kotlinx.coroutines.CancellationException) return
            state.lastError = e.message ?: e.javaClass.simpleName
        }
    }

    /**
     * 递归收集分享文件夹内所有文件（保持目录结构，照抄桌面 ResolveViewModel.collectShareFolder）。
     * @param prefix 相对路径前缀（如 "文件夹A/子目录"）
     * @param depth 当前深度；> 12 停止下钻（防御环路与超深目录，与桌面同款上限）
     */
    private suspend fun collectFolder(
        repo: ShareResolveRepository,
        session: ShareSession,
        dirFid: String,
        prefix: String,
        credential: String,
        out: MutableList<Pair<ShareFile, String>>,
        depth: Int
    ) {
        if (depth > 12) return
        val files = runCatching { repo.listFiles(session, dirFid, credential).getOrNull() }.getOrNull() ?: return
        files.filter { !it.isdir }.forEach { out.add(it to relPath(prefix, it.fname)) }
        files.filter { it.isdir }.forEach {
            collectFolder(repo, session, it.fid, relPath(prefix, it.fname), credential, out, depth + 1)
        }
    }

    /** 相对路径拼接（前缀为空时不产生前导斜杠）。 */
    private fun relPath(prefix: String, name: String): String =
        if (prefix.isBlank()) name else "$prefix/$name"

    // ---- C1/C2 多源（share_download 的 altSources）----

    /** 单文件多源分片（实验性 C2）支持的平台（对齐桌面 ResolveViewModel.multiSourcePlatforms）：其余平台即使开着也退回单源。 */
    private val multiSourcePlatforms = setOf(
        SharePlatform.QUARK,
        SharePlatform.UC,
        SharePlatform.XUNLEI,
        SharePlatform.BAIDU,
    )

    /**
     * 多账号下载（实验性 C1）的凭证池（对齐桌面 ResolveViewModel.credentialRotation）：
     * 当前平台全部账号的凭证，主账号在前；开关关闭 / 没有账号时回退 [fallback] 一条
     * （调用方传主源凭证，行为与未开启时完全一致）。
     */
    private suspend fun credentialRotation(platform: SharePlatform, fallback: String): List<String> {
        if (!SettingsRepository().multiAccountDownload) return listOf(fallback)
        val credentials = when (platform) {
            SharePlatform.QUARK -> ResolveGateway.quarkAccount.allCredentials()
            SharePlatform.UC -> ResolveGateway.ucAccount.allCredentials()
            SharePlatform.XUNLEI -> ResolveGateway.xunleiAccount.allCredentials()
            SharePlatform.BAIDU -> ResolveGateway.baiduAccount.allCredentials()
            SharePlatform.C139 -> ResolveGateway.c139Account.allCredentials()
            SharePlatform.PAN123 -> ResolveGateway.pan123Account.allCredentials()
            SharePlatform.PAN115 -> ResolveGateway.pan115Account.allCredentials()
            SharePlatform.GUANGYA -> ResolveGateway.guangyaAccount.allCredentials()
            SharePlatform.ILANZOU -> ResolveGateway.ilanzouAccount.allCredentials()
            SharePlatform.LANZOU -> ResolveGateway.lanzouAccount.allCredentials()
            SharePlatform.GITHUB -> emptyList()
        }.filter { it.isNotBlank() }
        return credentials.ifEmpty { listOf(fallback) }
    }

    /**
     * 单文件多源分片（实验性 C2）的备选源构造（对齐桌面 ResolveViewModel.fetchAltLinks）：
     * C1（multiAccountDownload）与 C2（multiSourceDownload）**同时开启**且平台受支持时，
     * 用同一平台除当前凭证外的其它账号凭证**各自重新取一条直链**，组成各自的 DownloadSource。
     *
     * ★ 夸克/UC 直链与取链账号绑定：同一 URL 换个账号 Cookie 会被 CDN 拒（412/403），
     *   备选源必须重新取链，不能复用主源 URL。任一账号取链失败就跳过它
     *   （多源只是提速手段，少一条照样能把文件下完）。
     *
     * @param repo 平台取链仓库（与主源同源，来自 ResolveGateway.repoFor）
     * @param session 当前分享会话
     * @param file 目标文件
     * @param currentCred 主源所用凭证（游客链路为空串 —— 游客 URL 配账号 Cookie 会被 CDN 拒，不做多源）
     */
    suspend fun buildAltSources(
        platform: SharePlatform,
        repo: ShareResolveRepository,
        session: ShareSession,
        file: ShareFile,
        currentCred: String
    ): List<DownloadSource> {
        // C2 依赖 C1：multiSourceActive = 两个开关同时开（C1 开 C2 关不建，对齐桌面口径）
        if (!SettingsRepository().multiSourceActive()) return emptyList()
        if (platform !in multiSourcePlatforms) return emptyList()
        if (currentCred.isBlank()) return emptyList()
        return credentialRotation(platform, currentCred)
            .filter { it != currentCred }
            .mapNotNull { cred ->
                // 每个备选账号各自重新取一条直链（与主链路同款：免转存优先，夸克失败回退转存）
                runCatching {
                    repo.getShareDownloadLinkWithoutSave(session, file, cred)
                        .recoverCatching {
                            if (platform == SharePlatform.QUARK) {
                                repo.getShareDownloadLink(session, file, cred).getOrThrow()
                            } else throw it
                        }
                        .getOrThrow()
                }.getOrNull()?.let { link ->
                    // 该源自己的 url + headers：夸克直链做 CDN 节点优选，请求头按备选凭证配
                    //（四平台下载头差异只有 Cookie 一项，downloadHeaders(备选凭证) 等价于主源头换 Cookie）
                    DownloadSource(
                        url = ResolveGateway.fastestUrl(platform, link.downloadUrl, cred),
                        headers = ResolveGateway.downloadHeaders(platform, cred, link),
                    )
                }
            }
    }

    // ---- 云盘分享凭证帮手（cloud_share；与 CloudGateway.require* 同源）----

    internal suspend fun requireQuarkCookie(): String {
        val c = ResolveGateway.quarkAccount.getFreshCookie() ?: ResolveGateway.quarkAccount.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录夸克网盘")
        return c
    }

    internal suspend fun requireUcCookie(): String {
        val c = ResolveGateway.ucAccount.getFreshCookie() ?: ResolveGateway.ucAccount.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录 UC 网盘")
        return c
    }

    internal suspend fun requireXunlei(): Triple<String, String, String> {
        val a = ResolveGateway.xunleiAccount.getAccount()
        if (a == null || a.accessToken.isBlank()) throw IllegalStateException("未登录迅雷云盘")
        return Triple(a.accessToken, a.deviceId, a.captchaToken)
    }

    internal suspend fun requireBaiduCookie(): String {
        val c = ResolveGateway.baiduAccount.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录百度网盘")
        return c
    }

    internal suspend fun requireC139Cookie(): String {
        val c = ResolveGateway.c139Account.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录移动云盘")
        return c
    }

    internal suspend fun requirePan123Token(): String {
        val t = ResolveGateway.pan123Account.getAccount()?.accessToken
        if (t.isNullOrBlank()) throw IllegalStateException("未登录 123 云盘")
        return t
    }

    internal suspend fun requirePan115Cookie(): String {
        val c = ResolveGateway.pan115Account.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录115网盘")
        return c
    }

    internal suspend fun requireGuangYaToken(): String {
        val t = ResolveGateway.guangyaAccount.ensureAccessToken()
        if (t.isNullOrBlank()) throw IllegalStateException("未登录光鸭云盘")
        return t
    }

    /**
     * 123 云盘分享的 expiration 绝对时间串（照抄桌面 Pan123CloudViewModel.expiration）：
     * 永久用 2099 哨兵常量，其余 = now + 天数，时区偏移手工拼接（避免 SimpleDateFormat "XXX" 兼容问题）。
     */
    internal fun pan123Expiration(days: Int?): String {
        if (days == null) return Pan123Constants.EXPIRATION_FOREVER
        val cal = java.util.Calendar.getInstance().apply { add(java.util.Calendar.DAY_OF_YEAR, days) }
        val sdf = java.text.SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss", java.util.Locale.US)
        val offsetMin = java.util.TimeZone.getDefault().getOffset(cal.timeInMillis) / 60000
        val sign = if (offsetMin >= 0) "+" else "-"
        val abs = kotlin.math.abs(offsetMin)
        return sdf.format(java.util.Date(cal.timeInMillis)) +
            String.format("%s%02d:%02d", sign, abs / 60, abs % 60)
    }

    // ---- 视频账号 DAO 直访（VideoAccountRepository 是 internal，DLL 只能走公开 DAO）----

    /** video_accounts_list：全部视频账号（platform/id/nickname/isPrimary/remark/updatedAt）。 */
    suspend fun videoAccountsJson(): JSONArray {
        val arr = JSONArray()
        for (a in AppDatabase.get().videoAccountDao().getAccounts()) {
            arr.put(
                JSONObject()
                    .put("platform", a.platform)
                    .put("id", a.id)
                    .put("nickname", a.nickname)
                    .put("isPrimary", a.isPrimary)
                    .put("remark", a.remark)
                    .put("updatedAt", a.updatedAt)
            )
        }
        return arr
    }

    /**
     * video_login_cookie 落库：Cookie 非空校验由调用方做（规格约定最简校验，不触发网络验证）。
     * 落位规则照抄 core 的 resolveAccountSlot（internal，DLL 不可引用）：
     * 该平台还没有账号 ⇒ 平台常量主键 + 主账号；否则覆盖当前账号行（继承主账号标记与备注）。
     */
    suspend fun saveVideoCookie(platform: VideoPlatform, cookie: String): Boolean {
        val dao = AppDatabase.get().videoAccountDao()
        val trimmed = cookie.trim()
        if (trimmed.isBlank()) return false
        val existing = dao.getActive(platform.name)
        val (id, isPrimary, remark) = when (existing) {
            null -> Triple(platform.name, true, "")
            else -> Triple(existing.id, existing.isPrimary, existing.remark)
        }
        dao.upsert(
            VideoAccountEntity(
                id = id,
                platform = platform.name,
                cookie = trimmed,
                nickname = "",
                isPrimary = isPrimary,
                remark = remark,
                updatedAt = System.currentTimeMillis()
            )
        )
        return true
    }

    // ---- 视频解析（video_parse / video_parse_quality / video_download）----

    /**
     * 解析结果：（完整解析模型, 平台枚举名）。
     *
     * medias 里带着下载所需的直链与逐媒体请求头；video_download **不**复用这份结果 ——
     * 直链带时效签名，入队前按同一套逻辑重新取链。
     */
    class VideoParseOutcome(val result: VideoParseResult, val platform: String)

    /**
     * 视频解析：识别 → （抖音直接拒绝：解析依赖 HeadlessWebView，DLL 无网页引擎）→
     * 展开短链 → 交给纯 HTTP 解析器。Cookie 取该平台当前生效账号（无则空串，解析器自行分流匿名/登录）。
     *
     * @param qualityQn 目标清晰度编号（B 站 qn）；null = 平台默认。只有实现
     *   [QualitySelectableParser] 的解析器（B站）支持指定，其它平台忽略该参数
     *   （对齐 core VideoParseRepository.parse 的分流规则）。
     */
    suspend fun parseVideo(url: String, qualityQn: Int? = null): VideoParseOutcome {
        val link = VideoLinkParser.parse(url)
            ?: throw IllegalStateException("无法识别的视频链接")
        if (link.platform == VideoPlatform.DOUYIN) {
            throw UnsupportedOperationException("抖音解析需网页引擎，待 M7")
        }
        val resolved = if (link.isShortLink) expandShortLink(link) else link
        val parser = VideoParsers.of(resolved.platform)
            ?: throw UnsupportedOperationException("暂不支持解析 ${resolved.platform.displayName}")
        val cookie = activeVideoCookie(resolved.platform)
        val result = if (qualityQn != null && parser is QualitySelectableParser) {
            parser.parseWithQuality(resolved, cookie, qualityQn)
        } else {
            parser.parse(resolved, cookie)
        }
        return VideoParseOutcome(result = result, platform = resolved.platform.name)
    }

    /**
     * video_parse / video_parse_quality 的全量返回组装：
     * - 元信息：platform/title/author/cover/content(正文)/publishTime(yyyy-MM-dd，未知为空串)；
     * - qualities：清晰度档位（id=平台内部编号串，label=展示文案，selected=本次实际采用的档位；
     *   目前只有 B 站会填，其它平台为空数组）；
     * - medias：逐媒体 type(video/image/audio)/url/label(清晰度标签)/size/isHls +
     *   audioUrl/motionUrl（DASH 分轨音轨 / 动图 Live Photo 资源直链，空串 = 无）；
     *   width/height 恒为 0（解析模型不带图片尺寸，界面占位字段）。
     */
    suspend fun parseVideoJson(url: String, qualityQn: Int? = null): JSONObject {
        val outcome = parseVideo(url, qualityQn)
        val r = outcome.result
        val medias = JSONArray()
        for (media in r.medias) {
            medias.put(
                JSONObject()
                    .put("type", media.kind.name.lowercase())
                    .put("url", media.url)
                    .put("label", media.quality)
                    .put("size", media.size)
                    .put("isHls", media.isHls)
                    .put("audioUrl", media.audioUrl)
                    .put("motionUrl", media.motionUrl)
                    .put("width", 0)
                    .put("height", 0)
            )
        }
        val qualities = JSONArray()
        for (q in r.qualities) {
            qualities.put(
                JSONObject()
                    .put("id", q.qn.toString())
                    .put("label", q.label)
                    .put("selected", q.qn == r.selectedQn)
            )
        }
        return JSONObject()
            .put("platform", outcome.platform)
            .put("title", r.title)
            .put("author", r.author)
            .put("cover", r.cover)
            .put("content", r.text)
            .put("publishTime", formatVideoDate(r.timestampSeconds))
            .put("qualities", qualities)
            .put("medias", medias)
    }

    /** 视频下载的分轨模式（对齐桌面 VideoStreamMode：MERGE/AUDIO/VIDEO/SEPARATE）。 */
    private enum class StreamMode { MERGE, AUDIO, VIDEO, SEPARATE }

    /**
     * video_download：取链（重新解析）后按 [mode] 入队。分流规则对齐桌面 enqueueVideoMedias：
     * - merge（默认）：分轨时视频轨 + 音轨（mergeAudioUrl）两路落库，下载完由 DashMuxer 合轨；
     *   非分轨媒体（单文件直链/图集）等同普通单文件下载；
     * - audio / video：仅音轨（.m4a）/ 仅视频轨（无声）；非分轨媒体没有单独音轨，退回单文件（同桌面）；
     * - separate：两轨各一条任务，不合轨。
     *
     * 图集（多条 image）逐条入队（桌面「下载所选」同语义），动图带 motionUrl 走 MotionPhotoWriter 合成。
     *
     * @return {taskId, fileName, size, queued, taskIds}（taskId/taskIds 为全部入队任务；
     *   separate 模式下视频轨在前、音轨在后）
     */
    suspend fun enqueueVideoDownload(
        manager: DownloadManager,
        url: String,
        qualityQn: Int?,
        saveDir: String,
        mode: String
    ): JSONObject {
        val streamMode = when (mode.trim().lowercase()) {
            "", "merge" -> StreamMode.MERGE
            "audio" -> StreamMode.AUDIO
            "video" -> StreamMode.VIDEO
            "separate" -> StreamMode.SEPARATE
            else -> throw IllegalStateException("未知下载模式：$mode（可选 merge/audio/video/separate）")
        }
        val outcome = parseVideo(url, qualityQn)
        val result = outcome.result
        if (result.medias.isEmpty()) throw IllegalStateException("该作品没有可下载的媒体")
        // 任务来源平台标识：video_<平台小写>（设置里可按平台单独配下载线程数）
        val platform = DownloadPlatform.forVideoPlatform(result.platform)
        val taskIds = mutableListOf<Long>()
        var firstBaseName = ""
        var firstSize = -1L
        for ((index, media) in result.medias.withIndex()) {
            val baseName = videoFileName(result, media, index, result.medias.size)
            val stem = baseName.substringBeforeLast('.', baseName)
            val ext = baseName.substringAfterLast('.', "mp4").ifBlank { "mp4" }
            val dash = media.audioUrl.isNotBlank()
            suspend fun enqueueOne(
                mediaUrl: String,
                fileName: String,
                headers: Map<String, String>,
                size: Long,
                mergeAudioUrl: String = "",
                mergeAudioHeaders: Map<String, String> = emptyMap(),
                motionUrl: String = ""
            ) {
                // 空 URL 跳过（与桌面 enqueueOne 的防御一致，不产生空任务）
                if (mediaUrl.isBlank()) return
                val id = manager.enqueue(
                    url = mediaUrl,
                    fileName = fileName,
                    headers = headers,
                    size = size,
                    platform = platform,
                    saveDir = saveDir,
                    mergeAudioUrl = mergeAudioUrl,
                    mergeAudioHeaders = mergeAudioHeaders,
                    motionUrl = motionUrl,
                )
                taskIds.add(id)
            }
            if (!dash || streamMode == StreamMode.MERGE) {
                // 非分轨媒体（或用户选「合并」）：单文件直链，分轨时附音轨合轨、动图附 motionUrl
                if (firstBaseName.isEmpty()) {
                    firstBaseName = baseName
                    firstSize = media.size
                }
                enqueueOne(
                    media.url, baseName, media.headers, media.size,
                    mergeAudioUrl = media.audioUrl,
                    mergeAudioHeaders = media.audioHeaders,
                    motionUrl = media.motionUrl,
                )
            } else if (streamMode == StreamMode.AUDIO) {
                // 仅音频：只下音轨，存 .m4a（大小未知，-1 由下载器探测）
                if (firstBaseName.isEmpty()) {
                    firstBaseName = "$stem-audio.m4a"
                    firstSize = -1L
                }
                enqueueOne(media.audioUrl, "$stem-audio.m4a", media.audioHeaders, -1L)
            } else if (streamMode == StreamMode.VIDEO) {
                // 仅视频：只下视频轨（无声）
                if (firstBaseName.isEmpty()) {
                    firstBaseName = "$stem-video.$ext"
                    firstSize = media.size
                }
                enqueueOne(media.url, "$stem-video.$ext", media.headers, media.size)
            } else {
                // 分别保存：两轨各一个任务，不合轨（视频轨在前、音轨在后）
                if (firstBaseName.isEmpty()) {
                    firstBaseName = "$stem-video.$ext"
                    firstSize = media.size
                }
                enqueueOne(media.url, "$stem-video.$ext", media.headers, media.size)
                enqueueOne(media.audioUrl, "$stem-audio.m4a", media.audioHeaders, -1L)
            }
        }
        if (taskIds.isEmpty()) throw IllegalStateException("没有可入队的下载任务")
        return JSONObject()
            .put("taskId", taskIds.first())
            .put("fileName", firstBaseName)
            .put("size", firstSize)
            .put("queued", taskIds.size)
            .put("taskIds", JSONArray(taskIds))
    }

    /**
     * 该平台当前生效账号的 Cookie（主账号优先；无账号 = 未登录，空串，解析器自行分流匿名/登录）。
     * 走公开的 videoAccountDao().getAccounts()（内部已做列级加解密）——与
     * VideoParseRepository 的 activeCookie 同语义（「Cookie 用当前生效账号，主账号优先」）。
     */
    private suspend fun activeVideoCookie(platform: VideoPlatform): String {
        val accounts = AppDatabase.get().videoAccountDao()
            .getAccounts().filter { it.platform == platform.name }
        return (accounts.firstOrNull { it.isPrimary } ?: accounts.firstOrNull())?.cookie.orEmpty()
    }

    /** 发布时间展示串（对齐桌面 VideoResultSection.formatVideoDate：yyyy-MM-dd；0 = 未知，空串）。 */
    private fun formatVideoDate(timestampSeconds: Long): String {
        if (timestampSeconds <= 0L) return ""
        return java.text.SimpleDateFormat("yyyy-MM-dd", java.util.Locale.getDefault())
            .format(java.util.Date(timestampSeconds * 1000L))
    }

    /**
     * 视频媒体 → 下载文件名（复制 core 内部 VideoDownloads.fileNameFor 的规则，该对象 internal）：
     * `标题[_清晰度][_序号].扩展名`，扩展名优先取 URL 路径后缀，HLS 固定 .ts。
     */
    private fun videoFileName(result: VideoParseResult, media: VideoMedia, index: Int, total: Int): String {
        val title = sanitizeFileName(result.title.trim()).ifBlank { result.platform.displayName }
        val quality = media.quality.trim()
            .takeIf { it.isNotBlank() }
            ?.let { "_" + sanitizeFileName(it) }
            .orEmpty()
        val order = if (total > 1) "_${index + 1}" else ""
        return "$title$quality$order.${mediaExtension(media)}"
    }

    /** 文件名清洗（复制 DownloadPathPolicy.sanitizeName，internal）：非法字符替换为 _，超 120 截断。 */
    private fun sanitizeFileName(name: String): String {
        var cleaned = name.replace(Regex("[\\\\/:*?\"<>|\\x00-\\x1f]"), "_").trim()
        if (cleaned.length > 120) {
            val ext = cleaned.substringAfterLast('.', "").take(10)
            val base = cleaned.substringBeforeLast('.').take(100)
            cleaned = if (ext.isNotBlank() && ext != cleaned) "$base.$ext" else cleaned.take(120)
        }
        return cleaned
    }

    /** 媒体扩展名推断（复制 VideoDownloads.extensionOf）：HLS 固定 ts，其余按 URL 后缀 + 类型兜底。 */
    private fun mediaExtension(media: VideoMedia): String {
        if (media.isHls) return "ts"
        val fromUrl = media.url
            .substringBefore('?')
            .substringBefore('#')
            .substringAfterLast('.')
            .lowercase()
        return when (media.kind) {
            VideoMediaKind.IMAGE -> if (fromUrl in IMAGE_EXTS) fromUrl else "jpg"
            VideoMediaKind.AUDIO -> if (fromUrl in AUDIO_EXTS) fromUrl else "m4a"
            VideoMediaKind.VIDEO -> if (fromUrl in VIDEO_EXTS) fromUrl else "mp4"
        }
    }

    private val IMAGE_EXTS = setOf("jpg", "jpeg", "png", "webp", "gif")
    private val VIDEO_EXTS = setOf("mp4", "mov", "mkv", "webm", "flv", "ts")
    private val AUDIO_EXTS = setOf("mp3", "m4a", "aac", "flac", "wav", "ogg")

    /**
     * 展开短链（照抄 VideoParseRepository.expandShortLink；该仓库构造函数 internal，DLL 不能实例化）：
     * 跟随一次重定向后重新识别，最多 5 跳；抖音/快手的分享短链认移动端 UA。
     */
    private suspend fun expandShortLink(link: DetectedVideoLink): DetectedVideoLink {
        var current = link
        repeat(5) {
            val ua = when (current.platform) {
                VideoPlatform.DOUYIN, VideoPlatform.KUAISHOU -> VideoHttp.UA_IPHONE
                else -> VideoHttp.UA_CHROME
            }
            val next = VideoHttp.expandRedirect(
                url = current.url,
                headers = mapOf("User-Agent" to ua),
            ) ?: return current
            val reidentified = VideoLinkParser.parse(next) ?: return current.copy(url = next)
            if (!reidentified.isShortLink) return reidentified
            current = reidentified
        }
        return current
    }
}
