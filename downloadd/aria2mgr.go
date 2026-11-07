// aria2mgr.go：aria2-next 子进程管理 + JSON-RPC 客户端 + 状态轮询。
//
// aria2-next 是 aria2 的兼容重写（JSON-RPC 协议/鉴权同 aria2：params[0]="token:<secret>"），
// 特性含 BitTorrent/ED2K/Metalink4/HTTPS——FTP 与 Metalink3 不支持，由本包其他文件补位。
// 进程生命周期：首次提交 aria2 类任务时拉起（ensureRunning），应用退出时优雅 kill（Shutdown）；
// 进程意外退出由 Wait goroutine 置 running=false，下次使用自动重启。
package downloadd

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Aria2Hooks aria2 管理器依赖的宿主回调（由服务层注入，避免反向依赖 main 包）。
type Aria2Hooks struct {
	// KvGet 读核心 KV（settings.aria2Path 用户显式配置的二进制路径）。
	KvGet func(key string) (string, bool)
	// DefaultDir 默认下载目录（核心设置 downloadDirUri，服务层注入）。
	DefaultDir func() string
}

// Aria2 aria2-next 进程管理器。
type Aria2 struct {
	mu      sync.Mutex
	db      *DB
	dataDir string
	hooks   Aria2Hooks

	binPath string // 最近一次探测结果（Aria2Status 用）
	found   bool

	cmd      *exec.Cmd
	cmdDone  chan struct{} // 监测协程 Wait 收尸后关闭（Shutdown 等它，绝不并发 Wait）
	onExit   func()        // 进程退出时通知 Manager（重置 optsApplied）
	proxyFn  func() string // 系统代理 URL（如 http://127.0.0.1:7890）；空=不走代理
	running  bool
	stopped  bool // Shutdown 后不再自启
	port     int
	secret   string

	client    *http.Client
	pollOnce  sync.Once
	pollQuit  chan struct{}
	closeOnce sync.Once
}

// NewAria2 构造（不启动进程；首个 aria2 任务才拉起）。
func NewAria2(db *DB, dataDir string, hooks Aria2Hooks) *Aria2 {
	return &Aria2{
		db:      db,
		dataDir: dataDir,
		hooks:   hooks,
		client:  &http.Client{Timeout: 10 * time.Second},
		pollQuit: make(chan struct{}),
	}
}

// ---- 二进制定位 ----

// FindBinary 定位 aria2-next 二进制：kv 配置 > env > PATH > 内置安装路径 > 数据目录。
// 返回 (路径, 是否找到)；找不到不报错（Aria2Status 据此引导用户去设置）。
func (a *Aria2) FindBinary() (string, bool) {
	// 1. 核心设置里的显式路径（settings.aria2Path）
	if a.hooks.KvGet != nil {
		if p, ok := a.hooks.KvGet("settings.aria2Path"); ok && p != "" {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, true
			}
		}
	}
	// 2. 环境变量（测试/便携部署用）
	if p := os.Getenv("YUNX_ARIA2_BIN"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	// 3. PATH
	if p, err := exec.LookPath("aria2-next"); err == nil {
		return p, true
	}
	// 4. 内置工具目录（安装器约定路径）
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "share", "yunx-desktop", "tools", "aria2-next")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	// 5. 数据目录下自带
	p := filepath.Join(a.dataDir, "tools", "aria2-next")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	return "", false
}

// ---- 进程管理 ----

