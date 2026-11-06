package main

// 浏览器插件桥（对齐 desktop 的 BrowserBridgeServer 协议，见 browser-extension/background.js）：
// - 本机 127.0.0.1:37021-37030 区间取第一个空闲端口监听（插件侧同区间扫描）
// - GET /ping → {"token":...}（校验 Origin 为浏览器扩展来源，防网页探测）
// - POST /hook（X-YunX-Token 校验）→ 把插件请求经 wails 事件 bridge:hook 交给前端弹窗，
//   挂起等待用户决定（最长 120s，超时按 return 归还浏览器），回 {action, saveDir?, threads?}
// - POST /bili、/video（X-YunX-Token 校验）→ 静默视频提交：不弹窗，直接调核心 video_download
//   取链入队（清晰度/方式已在插件侧定好或走平台默认），完成后回 {ok:true}

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type bridgeDecision struct {
	Action  string `json:"action"`
	SaveDir string `json:"saveDir,omitempty"`
	Threads int    `json:"threads,omitempty"`
}

type browserBridge struct {
	mu        sync.Mutex
	token     string
	pending   map[string]chan bridgeDecision
	seq       int
	app       *application.App
	core      *CoreService
	closer    chan struct{}
}

// StartBrowserBridge 启动插件桥（端口冲突/无端口时记录并放弃，不影响主程序）。
func StartBrowserBridge(app *application.App, core *CoreService) *browserBridge {
	b := &browserBridge{
		pending: map[string]chan bridgeDecision{},
		app:     app,
		core:    core,
		closer:  make(chan struct{}),
	}
	tok := make([]byte, 16)
	_, _ = rand.Read(tok)
	b.token = hex.EncodeToString(tok)

	var ln net.Listener
	for port := 37021; port <= 37030; port++ {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			ln = l
			break
		}
	}
	if ln == nil {
		fmt.Println("插件桥：37021-37030 端口均被占用，桥未启动")
		return b
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", b.handlePing)
	mux.HandleFunc("/hook", b.handleHook)
	mux.HandleFunc("/bili", b.handleVideoSubmit)  // B站视频静默提交
	mux.HandleFunc("/video", b.handleVideoSubmit) // 通用视频静默提交
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	fmt.Printf("插件桥：监听 127.0.0.1:%d\n", ln.Addr().(*net.TCPAddr).Port)
	return b
}

func extensionOriginOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return strings.HasPrefix(o, "chrome-extension://") ||
		strings.HasPrefix(o, "moz-extension://") ||
		strings.HasPrefix(o, "safari-extension://")
}

func (b *browserBridge) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if o := r.Header.Get("Origin"); o != "" {
		w.Header().Set("Access-Control-Allow-Origin", o)
		w.Header().Add("Vary", "Origin")
	}
	if !extensionOriginOK(r) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden origin"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"token": b.token})
}

func (b *browserBridge) handleHook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if o := r.Header.Get("Origin"); o != "" {
		w.Header().Set("Access-Control-Allow-Origin", o)
		w.Header().Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-YunX-Token")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		return
	}
	if r.Header.Get("X-YunX-Token") != b.token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// 登记挂起请求，经事件交给前端决定
	b.mu.Lock()
	b.seq++
	id := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), b.seq)
	ch := make(chan bridgeDecision, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	b.app.Event.Emit("bridge:hook", map[string]any{"id": id, "payload": string(body)})

	select {
	case d := <-ch:
		_ = json.NewEncoder(w).Encode(d)
	case <-time.After(120 * time.Second):
		_ = json.NewEncoder(w).Encode(bridgeDecision{Action: "return"})
	}
}

// videoSubmitRequest 静默视频提交载荷，兼容插件侧两种端点形态与规格形态
// （quality/qn 用 json.Number 兼容数字与字符串两种来源）：
//   - /bili：{bvid, pageUrl, qn, mode, title}（见 background.js yunx-bili-download）
//   - /video：{platform, url, title, pageUrl}（见 background.js yunx-video-download）
//   - 规格：{url, quality?, mode?}
type videoSubmitRequest struct {
	URL     string      `json:"url"`
	PageURL string      `json:"pageUrl"`
	BVID    string      `json:"bvid"`
	Quality json.Number `json:"quality"`
	QN      json.Number `json:"qn"`
	Mode    string      `json:"mode"`
	// Platform/Title 目前不参与入队（核心按解析结果自定平台与文件名），仅随载荷保留备查
	Platform string `json:"platform"`
	Title    string `json:"title"`
}

// handleVideoSubmit 静默视频提交（/bili、/video 共用）：与 /hook 的区别是不弹窗、
// 不挂起等用户决定——直接调核心 video_download 取链入队，回 {ok:true}。
// 鉴权/CORS 与 /hook 同一套（插件 postSilent 带 X-YunX-Token，Content-Type:application/json
// 会触发预检，须先应答 OPTIONS）。
func (b *browserBridge) handleVideoSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if o := r.Header.Get("Origin"); o != "" {
		w.Header().Set("Access-Control-Allow-Origin", o)
		w.Header().Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-YunX-Token")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		return
	}
	if r.Header.Get("X-YunX-Token") != b.token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error":"bad token"}`))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var req videoSubmitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		_, _ = w.Write([]byte(errorJSON("请求不是合法 JSON: " + err.Error())))
		return
	}
	// 地址兜底链：显式 url → 落地页 pageUrl → BVID 拼标准视频页（/bili 只带 bvid 的形态）
	url := req.URL
	if url == "" {
		url = req.PageURL
	}
	if url == "" && req.BVID != "" {
		url = "https://www.bilibili.com/video/" + req.BVID + "/"
	}
	if url == "" {
		_, _ = w.Write([]byte(errorJSON("缺少视频地址（url/pageUrl/bvid 均为空）")))
		return
	}
	// 清晰度 quality 优先、qn 兜底；0/空 = 平台最高档（核心 qualityId 空串同语义）
	quality := req.Quality.String()
	if quality == "" || quality == "0" {
		quality = req.QN.String()
	}
	dlReq := map[string]string{"cmd": "video_download", "url": url}
	if quality != "" && quality != "0" {
		dlReq["qualityId"] = quality
	}
	if req.Mode != "" {
		dlReq["mode"] = req.Mode
	}
	raw, err := json.Marshal(dlReq)
	if err != nil {
		_, _ = w.Write([]byte(errorJSON("构建下载请求失败: " + err.Error())))
		return
	}
	// 同步等核心取链入队（重新解析可能耗时数秒；插件 fetch 无超时，等得起）
	if resp, err := b.core.Call(string(raw)); err != nil || !strings.Contains(resp, `"ok":true`) {
		msg := resp
		if err != nil {
			msg = err.Error()
		}
		_, _ = w.Write([]byte(errorJSON("视频下载入队失败: " + msg)))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// BridgeDecide 前端弹窗的用户决定回传（CoreService 方法，bindings 暴露给前端）。
func (s *CoreService) BridgeDecide(id string, action string, saveDir string) (string, error) {
	s.bridge.mu.Lock()
	ch, ok := s.bridge.pending[id]
	s.bridge.mu.Unlock()
	if !ok {
		return `{"ok":false,"error":"请求已失效"}`, nil
	}
	if action != "accept" && action != "cancel" && action != "return" {
		action = "return"
	}
	ch <- bridgeDecision{Action: action, SaveDir: saveDir}
	return `{"ok":true}`, nil
}
