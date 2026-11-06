# 交接文档：YunX-Desktop 核心模块化 + DLL + Wails 3 服务层

> 接收方：负责界面（Wails 3 前端）与后续产品化的 AI / 开发者。
> 本文档描述 2026-11-05 这一批交付的**现状、架构、协议、构建方法与坑**，目标文档见 `GOAL.md`。

## 1. 交付范围

**已完成（均有实测证据，见 §8）**：
- 把原单模块 `:desktop` 的数据层正式拆为独立 Gradle 模块 `:core`，宿主能力全部接口化；
- 新增 `:dll` 模块：核心的 C ABI 门面（`yunx_ping` / `yunx_call`，JSON 协议），已用 GraalVM
  Native Image 编出动态库（Linux .so 实测；Windows .dll 用 `dll/windows-dll-kit` 在真机编）；
- 仓库根：Wails 3 Go 应用（服务层 + `frontend/` Vue3 前端）；
- `docs/WAILS-WEBVIEW2-CONTRACT.md`：登录与网页兜底改走 WebView2 的宿主契约；
- 原 Compose 桌面应用保持可编译、行为不变（作为对照与回退形态长期并存，直到 Wails 版验收）。

**明确不在本批范围**：Wails 前端界面实现、Windows 真机上的 .dll 构建与真机联调（工具包已备好）。

## 2. 基线与仓库状态

- 基线提交：`3700c1c`（github.com/tidain/YunX-Desktop）。本批改动在**工作区未提交**，
  用 `git status` 可见全貌：约 131 个文件从 `desktop/src` 移到 `core/src`（git 显示为删除+新增，
  内容除文内列明的接缝编辑外不变），以及新增 `core/`、`dll/`（含 `dll/windows-dll-kit/`）；Go/Wails 壳在仓库根，原版整仓挂 `upstream/YunX-Desktop` 子模块。
- 建议接收方先通读本文件，再跑一遍 §7 的构建与冒烟，最后开始界面工作；**不要先提交**，
  先在真机把 Windows 工具包流程跑通再决定提交粒度。

## 3. 终态架构（目标形态）

```
┌────────────────────────────────────────────────────────────┐
│ Wails 3 应用（Go 壳 + Web 前端）                             │
│   前端 ──bindings──> CoreService(Go) ──> yunxcore 封装       │
│                                            │ C ABI (JSON)   │
│   WebView2：交互登录窗 + 隐藏提取（宿主契约，见 docs/）       │
└────────────────────────────────────────────┼───────────────┘
                                             ▼
   yunxcore.dll（GraalVM native-image --shared）
     NativeExports: yunx_ping / yunx_call
     DllFacade: JSON 命令分发 ──> :core 全部业务逻辑
     宿主能力经 com.yunx.app.data.host 接口反向调用宿主（后续扩展）
┌────────────────────────────────────────────────────────────┐
│ 并存的原形态：Compose 桌面应用（:desktop 依赖 :core）         │
│   宿主接口的 Windows 实现由 DesktopHost.registerAll() 注册    │
└────────────────────────────────────────────────────────────┘
```

## 4. `:core` 模块详解

### 4.1 内容与边界

- 位置：`core/src/main/kotlin/com/yunx/app/`，含 `AppContext`、`data/**`（除 4 个宿主文件）、
  `util/Log.kt`、`util/LogRedactor.kt`、`util/DiagnosticLog.kt`，共 131 个文件。
- 留在 `:desktop` 的 4 个宿主文件（同包名、物理分离）：
  `data/preview/FfplayManager.kt`（改为委托核心的 `FfmpegLocator`）、
  `data/preview/FfplayWindowFx.kt`、`data/bridge/BrowserBridgeServer.kt`、
  `data/network/video/HeadlessWebView.kt`（已改名为 `JcefHeadlessWebEngine`，见 4.3）。
- 核心**零直依赖**：JNA、JCEF、java.util.prefs、Compose、JavaFX 一律不出现在 core 源码
  （可用 `grep -rn 'com.sun.jna\|org.cef\|java.util.prefs' core/src` 自查，只剩注释）。

### 4.2 宿主接口（`core/.../data/host/HostServices.kt`）

核心需要、但只能由宿主提供的能力，全部收敛于此。每个接口都有**无副作用的默认实现**，
未注册时核心可独立运行（语义与历史上的「JCEF 未就绪就跳过」等行为对齐）。

