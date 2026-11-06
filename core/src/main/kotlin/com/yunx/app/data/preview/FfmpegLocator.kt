package com.yunx.app.data.preview

import com.yunx.app.AppContext
import java.io.File

/**
 * FFmpeg 套件定位（从 FfplayManager 拆出的纯文件探测部分，供核心的 DASH 合轨直接使用；
 * 播放器进程管理、引擎下载、窗口嵌入仍在桌面模块的 FfplayManager 里，它也复用这里的定位结果）。
 *
 * 检测顺序：应用私有目录 `<dataDir>/tools/ffmpeg/bin` → 系统 PATH → 常见安装目录。
 * 只在 Windows 上有意义（引擎是 .exe），非 Windows 一律返回 null —— 与既有行为一致。
 */
object FfmpegLocator {

    private const val FFPLAY_EXE = "ffplay.exe"

    /** DASH 合轨用到的可执行文件（与 ffplay 同包，一起解出来） */
    private const val FFMPEG_EXE = "ffmpeg.exe"

    /** 应用私有 ffmpeg 目录：<dataDir>/tools/ffmpeg（不污染系统） */
    val installDir: File get() = File(AppContext.dataDir, "tools/ffmpeg")

    /** 私有目录下的 ffplay 可执行文件 */
    val privateExe: File get() = File(installDir, "bin/$FFPLAY_EXE")

    /** 私有目录下的 ffmpeg 可执行文件（DASH 合轨用） */
    val privateFfmpegExe: File get() = File(installDir, "bin/$FFMPEG_EXE")

    /** 常见安装目录（PATH 未命中时兜底） */
    private val commonDirs: List<File> = buildList {
        System.getenv("ProgramFiles")?.let { add(File(it, "ffmpeg/bin")) }
        System.getenv("ProgramFiles(x86)")?.let { add(File(it, "ffmpeg/bin")) }
        System.getenv("LOCALAPPDATA")?.let {
            add(File(it, "Microsoft/WinGet/Links"))
            add(File(it, "Programs/ffmpeg/bin"))
        }
        add(File("C:/ffmpeg/bin"))
    }

    @Volatile
    private var cached: File? = null

    @Volatile
    private var checked = false

    @Volatile
    private var cachedFfmpeg: File? = null

    @Volatile
    private var checkedFfmpeg = false

    /**
     * 宿主（Go/Wails 壳）注入的 ffmpeg 可执行文件路径：优先于内置探测顺序，
     * 且不受「非 Windows 返回 null」限制（Linux/macOS 上宿主装了 ffmpeg 也能用）。
     * DASH 合轨在 DLL 形态下的外部 ffmpeg 即由此提供；置 null 恢复内置探测。
     */
    @Volatile
    var externalFfmpeg: File? = null

    fun isWindows(): Boolean =
        System.getProperty("os.name", "").lowercase().contains("win")

    /** 定位可用的 ffplay（结果带缓存）。非 Windows 直接返回 null。 */
    @Synchronized
    fun locate(forceRefresh: Boolean = false): File? {
        if (!isWindows()) return null
        if (!forceRefresh && checked) return cached
        val found = detectExe(FFPLAY_EXE, privateExe)
        cached = found
        checked = true
        return found
    }

    /** 定位可用的 ffmpeg（结果带缓存），供 DASH 合轨使用。顺序同 [locate]；宿主注入最优先。 */
    @Synchronized
    fun locateFfmpeg(forceRefresh: Boolean = false): File? {
        externalFfmpeg?.let { override ->
            if (override.isFile) return override
        }
        if (!isWindows()) return null
        if (!forceRefresh && checkedFfmpeg) return cachedFfmpeg
        val found = detectExe(FFMPEG_EXE, privateFfmpegExe)
        cachedFfmpeg = found
        checkedFfmpeg = true
        return found
    }

    /** 当前生效的 ffplay 路径（无则 null），供设置页展示 */
    fun currentPath(): String? = locate()?.absolutePath

    /** 私有目录里是否已有本应用下载的 FFmpeg（引擎管理页据此显示「卸载」） */
    fun hasPrivateInstall(): Boolean = privateExe.isFile

    /** 使定位缓存失效（引擎下载完成 / 卸载后调用，下次 locate 重新探测磁盘事实） */
    @Synchronized
    fun invalidate() {
        cached = null
        checked = false
        cachedFfmpeg = null
        checkedFfmpeg = false
    }

    private fun detectExe(name: String, privateFile: File): File? {
        privateFile.takeIf { it.isFile }?.let { return it }
        pathSearch(name)?.let { return it }
        commonDirs.forEach { dir ->
            File(dir, name).takeIf { it.isFile }?.let { return it }
        }
        return null
    }

    private fun pathSearch(name: String): File? {
        val path = System.getenv("PATH") ?: return null
        return path.split(File.pathSeparatorChar)
            .asSequence()
            .map { it.trim() }
            .filter { it.isNotEmpty() }
            .map { File(it, name) }
            .firstOrNull { it.isFile }
    }
}
