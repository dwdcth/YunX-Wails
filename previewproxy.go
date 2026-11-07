// previewproxy.go：云盘/视频在线预览的本地流代理。
//
// 为什么需要：浏览器 <video> 发不了自定义请求头——夸克等平台 CDN 直链要带 Cookie/UA，
// B 站等要带 Referer（防盗链），直塞 URL 必然黑屏。本代理在 Go 侧用核心给的请求头拉流、
// 转发 Range（拖进度条），前端 <video src="http://127.0.0.1:<port>/p?k=..."> 即可播放，
// 同源无 CORS/CSP 问题。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"yunxwailsdemo/hostenv"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type previewProxy struct {
	mu    sync.Mutex
	entry map[string]*previewTarget // key → 目标（url+headers），短期缓存（直链 15-30 分钟有效）
	next  uint64
	srv   *http.Server
	base  string
}

type previewTarget struct {
	url     string
	headers map[string]string
	at      time.Time
}

var previewProxyOnce sync.Once
var previewProxyInst *previewProxy

// startPreviewProxy 单例启动（随机空闲端口），失败返回 ""（前端回退直链模式）。
func startPreviewProxy() string {
	previewProxyOnce.Do(func() {
		p := &previewProxy{entry: map[string]*previewTarget{}}
		mux := http.NewServeMux()
		mux.HandleFunc("/p", p.handle)
		for port := 37100; port <= 37130; port++ {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				continue
			}
			p.srv = &http.Server{Handler: mux}
			p.base = fmt.Sprintf("http://127.0.0.1:%d/p", port)
			go func() { _ = p.srv.Serve(ln) }()
			previewProxyInst = p
			fmt.Println("预览代理：监听 127.0.0.1:", port)
			return
		}
	})
	if previewProxyInst == nil {
		return ""
	}
	return previewProxyInst.base
}

// registerTarget 登记一个目标并返回可播放的代理 URL（k 参数）。
func (p *previewProxy) registerTarget(url string, headers map[string]string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	// 复用未过期同目标
	for k, t := range p.entry {
		if t.url == url && time.Since(t.at) < 10*time.Minute {
			return p.base + "?k=" + k
		}
	}
	p.next++
	k := fmt.Sprintf("%x", time.Now().UnixNano())[:10] + fmt.Sprintf("%x", p.next)
	p.entry[k] = &previewTarget{url: url, headers: headers, at: time.Now()}
	return p.base + "?k=" + k
}

// handle 流代理：透传 Range/状态码/Content-*，带上登记的请求头（剔除 hop-by-hop）。
func (p *previewProxy) handle(w http.ResponseWriter, r *http.Request) {
	k := r.URL.Query().Get("k")
	p.mu.Lock()
	t, ok := p.entry[k]
	p.mu.Unlock()
	if !ok || time.Since(t.at) > 30*time.Minute {
		http.Error(w, "预览链接已过期，请重新打开", http.StatusGone)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, t.url, nil)
	if err != nil {
		http.Error(w, "无效的源地址", http.StatusBadGateway)
		return
	}
	for hk, hv := range t.headers {
		if strings.EqualFold(hk, "Host") || strings.EqualFold(hk, "Accept-Encoding") {
			continue
		}
		req.Header.Set(hk, hv)
	}
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}
	req.Header.Set("User-Agent", t.headers["User-Agent"]) // 空则置空——由 http 层默认兜底
	client := &http.Client{Timeout: 0}                    // 流式播放不用整体超时（读由 ctx 控制）
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "源站请求失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// CloudPreviewURL 云盘文件预览代理地址：cloud_link 取直链+请求头 → 登记到代理 → 返回可播 URL。
func (s *CoreService) CloudPreviewURL(platform, fid, fname string, fsize int64, fidToken string) string {
	base := startPreviewProxy()
	req, err := json.Marshal(map[string]any{
		"cmd": "cloud_link", "platform": platform, "fid": fid, "fname": fname,
		"fsize": fsize, "fidToken": fidToken,
	})
	if err != nil {
		return errorJSON(err.Error())
	}
	raw, err := s.coreCall(string(req))
	if err != nil {
		return errorJSON("取直链失败: " + err.Error())
	}
	var r struct {
		OK      bool              `json:"ok"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Error   string            `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil || !r.OK || r.URL == "" {
		return errorJSON("取直链失败: " + raw)
	}
	if base == "" {
		// 代理起不来（端口全占）：退回直链（无头平台仍可播）
		return marshalMust(map[string]any{"ok": true, "url": r.URL, "proxied": false})
	}
	return marshalMust(map[string]any{
		"ok": true, "url": previewProxyInst.registerTarget(r.URL, r.Headers), "proxied": true,
	})
}

// PreviewProxyURL 通用预览代理（视频解析 CDN 防盗链用）：带 Referer/UA 拉流。
func (s *CoreService) PreviewProxyURL(url, referer string) string {
	base := startPreviewProxy()
	if url == "" {
		return errorJSON("url 为空")
	}
	if base == "" {
		return marshalMust(map[string]any{"ok": true, "url": url, "proxied": false})
	}
	headers := map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	}
	if referer != "" {
		headers["Referer"] = referer
	}
	return marshalMust(map[string]any{
		"ok": true, "url": previewProxyInst.registerTarget(url, headers), "proxied": true,
	})
}