| 接口 | 语义 | 核心默认 | 桌面实现（注册点：`DesktopHost.registerAll()`） |
| --- | --- | --- | --- |
| `KeyValueStore` + `HostStores.named(name)` | 键值存储（Preferences 的 9 方法子集） | `FileKeyValueStore`（`<数据目录>/kv/*.properties`） | `PrefsKeyValueStore`（节点名与拆分前一致：`<前缀>/misc`、`<前缀>/settings`、`<前缀>/xunlei_fp`、`<前缀>/announcement`，**用户数据零迁移**。前缀＝`YUNX_PREFS_NODE` env，默认 `yunx-wails` ⇒ 本移植与原版 `yunx` 分家、可双开；另 `YUNX_PREFS_USER_ROOT` env 可整体搬移 prefs 根目录，供 native 冒烟隔离） |
| `MachineProtector` | DPAPI 包裹/解包 + MachineGuid | 无（普通/严格档抛错，宽松档正常） | `JnaMachineProtector`（JNA；同时登记 ServiceLoader，单元测试无注册也能找到） |
| `RegistryReader` | HKCU 注册表读（系统代理检测） | 全 null | `JnaRegistryReader` |
| `HeadlessWebEngine` | 隐藏浏览器加载+JS 提取 | 返回 null | `JcefHeadlessWebEngine`（原 HeadlessWebView 全量逻辑） |
| `WebCookieCleaner` | 按域名清浏览器 Cookie | no-op | `JcefCookieCleaner`（原 util/CookieCleaner 逻辑） |
| `EngineInstallPrompter` | ffmpeg 缺失时征询安装 | 返回 false | `EngineInstallPrompt`（Compose 弹窗对象直接实现接口） |
| `DownloadNotifier` | 下载进度通知 | no-op | 适配 `WindowsToastNotifier` |
| `KeepAwake` | 下载期防睡眠 | no-op | 适配 `WindowsKeepAwake` |
| `HostDirs` | 默认下载目录等宿主目录约定 | `~/Downloads` | 适配 `DesktopActions.defaultDownloadDir` |

核心侧保留了两个**门面对象**使既有调用点零改动：
`util/CookieCleaner`（转发到 `HostServices.cookieCleaner`）、
`data/network/video/HeadlessWebView`（转发到 `HostServices.webEngine`）。

### 4.3 其它结构性改动

- `FfmpegLocator`（core，`data/preview/`）：从 FfplayManager 拆出的纯定位逻辑
  （私有目录 → PATH → 常见目录），`DownloadManager`/`FfmpegMuxer` 直接用它；
  FfplayManager 的播放/下载/窗口部分留在桌面并委托它。
- `AppContext` 的三个 prefs 字段类型由 `Preferences` 改为 `KeyValueStore`（方法签名一一对应，
  调用点几乎原样）；目录解析顺序与迁移/清理逻辑未动。
- `APP_VERSION`/`APP_IS_PREVIEW` 的生成任务从 `:desktop` 移到 `:core`，常量改为 public
  （internal 是模块作用域，跨模块读不到）；数据源仍是根目录 `version.txt`。
- **friend 模块过渡**：core 里有若干 `internal` 声明被桌面代码使用（约 30 处）。
  没有把它们一律放开为 public，而是让 `:desktop` 以 Kotlin 官方 friend 机制
  （`-Xfriend-paths`，配置在 `desktop/build.gradle.kts` 末尾并有注释）编译。
  将来核心 API 面收敛时，应把真正对外的声明逐个改 public 并移除该配置。

## 5. `:dll` 模块与调用协议

### 5.1 结构

| 文件 | 作用 |
| --- | --- |
| `dll/src/main/java/com/yunx/dll/NativeExports.java` | C 导出层：`yunx_ping`（恒 42）、`yunx_call(thread, reqJson, outBuf, outLen)`（响应写回调用方缓冲区，不足返回 -1） |
| `dll/src/main/kotlin/com/yunx/dll/DllFacade.kt` | JSON 命令分发（请求 `{"cmd":...}` → 响应 `{"ok":bool,...}`，异常统一转 `{"ok":false,"error":...}`） |
| `dll/src/main/kotlin/com/yunx/dll/DllSmokeMain.kt` | JVM 冒烟入口：与 DLL 同一门面，先在 JVM 上验逻辑（也供 native-image-agent 收集元数据） |
| `dll/go-loader/` | Go 冒烟加载器（cgo，分 OS 的链接参数），期望输出见 `dll/windows-dll-kit/README.md` |

