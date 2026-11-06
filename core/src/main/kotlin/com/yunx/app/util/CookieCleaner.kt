package com.yunx.app.util

import com.yunx.app.data.host.HostServices

/**
 * 退出登录时清除内嵌浏览器中指定网盘域名的 Cookie（门面）。
 *
 * 所有 AccountRepository.logout 都会调用本工具，避免再次登录时被浏览器已保留的会话 Cookie
 * 自动登录。真正的清理由宿主注册的 [com.yunx.app.data.host.WebCookieCleaner] 完成
 * （桌面版 = JCEF 实现 [com.yunx.app.util.JcefCookieCleaner]；未注册时静默忽略，
 * 与历史上「JCEF 未初始化时静默忽略」的语义一致）。
 *
 * ★ 线程约束（宿主实现必须遵守）：浏览器 Cookie 回调由 UI 事件线程泵动，
 *   **绝不能在该线程上阻塞等待回调**。因此这里提供两种用法：
 *   - [clearCookiesForDomains]：发出去就返回（退出登录等无需等待的场景）；
 *   - [clearCookiesAndWait]：挂起等待完成（「添加账号」前必须先清干净旧登录态）。
 */
object CookieCleaner {

    /**
     * 清除给定域名列表下的所有 Cookie（**不等待**完成）。
     * @param domains 域名片段列表，如 ["pan.baidu.com", "baidu.com"]；
     *                cookie 的 domain 命中任一（忽略前导点、忽略大小写）即清除。
     */
    fun clearCookiesForDomains(domains: List<String>) {
        if (domains.isEmpty()) return
        runCatching { HostServices.cookieCleaner.clearForDomains(domains) }
        Unit
    }

    /**
     * 清除并**等待**删除完成（最多 [timeoutMs]）；供「添加账号」这类必须先清干净的场景使用。
     * 调用方在协程里调用即可，不会阻塞 UI 事件线程。
     */
    suspend fun clearCookiesAndWait(domains: List<String>, timeoutMs: Long = 1500L) {
        if (domains.isEmpty()) return
        runCatching { HostServices.cookieCleaner.clearAndWait(domains, timeoutMs) }
        Unit
    }
}
