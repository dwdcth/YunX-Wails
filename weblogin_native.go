//go:build linux

package main

// 网页登录（Linux/WebKitGTK）：用 wails 自己的窗口系统开登录窗（保证 GTK plumbing 正确），
// 然后从视图的 NetworkSession → CookieManager 全量轮询 Cookie（含 HttpOnly）。
// 命中必需键后走 account_login_cookie 落库。
//
// 关键发现：自建 GTK 窗口（g_object_new + gtk_window_new）在 wails 环境下白屏
// （WebProcess GLib NULL pointer，缺 GTK Application 关联）；wails 窗口正常。
// 所以改为：wails Window.NewWithOptions 开窗 → C 层 get_network_session(view)
//   → get_cookie_manager(session) → get_cookies(cm, uri) 异步回调 → Go 轮询。

/*
#cgo pkg-config: webkitgtk-6.0

#include <webkit/webkit.h>

// 从 Go 传入 wails webview 的原始指针，拿默认 CookieManager
// （wails 视图未设自定义 network-session → 用默认的 → cookie manager 可从视图反查）
static WebKitCookieManager *g_cm = NULL;
static WebKitWebView *g_diag_view = NULL; // 登录窗口的 webview（inspector / JS 执行用）

static char g_cookieBuf[65536];
static volatile int g_cookieDone = 0;

const char *weblogin_cookie_buf(void) { return g_cookieBuf; }
int weblogin_cookie_done(void) { return g_cookieDone; }
void weblogin_cookie_reset(void) { g_cookieDone = 0; }

static void on_cookies(GObject *obj, GAsyncResult *res, gpointer user_data) {
	GList *cookies = webkit_cookie_manager_get_cookies_finish(WEBKIT_COOKIE_MANAGER(obj), res, NULL);
	g_cookieBuf[0] = 0;
	for (GList *l = cookies; l != NULL; l = l->next) {
		SoupCookie *c = (SoupCookie *)l->data;
		if (c == NULL) continue;
		const char *n = soup_cookie_get_name(c);
		const char *v = soup_cookie_get_value(c);
		if (n == NULL) continue;
		if (g_cookieBuf[0] != 0) g_strlcat(g_cookieBuf, "; ", sizeof(g_cookieBuf));
		g_strlcat(g_cookieBuf, n, sizeof(g_cookieBuf));
		if (v != NULL && v[0] != 0) {
			g_strlcat(g_cookieBuf, "=", sizeof(g_cookieBuf));
			g_strlcat(g_cookieBuf, v, sizeof(g_cookieBuf));
		}
	}
	g_list_free(cookies);
	g_cookieDone = 1;
}

typedef struct { char *uri; } PollArgs;

static gboolean idle_poll(gpointer data) {
	PollArgs *p = (PollArgs *)data;
	if (g_cm != NULL) {
		webkit_cookie_manager_get_cookies(g_cm, p->uri, NULL, on_cookies, NULL);
	}
	g_free(p->uri); g_free(p);
	return G_SOURCE_REMOVE;
}

// 从 GtkWindow 遍历子控件找到 WebKitWebView（wails 布局：GtkWindow > GtkBox > WebKitWebView）
static WebKitWebView *find_webview(GtkWindow *window) {
	if (window == NULL) return NULL;
	GtkWidget *child = gtk_window_get_child(window);
	if (child == NULL) return NULL;
	if (GTK_IS_BOX(child)) {
		for (GtkWidget *w2 = gtk_widget_get_first_child(child); w2 != NULL; w2 = gtk_widget_get_next_sibling(w2)) {
			if (WEBKIT_IS_WEB_VIEW(w2)) return WEBKIT_WEB_VIEW(w2);
		}
	}
	if (WEBKIT_IS_WEB_VIEW(child)) return WEBKIT_WEB_VIEW(child);
	return NULL;
}

// 初始化：从 wails 窗口的 GtkWindow 指针拿到 cookie manager（须在主循环执行）
// ★ args 由 C.malloc 分配（不能是 Go 栈变量——异步回调时栈可能已回收！）
typedef struct { void *window; int *result; } InitArgs;
// Safari UA（WebKitGTK 本身就是 WebKit 引擎，Safari UA 与引擎行为一致，不会被反爬识破；
// Chrome UA 伪装会被 window.chrome/userAgentData 特性检测识破 → 百度/夸克中断登录组件渲染）
static const char *LOGIN_SAFARI_UA =
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 "
	"(KHTML, like Gecko) Version/17.4 Safari/605.1.15";

static gboolean idle_init(gpointer data) {
	InitArgs *a = (InitArgs *)data;
	if (a == NULL || a->window == NULL) { *a->result = -1; g_free(a); return G_SOURCE_REMOVE; }
	GtkWindow *window = (GtkWindow *)a->window;
	WebKitWebView *view = find_webview(window);
	if (view == NULL) { *a->result = -2; g_free(a); return G_SOURCE_REMOVE; }
	// 设 Safari UA + 开发者工具 + 关 ITP + 放开 Cookie 策略
	WebKitSettings *settings = webkit_web_view_get_settings(view);
	webkit_settings_set_user_agent(settings, LOGIN_SAFARI_UA);
	webkit_settings_set_enable_developer_extras(settings, TRUE);       // 右键 → Inspect Element
	webkit_settings_set_enable_javascript(settings, TRUE);
	webkit_settings_set_javascript_can_access_clipboard(settings, TRUE);
	webkit_web_view_set_settings(view, settings);
	WebKitNetworkSession *session = webkit_web_view_get_network_session(view);
	if (session == NULL) { *a->result = -3; g_free(a); return G_SOURCE_REMOVE; }
	// ★ 关 ITP + Cookie 全收 + 保存视图（百度登录依赖 passport.baidu.com 跨站 Cookie）
	webkit_network_session_set_itp_enabled(session, FALSE);
	WebKitCookieManager *cm = webkit_network_session_get_cookie_manager(session);
	webkit_cookie_manager_set_accept_policy(cm, WEBKIT_COOKIE_POLICY_ACCEPT_ALWAYS);
	g_cm = cm;
	g_diag_view = view; // 保存给 Inspector / JS 执行用
	*a->result = (g_cm != NULL) ? 1 : -4;
	g_free(a);
	return G_SOURCE_REMOVE;
}

// 诊断 JS 结果缓冲
static char g_diagBuf[32768];
static volatile int g_diagDone = 0;
const char *weblogin_diag_buf(void) { return g_diagBuf; }
int weblogin_diag_done(void) { return g_diagDone; }
void weblogin_diag_reset(void) { g_diagDone = 0; }

static void on_diag_js(GObject *obj, GAsyncResult *res, gpointer user_data) {
	JSCValue *value = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(obj), res, NULL);
	g_diagBuf[0] = 0;
	if (value != NULL) {
		char *str = jsc_value_to_string(value);
		if (str != NULL) {
			g_strlcpy(g_diagBuf, str, sizeof(g_diagBuf));
			g_free(str);
		}
		g_object_unref(value);
	}
	g_diagDone = 1;
}

typedef struct { char *js; } DiagArgs;
static gboolean idle_diag(gpointer data) {
	DiagArgs *d = (DiagArgs *)data;
	if (g_diag_view != NULL) {
		webkit_web_view_evaluate_javascript(g_diag_view, d->js, -1, NULL, NULL, NULL, on_diag_js, NULL);
	}
	g_free(d->js); g_free(d);
	return G_SOURCE_REMOVE;
}

static void nativeRunJS(const char *js) {
	DiagArgs *d = g_new0(DiagArgs, 1);
	d->js = g_strdup(js);
	g_main_context_invoke(NULL, idle_diag, d);
}

// 编程式打开 Inspector（不依赖右键菜单，wails 可能吞掉 context menu）
static gboolean idle_inspector(gpointer data) {
	(void)data;
	if (g_diag_view != NULL) {
		WebKitWebInspector *insp = webkit_web_view_get_inspector(g_diag_view);
		webkit_web_inspector_show(insp);
	}
	return G_SOURCE_REMOVE;
}
static void nativeShowInspector(void) {
	g_main_context_invoke(NULL, idle_inspector, NULL);
}

static void nativeInit(void *gtk_window_ptr, int *result_ptr) {
	InitArgs *a = g_new0(InitArgs, 1);
	a->window = gtk_window_ptr;
	a->result = result_ptr;
	*result_ptr = 0; // pending
	g_main_context_invoke(NULL, idle_init, a);
}
static void nativePoll(const char *uri) {
	PollArgs *p = g_new0(PollArgs, 1);
	p->uri = g_strdup(uri);
	g_main_context_invoke(NULL, idle_poll, p);
}
*/
import "C"

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type webLoginSpec struct {
	URL      string
	Domains  []string
	Required []string   // 必需键（全部命中才算登录成功）
	AnyOf    []string   // 备选键（任一命中即算登录成功，对齐原版 JcefLoginPane anyOfKeys）
	JSCIssue bool       // WebKitGTK JavaScriptCore 兼容性问题（白屏），建议走粘贴 Cookie
}

