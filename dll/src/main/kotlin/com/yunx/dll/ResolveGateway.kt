package com.yunx.dll

import com.yunx.app.data.db.AppDatabase
import com.yunx.app.data.network.BaiduApi
import com.yunx.app.data.network.BaiduConstants
import com.yunx.app.data.network.C139Api
import com.yunx.app.data.network.C139Constants
import com.yunx.app.data.network.GuangYaApi
import com.yunx.app.data.network.GuangYaConstants
import com.yunx.app.data.network.ILanzouApi
import com.yunx.app.data.network.ILanzouConstants
import com.yunx.app.data.network.LanzouApi
import com.yunx.app.data.network.LanzouConstants
import com.yunx.app.data.network.Pan115Api
import com.yunx.app.data.network.Pan115Constants
import com.yunx.app.data.network.Pan123Api
import com.yunx.app.data.network.Pan123Constants
import com.yunx.app.data.network.QuarkApi
import com.yunx.app.data.network.QuarkCdn
import com.yunx.app.data.network.QuarkConstants
import com.yunx.app.data.network.SharePlatform
import com.yunx.app.data.network.UCApi
import com.yunx.app.data.network.UCConstants
import com.yunx.app.data.network.XunleiApi
import com.yunx.app.data.network.XunleiConstants
import com.yunx.app.data.network.model.DownloadLink
import com.yunx.app.data.network.model.ShareSession
import com.yunx.app.data.repository.BaiduAccountRepository
import com.yunx.app.data.repository.BaiduResolveRepository
import com.yunx.app.data.repository.C139AccountRepository
import com.yunx.app.data.repository.C139ResolveRepository
import com.yunx.app.data.repository.GuangYaAccountRepository
import com.yunx.app.data.repository.GuangYaResolveRepository
import com.yunx.app.data.repository.ILanzouAccountRepository
import com.yunx.app.data.repository.ILanzouResolveRepository
import com.yunx.app.data.repository.LanzouAccountRepository
import com.yunx.app.data.repository.LanzouResolveRepository
import com.yunx.app.data.repository.Pan115AccountRepository
import com.yunx.app.data.repository.Pan115ResolveRepository
import com.yunx.app.data.repository.Pan123AccountRepository
import com.yunx.app.data.repository.Pan123ResolveRepository
import com.yunx.app.data.repository.QuarkAccountRepository
import com.yunx.app.data.repository.QuarkResolveRepository
import com.yunx.app.data.repository.ShareResolveRepository
import com.yunx.app.data.repository.UCAccountRepository
import com.yunx.app.data.repository.UCResolveRepository
import com.yunx.app.data.repository.XunleiAccountRepository
import com.yunx.app.data.repository.XunleiResolveRepository
import kotlinx.coroutines.runBlocking

/**
 * 解析网关：桌面 ResolveViewModel 的 DLL 等价物。
 *
 * - 10 平台共用 [ShareResolveRepository] 统一接口，按 [SharePlatform] 路由；
 * - 单会话模型（对齐桌面 VM：一次解析持有一个 ShareSession）；
 * - 登录态从各账号仓库取（与桌面 currentCredential 同源）；
 * - 下载请求头按平台差异表拼装（对齐桌面 ResolveViewModel.enqueueDownload）。
 */
object ResolveGateway {

    // ---- Api（全部默认 OkHttp 客户端，走 HttpClients.apiClient 代理注入）----
    internal val quarkApi by lazy { QuarkApi() }
    internal val ucApi by lazy { UCApi() }
    internal val xunleiApi by lazy { XunleiApi() }
    internal val baiduApi by lazy { BaiduApi() }
    internal val c139Api by lazy { C139Api() }
    internal val pan123Api by lazy { Pan123Api() }
    internal val pan115Api by lazy { Pan115Api() }
    internal val guangyaApi by lazy { GuangYaApi() }
    internal val ilanzouApi by lazy { ILanzouApi() }
    internal val lanzouApi by lazy { LanzouApi() }

