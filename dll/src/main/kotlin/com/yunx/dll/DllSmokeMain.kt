package com.yunx.dll

import com.sun.net.httpserver.HttpServer
import java.io.File
import java.net.InetSocketAddress

/**
 * JVM 冒烟入口：与 DLL 完全相同的门面逻辑，在 JVM 上先行验证，
 * 也供 native-image-agent 收集反射/资源元数据时驱动（见 windows-dll-kit）。
 *
 * 下载链路用本地 HTTP 服务器（支持 Range 分片）做真实端到端：submit → 轮询 → 校验文件 → 清理。
 */
fun main() {
    val cases = mutableListOf(
        """{"cmd":"version"}""",
        """{"cmd":"parse_share","text":"链接：https://pan.quark.cn/s/abc123XYZ 提取码：a1b2"}""",
        """{"cmd":"parse_share","text":"https://pan.baidu.com/s/1qwertyuiop?pwd=zz99"}""",
        """{"cmd":"kv_put","key":"smoke","value":"hello"}""",
        """{"cmd":"kv_get","key":"smoke"}""",
        """{"cmd":"key_roundtrip"}""",
        """{"cmd":"host_set_machine_id","id":"dll-smoke-machine-id"}""",
        """{"cmd":"settings_list"}""",
        // ★ settings_set 只在 prefs 根被 -Djava.util.prefs.userRoot 隔离时执行（见下方 SETTINGS_SET_ROUNDTRIP），
        //   java.util.prefs 不跟随 YUNX_DESKTOP_DATA_DIR，无条件执行会改写真实用户设置
        """{"cmd":"accounts_list"}""",
        """{"cmd":"host_set_proxy"}""",
        """{"cmd":"host_set_ffmpeg","path":"/nonexistent/ffmpeg"}""",
        """{"cmd":"nope"}"""
    )
    // Linux 沙箱/CI 上顺带验一次有效路径的注入
    File("/usr/bin/ffmpeg").takeIf { it.isFile }?.let {
        cases.add("""{"cmd":"host_set_ffmpeg","path":"/usr/bin/ffmpeg"}""")
        cases.add("""{"cmd":"host_set_ffmpeg","path":""}""")
    }
    for (c in cases) {
        println("REQ $c")
        println("RESP " + DllFacade.handle(c))
    }

    // 下载冒烟会建库/建钥：必须用隔离的数据目录（曾因未隔离导致真实 credential.key 被轮换）。
    val dataDir = System.getenv("YUNX_DESKTOP_DATA_DIR")
    if (dataDir.isNullOrBlank() || dataDir == System.getProperty("user.home")) {
        println("DOWNLOAD_SMOKE_SKIPPED：未设置 YUNX_DESKTOP_DATA_DIR 隔离目录，拒绝触碰真实数据")
    } else {
        downloadSmoke()
    }
    // 历史/收藏 DAO 往返（隔离环境，纯本地）
    for (c in listOf(
        """{"cmd":"link_history_add","url":"https://pan.quark.cn/s/smoke1","title":"冒烟分享","platform":"QUARK","pwd":"a1b2"}""",
        """{"cmd":"link_history_add","url":"https://pan.quark.cn/s/smoke1","title":"冒烟分享","platform":"QUARK"}""",
        """{"cmd":"link_history_list"}""",
        """{"cmd":"bookmark_add","link":"https://pan.baidu.com/s/1smoke","title":"收藏冒烟","platform":"BAIDU","category":"测试"}""",
        """{"cmd":"bookmark_list"}""",
        """{"cmd":"share_list","dirFid":"0"}""",
    )) {
        println("REQ $c")
        println("RESP " + DllFacade.handle(c))
    }

    // 认证备份冒烟：隔离环境（数据目录+prefs 均隔离）里做 加密/明文 导出→导入 往返与错误口令分支
    if (System.getProperty("java.util.prefs.userRoot")?.isNotBlank() == true &&
        !System.getenv("YUNX_DESKTOP_DATA_DIR").isNullOrBlank()
    ) {
        val dir = File(System.getProperty("java.io.tmpdir"), "yunx-dll-smoke-backup")
        dir.mkdirs()
        val encPath = File(dir, "backup.yunx").absolutePath
        val jsonPath = File(dir, "backup.json").absolutePath
        for (c in listOf(
            """{"cmd":"auth_backup_name","encrypted":true}""",
            """{"cmd":"auth_export","path":"$encPath","password":"smoke12345"}""",
            """{"cmd":"auth_import","path":"$encPath","password":"smoke12345"}""",
            """{"cmd":"auth_import","path":"$encPath","password":"wrong-password"}""",
            """{"cmd":"auth_export","path":"$jsonPath"}""",
            """{"cmd":"auth_import","path":"$jsonPath"}""",
        )) {
            println("REQ $c")
            println("RESP " + DllFacade.handle(c))
        }
        println("BACKUP_FILE_EXISTS enc=${File(encPath).isFile} json=${File(jsonPath).isFile}")
    }

    // settings/account 冒烟与真实设置同源（java.util.prefs）：仅当 prefs 根已被
    // -Djava.util.prefs.userRoot 重定向到隔离目录时才跑 settings_set（避免改写真实设置）。
    if (System.getProperty("java.util.prefs.userRoot")?.isNotBlank() == true) {
        DllFacade.handle("""{"cmd":"settings_set","key":"downloadThreads","value":"16"}""")
        val back = DllFacade.handle("""{"cmd":"settings_list"}""")
        val threadsOk = Regex("\"downloadThreads\":16").containsMatchIn(back)
        println("SETTINGS_SET_ROUNDTRIP ok=$threadsOk")
        // 强制 flush：让 native-image-agent 采到 java.util.prefs 的 XML 写出链路元数据
        runCatching { java.util.prefs.Preferences.userRoot().flush() }
    }

    // 储存目录冒烟（prefs 根 + 数据目录均隔离时才跑；上面已注入 host_set_machine_id）：
    // ① 三键 settings_set 往返；② 加密档位 loose↔normal 重包裹后 key_roundtrip 仍一致；
    // ③ storage_migrate_db 拷贝 yunx.db/credential.key 到新目录 + 配置回读（重启语义由 native 两阶段冒烟验证）。
    if (System.getProperty("java.util.prefs.userRoot")?.isNotBlank() == true &&
        !System.getenv("YUNX_DESKTOP_DATA_DIR").isNullOrBlank()
    ) {
        val dataDir = File(System.getenv("YUNX_DESKTOP_DATA_DIR"))
        val target = File(System.getProperty("java.io.tmpdir"), "yunx-dll-smoke-db-migrate")
        target.deleteRecursively()
        for (c in listOf(
            // ① 缓存/数据库目录配置往返（空串 = 清 null 恢复默认）
            """{"cmd":"settings_set","key":"storageCacheDir","value":"/tmp/yunx-smoke-cache"}""",
            """{"cmd":"settings_set","key":"storageCacheDir","value":""}""",
            // ② 建钥（宽松档）→ 切普通档（重包裹）→ 回读仍一致 → 切回宽松
            """{"cmd":"key_roundtrip","dir":"${dataDir.absolutePath}"}""",
            """{"cmd":"storage_apply_encryption","level":"normal"}""",
            """{"cmd":"key_roundtrip","dir":"${dataDir.absolutePath}"}""",
            """{"cmd":"storage_apply_encryption","level":"loose"}""",
            """{"cmd":"key_roundtrip","dir":"${dataDir.absolutePath}"}""",
            // ③ 数据库搬迁（占位库文件即可；checkpoint 失败会被 StorageMigrator 内部吞掉并连同 -wal 拷贝兜底）
            """{"cmd":"storage_migrate_db","targetDir":"${target.absolutePath}"}""",
            """{"cmd":"storage_migrate_db","targetDir":"${target.absolutePath}"}""",
            """{"cmd":"settings_list"}""",
        )) {
            // ③ 前先放占位库文件（真实 yunx.db 未建时兜底；建了则保留真实文件）
            if (c.contains("storage_migrate_db") && !File(dataDir, "yunx.db").isFile) {
                File(dataDir, "yunx.db").writeText("placeholder")
            }
            println("REQ $c")
            println("RESP " + DllFacade.handle(c))
        }
        println(
            "DB_MIGRATE_FILES target=" +
                listOf("yunx.db", "credential.key").all { File(target, it).isFile } +
                " oldKept=" + File(dataDir, "yunx.db").isFile
        )
        runCatching { java.util.prefs.Preferences.userRoot().flush() }
    }
    println("SMOKE_DONE")
}

