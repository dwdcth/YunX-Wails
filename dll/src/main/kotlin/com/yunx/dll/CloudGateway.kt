package com.yunx.dll

import com.yunx.app.data.network.GuangYaDevice
import com.yunx.app.data.network.GuangYaConstants
import com.yunx.app.data.network.ILanzouConstants
import com.yunx.app.data.network.LanzouConstants
import com.yunx.app.data.network.Pan115Constants
import com.yunx.app.data.network.SharePlatform
import com.yunx.app.data.network.model.DownloadLink
import com.yunx.app.data.network.model.QuotaInfo
import com.yunx.app.data.network.model.ShareFile

/**
 * 云盘网关：桌面各平台 CloudViewModel 的 DLL 等价物（自己的网盘文件操作，非分享）。
 * 列目录/建夹/重命名/移动/删除/配额/直链下载/转存落位，按平台适配签名差异。
 */
object CloudGateway {

    private val rg get() = ResolveGateway

    fun rootFor(platform: SharePlatform): String = when (platform) {
        SharePlatform.QUARK, SharePlatform.UC, SharePlatform.PAN123 -> "0"
        SharePlatform.XUNLEI -> ""
        SharePlatform.BAIDU, SharePlatform.C139 -> "/"
        SharePlatform.PAN115 -> Pan115Constants.ROOT_CID
        SharePlatform.GUANGYA -> GuangYaConstants.ROOT_PARENT_ID
        SharePlatform.ILANZOU -> ILanzouConstants.ROOT_FOLDER_ID
        SharePlatform.LANZOU -> LanzouConstants.ROOT_FOLDER_ID
        SharePlatform.GITHUB -> ""
    }

    // ---- 凭证帮手（未登录抛错）----

    private suspend fun requireQuarkCookie(): String {
        val c = rg.quarkAccount.getFreshCookie() ?: rg.quarkAccount.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录夸克网盘")
        return c
    }

    private suspend fun requireUcCookie(): String {
        val c = rg.ucAccount.getFreshCookie() ?: rg.ucAccount.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录 UC 网盘")
        return c
    }

    private suspend fun requireXunlei(): Triple<String, String, String> {
        val a = rg.xunleiAccount.getAccount()
        if (a == null || a.accessToken.isBlank()) throw IllegalStateException("未登录迅雷云盘")
        return Triple(a.accessToken, a.deviceId, a.captchaToken)
    }

    private suspend fun requireBaiduCookie(): String {
        val c = rg.baiduAccount.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录百度网盘")
        return c
    }

    private suspend fun requireC139Cookie(): String {
        val c = rg.c139Account.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录移动云盘")
        return c
    }

    private suspend fun requirePan123Token(): String {
        val t = rg.pan123Account.getAccount()?.accessToken
        if (t.isNullOrBlank()) throw IllegalStateException("未登录 123 云盘")
        return t
    }

    private suspend fun requirePan115Cookie(): String {
        val c = rg.pan115Account.getAccount()?.cookie
        if (c.isNullOrBlank()) throw IllegalStateException("未登录 115 网盘")
        return c
    }

    private suspend fun requireGuangYa(): Pair<String, GuangYaDevice> {
        val a = rg.guangyaAccount.getAccount()
        if (a == null || a.accessToken.isBlank()) throw IllegalStateException("未登录光鸭云盘")
        return a.accessToken to GuangYaDevice(a.deviceId, a.deviceSign)
    }

    private suspend fun requireILanzou(): Pair<String, String> {
        val a = rg.ilanzouAccount.getAccount()
        if (a == null || a.appToken.isBlank()) throw IllegalStateException("未登录蓝奏云优享版")
        return a.appToken to a.uuid
    }

    private suspend fun requireLanzou(): Triple<String, String, String> {
        val a = rg.lanzouAccount.getAccount()
        if (a == null || a.cookie.isBlank()) throw IllegalStateException("未登录蓝奏云")
        // uid/vei 由 Cookie 实时解析（对齐桌面 LanzouCloudViewModel.creds，无本地缓存）
        val params = rg.lanzouApi.fetchLoginParams(a.cookie)
            ?: throw IllegalStateException("蓝奏登录态已失效，请重新登录")
        return Triple(a.cookie, params.uid, params.vei)
    }

