package com.yunx.app.data.host

import java.io.File
import java.util.Properties
import java.util.ServiceLoader
import java.util.concurrent.ConcurrentHashMap

/**
 * 核心与宿主之间的能力边界。
 *
 * 核心（:core）只依赖这里定义的接口，不直接碰 JNA / JCEF / java.util.prefs / Compose：
 * - 桌面应用（:desktop）在启动时把 Windows 实现注册进来（见 DesktopHost.registerAll）；
 * - 将来的 Go/Wails 壳把同一批接口经 DLL 边界另一侧实现（DPAPI 与 WebView2 由宿主提供，
 *   或由 DLL 在 Windows 上直接调用——那是产品化阶段的决定，不影响本接口形态）；
 * - 未注册时一律退化为无副作用的默认实现（与历史上「JCEF 未就绪就跳过」等行为一致），
 *   保证核心在纯 JVM / 测试环境里也能独立跑。
 */

// ---------------------------------------------------------------------------
// 键值存储
// ---------------------------------------------------------------------------

/**
 * 键值存储：`java.util.prefs.Preferences` 的最小子集（核心实际用到的全部方法，签名一一对应，
 * 因此既有调用点几乎不用改）。桌面宿主用 java.util.prefs 实现以保留既有用户数据；
 * 默认实现是文件版（见 [FileKeyValueStore]）。
 */
interface KeyValueStore {
    fun get(key: String, def: String?): String?
    fun put(key: String, value: String)
    fun getBoolean(key: String, def: Boolean): Boolean
    fun putBoolean(key: String, value: Boolean)
    fun getInt(key: String, def: Int): Int
    fun putInt(key: String, value: Int)
    fun getLong(key: String, def: Long): Long
    fun putLong(key: String, value: Long)
    fun remove(key: String)
}

/** 按名取存储（"misc" / "settings" / "xunlei_fp" / "announcement"）；同一名字全程同一实例。 */
object HostStores {
    @Volatile
    var factory: (String) -> KeyValueStore = { name -> FileKeyValueStore(name) }

    private val cache = ConcurrentHashMap<String, KeyValueStore>()

    fun named(name: String): KeyValueStore = cache.computeIfAbsent(name) { factory(it) }
}

/**
 * 文件版 [KeyValueStore]（核心默认实现）：`<user.home>/.yunx-desktop/kv/<name>.properties`。
 * 数据量很小（偏好项），每次写入全量落盘；同进程内以本对象为锁串行化。
 */
class FileKeyValueStore(name: String) : KeyValueStore {
    private val file: File =
        File(System.getProperty("user.home"), ".yunx-desktop/kv/$name.properties")

    private val props = Properties()

    init {
        runCatching {
            if (file.isFile) file.inputStream().use { props.load(it) }
        }
    }

    @Synchronized
    override fun get(key: String, def: String?): String? = props.getProperty(key) ?: def

    @Synchronized
    override fun put(key: String, value: String) {
        props.setProperty(key, value)
        persist()
    }

    @Synchronized
    override fun getBoolean(key: String, def: Boolean): Boolean =
        props.getProperty(key)?.toBooleanStrictOrNull() ?: def

    @Synchronized
    override fun putBoolean(key: String, value: Boolean) = put(key, value.toString())

    @Synchronized
    override fun getInt(key: String, def: Int): Int =
        props.getProperty(key)?.toIntOrNull() ?: def

    @Synchronized
    override fun putInt(key: String, value: Int) = put(key, value.toString())

    @Synchronized
    override fun getLong(key: String, def: Long): Long =
        props.getProperty(key)?.toLongOrNull() ?: def

    @Synchronized
    override fun putLong(key: String, value: Long) = put(key, value.toString())

    @Synchronized
    override fun remove(key: String) {
        props.remove(key)
        persist()
    }

    private fun persist() {
        runCatching {
            file.parentFile?.mkdirs()
            file.outputStream().use { props.store(it, "yunx kv store") }
        }
    }
}

// ---------------------------------------------------------------------------
// 平台能力
// ---------------------------------------------------------------------------

/**
 * 机器密钥保护：Windows DPAPI 包裹/解包 + 机器指纹（MachineGuid）读取。
 * 桌面宿主经 JNA 实现（[com.yunx.app.data.security.JnaMachineProtector]，同时登记了 ServiceLoader，
 * 因此单元测试等没有显式注册的场景也能找到它）。
 */
interface MachineProtector {
    fun dpapiProtect(data: ByteArray): ByteArray
    fun dpapiUnprotect(data: ByteArray): ByteArray

    /** Windows MachineGuid（HKLM\SOFTWARE\Microsoft\Cryptography）；读不到返回 null（暂时故障语义） */
    fun machineGuid(): String?
}

