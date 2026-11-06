plugins {
    kotlin("jvm")
}

kotlin {
    jvmToolchain(17)
}

dependencies {
    // 核心模块（被编进 DLL 的全部业务逻辑）
    implementation(project(":core"))
    // GraalVM 注解（@CEntryPoint 等）：仅编译期。
    // 路径优先级：-PgraalvmSdkJar 指定 > $GRAALVM_HOME/lib/graalvm/sdk-api.jar。
    // GraalVM 21 的发行包不带这个 jar 时，按 windows-dll-kit/README 的步骤从 jimage 提取一份再用 -P 指过来。
    val sdkJar = providers.gradleProperty("graalvmSdkJar").orNull
        ?: System.getenv("GRAALVM_HOME")?.let { "$it/lib/graalvm/sdk-api.jar" }
        ?: "sdk-api.jar-not-found"
    compileOnly(files(sdkJar))
}

// 打印运行时 classpath（native-image 构建脚本用）
tasks.register("printRuntimeClasspath") {
    doLast {
        val cp = configurations.runtimeClasspath.get().files.joinToString(":") { it.absolutePath }
        println("RUNTIME_CP=$cp")
    }
}

// JVM 冒烟入口打包（验证门面逻辑 + 给 native-image-agent 收集元数据用）
tasks.register<Jar>("smokeJar") {
    archiveBaseName.set("yunx-dll-smoke")
    dependsOn(tasks.named("classes"))
    from(sourceSets.main.get().output)
    manifest {
        attributes("Main-Class" to "com.yunx.dll.DllSmokeMainKt")
    }
}
