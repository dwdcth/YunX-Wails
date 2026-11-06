package com.yunx.app.data.network.video

import com.yunx.app.data.host.HostServices

/**
 * 隐藏 Web 引擎门面：在不可见浏览器里加载页面、等 JS 挑战/渲染完成、提取数据。
 *
 * 真正的实现由宿主注册到 [HostServices.webEngine]：
 * - 桌面版 = JCEF 实现（com.yunx.app.data.network.video.JcefHeadlessWebEngine）；
 * - Wails 版 = WebView2 实现（宿主侧，接口不变）；
 * - 未注册时 [loadAndExtract] 返回 null、预热/关闭为 no-op，
 *   与历史上「JCEF 不可用则跳过网页兜底」的行为一致。
 *
 * 调用方：抖音解析（acrawler 挑战兜底）、迅雷网页凭证获取、应用启动预热。
 */
object HeadlessWebView {

    /**
     * 加载 [url]，轮询 [readyCheckJs]（返回真值视为就绪）后执行 [extractJs] 并返回其字符串结果；
     * 超时 / 引擎不可用 / 出错返回 null。
     *
     * @param userAgent 页面 UA（部分站点按 UA 返回不同挑战，必须与解析侧一致）
     * @param extraDocumentStartJs 在页面脚本之前注入的 JS（如改写指纹、预置变量）
     */
    suspend fun loadAndExtract(
        url: String,
        userAgent: String,
        readyCheckJs: String,
        extractJs: String,
        timeoutMs: Long = 25_000L,
        pollIntervalMs: Long = 120L,
        extraDocumentStartJs: List<String> = emptyList()
    ): String? = HostServices.webEngine.loadAndExtract(
        url, userAgent, readyCheckJs, extractJs, timeoutMs, pollIntervalMs, extraDocumentStartJs
    )

    /** 后台预热：提前把引擎与目标站点 Cookie 热起来，失败无副作用。 */
    fun prewarm(url: String) = HostServices.webEngine.prewarm(url)

    /** 安静关闭引擎（进程退出时调用）。 */
    fun closeQuietly() = HostServices.webEngine.closeQuietly()
}
