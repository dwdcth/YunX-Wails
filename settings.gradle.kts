pluginManagement {
    repositories {
        // 国内镜像优先（本机无法直连 dl.google.com）
        maven("https://maven.aliyun.com/repository/gradle-plugin")
        maven("https://maven.aliyun.com/repository/google")
        maven("https://maven.aliyun.com/repository/central")
        gradlePluginPortal()
        mavenCentral()
        google()
    }
}

dependencyResolutionManagement {
    repositories {
        maven("https://maven.aliyun.com/repository/google")
        maven("https://maven.aliyun.com/repository/central")
        mavenCentral()
        google()
    }
}

// YunX-Wails：Go/Wails 壳的原生核心构建（原版 YunX-Desktop 整仓见 upstream/YunX-Desktop 子模块）
rootProject.name = "YunX-Wails"
include(":core")
include(":dll")