    // ---- 账号仓库 ----
    private val db get() = AppDatabase.get()
    internal val quarkAccount by lazy { QuarkAccountRepository(db.quarkAccountDao(), quarkApi) }
    internal val ucAccount by lazy { UCAccountRepository(db.ucAccountDao(), ucApi) }
    internal val xunleiAccount by lazy { XunleiAccountRepository(db.xunleiAccountDao(), xunleiApi) }
    internal val baiduAccount by lazy { BaiduAccountRepository(db.baiduAccountDao(), baiduApi) }
    internal val c139Account by lazy { C139AccountRepository(db.c139AccountDao()) }
    internal val pan123Account by lazy { Pan123AccountRepository(db.pan123AccountDao(), pan123Api) }
    internal val pan115Account by lazy { Pan115AccountRepository(db.pan115AccountDao(), pan115Api) }
    internal val lanzouAccount by lazy { LanzouAccountRepository(db.lanzouAccountDao(), lanzouApi) }
    internal val guangyaAccount by lazy { GuangYaAccountRepository(db.guangyaAccountDao(), guangyaApi) }
    internal val ilanzouAccount by lazy { ILanzouAccountRepository(db.ilanzouAccountDao(), ilanzouApi) }

    // ---- 解析仓库 ----
    private val quarkRepo by lazy { QuarkResolveRepository(quarkApi) }
    private val ucRepo by lazy { UCResolveRepository(ucApi) }
    private val baiduRepo by lazy { BaiduResolveRepository(baiduApi) }
    private val c139Repo by lazy { C139ResolveRepository(c139Api) }
    private val pan115Repo by lazy { Pan115ResolveRepository(pan115Api) }
    private val lanzouRepo by lazy { LanzouResolveRepository(lanzouApi) }
    private val pan123Repo by lazy {
        Pan123ResolveRepository(pan123Api) { pan123Account.getAccount()?.accessToken }
    }
    private val guangyaRepo by lazy {
        GuangYaResolveRepository(guangyaApi) { guangyaAccount.ensureAccessToken() }
    }
    private val ilanzouRepo by lazy { ILanzouResolveRepository(ilanzouApi, ilanzouAccount) }
    private val xunleiRepo by lazy {
        XunleiResolveRepository(
            api = xunleiApi,
            accountProvider = { xunleiAccount.getAccount()?.accessToken },
            deviceIdProvider = { xunleiAccount.getAccount()?.deviceId },
            captchaProvider = { xunleiAccount.getAccount()?.captchaToken },
            refreshProvider = null,
        )
    }

    fun repoFor(platform: SharePlatform): ShareResolveRepository = when (platform) {
        SharePlatform.QUARK -> quarkRepo
        SharePlatform.UC -> ucRepo
        SharePlatform.XUNLEI -> xunleiRepo
        SharePlatform.BAIDU -> baiduRepo
        SharePlatform.C139 -> c139Repo
        SharePlatform.PAN123 -> pan123Repo
        SharePlatform.PAN115 -> pan115Repo
        SharePlatform.GUANGYA -> guangyaRepo
        SharePlatform.ILANZOU -> ilanzouRepo
        SharePlatform.LANZOU -> lanzouRepo
        SharePlatform.GITHUB ->
            throw UnsupportedOperationException("GitHub 发布走直链下载，不经过分享会话")
    }

    /** 各平台分享根目录（对齐桌面 currentDefaultDirFid）。 */
    fun rootDirFor(platform: SharePlatform): String = when (platform) {
        SharePlatform.BAIDU, SharePlatform.GUANGYA, SharePlatform.ILANZOU, SharePlatform.LANZOU -> ""
        else -> "0"
    }

    /** 当前登录凭证（对齐桌面 currentCredential）：游客返回空串。 */
    suspend fun credentialFor(platform: SharePlatform): String = when (platform) {
        SharePlatform.QUARK -> quarkAccount.getAccount()?.cookie.orEmpty()
        SharePlatform.UC -> ucAccount.getAccount()?.cookie.orEmpty()
        SharePlatform.XUNLEI -> xunleiAccount.getAccount()?.accessToken.orEmpty()
        SharePlatform.BAIDU -> baiduAccount.getAccount()?.cookie.orEmpty()
        SharePlatform.C139 -> c139Account.getAccount()?.cookie.orEmpty()
        SharePlatform.PAN123 -> pan123Account.getAccount()?.accessToken.orEmpty()
        SharePlatform.PAN115 -> pan115Account.getAccount()?.cookie.orEmpty()
        SharePlatform.GUANGYA -> guangyaAccount.ensureAccessToken().orEmpty()
        SharePlatform.ILANZOU -> ilanzouAccount.getAccount()?.appToken.orEmpty()
        SharePlatform.LANZOU -> lanzouAccount.getAccount()?.cookie.orEmpty()
        SharePlatform.GITHUB -> ""
    }

