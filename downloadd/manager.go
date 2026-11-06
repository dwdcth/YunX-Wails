// manager.go：downloadd 包门面——服务层（normaldl.go）唯一入口。
// 职责：初始化（DB + 两个引擎 + 宿主回调注入）、提交分流（classify → aria2/ftp）、
// 列表/暂停/恢复/移除（按引擎分派）、默认下载目录解析（核心设置 downloadDirUri）。
package downloadd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Hooks 宿主注入的回调（服务层提供，避免 downloadd 反向依赖 main 包）。
type Hooks struct {
	// KvGet 经核心通道读 KV（settings.aria2Path 等配置）。可空。
	KvGet func(key string) (string, bool)
	// Call 核心通用命令通道（读正式设置用，如 settings_list 的 downloadDirUri）。可空。
	Call func(requestJSON string) (string, error)
}

// Manager 普通下载管理器。
type Manager struct {
	mu      sync.Mutex
	dataDir string
	db      *DB
	hooks   Hooks
	aria2   *Aria2
	ftp     *FTPEngine

	dirCache struct {
		val string
		at  time.Time
	}
}

// Init 初始化管理器：打开任务库、构造引擎（aria2 惰性启动，FTP worker 池即起）。
func Init(dataDir string, hooks Hooks) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("建数据目录: %w", err)
	}
	db, err := OpenDB(dataDir)
	if err != nil {
		return nil, err
	}
	m := &Manager{dataDir: dataDir, db: db, hooks: hooks}
	m.aria2 = NewAria2(db, dataDir, Aria2Hooks{KvGet: hooks.KvGet, DefaultDir: m.DefaultDir})
	m.ftp = NewFTPEngine(db)
	return m, nil
}

// Close 释放资源（停 aria2 进程与 FTP worker；应用退出路径）。
func (m *Manager) Close() {
	m.aria2.Shutdown()
	m.ftp.Stop()
	_ = m.db.Close()
}

// DefaultDir 默认下载目录：核心正式设置 downloadDirUri（file:// 前缀剥掉）
// → ~/Downloads → <dataDir>/downloads。结果缓存 30s（每次 Submit 都查核心不划算）。
func (m *Manager) DefaultDir() string {
	m.mu.Lock()
	if time.Since(m.dirCache.at) < 30*time.Second && m.dirCache.val != "" {
		v := m.dirCache.val
		m.mu.Unlock()
		return v
	}
	m.mu.Unlock()

	dir := ""
	if m.hooks.Call != nil {
		if raw, err := m.hooks.Call(`{"cmd":"settings_list"}`); err == nil {
			var r struct {
				OK       bool           `json:"ok"`
				Settings map[string]any `json:"settings"`
			}
			if json.Unmarshal([]byte(raw), &r) == nil && r.OK {
				if v, ok := r.Settings["downloadDirUri"].(string); ok {
					dir = v
				}
			}
		}
	}
	dir = strings.TrimPrefix(dir, "file://")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, "Downloads")
		}
	}
	if dir == "" {
		dir = filepath.Join(m.dataDir, "downloads")
	}
	m.mu.Lock()
	m.dirCache.val, m.dirCache.at = dir, time.Now()
	m.mu.Unlock()
	return dir
}

// SubmitResult 提交结果（单个任务或 metalink 多文件展开的多任务）。
type SubmitResult struct {
	TaskID  int64   `json:"taskId"`
	TaskIDs []int64 `json:"taskIds"`
	Engine  string  `json:"engine"`
	Name    string  `json:"name"`
}

// Submit 提交下载：分流 → 对应引擎 → 落库。saveDir 空时用默认下载目录。
func (m *Manager) Submit(source, saveDir string) (*SubmitResult, error) {
	c := Classify(source)
	if c.Err != "" {
		return nil, fmt.Errorf("%s", c.Err)
	}
	if saveDir == "" {
		saveDir = m.DefaultDir()
	}
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return nil, fmt.Errorf("创建保存目录失败: %w", err)
	}

	switch c.Kind {
	case KindFTP, KindFTPS:
		id, err := m.ftp.Submit(c.URL, saveDir, c.Name)
		if err != nil {
			return nil, err
		}
		return &SubmitResult{TaskID: id, TaskIDs: []int64{id}, Engine: EngineFTP, Name: c.Name}, nil

	case KindMagnet, KindHTTP:
		gid, err := m.aria2.AddURI([]string{c.URL}, map[string]any{"dir": saveDir})
		if err != nil {
			return nil, fmt.Errorf("提交到 aria2 失败: %w", err)
		}
		t := &Task{Engine: EngineAria2, Gid: gid, URL: c.URL, Name: c.Name, SaveDir: saveDir, Status: StatusPending}
		if err := m.db.InsertTask(t); err != nil {
			return nil, err
		}
		return &SubmitResult{TaskID: t.ID, TaskIDs: []int64{t.ID}, Engine: EngineAria2, Name: c.Name}, nil

	case KindTorrent:
		gid, err := m.aria2.AddTorrent(base64.StdEncoding.EncodeToString(c.TorrentData), map[string]any{"dir": saveDir})
		if err != nil {
			return nil, fmt.Errorf("提交种子到 aria2 失败: %w", err)
		}
		t := &Task{Engine: EngineAria2, Gid: gid, URL: source, Name: c.Name, SaveDir: saveDir, Status: StatusPending}
		if err := m.db.InsertTask(t); err != nil {
			return nil, err
		}
		return &SubmitResult{TaskID: t.ID, TaskIDs: []int64{t.ID}, Engine: EngineAria2, Name: c.Name}, nil

	case KindMetalink:
		return m.submitMetalink(source, c.MetalinkData, saveDir)
	}
	return nil, fmt.Errorf("未知下载类型: %s", c.Kind)
}