/**
 * Windows 注册表读取（固定 HKCU，与既有 SystemProxy 的读取范围一致）。读不到一律返回 null。
 */
interface RegistryReader {
    fun readString(path: String, name: String): String?
    fun readInt(path: String, name: String): Int?
}

/**
 * 隐藏 Web 引擎：在不可见浏览器里加载页面、轮询就绪条件、执行 JS 提取数据
 * （抖音 acrawler 挑战、迅雷网页凭证等「纯 HTTP 拿不到」的兜底通道）。
 * 桌面宿主用 JCEF 实现；Wails 宿主将用 WebView2 实现同一接口。
 */
interface HeadlessWebEngine {
    suspend fun loadAndExtract(
        url: String,
        userAgent: String,
        readyCheckJs: String,
        extractJs: String,
        timeoutMs: Long = 25_000L,
        pollIntervalMs: Long = 120L,
        extraDocumentStartJs: List<String> = emptyList()
    ): String?

    fun prewarm(url: String)
    fun closeQuietly()
}

/** Web Cookie 清理（退出登录时清掉内嵌浏览器里的会话 Cookie，避免下次自动登录回旧账号）。 */
interface WebCookieCleaner {
    fun clearForDomains(domains: List<String>)
    suspend fun clearAndWait(domains: List<String>, timeoutMs: Long = 1500L)
}

/** 引擎（ffmpeg）缺失的触发场景（决定提示文案）。 */
enum class EngineInstallReason { PREVIEW, MERGE }

/** 引擎缺失时的安装征询：由 UI 宿主弹窗询问。返回值语义与既有实现一致（是否真的弹出了提示）。 */
fun interface EngineInstallPrompter {
    fun ask(reason: EngineInstallReason): Boolean
}

/** 下载进度通知（桌面宿主 = Windows Toast 守护进程；无宿主时静默）。 */
interface DownloadNotifier {
    fun sessionStart()
    fun updateProgress(
        doneSum: Long,
        totalSum: Long,
        line1: String,
        statusLine: String,
        speedText: String,
        statusLabel: String
    )

    fun sessionEnd()
}

/** 保持唤醒（下载期间防系统睡眠）。 */
interface KeepAwake {
    fun acquire()
    fun release()
}

/** 宿主目录约定（Windows 上要查 Known Folder / 注册表，核心不自己猜）。 */
interface HostDirs {
    fun defaultDownloadDir(): File
}

// ---------------------------------------------------------------------------
// 注册表（进程级单例组）
// ---------------------------------------------------------------------------

object HostServices {

    @Volatile
    var machineProtector: MachineProtector? = null

    /** 显式注册优先；未注册时经 ServiceLoader 兜底（桌面模块的 JNA 实现登记在 META-INF/services）。 */
    fun machineProtectorOrNull(): MachineProtector? =
        machineProtector ?: runCatching {
            ServiceLoader.load(MachineProtector::class.java).firstOrNull()
        }.getOrNull()?.also { machineProtector = it }

    fun requireMachineProtector(): MachineProtector =
        machineProtectorOrNull()
            ?: throw IllegalStateException("宿主未提供 MachineProtector（DPAPI 不可用）")

    @Volatile
    var registryReader: RegistryReader = object : RegistryReader {
        override fun readString(path: String, name: String): String? = null
        override fun readInt(path: String, name: String): Int? = null
    }

    @Volatile
    var webEngine: HeadlessWebEngine = object : HeadlessWebEngine {
        override suspend fun loadAndExtract(
            url: String,
            userAgent: String,
            readyCheckJs: String,
            extractJs: String,
            timeoutMs: Long,
            pollIntervalMs: Long,
            extraDocumentStartJs: List<String>
        ): String? = null

        override fun prewarm(url: String) = Unit
        override fun closeQuietly() = Unit
    }

    @Volatile
    var cookieCleaner: WebCookieCleaner = object : WebCookieCleaner {
        override fun clearForDomains(domains: List<String>) = Unit
        override suspend fun clearAndWait(domains: List<String>, timeoutMs: Long) = Unit
    }

    @Volatile
    var engineInstallPrompter: EngineInstallPrompter = EngineInstallPrompter { false }

    @Volatile
    var notifier: DownloadNotifier = object : DownloadNotifier {
        override fun sessionStart() = Unit
        override fun updateProgress(
            doneSum: Long,
            totalSum: Long,
            line1: String,
            statusLine: String,
            speedText: String,
            statusLabel: String
        ) = Unit

        override fun sessionEnd() = Unit
    }

    @Volatile
    var keepAwake: KeepAwake = object : KeepAwake {
        override fun acquire() = Unit
        override fun release() = Unit
    }

    @Volatile
    var dirs: HostDirs = object : HostDirs {
        override fun defaultDownloadDir(): File = File(System.getProperty("user.home"), "Downloads")
    }
}