    /** 蓝奏 fid 前缀（d:/f:）转数字 id（对齐桌面 folderNumeric/fileNumeric）。 */
    private fun lzNum(fid: String): String =
        fid.removePrefix(LanzouConstants.FOLDER_PREFIX).removePrefix(LanzouConstants.FILE_PREFIX)

    // ---- 列目录（游标/分页平台在网关内翻全量，对齐各 CloudViewModel）----

    suspend fun list(platform: SharePlatform, dirFid: String): List<ShareFile> = when (platform) {
        SharePlatform.QUARK -> rg.quarkApi.listAllCloudFiles(dirFid, requireQuarkCookie()) ?: emptyList()
        SharePlatform.UC -> rg.ucApi.listAllCloudFiles(dirFid, requireUcCookie()) ?: emptyList()
        SharePlatform.XUNLEI -> requireXunlei().let { (t, d, c) ->
            rg.xunleiApi.getFiles(dirFid, t, d, c) ?: emptyList()
        }
        SharePlatform.BAIDU -> rg.baiduApi.listCloudFiles(dirFid, requireBaiduCookie())
        SharePlatform.C139 -> {
            val out = mutableListOf<ShareFile>()
            var cursor: String? = null
            do {
                val (files, next) = rg.c139Api.listCloudFiles(dirFid, requireC139Cookie(), cursor)
                out += files
                cursor = next
            } while (cursor != null)
            out
        }
        SharePlatform.PAN123 -> {
            val out = mutableListOf<ShareFile>()
            var next: String? = null
            do {
                val (files, cur) = rg.pan123Api.listCloudFiles(dirFid, requirePan123Token())
                out += files
                next = cur
            } while (next != null)
            out
        }
        SharePlatform.PAN115 -> {
            val all = mutableListOf<ShareFile>()
            var offset = 0
            do {
                val page = rg.pan115Api.listFiles(dirFid, requirePan115Cookie(), offset, Pan115Constants.PAGE_LIMIT)
                offset += page.files.size
                all += page.files
            } while (page.total > 0 && all.size < page.total)
            all
        }
        SharePlatform.GUANGYA -> requireGuangYa().let { (t, _) -> rg.guangyaApi.listCloudFiles(t, dirFid) }
        SharePlatform.ILANZOU -> requireILanzou().let { (t, u) -> rg.ilanzouApi.listFiles(t, u, dirFid) }
        SharePlatform.LANZOU -> requireLanzou().let { (c, uid, vei) ->
            rg.lanzouApi.listCloudFiles(c, uid, vei, dirFid)
        }
        SharePlatform.GITHUB -> emptyList()
    }

    // ---- 建夹 / 重命名 / 移动 / 删除 ----

    suspend fun mkdir(platform: SharePlatform, dirFid: String, name: String) {
        when (platform) {
            SharePlatform.QUARK -> rg.quarkApi.createFolder(name, dirFid, requireQuarkCookie())
            SharePlatform.UC -> rg.ucApi.createFolder(name, dirFid, requireUcCookie())
            SharePlatform.XUNLEI -> requireXunlei().let { (t, d, c) ->
                rg.xunleiApi.createFolder(name, dirFid, t, d, c)
            }
            SharePlatform.BAIDU -> {
                val parent = dirFid.trimEnd('/')
                rg.baiduApi.createDir("$parent/$name", requireBaiduCookie())
            }
            SharePlatform.C139 -> rg.c139Api.createDir(dirFid, name, requireC139Cookie())
            SharePlatform.PAN123 -> rg.pan123Api.createDir(dirFid, name, requirePan123Token())
            SharePlatform.PAN115 -> rg.pan115Api.createDir(dirFid, name, requirePan115Cookie())
            SharePlatform.GUANGYA -> requireGuangYa().let { (t, _) ->
                rg.guangyaApi.createDir(t, dirFid, name)
            }
            SharePlatform.ILANZOU -> requireILanzou().let { (t, u) ->
                rg.ilanzouApi.createDir(t, u, dirFid, name)
            }
            SharePlatform.LANZOU -> requireLanzou().let { (c, uid, vei) ->
                rg.lanzouApi.createFolder(c, uid, vei, dirFid, name)
            }
            SharePlatform.GITHUB -> {}
        }
    }