var webLogins = map[string]webLoginSpec{
	"quark":  {URL: "https://pan.quark.cn/?fr=pc&platform=pc", Domains: []string{".quark.cn"}, Required: []string{"__pus", "__puus"}, JSCIssue: true},
	"uc":     {URL: "https://drive.uc.cn/", Domains: []string{".uc.cn"}, Required: []string{"__pus", "__puus"}},
	"baidu":  {URL: "https://pan.baidu.com/", Domains: []string{".baidu.com"}, Required: []string{"BDUSS"}},
	"c139":   {
		URL:      "https://yun.139.com/m/#/login",
		Domains:  []string{"mail.10086.cn", "yun.139.com", ".10086.cn"},
		Required: []string{"Os_SSo_Sid", "RMKEY"},  // 旧版登录流程
		AnyOf:    []string{"authorization"},          // 新版和彩云流程（实测命中）
	},
	"pan115": {URL: "https://115.com/", Domains: []string{"115.com"}, Required: []string{"UID", "SEID"}},
}

func domainURIs(domains []string) []string {
	var uris []string
	for _, d := range domains {
		d = strings.TrimPrefix(d, ".")
		uris = append(uris, "https://"+d+"/")
	}
	return uris
}

func pollCookieURIs(uris []string) string {
	var parts []string
	for _, uri := range uris {
		cUri := C.CString(uri)
		C.nativePoll(cUri)
		C.free(unsafe.Pointer(cUri))
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if C.weblogin_cookie_done() != 0 {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		if C.weblogin_cookie_done() == 0 {
			continue
		}
		buf := C.GoString(C.weblogin_cookie_buf())
		C.weblogin_cookie_reset()
		if buf != "" {
			parts = append(parts, buf)
		}
	}
	return strings.Join(parts, "; ")
}

var webLoginMu sync.Mutex

// WebLogin 用 wails 窗口系统打开平台登录页，后台轮询 Cookie Manager（CoreService 方法）。
// 返回 {"ok":true,"opened":true}；结果经 wails 事件 weblogin:done 通知。
func (s *CoreService) WebLogin(platform string) (string, error) {
	spec, ok := webLogins[platform]
	if !ok {
		return `{"ok":false,"error":"该平台暂不支持网页登录"}`, nil
	}
	// WebKitGTK JSC 兼容性问题：提示走粘贴 Cookie（不阻止尝试，但提示用户）
	if spec.JSCIssue {
		wailsApp.Event.Emit("weblogin:done", map[string]any{
			"platform": platform, "ok": false,
			"error":    "夸克网页在 WebKitGTK 下可能白屏（JS 引擎差异），建议用「粘贴凭据」通道登录",
		})
	}
	if wailsApp == nil {
		return "", fmt.Errorf("应用尚未就绪")
	}
	if !webLoginMu.TryLock() {
		return `{"ok":false,"error":"已有登录窗口进行中"}`, nil
	}
	defer webLoginMu.Unlock()

	// ★ 先开空白窗口，设好 UA/ITP/Cookie 策略后再导航到真实 URL
	//（否则页面在默认设置下就开始加载 JS——百度/夸克检测到非 Chrome 引擎直接中断登录组件）
	win := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "登录 " + platform,
		Width:  1100,
		Height: 820,
		URL:    "about:blank",
	})

	// 从 wails 窗口的 GtkWindow 指针 → 遍历子控件找到 WebKitWebView → cookie manager
	// 窗口创建是异步的（GTK 需要映射），等 NativeWindow() 可用
	var gtkWinPtr unsafe.Pointer
	for i := 0; i < 40; i++ {
		gtkWinPtr = win.NativeWindow()
		if gtkWinPtr != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if gtkWinPtr == nil {
		win.Close()
		return `{"ok":false,"error":"窗口原生指针不可用（创建超时）"}`, nil
	}

	// ★ result 也必须在 C 堆上（Go 栈变量在异步回调时可能已回收）
	resultPtr := (*C.int)(C.malloc(C.sizeof_int))
	defer C.free(unsafe.Pointer(resultPtr))
	C.nativeInit(gtkWinPtr, resultPtr)
	for i := 0; i < 100 && *resultPtr == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if *resultPtr != 1 {
		win.Close()
		return fmt.Sprintf(`{"ok":false,"error":"Cookie Manager 初始化失败（code=%d）"}`, int(*resultPtr)), nil
	}
	fmt.Printf("网页登录[%s]：Cookie Manager 已初始化（UA/ITP/Cookie 已设置）\n", platform)

	// ★ 现在所有设置已生效，导航到真实登录 URL
	win.SetURL(spec.URL)
	fmt.Printf("网页登录[%s]：已导航到 %s\n", platform, spec.URL)

	// 打开 Inspector（独立调试窗口：Console + Network）
	C.nativeShowInspector()
	fmt.Println("网页登录：Inspector 已打开（如果没弹出来，等 2 秒页面加载后再试）")

	// 等 3 秒页面加载，然后跑诊断 JS
	go func() {
		time.Sleep(3 * time.Second)
		diagJS := C.CString(`JSON.stringify({
			title: document.title,
			ready: document.readyState,
			body: document.body ? document.body.children.length : -1,
			scripts: document.scripts ? document.scripts.length : -1,
			ls: (function(){ try { localStorage.setItem('t','1'); return localStorage.getItem('t') } catch(e) { return 'ERR:'+e.message } })(),
			sw: 'serviceWorker' in navigator,
			ua: typeof window.chrome,
			uaData: typeof navigator.userAgentData,
			pwd: !!document.querySelector('input[type=password]'),
			qr: !!document.querySelector('canvas, img[src*="qr"], [class*="qr"], [id*="qr"]'),
			fetch: 'fetch' in window
		})`)
		defer C.free(unsafe.Pointer(diagJS))
		C.nativeRunJS(diagJS)
		for i := 0; i < 30 && C.weblogin_diag_done() == 0; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if C.weblogin_diag_done() != 0 {
			fmt.Printf("=== 诊断 JS 结果 ===\n%s\n=== 诊断结束 ===\n", C.GoString(C.weblogin_diag_buf()))
		} else {
			fmt.Println("诊断 JS 超时（页面可能还没加载完）")
		}
	}()

	uris := domainURIs(spec.Domains)
	go func() {
		defer win.Close()
		deadline := time.Now().Add(5 * time.Minute)
		var lastNote time.Time
		for time.Now().Before(deadline) {
			time.Sleep(1200 * time.Millisecond)
			cookieStr := pollCookieURIs(uris)
			if cookieStr == "" {
				continue
			}
			names := map[string]bool{}
			for _, kv := range strings.Split(cookieStr, "; ") {
				names[strings.SplitN(kv, "=", 2)[0]] = true
			}
			if time.Since(lastNote) > 15*time.Second {
				lastNote = time.Now()
				fmt.Printf("网页登录[%s]：当前 cookie 键 %d 个\n", platform, len(names))
				// 首次或键数变化时打印实际 Cookie 名（诊断必需键匹配）
				var nameList []string
				for n := range names {
					nameList = append(nameList, n)
				}
				fmt.Printf("网页登录[%s]：Cookie 名单: %v\n", platform, nameList)
			}
			// 命中判定（对齐原版 JcefLoginPane）：Required 全部命中，或 AnyOf 任一命中
			hit := true
			for _, k := range spec.Required {
				if !names[k] {
					hit = false
					break
				}
			}
			if !hit && len(spec.AnyOf) > 0 {
				for _, k := range spec.AnyOf {
					if names[k] {
						hit = true
						break
					}
				}
			}
			if !hit {
				continue
			}
			resp, err := s.core.Call(fmt.Sprintf(`{"cmd":"account_login_cookie","platform":%q,"credential":%q}`, platform, cookieStr))
			success := err == nil && strings.Contains(resp, `"loggedIn":true`)
			if success {
				wailsApp.Event.Emit("weblogin:done", map[string]any{"platform": platform, "ok": true})
				fmt.Printf("网页登录[%s]：保存成功\n", platform)
			} else {
				msg := resp
				if err != nil {
					msg = err.Error()
				}
				wailsApp.Event.Emit("weblogin:done", map[string]any{"platform": platform, "ok": false, "error": msg})
				fmt.Printf("网页登录[%s]：校验失败 %s\n", platform, msg)
			}
			return
		}
		wailsApp.Event.Emit("weblogin:done", map[string]any{"platform": platform, "ok": false, "error": "登录超时（5 分钟）"})
	}()

	return `{"ok":true,"opened":true}`, nil
}
