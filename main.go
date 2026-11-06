// Wails 3 极简启动：只注册核心服务，不含界面实现（界面由界面方接管 frontend/ 与 bindings 生成）。
//
// 构建要求：Go 1.26+、Wails v3（见 go.mod）。Windows 上把 yunxcore.dll 放到与 exe 同目录。
package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"yunxwailsdemo/hostenv"
)

// 前端产物（Vue3 + Vite）：在 frontend/ 下 npm install && npm run build 产出 dist/ 后再 go build。
//
//go:embed frontend/dist
var assets embed.FS

func main() {
	// 数据目录独立（与 Compose 原版 ~/.yunx-desktop 分家）：env 未显式设置时默认 ~/.yunx-wails。
	// 必须先于任何核心动作（单实例锁 / isolate 初始化都会摸数据目录）；
	// 原版数据的首启导入由前端经 LegacyImport* 服务方法询问后执行（见 legacyimport.go）。
	setDefaultDataDir()

	// 单实例锁：双开会导致插件桥端口抢占与核心数据（KV/SQLite）并发写损坏
	acquireInstanceLock()

	// 首启导入闸门：当前「可导入原版数据」时布防——所有核心初始化（含看门狗等
	// Go 侧自主调用者）等用户在首启弹窗做完决定再放行，保证导入的数据被直接吃到。
	armImportGate()

	// WebKitGTK DMABUF 渲染器规避（白屏问题，Arch/NVIDIA/特定 Mesa 常见；
	// 必须在任何 WebKit 进程启动前设置，见 https://bugs.webkit.org/show_bug.cgi?id=259644）
	// 用户已设置时不覆盖（支持 WEBKIT_FORCE_DMABUF=1 强制启用）
	if os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" && os.Getenv("WEBKIT_FORCE_DMABUF") == "" {
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
		fmt.Println("WebKit: DMABUF 渲染器已禁用（防白屏）")
	}

	coreService, err := NewCoreService()
	if err != nil {
		log.Fatalf("core init: %v", err)
	}
	// 宿主装配（机器标识/代理/ffmpeg）已并入 ensureCore：首个核心调用时自动执行，
	// 不在启动时显式调用（保持惰性初始化，配合首启导入原版数据的门控）。

	app := application.New(application.Options{
		Name:        "YunX",
		Description: "YunX Desktop (Wails 3 + core DLL)",
		Services: []application.Service{
			application.NewService(coreService),
		},
		Assets: application.AssetOptions{
			// BundledAssetFileServer：静态资源之外还伺服 /wails/runtime.js
			// （bindings 用 -b 内置 runtime 时必需，AssetFileServerFS 不带会 404）
			Handler: application.BundledAssetFileServer(assets),
		},
	})
	wailsApp = app

	// 宿主能力（Linux）：下载期防睡眠 + 剪贴板分享链接检测（对齐原版 KeepAwake/ClipboardLinkPopup）
	hostenv.StartKeepAwakeWatcher(func(req string) (string, error) { return coreService.Call(req) })
	hostenv.StartClipboardWatcher(func(link string) {
		wailsApp.Event.Emit("clipboard:link", link)
	})
	// 浏览器插件桥（对齐原版 BrowserBridgeServer 协议，37021-37030 端口扫描）
	coreService.bridge = StartBrowserBridge(app, coreService)

	// 网页登录链路自检门：YUNX_WEBLOGIN_DEBUG=<platform> 时启动后自动开登录窗（验证 cookie 库轮询）
	if p := os.Getenv("YUNX_WEBLOGIN_DEBUG"); p != "" {
		go func() {
			time.Sleep(2 * time.Second)
			if _, err := coreService.WebLogin(p); err != nil {
				log.Printf("weblogin debug: %v", err)
			}
		}()
	}

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "YunX",
		Width:  1200,
		Height: 800,
		URL:    "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// setDefaultDataDir 数据目录默认值：进程启动最早处执行（先于单实例锁与任何核心初始化）。
// YUNX_DESKTOP_DATA_DIR 显式设置时不动（沿用调用方指定的目录，测试/沙箱用）。
func setDefaultDataDir() {
	if os.Getenv("YUNX_DESKTOP_DATA_DIR") != "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return // 拿不到用户目录时留给核心自身默认，不阻断启动
	}
	_ = os.Setenv("YUNX_DESKTOP_DATA_DIR", filepath.Join(home, ".yunx-wails"))
}

// acquireInstanceLock 单实例锁：instance.lock 记录当前进程 PID，启动时先查旧 PID 是否存活。
// 数据目录与核心 AppContext 同源：YUNX_DESKTOP_DATA_DIR 可覆盖，默认 ~/.yunx-wails
//（锁在独立目录里 ⇒ 与 Compose 原版可双开互不影响）。
// 锁进程退出后不清理（崩溃残留视为陈旧锁：PID 已死则直接接管），探测逻辑在 processAlive。
func acquireInstanceLock() {
	base := os.Getenv("YUNX_DESKTOP_DATA_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return // 拿不到用户目录时放弃锁定，不阻断启动
		}
		base = filepath.Join(home, ".yunx-wails")
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return
	}
	lockFile := filepath.Join(base, "instance.lock")
	if content, err := os.ReadFile(lockFile); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(content)))
		if pid > 0 && processAlive(pid) {
			fmt.Println("已有实例运行中 (PID", pid, ")，退出")
			os.Exit(0)
		}
	}
	_ = os.WriteFile(lockFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
}