### 5.2 现有命令（全部调真实核心逻辑、离线可验）

| cmd | 参数 | 返回 | 核心落点 |
| --- | --- | --- | --- |
| `version` | — | `version`, `preview` | `APP_VERSION` |
| `parse_share` | `text` | `found`, `platform?`, `shareId?`, `pwd?` | `ShareLinkParser.parse` |
| `kv_put` / `kv_get` | `key`, `value` | `stored` / `value` | `HostStores.named("dll")` |
| `key_roundtrip` | `dir?` | `consistent`, `keyBytes`, `wrapped` | `DbKeyStore`（宽松档） |

### 5.3 扩展新命令的标准步骤（界面/产品化阶段反复用）

1. 在 `DllFacade.handle` 加分支，先在 JVM 冒烟（`DllSmokeMain` 加一条用例）跑通；
2. 若命令需要宿主能力（DPAPI、WebView2、通知等），**先在 `HostServices` 加接口+默认实现**，
   不要在 core 里直连平台 API；DLL 侧的宿主实现走「回调表」由 Go 壳提供（下一步设计，
   协议预留在 `docs/WAILS-WEBVIEW2-CONTRACT.md` 第 2 节）；
3. 异步/长任务命令（解析、下载）不要阻塞 `yunx_call`：约定为「提交返回 taskId +
   事件/轮询取进度」，请求与文件字节不跨界（路径传字符串，内容留各自一侧）；
4. 重新 native-image 构建；若出现反射/资源缺失，先用 JVM + `native-image-agent`
   跑冒烟收集元数据再构建，不要手写猜测配置；
5. 用 `dll/go-loader` 或 `cmd/yunx-call` 复验。

### 5.4 命令面增补（截至 2026-11-07，权威清单以 DllFacade.kt 的 when 分支为准）

上表是 M1 基线；此后命令面已扩至 70+，按族归纳：

| 族 | 命令 | 说明 |
| --- | --- | --- |
| 设置 | `settings_list` / `settings_set` | 全键读写；调优类即时联动（HttpClients/DownloadTuning/DiagnosticLog/downloadDebug 日志开关） |
| 账号 | `accounts_list`、`account_set_primary/remark/remove`、`account_login_cookie/password` | 10 平台统一 |
| 解析/网盘 | `share_resolve/list/download/link`、`share_transfer(_batch)`、`share_folder_download`、`batch_list` | 批量转存整批空间预算+skipSpaceCheck；文件夹递归下载带 C1 凭证轮换 |
| 云盘 | `cloud_list/mkdir/rename/move/delete/quota/download/share` | 10 平台 |
| 下载 | `download_submit/list/pause/resume/remove(+_all)/redownload/get_link`、`download_debug_info/export/preempt` | 调试页三命令（设置开 downloadDebug 后行菜单入口） |
| 视频 | `video_parse(_quality)`、`video_download`、`video_accounts_list/login_cookie` | DASH 合轨/三模式 |
| 历史/书签 | `link_history_*`、`bookmark_*` | 解析历史（share_resolve 自动记录） |
| 公告/更新 | `announcements_list`（缓存+后台刷新，绝不同步走网络）、`announcement_*`、`update_check`、`github_*`、`misc xunlei_fp` | |
| 储存/安全 | `auth_export/import`、`storage_migrate_db`、`storage_apply_encryption`、`key_roundtrip` | 加密类命令在真实目录上属最高危（密钥重包裹），只在 /tmp 隔离演练 |

**Go 侧服务**（bindings 直调，不走 JSON 命令面）：`NormalDownload_*`（普通下载 10 方法）、`Aria2Status/ApplyOptions`、`LegacyImportStatus/Decide`（首启导入，Go 侧「导入决定闸门」保证核心惰性初始化发生在决定之后）、`HostStatus/SetProxy/SetFfmpeg`、`OpenFile/OpenDirectory/Shutdown`、`PickDirectory`（无）。
普通下载引擎架构（aria2-next + 内置 FTP + 独立 downloads.db）见 README「普通下载引擎」一节与 `downloadd/` 包头注。

