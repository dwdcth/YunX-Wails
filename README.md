# YunX-Wails

**云析桌面版的 Go + Wails 3 跨平台移植** —— 原版 [YunX-Desktop](https://github.com/tidain/YunX-Desktop)（Kotlin + Compose Desktop，Windows）的 Linux/macOS/Windows 原生移植。

原版 Kotlin 核心业务逻辑（解析 / 网盘 / 下载 / 存储 / 安全）经 GraalVM Native Image 编译为 C 共享库
`libyunxcore.so`（Windows 为 `yunxcore.dll`），Go 宿主经 purego 运行时 dlopen 加载（无 CGo），界面用 Vue 3 + Wails 3 渲染。
核心代码保持与原版同源，便于跟随上游同步。

- 网盘：夸克 / UC / 迅雷 / 百度 / 139 / 123 / 115 / 光鸭 / 蓝奏云 / 蓝奏云优享版
- 视频：哔哩哔哩 / 小红书 / 抖音 / 微博 / 快手 / AcFun / Pixiv（DASH 自动合轨）
- 下载：双引擎三层分流——网盘直链走内置分片引擎（Range 并发 + SQLite 断点续传）；磁力 / BT / ed2k / Metalink / 普通 HTTP 走 aria2-next（JSON-RPC 子进程）；FTP/FTPS 走 Go 内置引擎（jlaffaye/ftp 断点续传）
- 数据目录独立：`~/.yunx-wails/`（与原版 `~/.yunx-desktop/` 分家、可双开）；
  首次启动询问「是否从原版导入数据」（`LegacyImport*` 服务方法，不自动导入）

## 目录结构

| 路径 | 内容 |
| --- | --- |
| `main.go` 等 Go 源 | Wails 3 应用壳：服务层、浏览器插件桥、WebKitGTK 网页登录、单实例锁 |
| `frontend/` | Vue 3 + Vite 前端（解析 / 网盘 / 下载 / 视频 / 设置）；`frontend/bindings/` 为生成的服务绑定 |
| `core/` | 从原版 desktop 抽出的纯 Kotlin 数据层（本项目拆分，无 UI 依赖） |
| `dll/` | C ABI 桥接模块（`DllFacade` 48+ 命令）+ `build-native.sh`（Linux 原生库构建） |
| `dll/windows-dll-kit/` | Windows 侧 `yunxcore.dll` 构建脚本（PowerShell） |
| `cmd/yunx-call/` | 无界面冒烟入口（CI / 无 GUI 环境用） |
| `upstream/YunX-Desktop/` | ★ git submodule：原版仓库，**只读对照**（功能对齐基准、上游同步源），不做任何修改 |

## 构建（Linux）

```bash
mise install                                   # 工具链：Gradle 8.13 / Temurin 17 / GraalVM 25 / Go / wails3
bash dll/build-native.sh                       # 编译 libyunxcore.so → dll/build/native/（约 40MB，-Os）
cd frontend && npm install && npm run build && cd ..   # 产出 frontend/dist（go:embed 依赖）
mise exec -- go build -trimpath -ldflags="-s -w" -o bin/yunx .
./bin/yunx
```

冒烟（不开 GUI）：`go run ./cmd/yunx-call` —— 期望 `PING 42`、`version 1.3.3`、parse_share 命中、
kv 往返、`key_roundtrip consistent=true`。

Windows：按 `dll/windows-dll-kit/README.md` 在真机编出 `yunxcore.dll` 放入 `dll/build/native/`
（核心库经 [purego](https://github.com/ebitengine/purego) 运行时 dlopen 加载，不需要导入库 `.lib`；
也可用 `YUNX_CORE_LIB` 显式指定库路径），其余步骤相同（网页登录 / 关机等平台能力已分文件适配）。

服务方法增改后：`mise exec -- wails3 generate bindings -b` 重新生成绑定再构建。

## 数据目录与原版数据导入

- **默认数据目录 `~/.yunx-wails/`**（原版 Compose 版用 `~/.yunx-desktop/`）：数据库 `yunx.db`、
  密钥 `credential.key`、`kv/`、`files/`（日志）都在这里；单实例锁 `instance.lock` 同目录
  ⇒ 与原版**可双开互不影响**。
- **首启导入**：首次启动检测到原版数据（`~/.yunx-desktop/yunx.db`）且本地尚无数据时，前端弹窗
  询问是否导入（`LegacyImportStatus` / `LegacyImportDecide`，绝不自动导入）。导入拷贝
  yunx.db(+wal/shm) / credential.key / kv/ / files/（单文件 >64MB 跳过）与原版设置
  （`~/.java/.userPrefs/yunx` → `yunx-wails` 节点）；跳过 `cache/`（分片可重下）、
  `diagnostic_logs/` 与锁文件。原版目录**只读**，导入后两边独立演化。
- **prefs 节点独立**：设置存储（java.util.prefs）默认走 `yunx-wails` 节点（原版用 `yunx`）。
- **隔离/调试环境变量**（两个应用形态共用同一核心，靠 env 分家）：
  - `YUNX_DESKTOP_DATA_DIR`：数据目录（Go 壳启动时若未设置则默认 `~/.yunx-wails`）；
  - `YUNX_PREFS_NODE`：prefs 节点前缀（默认 `yunx-wails`；设为 `yunx` 可与原版共享设置）；
  - `YUNX_PREFS_USER_ROOT`：非空时整体搬移 java.util.prefs 根目录（native 冒烟/测试隔离用）。

## 调用约定（实测结论，务必遵守）

1. **一个进程一个 isolate，终生持有**，退出即回收；不要调 `graal_tear_down_isolate`。
2. **isolate 绑定创建它的 OS 线程**：跨线程再进 isolate 会按原线程栈边界误判溢出致命崩溃。
   `yunxcore/` 已用「LockOSThread 专用协程 + channel 串行化」处理，服务层不要绕过它直接调 DLL。
3. 协议是 JSON 字符串进出（命令表见 `dll` 模块 `DllFacade`），文件字节不走这条通道。

## 已知平台差异（Linux）

- 百度 / 夸克网页登录受 WAF TLS 指纹拦截（GnuTLS JA3），改用粘贴 Cookie / 密码登录；
  139 / UC / 115 网页登录正常。
- 系统托盘常驻受 Wails v3.0.0 API 限制（Gopeed 已由 aria2-next 替代，不再列为缺口）。

## 普通下载引擎（aria2-next + 内置 FTP）

```
输入 → downloadd.Classify 分流
├─ ftp:// ftps://            → Go 内置引擎（2 并发、RetrFrom 续传、三重超时防挂死）
├─ magnet/ed2k/bc1/HTTP      → aria2-next addUri
├─ .torrent（本地/URL/base64:内联）→ aria2-next addTorrent
└─ .metalink/.meta4（v3 自动转 v4）→ aria2-next addMetalink
```

- aria2-next 进程惰性拉起（6900-6999 随机端口 + 随机 secret，`~/.local/share/yunx-desktop/tools/aria2-next`），崩溃自动重启；BT/DHT/Tracker/并发/限速设置经 `changeGlobalOption` 接通核心正式设置
- 任务存独立 SQLite `<数据目录>/downloads.db`（modernc 纯 Go），与 Java 侧 yunx.db 分家；下载页双库合并展示
- 二进制获取：[aria2-next Releases](https://github.com/AnInsomniacy/aria2-next/releases)（或设置页配置路径 kv `settings.aria2Path`）

## 上游同步

`upstream/YunX-Desktop` 固定在子模块指针指向的提交；原版更新后：

```bash
cd upstream/YunX-Desktop && git fetch && git checkout <新版本> && cd ..
git add upstream/YunX-Desktop        # 再把 core/ 中的对应改动同步过来
```

`core/` 是从原版 `desktop/` 抽出的数据层，接缝编辑清单见 [HANDOFF.md](HANDOFF.md)。

## License

AGPL-3.0（随原版）。本项目为其衍生作品，原版版权归上游作者所有。