// submitMetalink metalink 提交：v3 → 转 v4 落盘缓存；v4 直接用。
// addMetalink 返回的 gid 与文件顺序一致 → 一个文件一条任务记录（name 用 File.Name）。
func (m *Manager) submitMetalink(source string, data []byte, saveDir string) (*SubmitResult, error) {
	v4, err := parseMetalink4(data)
	converted := false
	if err != nil || isMetalinkV3(data) {
		// v4 解析失败或嗅探出 v3 → 按 v3 转换
		v4, err = convertMetalink3ToV4(data)
		if err != nil {
			return nil, fmt.Errorf("不支持的 Metalink 内容: %w", err)
		}
		converted = true
	}
	// 清洗：只保留可执行条目（有 URL 的文件），名字按文件顺序对齐 gid
	var names []string
	for _, f := range v4.Files {
		if len(f.URLs) > 0 {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("metalink 中没有可下载的文件")
	}

	payload := data
	if converted {
		out, err := marshalMetalink4(v4)
		if err != nil {
			return nil, err
		}
		payload = out
		cacheDir := filepath.Join(m.dataDir, "cache")
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			return nil, fmt.Errorf("建缓存目录: %w", err)
		}
		cachePath := filepath.Join(cacheDir, fmt.Sprintf("metalink4-%d.meta4", time.Now().UnixMilli()))
		if err := os.WriteFile(cachePath, payload, 0644); err != nil {
			return nil, fmt.Errorf("写 metalink 缓存: %w", err)
		}
	}

	gids, err := m.aria2.AddMetalink(base64.StdEncoding.EncodeToString(payload), map[string]any{"dir": saveDir})
	if err != nil {
		return nil, fmt.Errorf("提交 metalink 到 aria2 失败: %w", err)
	}
	res := &SubmitResult{Engine: EngineAria2}
	for i, gid := range gids {
		name := ""
		if i < len(names) {
			name = names[i]
		} // gid 多于已知文件名时名字留空，由轮询 files[0].path 补
		t := &Task{Engine: EngineAria2, Gid: gid, URL: source, Name: name, SaveDir: saveDir, Status: StatusPending}
		if err := m.db.InsertTask(t); err != nil {
			return nil, err
		}
		res.TaskIDs = append(res.TaskIDs, t.ID)
	}
	if len(names) > 0 {
		res.Name = names[0]
	}
	res.TaskID = res.TaskIDs[0]
	return res, nil
}

// List 全量任务列表（created_at 倒序）。
func (m *Manager) List() ([]Task, error) { return m.db.ListTasks() }

// Pause 暂停单任务（按引擎分派）。aria2 的 pause 是优雅暂停（等手头分片收尾），
// 状态迁移可能滞后于 RPC 返回——等 aria2 侧确已 paused 再落库，防紧随的 Resume
// 对仍 active 的任务 unpause 失败被吞掉、任务卡死在 paused。
func (m *Manager) Pause(id int64) error {
	t, err := m.db.GetTask(id)
	if err != nil || t == nil {
		return fmt.Errorf("任务不存在")
	}
	if t.terminal() {
		return fmt.Errorf("任务已结束")
	}
	if t.Engine == EngineFTP {
		return m.ftp.Pause(id)
	}
	if t.Gid != "" {
		if err := m.aria2.Pause(t.Gid); err != nil && !tolerableAria2Err(err) {
			return err
		}
		m.waitAria2Status(t.Gid, "paused", 5*time.Second)
	}
	return m.db.SetStatus(id, StatusPaused, "")
}