## 6. Go 侧（仓库根）

- `yunxcore/yunxcore.go`：DLL 的进程级封装（单例）。**两条硬约束（都有实测证据）**：
  1. **isolate 终生制**：只在进程内创建一个，退出即回收；禁止 `graal_tear_down_isolate`
     （实测：一次等满 60 秒、一次在 teardown hooks 致命崩溃）。
  2. **isolate 绑 OS 线程**：创建 isolate 的 OS 线程之外调用会触发 VM 按原线程栈边界
     误判栈溢出——实测前 4 次调用正常、第 5 次致命崩溃（崩溃栈 `IsolateEnterStub`
     的 yellow-zone 检查），根因是 Go 协程跨 OS 线程迁移。封装已用
     「`runtime.LockOSThread` 专用协程 + channel 串行化」解决，**界面方不要绕过它直调核心库（现经 purego dlopen，约束不变）**。
- `service.go`：`CoreService`（`Ping` / `Call` / `Version` / `ParseShare`），Wails 侧
  `application.NewService` 注册；界面方用 `wails3 generate bindings` 生成前端绑定调用。
  核心为**惰性初始化**（`ensureCore` + `sync.Once`）：`NewCoreService` 不再立即 `yunxcore.Open()`，
  首个用到核心的调用才加载并执行宿主装配——给「首启先问原版数据导入」留窗口（见 `legacyimport.go`）。
- `legacyimport.go`：原版数据导入（纯 Go，不碰核心）：`LegacyImportStatus` 探测 / `LegacyImportDecide`
  执行或跳过；源 `~/.yunx-desktop`（**只读**），目标为当前数据目录。
- `main.go`：Wails 3 启动骨架（窗口参数可改）。数据目录独立：启动最早处把
  `YUNX_DESKTOP_DATA_DIR` 默认为 `~/.yunx-wails`（原版用 `~/.yunx-desktop`，⇒ 可双开）；
  单实例锁 `instance.lock` 同在数据目录（原版锁文件名不同且分家目录，互不干扰）。
  构建要求 **Go 1.26+ 与 Wails v3**
  （本沙箱 Go 1.23，未在此构建主程序；`yunxcore` + `cmd/yunx-call` 已实测调通）。
- DLL 放置：`yunxcore/` 经 purego dlopen 运行时查找（$YUNX_CORE_LIB > exe 同目录 > exe ../dll/build/native > ./dll/build/native > 系统路径）。

## 7. 构建手册（已验证的命令）

### 7.1 JVM 侧（Linux 沙箱实测；Windows 同理）

```bash
# 需要：JDK 17（toolchain）+ Gradle 8.13（仓库无 gradlew 脚本，用本机 gradle 或 wrapper jar）
gradle :core:compileKotlin :desktop:compileKotlin \
       :dll:compileKotlin :dll:compileJava -PgraalvmSdkJar=<sdk-api.jar 路径>
gradle :dll:printRuntimeClasspath -PgraalvmSdkJar=<...>   # 打印 native-image 用的 classpath
```

- `sdk-api.jar`：`@CEntryPoint` 的编译期 API。GraalVM 23+ 自带于 `lib/graalvm/sdk-api.jar`；
  21 发行包不带时按 `dll/windows-dll-kit/README.md` 从 jimage 提取。**只能进编译 classpath，
  绝不能进 native-image 的 classpath**（会 ForceOnModulePath 报错）。
- 沙箱特有（普通机器忽略）：Gradle daemon 回环连接需 `-Djava.net.preferIPv4Stack=true`
  （GRADLE_OPTS 与 daemon jvmargs 都要），内存紧张时在用户级 gradle.properties 压
  `org.gradle.jvmargs=-Xmx1400m`（仓库默认 -Xmx4g）。

### 7.2 动态库（Linux 实测命令；Windows 见工具包）

```bash
export JAVA_HOME=<GraalVM 21>
CP="dll/build/classes/java/main:dll/build/classes/kotlin/main:$(gradle -q :dll:printRuntimeClasspath | grep RUNTIME_CP= | cut -d= -f2-)"
native-image -J-Xmx1800m -cp "$CP" --shared -H:Name=libyunxcore -H:Path=dll/build/native \
             --no-fallback --enable-all-security-services
# 产物：libyunxcore.so（22.8MB）+ 头文件；Windows 为 yunxcore.dll + .lib
cd dll/go-loader && go build -o loader . && ./loader     # 冒烟（期望输出见工具包 README）
```

