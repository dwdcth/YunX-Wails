plugins {
    kotlin("jvm")
}

kotlin {
    jvmToolchain(17)
}

dependencies {
    // 核心的第三方依赖用 api 暴露：桌面模块的 UI 代码历史上也直接引用 OkHttp / 协程类型，
    // 拆分后保持其编译期可见性不变，避免为拆分去改一圈 UI import。
    api("com.squareup.okhttp3:okhttp:4.12.0")
    api("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.9.0")
    api("org.json:json:20240303")
    // 与桌面模块同一個裁剪 jar（只含 Windows 原生库；类文件与官方版一致）
    api(files("${rootProject.projectDir}/desktop/libs/sqlite-jdbc-3.47.1.0-win.jar"))
    // DASH 合轨 / MP4 重封装（纯 Java，无原生库）；排除其 compile 作用域的 junit（与桌面模块一致）
    api("org.mp4parser:muxer:1.9.56") {
        exclude(group = "junit", module = "junit")
    }
}

// ---- 版本号常量：与桌面模块原 generateAppVersion 相同来源（仓库根 version.txt），生成位置改到核心 ----
// 桌面模块与核心共用 com.yunx.app 包名下的 APP_VERSION / APP_IS_PREVIEW；改为 public，
// 因为 internal 是模块作用域，拆分后桌面模块读不到核心的 internal 常量。
val versionLines = rootProject.file("version.txt").readLines().map { it.trim() }.filter { it.isNotEmpty() }
val appVersion = versionLines.firstOrNull().orEmpty()
require(appVersion.matches(Regex("""\d+(\.\d+)*"""))) { "version.txt 内容非法: '$appVersion'" }
val appIsPreview = versionLines.drop(1).firstOrNull()?.let { line ->
    when (line.substringAfter('=', "").trim()) {
        "1" -> true
        "0" -> false
        else -> line.lowercase() in setOf("preview", "beta")
    }
} ?: false

val generateAppVersion by tasks.registering {
    val outDir = layout.buildDirectory.dir("generated/appVersion/kotlin")
    val version = appVersion
    val isPreview = appIsPreview.toString()
    outputs.dir(outDir)
    inputs.property("version", version)
    inputs.property("isPreview", isPreview)
    doLast {
        val pkgDir = outDir.get().asFile.resolve("com/yunx/app")
        pkgDir.mkdirs()
        pkgDir.resolve("AppVersion.kt").writeText(
            """
            |package com.yunx.app
            |
            |/** 由根目录 version.txt 生成，请勿手改 */
            |const val APP_VERSION = "$version"
            |
            |/** 是否为预览版（version.txt 第二行 Beta=1）；预览版在关于页显示 Beta 标记 */
            |const val APP_IS_PREVIEW = $isPreview
            |""".trimMargin() + "\n"
        )
    }
}
kotlin.sourceSets["main"].kotlin.srcDir(generateAppVersion)