    suspend fun rename(platform: SharePlatform, file: ShareFile, newName: String) {
        when (platform) {
            SharePlatform.QUARK ->
                check(rg.quarkApi.renameFile(file.fid, newName, requireQuarkCookie())) { "重命名失败" }
            SharePlatform.UC ->
                check(rg.ucApi.renameFile(file.fid, newName, requireUcCookie())) { "重命名失败" }
            SharePlatform.XUNLEI -> requireXunlei().let { (t, d, c) ->
                rg.xunleiApi.renameFile(file.fid, newName, t, d, c)
            }
            // 百度按完整路径重命名（fid 存的是 fs_id，路径在 fidToken）
            SharePlatform.BAIDU ->
                check(rg.baiduApi.renameFile(file.fidToken, newName, requireBaiduCookie())) { "重命名失败" }
            SharePlatform.C139 ->
                check(rg.c139Api.renameFile(file.fid, newName, requireC139Cookie())) { "重命名失败" }
            SharePlatform.PAN123 -> rg.pan123Api.renameFile(file.fid, newName, requirePan123Token())
            SharePlatform.PAN115 -> rg.pan115Api.rename(file.fid, newName, requirePan115Cookie())
            SharePlatform.GUANGYA -> requireGuangYa().let { (t, _) ->
                rg.guangyaApi.renameFile(t, file.fid, newName)
            }
            SharePlatform.ILANZOU -> requireILanzou().let { (t, u) ->
                if (file.isdir) rg.ilanzouApi.renameFolder(t, u, file.fid, newName)
                else rg.ilanzouApi.renameFile(t, u, file.fid, newName)
            }
            SharePlatform.LANZOU -> requireLanzou().let { (c, uid, vei) ->
                if (file.isdir) rg.lanzouApi.renameFolder(c, uid, vei, lzNum(file.fid), newName)
                else rg.lanzouApi.renameFile(c, uid, vei, lzNum(file.fid), newName)
            }
            SharePlatform.GITHUB -> {}
        }
    }

    /**
     * 移动到目标目录。targetFid：目标目录 fid；targetPath：目标目录的路径形态
     * （仅百度需要，其余平台忽略）。
     */
    suspend fun move(platform: SharePlatform, files: List<ShareFile>, targetFid: String, targetPath: String?) {
        when (platform) {
            SharePlatform.QUARK -> files.forEach {
                rg.quarkApi.moveFile(it.fid, targetFid, requireQuarkCookie())
            }
            SharePlatform.UC -> files.forEach {
                rg.ucApi.moveFile(it.fid, targetFid, requireUcCookie())
            }
            SharePlatform.XUNLEI -> requireXunlei().let { (t, d, c) ->
                rg.xunleiApi.moveFile(files.map { it.fid }, targetFid, t, d, c)
            }
            SharePlatform.BAIDU ->
                check(rg.baiduApi.moveFiles(files.map { it.fidToken }, targetPath ?: "/", requireBaiduCookie())) { "移动失败" }
            SharePlatform.C139 -> rg.c139Api.moveFiles(files.map { it.fid }, targetFid, requireC139Cookie())
            SharePlatform.PAN123 -> rg.pan123Api.moveFiles(files.map { it.fid }, targetFid, requirePan123Token())
            SharePlatform.PAN115 -> rg.pan115Api.move(files.map { it.fid }, targetFid, requirePan115Cookie())
            SharePlatform.GUANGYA -> requireGuangYa().let { (t, _) ->
                rg.guangyaApi.moveFiles(t, files.map { it.fid }, targetFid)
            }
            SharePlatform.ILANZOU -> requireILanzou().let { (t, u) ->
                rg.ilanzouApi.moveEntries(
                    t, u,
                    folderIds = files.filter { it.isdir }.map { it.fid },
                    fileIds = files.filter { !it.isdir }.map { it.fid },
                    targetId = targetFid,
                )
            }
            SharePlatform.LANZOU -> requireLanzou().let { (c, uid, vei) ->
                files.filter { !it.isdir }.forEach {
                    rg.lanzouApi.moveFile(c, uid, vei, lzNum(targetFid), lzNum(it.fid))
                }
            }
            SharePlatform.GITHUB -> {}
        }
    }