### 7.3 Windows 一键

见 `dll/windows-dll-kit/build-dll.ps1`（前置检查 → Gradle → native-image（vcvars 包裹）
→ dumpbin 验导出 → Go 冒烟）与同目录 README。

## 8. 验证证据（2026-11-05，Linux 沙箱）

- 编译：`:core:compileKotlin`、`:desktop:compileKotlin`、`:dll` 均 BUILD SUCCESSFUL
  （拆分过程中出现过的 39 个编译错误已全部归零：core 6 个、desktop 33 个）。
- 切片基线（同款依赖的最小集，先行验证）：JVM 峰值 RSS 172–174MB vs 原生 69.8MB；
  isolate 初始化 3–63ms；SQLite/加密单项原生持平或更快。
- 本批原生库：构建 2 分 50 秒，`libyunxcore.so` 22.8MB；Go 加载器 8 条命令全对、exit 0：
  version=1.3.3、夸克解析（shareId+提取码）、百度解析、无链接 found=false、
  kv 往返、key_roundtrip consistent=true（32 字节）、未知命令返回 ok=false。
- `cmd/yunx-call`：经正式 `yunxcore` 封装复验同一组命令，全对、exit 0。

## 9. 已知限制与待办（按优先级）

1. **Windows 真机 .dll 未构建**：用 `dll/windows-dll-kit` 跑通并按其 README 的基线核对输出（唯一剩余的「未验」项）。
2. **DLL 内 DPAPI 未接**：DLL 里 `MachineProtector` 未注册，DbKeyStore 普通/严格档不可用（宽松档正常）。
   方案二选一：Go 宿主经回调表提供 DPAPI；或 DLL 在 Windows 上直调 Crypt32（需 native-image 的 WinAPI 绑定工作）。
3. **网络命令未进 DLL**：解析（各平台 Api）、下载（DownloadManager）尚在桌面进程内使用；
   按 §5.3 扩展时先处理：OkHttp 在 DLL 内的代理/CA（宿主注入）、HeadlessWebEngine 的 WebView2 实现、
   下载进度事件通道。注意沙箱 IPv6 RST 怪癖只是本环境问题，真机不存在（见契约与工具包说明）。
4. **登录仍为 JCEF**（桌面形态内）：Wails 形态按 `docs/WAILS-WEBVIEW2-CONTRACT.md` 实现，
   三个不变量：登录/隐藏提取/Cookie 清理共用同一 WebView2 profile；ExecuteScript 返回值要 JSON 解码；
   requiredKeys 判定逻辑与 `JcefLoginPane.collectCookies` 保持一致。
5. **friend-paths 是过渡手段**：核心对外 API 收敛后逐步改 public 并移除（见 §4.3）。
6. **仓库原有测试**未在本沙箱跑（`gradle :desktop:test` 需 Windows 环境与 JNA/DPAPI 实机）；
   接收方请在真机跑一遍 `testInProcess` 任务确认拆分无行为回归（尤其 DbKeyStoreTest、
   SecureAccountDaosTest——它们依赖 ServiceLoader 找到 `JnaMachineProtector`）。

## 10. 关键文件索引

| 主题 | 路径 |
| --- | --- |
| 目标文档 | `GOAL.md` |
| 宿主接口总表 | `core/src/main/kotlin/com/yunx/app/data/host/HostServices.kt` |
| 桌面注册点 | `desktop/src/main/kotlin/com/yunx/app/data/host/DesktopHost.kt` |
| DLL 门面/协议 | `dll/src/main/kotlin/com/yunx/dll/DllFacade.kt`、`dll/src/main/java/com/yunx/dll/NativeExports.java` |
| Go 封装 | `yunxcore/yunxcore.go` |
| Wails 服务 | `service.go`、`main.go` |
| WebView2 契约 | `docs/WAILS-WEBVIEW2-CONTRACT.md` |
| Windows 工具包 | `dll/windows-dll-kit/build-dll.ps1`、`dll/windows-dll-kit/README.md` |
| 切片验证全文 | （交付方工作区）`~/workspace/yunx-dll-slice/results/SUMMARY.md` |
