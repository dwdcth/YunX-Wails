# YunX 核心 DLL 一键构建（Windows）
#
# 流程（与 Linux 沙箱已验证的命令一一对应）：
#   1. 前置检查：GraalVM（native-image）、MSVC、Go、JDK17 工具链
#   2. gradle 编 :dll（含 :core）并导出运行时 classpath
#   3. native-image --shared 产出 yunxcore.dll（+ .lib 导入库 + 头文件）
#   4. dumpbin 校验导出符号 yunx_ping / yunx_call
#   5. 编 Go 冒烟加载器并运行（期望 PING 42 与各命令响应）
#
# 用法（在仓库根目录）：
#   $env:GRAALVM_HOME = "C:\graalvm-jdk-21*"   # 或用 -GraalVMHome 参数
#   .\dll\windows-dll-kit\build-dll.ps1 [-GraalVMHome <path>] [-SkipSmoke]
param(
    [string]$GraalVMHome = $env:GRAALVM_HOME,
    [switch]$SkipSmoke
)

$ErrorActionPreference = "Stop"
$Repo = Split-Path -Parent $PSScriptRoot
Set-Location $Repo

function Fail($msg) { Write-Host "[FAIL] $msg" -ForegroundColor Red; exit 1 }
function Step($msg) { Write-Host "`n=== $msg ===" -ForegroundColor Cyan }

# ---------- 1. 前置检查 ----------
Step "前置检查"
if (-not $GraalVMHome -or -not (Test-Path "$GraalVMHome\bin\native-image.cmd")) {
    Fail "找不到 GraalVM 的 native-image：请装 GraalVM 21+（含 Native Image），并设 GRAALVM_HOME 或传 -GraalVMHome"
}
$env:JAVA_HOME = $GraalVMHome
Write-Host "GraalVM: $GraalVMHome"

# MSVC：native-image 在 Windows 上需要 cl.exe，经 vcvars64 注入环境
$vsWhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
$vcvars = $null
if (Test-Path $vsWhere) {
    $vsPath = & $vsWhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
    if ($vsPath) { $vcvars = Join-Path $vsPath "VC\Auxiliary\Build\vcvars64.bat" }
}
if (-not $vcvars -or -not (Test-Path $vcvars)) {
    Fail "找不到 Visual Studio 的 vcvars64.bat：请安装 VS 2022 并勾选「使用 C++ 的桌面开发」"
}
Write-Host "MSVC vcvars: $vcvars"

if (-not $SkipSmoke) {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Fail "找不到 Go（冒烟加载器需要）；或加 -SkipSmoke 跳过" }
    Write-Host "Go: $(go version)"
}

# Gradle：仓库未附 gradlew 脚本，本机有 gradle 就用；没有则用 wrapper jar 拉起
$gradleCmd = $null
if (Get-Command gradle -ErrorAction SilentlyContinue) { $gradleCmd = "gradle" }

# sdk-api.jar（编译 @CEntryPoint 需要）：GraalVM 21 发行包可能不带，按 README 从 jimage 提取后用环境变量传入
$sdkJar = $env:YUNX_SDK_JAR
if (-not $sdkJar) {
    $candidate = Join-Path $GraalVMHome "lib\graalvm\sdk-api.jar"
    if (Test-Path $candidate) { $sdkJar = $candidate }
}
if (-not $sdkJar) {
    Fail "缺 sdk-api.jar：按 dll\windows-dll-kit\README.md 的「提取 sdk-api.jar」一节生成后，设 YUNX_SDK_JAR=<路径> 再跑"
}
Write-Host "sdk-api.jar: $sdkJar"

# ---------- 2. Gradle 编 :dll 并导出 classpath ----------
Step "Gradle 编译 :dll"
$gradleArgs = @(":dll:compileKotlin", ":dll:compileJava", ":dll:printRuntimeClasspath", "-PgraalvmSdkJar=$sdkJar", "--console=plain")
$cpLine = $null
if ($gradleCmd) {
    $out = & gradle @gradleArgs 2>&1
} else {
    $out = & java -cp "gradle\wrapper\gradle-wrapper.jar" org.gradle.wrapper.GradleWrapperMain @gradleArgs 2>&1
}
$out | ForEach-Object { if ($_ -match "^RUNTIME_CP=(.*)$") { $script:cpLine = $Matches[1] } }
if (-not $cpLine) { $out | Select-Object -Last 30; Fail "Gradle 失败或未输出 RUNTIME_CP" }

$dllClasses = @("dll\build\classes\java\main", "dll\build\classes\kotlin\main") -join ";"
$cp = "$dllClasses;$cpLine"
Write-Host "classpath 已导出（$($cp.Split(';').Count) 项）"

# ---------- 3. native-image --shared ----------
Step "native-image 构建 yunxcore.dll"
$nativeDir = "dll\build\native"
New-Item -ItemType Directory -Force $nativeDir | Out-Null
# native-image 需要 MSVC 环境：用 cmd 包一层 vcvars
$niArgs = "-J-Xmx3g -cp `"$cp`" --shared -Os -H:Name=yunxcore -H:Path=$nativeDir --no-fallback -H:MaxDuplicationFactor=100"
$cmdLine = "call `"$vcvars`" >nul && `"$GraalVMHome\bin\native-image.cmd`" $niArgs"
cmd /c $cmdLine
if ($LASTEXITCODE -ne 0) { Fail "native-image 构建失败（exit $LASTEXITCODE）" }
$dllFile = Join-Path $nativeDir "yunxcore.dll"
if (-not (Test-Path $dllFile)) { Fail "未产出 $dllFile" }
Write-Host ("产出: {0} ({1:N1} MB)" -f $dllFile, ((Get-Item $dllFile).Length / 1MB))

# ---------- 4. 导出符号校验 ----------
Step "dumpbin 校验导出"
$dumpbin = Get-ChildItem -Path (Split-Path $vcvars -Parent) -Recurse -Filter dumpbin.exe -ErrorAction SilentlyContinue |
    Where-Object { $_.FullName -match "Hostx64\\x64" } | Select-Object -First 1
if ($dumpbin) {
    $exports = & $dumpbin.FullName /exports $dllFile | Out-String
    foreach ($sym in @("yunx_ping", "yunx_call", "graal_create_isolate")) {
        if ($exports -notmatch $sym) { Fail "导出缺失: $sym" }
        Write-Host "  export ok: $sym"
    }
} else {
    Write-Host "  (未找到 dumpbin，跳过符号校验)"
}

# ---------- 5. Go 冒烟 ----------
if (-not $SkipSmoke) {
    Step "Go 冒烟（dll\go-loader，cgo 路径已指向 dll\build\native）"
    Push-Location "dll\go-loader"
    try {
        go build -o loader.exe .
        .\loader.exe
        if ($LASTEXITCODE -ne 0) { Fail "冒烟加载器失败（exit $LASTEXITCODE）" }
    } finally { Pop-Location }
}

Write-Host "`n[DONE] yunxcore.dll 构建与冒烟完成" -ForegroundColor Green