    suspend fun delete(platform: SharePlatform, files: List<ShareFile>) {
        when (platform) {
            SharePlatform.QUARK -> files.forEach { rg.quarkApi.deleteFile(it.fid, requireQuarkCookie()) }
            SharePlatform.UC -> files.forEach { rg.ucApi.deleteFile(it.fid, requireUcCookie()) }
            SharePlatform.XUNLEI -> requireXunlei().let { (t, d, c) ->
                check(rg.xunleiApi.deleteFiles(files.map { it.fid }, t, d, c)) { "删除失败" }
            }
            SharePlatform.BAIDU ->
                check(rg.baiduApi.deleteFiles(files.map { it.fidToken }, requireBaiduCookie())) { "删除失败" }
            SharePlatform.C139 -> rg.c139Api.deleteFiles(files.map { it.fid }, requireC139Cookie())
            SharePlatform.PAN123 -> rg.pan123Api.deleteFiles(files, requirePan123Token())
            SharePlatform.PAN115 -> rg.pan115Api.delete(
                files.map { it.fid }, files.firstOrNull()?.pdirFid ?: "0", requirePan115Cookie())
            SharePlatform.GUANGYA -> requireGuangYa().let { (t, _) ->
                rg.guangyaApi.deleteFiles(t, files.map { it.fid })
            }
            SharePlatform.ILANZOU -> requireILanzou().let { (t, u) ->
                rg.ilanzouApi.deleteEntries(
                    t, u,
                    folderIds = files.filter { it.isdir }.map { it.fid },
                    fileIds = files.filter { !it.isdir }.map { it.fid },
                )
            }
            SharePlatform.LANZOU -> requireLanzou().let { (c, uid, vei) ->
                files.forEach {
                    if (it.isdir) rg.lanzouApi.deleteFolder(c, uid, vei, lzNum(it.fid))
                    else rg.lanzouApi.deleteFile(c, uid, vei, lzNum(it.fid))
                }
            }
            SharePlatform.GITHUB -> {}
        }
    }

    // ---- 配额（蓝奏云官方无接口，返回 null）----

    suspend fun quota(platform: SharePlatform): QuotaInfo? = runCatching {
        when (platform) {
            SharePlatform.QUARK -> rg.quarkApi.getQuota(requireQuarkCookie())
            SharePlatform.UC -> rg.ucApi.getQuota(requireUcCookie())
            SharePlatform.XUNLEI -> requireXunlei().let { (t, d, c) -> rg.xunleiApi.getQuota(t, d, c) }
            SharePlatform.BAIDU -> rg.baiduApi.getQuota(requireBaiduCookie())
            SharePlatform.C139 -> rg.c139Api.getQuota(requireC139Cookie())
            SharePlatform.PAN123 -> rg.pan123Api.getQuota(requirePan123Token())
            SharePlatform.PAN115 -> rg.pan115Api.getQuota(requirePan115Cookie())
            SharePlatform.GUANGYA -> requireGuangYa().let { (t, dev) -> rg.guangyaApi.getQuota(t, dev) }
            SharePlatform.ILANZOU -> requireILanzou().let { (t, u) -> rg.ilanzouApi.getQuota(t, u) }
            SharePlatform.LANZOU, SharePlatform.GITHUB -> null
        }
    }.getOrNull()

    // ---- 个人盘直链下载（夸克/UC/蓝奏经典）----

    suspend fun downloadLink(platform: SharePlatform, file: ShareFile): DownloadLink = when (platform) {
        SharePlatform.QUARK ->
            rg.quarkApi.getDownloadLink(file.fid, requireQuarkCookie())
                ?: throw IllegalStateException("取直链失败")
        SharePlatform.UC ->
            rg.ucApi.getDownloadLink(file.fid, requireUcCookie())
                ?: throw IllegalStateException("取直链失败")
        SharePlatform.LANZOU -> requireLanzou().let { (c, uid, vei) ->
            rg.lanzouApi.getPersonalDownloadLink(c, uid, vei, lzNum(file.fid), file.fname)
        }
        else -> throw UnsupportedOperationException("该平台的个人盘直链下载暂未接入")
    }
}
