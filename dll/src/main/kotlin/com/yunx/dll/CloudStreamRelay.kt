package com.yunx.dll

import java.io.BufferedInputStream
import java.io.BufferedOutputStream
import java.io.InputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit
import java.util.concurrent.ConcurrentHashMap
import kotlin.concurrent.thread

/**
 * 云盘直链的本地流中继：核心用 Java TLS 栈（唯一能过夸克等 CDN WAF 的栈）拉流，
 * 经 127.0.0.1 私有端口以纯 HTTP 输出，供 ffplay / <video> 等任何播放器播放。
 *
 * 背景：夸克 CDN 做 TLS 指纹过滤（OkHttp 放行，curl/ffmpeg/Go 全 403），
 * 直接把直链交给外部播放器必然 403——必须由核心代拉。
 *
 * Range 透传：播放器 seek 时发 Range，中继向上游新开带 Range 的请求转发响应头
 * （Content-Range/Length/Accept-Ranges），拖进度可用。
 */
object CloudStreamRelay {

    private class Target(val url: String, val headers: Map<String, String>)

    /** relayId → 目标（直链+请求头）；过期由 URL 有效期兜底（夸克 15-30 分钟） */
    private val targets = ConcurrentHashMap<String, Target>()

    /**
     * 登记直链并启动一个一次性中继（每目标独立端口，简化生命周期）。
     * 返回可直接播放的本地 URL。
     */
    fun start(url: String, headers: Map<String, String>): String {
        val id = "r" + java.lang.Long.toHexString(System.nanoTime())
        targets[id] = Target(url, headers)
        val server = ServerSocket(0, 4, InetAddress.getByName("127.0.0.1"))
        val port = server.localPort
        thread(name = "cloud-relay-$port", isDaemon = true) {
            try {
                while (true) {
                    val sock = server.accept()
                    val t = targets[id]
                    if (t == null) {
                        sock.close()
                    } else {
                        thread(name = "cloud-relay-conn", isDaemon = true) { serve(sock, t) }
                    }
                }
            } catch (_: Exception) {
                // server 关闭即退出
            }
        }
        return "http://127.0.0.1:$port/?k=$id"
    }

    /** 服务单连接：解析 Range → 上游拉流 → 回写状态行+关键头+实体。 */
    private fun serve(sock: Socket, t: Target) {
        try {
            sock.soTimeout = 60_000
            val input = BufferedInputStream(sock.getInputStream())
            val output = BufferedOutputStream(sock.getOutputStream())
            // 读取请求头（到空行），取 Range
            var range: String? = null
            while (true) {
                val line = readLine(input) ?: return
                if (line.isEmpty()) break
                if (line.startsWith("Range:", ignoreCase = true)) {
                    range = line.substringAfter(':').trim()
                }
            }
            openUpstream(t, range).use { conn ->
                val status = conn.code
                val sb = StringBuilder("HTTP/1.1 $status ${statusText(status)}\r\n")
                for (h in listOf("Content-Type", "Content-Length", "Content-Range", "Accept-Ranges")) {
                    conn.header(h)?.let { sb.append(h).append(": ").append(it).append("\r\n") }
                }
                sb.append("Connection: close\r\n\r\n")
                output.write(sb.toString().toByteArray())
                output.flush()
                if (status in 200..399) {
                    conn.body?.byteStream()?.use { up -> up.copyTo(output, 64 * 1024) }
                }
                output.flush()
            }
        } catch (e: Exception) {
            System.err.println("cloud-relay serve 异常: " + e)
            e.printStackTrace()
        } finally {
            runCatching { sock.close() }
        }
    }

    /** OkHttp 客户端：核心下载同款栈（唯一能过夸克 CDN TLS 指纹过滤的），跟随重定向。 */
    private val client by lazy {
        OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(60, TimeUnit.SECONDS)
            .followRedirects(true)
            .build()
    }

    private fun openUpstream(t: Target, range: String?): okhttp3.Response {
        val b = Request.Builder().url(t.url)
        for ((k, v) in t.headers) {
            if (k.equals("Host", true) || k.equals("Range", true) || k.equals("Accept-Encoding", true)) continue
            b.header(k, v)
        }
        range?.let { b.header("Range", it) }
        return client.newCall(b.build()).execute()
    }

    private fun readLine(input: InputStream): String? {
        val sb = StringBuilder()
        while (true) {
            val c = input.read()
            if (c < 0) return if (sb.isEmpty()) null else sb.toString()
            if (c == '\n'.code) return sb.toString().trimEnd('\r')
            sb.append(c.toChar())
            if (sb.length > 16 * 1024) return sb.toString()
        }
    }

    private fun statusText(code: Int): String = when (code) {
        200 -> "OK"; 206 -> "Partial Content"; 302 -> "Found"
        403 -> "Forbidden"; 404 -> "Not Found"; else -> "Status"
    }
}
