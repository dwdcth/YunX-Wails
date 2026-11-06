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
