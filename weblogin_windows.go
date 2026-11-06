//go:build windows

package main

// 网页登录 Windows 占位：Linux 走 WebKitGTK 的 CookieManager 轮询（weblogin_native.go），
// Windows 对应实现应基于 WebView2 的 CookieManager 同等轮询。
// TODO(Windows): WebView2 里程碑接入——从 wails 的 WebView2 环境取 ICoreWebView2_2 →
//   CookieManager，GetCookies 异步枚举、命中各平台必需键后走 account_login_cookie 落库，
//   交互流程（开窗 → UA/策略设置 → 轮询 → weblogin:done 事件）对齐 weblogin_native.go。

// WebLogin Windows 占位实现：明确引导走「粘贴凭据」通道
// （CoreService 方法签名须与 Linux 版一致，bindings/前端按同名方法调用）。
func (s *CoreService) WebLogin(platform string) (string, error) {
	return `{"ok":false,"error":"网页登录暂未在 Windows 接入，请用「粘贴凭据」通道登录"}`, nil
}