// ensureRunning 确保子进程存活（未启动则拉起并等 RPC 就绪）。所有 RPC 入口先走这里。
func (a *Aria2) ensureRunning() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return fmt.Errorf("aria2 已随应用停止")
	}
	if a.running {
		return nil
	}
	bin, found := a.FindBinary()
	a.binPath, a.found = bin, found
	if !found {
		return fmt.Errorf("未找到 aria2-next，请在设置中配置路径或安装到 PATH")
	}
	port, err := pickFreePort(6900, 6999)
	if err != nil {
		return fmt.Errorf("无可用的 RPC 端口(6900-6999): %w", err)
	}
	secretBytes := make([]byte, 16)
	if _, err := rand.Read(secretBytes); err != nil {
		return fmt.Errorf("生成 rpc secret: %w", err)
	}
	stateDir := filepath.Join(a.dataDir, "aria2")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("建 aria2 state 目录: %w", err)
	}
	dir := ""
	if a.hooks.DefaultDir != nil {
		dir = a.hooks.DefaultDir()
	}
	if dir == "" {
		dir = a.dataDir
	}
	logFile, err := os.OpenFile(filepath.Join(a.dataDir, "aria2.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开 aria2 日志: %w", err)
	}
	defer logFile.Close()

	args := []string{
		"--enable-rpc",
		fmt.Sprintf("--rpc-listen-port=%d", port),
		"--rpc-listen-all=false",
		fmt.Sprintf("--rpc-secret=%s", hex.EncodeToString(secretBytes)),
		fmt.Sprintf("--state-dir=%s", stateDir),
		fmt.Sprintf("--dir=%s", dir),
		"--continue=true",
		"--allow-overwrite=false",
		"--auto-file-renaming=false",
	}
	// 系统代理透传（Mihomo/Clash 等 Fake-IP 环境下 BT/DHT 的 UDP 不走代理管道，
	// 显式传 --all-proxy 让 aria2 经代理解决 peer/tracker 连通性）
	if a.proxyFn != nil {
		if proxy := a.proxyFn(); proxy != "" {
			args = append(args, fmt.Sprintf("--all-proxy=%s", proxy))
			fmt.Printf("aria2-next 代理: %s\n", proxy)
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 aria2-next: %w", err)
	}
	// Wait 只允许这一个协程调用（os/exec 并发 Wait 是数据竞争，race 实测撞过）；
	// done 由它关闭，Shutdown 等 done 而不是自己再 Wait。
	done := make(chan struct{})
	a.cmd, a.cmdDone, a.port, a.secret, a.running = cmd, done, port, hex.EncodeToString(secretBytes), true
	fmt.Printf("aria2-next 已启动: pid=%d port=%d state=%s\n", cmd.Process.Pid, port, stateDir)

	// 进程退出监测：置 running=false，下次 ensureRunning 重启
	go func() {
		err := cmd.Wait()
		a.mu.Lock()
		wasCurrent := a.cmd == cmd
		if wasCurrent {
			a.running = false
		}
		a.mu.Unlock()
		close(done)
		if a.onExit != nil {
			runCatchingGo(a.onExit)
		}
		// 意外退出才提示（Shutdown 主动终止拿到 signal: terminated 属正常）
		if err != nil && wasCurrent {
			fmt.Printf("aria2-next 退出: %v\n", err)
		}
	}()

	// 等 RPC 就绪（最多 5s）
	rpcURL := fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := a.postRPC(rpcURL, a.secret, "aria2.getVersion"); err == nil {
			a.pollOnce.Do(a.startPolling)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// 就绪超时：回收僵尸进程、复位状态，下次调用换端口重试
	_ = cmd.Process.Kill()
	<-done // 监测协程 Wait 收尸后再复位，避免与下次启动的 cmd 混写
	a.mu.Lock()
	a.running = false
	a.cmd = nil
	a.cmdDone = nil
	a.mu.Unlock()
	return fmt.Errorf("aria2-next RPC 就绪超时（端口 %d）", port)
}

// pickFreePort 在 [lo,hi] 逐个试绑定，返回第一个可用端口（绑定后立即释放，存在微小竞态，
// aria2 启动失败时 ensureRunning 下一轮换端口重试）。
func pickFreePort(lo, hi int) (int, error) {
	for p := lo; p <= hi; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			l.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("端口 %d-%d 均被占用", lo, hi)
}

// Shutdown 优雅停止：SIGTERM → 等 3s → SIGKILL；之后 stopped=true 不再自启。幂等。
// Wait 由 ensureRunning 的监测协程独占，这里只等它关闭的 done（并发 Wait 是数据竞争）。
func (a *Aria2) Shutdown() {
	a.mu.Lock()
	cmd := a.cmd
	done := a.cmdDone
	a.stopped = true
	a.running = false
	a.cmd = nil
	a.cmdDone = nil
	a.mu.Unlock()
	if cmd == nil || cmd.Process == nil || done == nil {
		return
	}
	a.closeOnce.Do(func() { close(a.pollQuit) })
	_ = cmd.Process.Signal(termSignal())
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// Kill 后仍未收尸：放弃等待（监测协程最终会 close(done)）
		}
	}
	fmt.Println("aria2-next 已停止")
}

// Status 当前运行状态（Aria2Status 服务方法用；不拉起进程）。
func (a *Aria2) Status() (running bool, port int, version string, binPath string, found bool) {
	a.mu.Lock()
	running, port = a.running, a.port
	a.mu.Unlock()
	bin, f := a.FindBinary()
	if running {
		if v, err := a.GetVersion(); err == nil {
			version = v
		}
	}
	return running, port, version, bin, f
}