/** 本地 HTTP 服务器（支持 Range → 多分片路径）上的真实下载冒烟。 */
private fun downloadSmoke() {
    val payload = ByteArray(4 shl 20) { (it % 251).toByte() } // 4MB 确定性数据
    val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
    server.createContext("/file.bin") { ex ->
        val range = ex.requestHeaders.getFirst("Range")
        val (from, endIncl) = parseRange(range, payload.size)
        val slice = payload.copyOfRange(from, endIncl + 1)
        ex.responseHeaders.add("Accept-Ranges", "bytes")
        if (range != null) ex.responseHeaders.add("Content-Range", "bytes $from-$endIncl/${payload.size}")
        ex.sendResponseHeaders(if (range != null) 206 else 200, slice.size.toLong())
        ex.responseBody.use { it.write(slice) }
    }
    server.executor = null
    server.start()
    try {
        val dir = File(System.getProperty("java.io.tmpdir"), "yunx-dll-smoke-dl").apply { mkdirs() }
        val submit = DllFacade.handle(
            """{"cmd":"download_submit","url":"http://127.0.0.1:${server.address.port}/file.bin",""" +
                """"fileName":"smoke.bin","saveDir":"${dir.absolutePath.replace("\\", "\\\\")}"}"""
        )
        println("REQ download_submit")
        println("RESP $submit")
        val taskId = Regex("\"taskId\":(\\d+)").find(submit)?.groupValues?.get(1)?.toLong()
            ?: error("no taskId in $submit")

        var last: String? = null
        var status = -1
        val deadline = System.currentTimeMillis() + 60_000
        while (System.currentTimeMillis() < deadline) {
            Thread.sleep(300)
            last = DllFacade.handle("""{"cmd":"download_list"}""")
            status = Regex("\"taskId\":$taskId,.{0,400}?\"status\":(\\d)")
                .find(last ?: "")?.groupValues?.get(1)?.toInt() ?: -1
            if (status == 3 || status == 4) break
        }
        println("DOWNLOAD_STATUS final=$status")
        println("RESP download_list(最后快照) " + (last ?: "").take(600))
        val file = File(dir, "smoke.bin")
        val ok = file.isFile && file.length() == payload.size.toLong() && file.inputStream().use {
            it.readBytes().contentEquals(payload)
        }
        println("DOWNLOAD_VERIFY file_exists=${file.isFile} size_ok=${file.length() == payload.size.toLong()} content_ok=$ok")
        DllFacade.handle("""{"cmd":"download_remove","taskId":$taskId,"deleteLocal":true}""")
        println("RESP download_remove")
    } finally {
        server.stop(0)
    }
}

private fun parseRange(range: String?, total: Int): Pair<Int, Int> {
    if (range == null) return 0 to total - 1
    val spec = range.removePrefix("bytes=").trim()
    val parts = spec.split("-")
    val from = parts.getOrNull(0)?.toIntOrNull()?.coerceIn(0, total - 1) ?: 0
    val endIncl = parts.getOrNull(1)?.toIntOrNull()?.coerceIn(from, total - 1) ?: (total - 1)
    return from to endIncl
}