// waitAria2Status 轮询 aria2 侧真实状态；超时不报错（轮询器随后会纠偏 DB）。
func (m *Manager) waitAria2Status(gid, want string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := m.aria2.TellStatus(gid); err == nil && st.Status == want {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// Resume 恢复单任务。error 任务在 aria2 侧无法 unpause → 摘旧 gid 重提。
func (m *Manager) Resume(id int64) error {
	t, err := m.db.GetTask(id)
	if err != nil || t == nil {
		return fmt.Errorf("任务不存在")
	}
	if t.Status != StatusPaused && t.Status != StatusError {
		return fmt.Errorf("任务当前状态(%s)不可恢复", t.Status)
	}
	if t.Engine == EngineFTP {
		return m.ftp.Resume(id)
	}
	if t.Status == StatusError {
		return m.retryAria2(t)
	}
	if t.Gid != "" {
		if err := m.aria2.Unpause(t.Gid); err != nil && !tolerableAria2Err(err) {
			return err
		}
	}
	return m.db.SetStatus(id, StatusPending, "")
}

// retryAria2 error 任务重试：摘掉 aria2 里的 error 旧任务，按原 URL 重提新 gid。
// 仅对可直接 addUri 的来源（magnet/ed2k/bc1 与非 .torrent/.metalink 后缀的 http URL——
// URL 版种子/索引的判定恰好只在后缀命中，故该判别是精确的）；本地种子/metalink 请重新提交。
func (m *Manager) retryAria2(t *Task) error {
	lower := strings.ToLower(t.URL)
	path := lower
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	retryable := strings.HasPrefix(lower, "magnet:") || strings.HasPrefix(lower, "ed2k:") ||
		strings.HasPrefix(lower, "bc1:") ||
		((strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")) &&
			!strings.HasSuffix(path, ".torrent") && !strings.HasSuffix(path, ".metalink") && !strings.HasSuffix(path, ".meta4"))
	if !retryable {
		return fmt.Errorf("该任务失败后暂不支持原地重试，请移除后重新提交")
	}
	if t.Gid != "" {
		_ = m.aria2.Remove(t.Gid)
	}
	gid, err := m.aria2.AddURI([]string{t.URL}, map[string]any{"dir": t.SaveDir})
	if err != nil {
		return fmt.Errorf("重新提交失败: %w", err)
	}
	t.Gid = gid
	t.Status = StatusPending
	t.Err = ""
	t.Speed = 0
	return m.db.UpdateTask(t)
}

// Remove 移除单任务（aria2 侧摘下载；不删已下载文件）。
func (m *Manager) Remove(id int64) error {
	t, err := m.db.GetTask(id)
	if err != nil || t == nil {
		return nil // 幂等
	}
	if t.Engine == EngineFTP {
		return m.ftp.Remove(id)
	}
	if t.Gid != "" && !t.terminal() {
		if err := m.aria2.Remove(t.Gid); err != nil && !tolerableAria2Err(err) {
			return err
		}
	}
	return m.db.DeleteTask(id)
}

// PauseAll 全部暂停（两引擎各自批量）。
func (m *Manager) PauseAll() {
	if err := m.aria2.PauseAll(); err != nil {
		fmt.Printf("aria2 pauseAll: %v\n", err)
	}
	m.ftp.PauseAll()
	// aria2 侧终态由轮询回写；DB 里非 ftp 的 pending/active 兜底置 paused
	tasks, _ := m.db.ListTasks()
	for _, t := range tasks {
		if t.Engine == EngineAria2 && (t.Status == StatusPending || t.Status == StatusActive) {
			_ = m.db.SetStatus(t.ID, StatusPaused, "")
		}
	}
}

// ResumeAll 全部恢复。
func (m *Manager) ResumeAll() {
	if err := m.aria2.UnpauseAll(); err != nil {
		fmt.Printf("aria2 unpauseAll: %v\n", err)
	}
	m.ftp.ResumeAll()
	tasks, _ := m.db.ListTasks()
	for _, t := range tasks {
		if t.Engine == EngineAria2 && (t.Status == StatusPaused || t.Status == StatusError) {
			_ = m.db.SetStatus(t.ID, StatusPending, "")
		}
	}
}

// RemoveAll 全部移除（不删已下载文件）。
func (m *Manager) RemoveAll() {
	tasks, _ := m.db.ListTasks()
	for _, t := range tasks {
		_ = m.Remove(t.ID)
	}
}

// Aria2StatusInfo aria2 引擎状态（UI 展示/引导配置用）。
type Aria2StatusInfo struct {
	Running bool   `json:"running"`
	Version string `json:"version"`
	Port    int    `json:"port"`
	BinPath string `json:"binPath"`
	Found   bool   `json:"found"`
}

// Aria2Status 查询 aria2 状态（不拉起进程）。
func (m *Manager) Aria2Status() Aria2StatusInfo {
	running, port, version, bin, found := m.aria2.Status()
	return Aria2StatusInfo{Running: running, Version: version, Port: port, BinPath: bin, Found: found}
}

// tolerableAria2Err aria2 对已结束/已消失任务的 pause/unpause/remove 报错可容忍
//（本地 DB 状态为准即可）。
func tolerableAria2Err(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	for _, k := range []string{"cannot be paused", "cannot be unpaused", "cannot be removed", "is not found", "GID", "already"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}