// ---- JSON-RPC ----

// rpcTarget 取当前 RPC 端点（调用方已保证 running）。
func (a *Aria2) rpcTarget() (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return "", "", fmt.Errorf("aria2 未运行")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", a.port), a.secret, nil
}

func (a *Aria2) postRPC(url, secret, method string, params ...any) (json.RawMessage, error) {
	all := make([]any, 0, len(params)+1)
	all = append(all, "token:"+secret)
	all = append(all, params...)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "yunx", "method": method, "params": all,
	})
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("RPC 响应解析失败: %s", oneLineStr(raw))
	}
	if env.Error != nil {
		return env.Result, fmt.Errorf("aria2: %s", env.Error.Message)
	}
	return env.Result, nil
}

// call 发起一次 RPC（自动 ensureRunning）。
func (a *Aria2) call(method string, params ...any) (json.RawMessage, error) {
	if err := a.ensureRunning(); err != nil {
		return nil, err
	}
	url, secret, err := a.rpcTarget()
	if err != nil {
		return nil, err
	}
	return a.postRPC(url, secret, method, params...)
}

func oneLineStr(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "…"
	}
	return string(b)
}

// GetVersion 版本串（如 3.0.10）。
func (a *Aria2) GetVersion() (string, error) {
	res, err := a.call("aria2.getVersion")
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(res, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// AddURI 提交 URL/磁力/ed2k 类下载（uris 至少 1 条），返回 gid。
func (a *Aria2) AddURI(uris []string, options map[string]any) (string, error) {
	if options == nil {
		options = map[string]any{}
	}
	res, err := a.call("aria2.addUri", uris, options)
	if err != nil {
		return "", err
	}
	var gid string
	return gid, json.Unmarshal(res, &gid)
}

// AddTorrent 提交种子文件内容（base64），返回 gid。
func (a *Aria2) AddTorrent(torrentB64 string, options map[string]any) (string, error) {
	if options == nil {
		options = map[string]any{}
	}
	res, err := a.call("aria2.addTorrent", torrentB64, []string{}, options)
	if err != nil {
		return "", err
	}
	var gid string
	return gid, json.Unmarshal(res, &gid)
}

// AddMetalink 提交 metalink4 文件内容（base64），返回文件顺序对应的 gid 列表。
func (a *Aria2) AddMetalink(metalinkB64 string, options map[string]any) ([]string, error) {
	if options == nil {
		options = map[string]any{}
	}
	res, err := a.call("aria2.addMetalink", metalinkB64, options)
	if err != nil {
		return nil, err
	}
	var gids []string
	return gids, json.Unmarshal(res, &gids)
}

// Pause 暂停（对已结束的任务报错属正常，调用方按需容忍）。
func (a *Aria2) Pause(gid string) error {
	_, err := a.call("aria2.pause", gid)
	return err
}

// Unpause 恢复。
func (a *Aria2) Unpause(gid string) error {
	_, err := a.call("aria2.unpause", gid)
	return err
}

// Remove 移除（异步摘除；失败再试 forceRemove 立即摘）。
func (a *Aria2) Remove(gid string) error {
	if _, err := a.call("aria2.remove", gid); err == nil {
		return nil
	}
	_, err := a.call("aria2.forceRemove", gid)
	return err
}

// RemoveDownloadFile 删除 gid 对应任务已下载的文件（上游 aria2 ≥1.37 的
// aria2.removeDownloadFile：对仍在会话中的已完成/出错/已移除任务有效，不会摘任务本身）。
// 注意：aria2-next 3.x 尚未实现该 RPC（返回 "No such method: aria2.removeDownloadFile"），
// 调用方（Manager.RemoveFile）须在报错时回退 os.Remove(savePath)。
func (a *Aria2) RemoveDownloadFile(gid string) error {
	_, err := a.call("aria2.removeDownloadFile", gid)
	return err
}

// PauseAll / UnpauseAll 全量暂停/恢复。
func (a *Aria2) PauseAll() error {
	_, err := a.call("aria2.pauseAll")
	return err
}

func (a *Aria2) UnpauseAll() error {
	_, err := a.call("aria2.unpauseAll")
	return err
}

// ChangeGlobalOption 应用全局选项（bt-enable-dht / bt-tracker / max-concurrent-downloads /
// max-overall-download-limit 等，aria2.changeGlobalOption）。需进程已在运行。
func (a *Aria2) ChangeGlobalOption(opts map[string]string) error {
	res, err := a.call("aria2.changeGlobalOption", opts)
	fmt.Printf("RPC changeGlobalOption: opts=%v res=%s err=%v\n", opts, string(res), err)
	return err
}

// Running 进程是否在运行（Manager 据此决定是否即时应用全局选项）。
func (a *Aria2) Running() bool { return a.running }

// ---- tellStatus 与轮询 ----

type aria2Status struct {
	GID             string `json:"gid"`
	Status          string `json:"status"` // active/waiting/paused/error/complete/removed
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	ErrorCode       string `json:"errorCode"`
	ErrorMessage    string `json:"errorMessage"`
	Files           []struct {
		Path string `json:"path"`
	} `json:"files"`
}

// TellStatus 查询单任务状态。
func (a *Aria2) TellStatus(gid string) (*aria2Status, error) {
	res, err := a.call("aria2.tellStatus", gid)
	if err != nil {
		return nil, err
	}
	var st aria2Status
	if err := json.Unmarshal(res, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// startPolling 1s 轮询：库里所有有 gid 且未终态的任务 → tellStatus → 写回 DB。
// 单飞（pollOnce 保证只有一个 goroutine）。
func (a *Aria2) startPolling() {
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-a.pollQuit:
				return
			case <-tick.C:
				a.pollTick()
			}
		}
	}()
}

func (a *Aria2) pollTick() {
	tasks, err := a.db.ListPollTasks()
	if err != nil {
		return
	}
	for i := range tasks {
		a.pollTask(&tasks[i])
	}
}

func (a *Aria2) pollTask(t *Task) {
	url, secret, err := a.rpcTarget()
	if err != nil {
		return // 进程不在（重启间隙）——本轮跳过，不动 DB
	}
	res, err := a.postRPC(url, secret, "aria2.tellStatus", t.Gid)
	if err != nil {
		// RPC 层故障（进程刚退/重启中）：整轮跳过，不清任务
		if a.rpcAlive(url, secret) {
			// RPC 活着但报错：GID 不存在等 → 任务在 aria2 侧已消失
			_ = a.db.SetStatus(t.ID, StatusPaused, "")
		}
		return
	}
	var st aria2Status
	if err := json.Unmarshal(res, &st); err != nil {
		return
	}
	size, _ := strconv.ParseInt(st.TotalLength, 10, 64)
	done, _ := strconv.ParseInt(st.CompletedLength, 10, 64)
	speed, _ := strconv.ParseInt(st.DownloadSpeed, 10, 64)
	progress := 0
	if size > 0 {
		progress = int(float64(done) / float64(size) * 100)
	}

	switch st.Status {
	case "active":
		_ = a.db.UpdatePoll(t.ID, StatusActive, size, done, speed, progress, "")
		a.fillPath(t.ID, st)
	case "waiting":
		_ = a.db.UpdatePoll(t.ID, StatusPending, size, done, 0, progress, "")
	case "paused":
		_ = a.db.UpdatePoll(t.ID, StatusPaused, size, done, 0, progress, "")
	case "complete":
		_ = a.db.FinishTask(t.ID, StatusDone, size, done, 100, "")
		a.fillPath(t.ID, st)
	case "error":
		msg := st.ErrorMessage
		if msg == "" {
			msg = "下载失败（errorCode=" + st.ErrorCode + "）"
		}
		_ = a.db.FinishTask(t.ID, StatusError, size, done, progress, msg)
	case "removed":
		_ = a.db.FinishTask(t.ID, StatusError, size, done, progress, "任务已被 aria2 移除")
	}
}

// fillPath 用 files[0].path 回填 save_path 与 name（仅当有值时）。
func (a *Aria2) fillPath(id int64, st aria2Status) {
	if len(st.Files) == 0 || st.Files[0].Path == "" {
		return
	}
	p := st.Files[0].Path
	name := p
	if i := strings.LastIndexAny(p, `/\`); i >= 0 && i < len(p)-1 {
		name = p[i+1:]
	}
	_ = a.db.SetSavePath(id, p, name)
}

// rpcAlive 探测 RPC 是否可达（区分「进程不在」与「GID 报错」）。
func (a *Aria2) rpcAlive(url, secret string) bool {
	_, err := a.postRPC(url, secret, "aria2.getVersion")
	return err == nil
}

func runCatchingGo(f func()) {
	defer func() { _ = recover() }()
	f()
}