    /** 免登录可取直链的平台（对齐桌面 supportsGuestDownload）。 */
    fun supportsGuestDownload(platform: SharePlatform): Boolean = platform in setOf(
        SharePlatform.QUARK, SharePlatform.UC, SharePlatform.GUANGYA,
        SharePlatform.ILANZOU, SharePlatform.LANZOU,
    )

    /** 取链前的登录态刷新（夸克/UC 的 __puus 保鲜；其余平台直通）。 */
    suspend fun freshCredential(platform: SharePlatform, credential: String): String = when (platform) {
        SharePlatform.QUARK -> quarkAccount.getFreshCookie() ?: credential
        SharePlatform.UC -> ucAccount.getFreshCookie() ?: credential
        else -> credential
    }

    /**
     * 直链下载请求头（平台差异表，对齐桌面 ResolveViewModel.enqueueDownload :1916-1982）。
     * 游客链路时 credential 传 link.guestCookie。
     */
    fun downloadHeaders(platform: SharePlatform, credential: String, link: DownloadLink): Map<String, String> =
        when (platform) {
            SharePlatform.QUARK -> mapOf(
                "Cookie" to credential,
                "User-Agent" to QuarkConstants.API_USER_AGENT,
                "Referer" to "https://pan.quark.cn/",
            )
            SharePlatform.UC ->
                if (link.guestCookie.isNotBlank() && credential == link.guestCookie) {
                    mapOf(
                        "Cookie" to link.guestCookie,
                        "User-Agent" to UCConstants.GUEST_UA,
                        "Sec-Ch-Ua" to UCConstants.GUEST_SEC_CH_UA,
                        "Referer" to "https://drive.uc.cn/",
                        "Origin" to "https://drive.uc.cn",
                    )
                } else {
                    mapOf(
                        "Cookie" to credential,
                        "User-Agent" to UCConstants.USER_AGENT,
                        "Referer" to "https://drive.uc.cn/",
                        "Origin" to "https://drive.uc.cn",
                    )
                }
            SharePlatform.XUNLEI -> mapOf("User-Agent" to XunleiConstants.APP_UA)
            SharePlatform.BAIDU -> mapOf(
                "Cookie" to credential,
                "User-Agent" to BaiduConstants.UA_NETDISK,
            )
            SharePlatform.C139 -> mapOf("User-Agent" to C139Constants.PC_UA)
            SharePlatform.PAN115 -> mapOf(
                "Cookie" to Pan115Constants.mergeCookies(credential, link.guestCookie),
                "User-Agent" to Pan115Constants.CLIENT_UA,
                "Referer" to "https://115.com/",
            )
            SharePlatform.PAN123 -> mapOf(
                "User-Agent" to Pan123Constants.WEB_UA,
                "Referer" to "https://yun.123pan.cn/",
            )
            SharePlatform.GUANGYA -> mapOf(
                "User-Agent" to GuangYaConstants.WEB_UA,
                "Referer" to "https://www.guangyapan.com/",
            )
            SharePlatform.ILANZOU -> mapOf(
                "User-Agent" to ILanzouConstants.WEB_UA,
                "Referer" to "https://www.ilanzou.com/",
                "Origin" to "https://www.ilanzou.com",
            )
            SharePlatform.LANZOU -> {
                val origin = runCatching {
                    val u = java.net.URI(link.downloadUrl)
                    "${u.scheme}://${u.host}/"
                }.getOrDefault("https://wwap.lanzouj.com/")
                mapOf(
                    "User-Agent" to LanzouConstants.WEB_UA,
                    "Referer" to origin,
                    "Cookie" to "down_ip=1",
                )
            }
            SharePlatform.GITHUB -> emptyMap()
        }

    /** 夸克 CDN 节点优选（对齐桌面 :1984）。 */
    fun fastestUrl(platform: SharePlatform, url: String, credential: String): String =
        if (platform == SharePlatform.QUARK) {
            runBlocking { runCatching { QuarkCdn.fastest(url, credential) }.getOrDefault(url) }
        } else {
            url
        }

    // ---- 单会话状态 ----
    var session: ShareSession? = null
        private set
    var platform: SharePlatform? = null
        private set

    fun bind(platform: SharePlatform, session: ShareSession) {
        this.platform = platform
        this.session = session
    }

    fun clear() {
        session = null
        platform = null
    }
}