// ---- ffplay 外部播放器预览（对齐原版 FfplayManager.buildCommand；独立窗口、自带关闭）----

// locateFfplay 找 ffplay：ffmpeg 同目录 > PATH。ffmpeg 路径沿用宿主探测（hostenv.FfmpegAuto）。
func locateFfplay() string {
	if ff := hostenv.FfmpegAuto(); ff != "" {
		if p := filepath.Join(filepath.Dir(ff), "ffplay"); fileExistsGo(p) {
			return p
		}
	}
	if p, err := exec.LookPath("ffplay"); err == nil {
		return p
	}
	return ""
}

func fileExistsGo(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ffplayArgs 组装命令行（对齐 FfplayManager.buildCommand：UA/Referer/Cookie 独立旗标，
// 其余头走 -headers，\r\n 分隔；ProcessBuilder 等价 = exec 不经 shell）。
func ffplayArgs(exe, url, title string, headers map[string]string) []string {
	get := func(key string) string {
		for k, v := range headers {
			if strings.EqualFold(k, key) {
				return v
			}
		}
		return ""
	}
	args := []string{exe, "-hide_banner", "-loglevel", "info", "-stats", "-autoexit",
		"-window_title", title}
	if ua := get("User-Agent"); ua != "" {
		args = append(args, "-user_agent", ua)
	}
	if r := get("Referer"); r != "" {
		args = append(args, "-referer", r)
	}
	if c := get("Cookie"); c != "" {
		args = append(args, "-cookies", c)
	}
	var rest []string
	for k, v := range headers {
		if strings.EqualFold(k, "User-Agent") || strings.EqualFold(k, "Referer") ||
			strings.EqualFold(k, "Cookie") || strings.EqualFold(k, "Range") {
			continue
		}
		rest = append(rest, k+": "+v)
	}
	if len(rest) > 0 {
		args = append(args, "-headers", strings.Join(rest, "\r\n"))
	}
	return append(args, url)
}

// CloudPreviewPlay 云盘文件用 ffplay 在线预览（原生窗口，自带关闭/-autoexit 播完自退）。
// 找不到 ffplay 返回 ok:false（前端回退页内代理播放）。
func (s *CoreService) CloudPreviewPlay(platform, fid, fname string, fsize int64, fidToken string) string {
	exe := locateFfplay()
	if exe == "" {
		return marshalMust(map[string]any{"ok": false, "error": "未找到 ffplay"})
	}
	// 走核心本地流中继：夸克等 CDN 做 TLS 指纹过滤（curl/ffmpeg/Go 全 403），
	// 只有核心的 Java TLS 栈能过——中继本地纯 HTTP 输出，ffplay 直接播，无需带请求头。
	req, _ := json.Marshal(map[string]any{
		"cmd": "cloud_stream_start", "platform": platform, "fid": fid, "fname": fname,
		"fsize": fsize, "fidToken": fidToken,
	})
	raw, err := s.coreCall(string(req))
	if err != nil {
		return errorJSON("取直链失败: " + err.Error())
	}
	var r struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil || !r.OK || r.URL == "" {
		return errorJSON("取直链失败: " + raw)
	}
	cmd := exec.Command(exe, "-hide_banner", "-loglevel", "info", "-stats", "-autoexit",
		"-window_title", fname, r.URL)
	if err := cmd.Start(); err != nil {
		return errorJSON("启动 ffplay 失败: " + err.Error())
	}
	go func() { _ = cmd.Wait() }() // 回收，不阻塞
	fmt.Println("预览：ffplay 已启动 pid=", cmd.Process.Pid, "file=", fname, "via relay")
	return marshalMust(map[string]any{"ok": true, "mode": "ffplay-relay"})
}

// PreviewPlayURL 视频解析直链用 ffplay 播（B 站等 Referer 防盗链平台）。
func (s *CoreService) PreviewPlayURL(url, referer, title string) string {
	exe := locateFfplay()
	if exe == "" {
		return marshalMust(map[string]any{"ok": false, "error": "未找到 ffplay"})
	}
	headers := map[string]string{}
	if referer != "" {
		headers["Referer"] = referer
	}
	args := ffplayArgs(exe, url, title, headers)
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		return errorJSON("启动 ffplay 失败: " + err.Error())
	}
	go func() { _ = cmd.Wait() }()
	return marshalMust(map[string]any{"ok": true, "mode": "ffplay"})
}
