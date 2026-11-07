package downloadd

// magnet.go：迅雷式磁力流程——先取元数据（aria2 follow-torrent=false 只下 .torrent），
// 前端弹窗让用户选文件/确认文件名（真名，不是 hash），再 addTorrent(select-file/out) 开始下载。

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TorrentFile 种子内单个文件（Index = aria2 getFiles 的 1 基序号，select-file 用）。
type TorrentFile struct {
	Index  int    `json:"index"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// MagnetInfo 磁力元数据解析结果（resolveId 关联服务端缓存的种子字节）。
type MagnetInfo struct {
	ResolveID    string        `json:"resolveId"`
	Name         string        `json:"name"`
	Files        []TorrentFile `json:"files"`
	TotalLength  int64         `json:"totalLength"`
	SingleFile   bool          `json:"singleFile"`
}

// magnetResolveTimeout 元数据获取上限（冷 DHT 可能很慢，给足）
const magnetResolveTimeout = 3 * time.Minute

// magnetCache resolveId → 种子字节（10 分钟过期，前端确认后 MagnetSubmit 消费）
var magnetCache = struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}{m: map[string]cacheEntry{}}

type cacheEntry struct {
	data []byte
	at   time.Time
}

func cachePut(id string, data []byte) {
	magnetCache.mu.Lock()
	defer magnetCache.mu.Unlock()
	// 顺手清理过期项
	for k, e := range magnetCache.m {
		if time.Since(e.at) > 10*time.Minute {
			delete(magnetCache.m, k)
		}
	}
	magnetCache.m[id] = cacheEntry{data: data, at: time.Now()}
}

func cacheGet(id string) ([]byte, bool) {
	magnetCache.mu.Lock()
	defer magnetCache.mu.Unlock()
	e, ok := magnetCache.m[id]
	if !ok || time.Since(e.at) > 10*time.Minute {
		return nil, false
	}
	return e.data, true
}

// MagnetResolve 取磁力元数据：aria2 只下 .torrent（follow-torrent=false，落 cache/magnet-meta），
// 完成后读种子字节并解析文件清单。同步等待（调用方是 Go 服务方法，不占核心命令通道）。
func (m *Manager) MagnetResolve(magnet string) (*MagnetInfo, error) {
	if !strings.HasPrefix(strings.ToLower(magnet), "magnet:") {
		return nil, fmt.Errorf("不是磁力链接")
	}
	metaDir := filepath.Join(m.dataDir, "cache", "magnet-meta")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		return nil, fmt.Errorf("建元数据目录: %w", err)
	}
	gid, err := m.aria2.AddURI([]string{magnet}, map[string]any{
		"dir": metaDir, "follow-torrent": false,
		"auto-file-renaming": false, "allow-overwrite": true,
	})
	if err != nil {
		return nil, fmt.Errorf("提交元数据下载失败: %w", err)
	}
	deadline := time.Now().Add(magnetResolveTimeout)
	var metaPath string
	for time.Now().Before(deadline) {
		st, err := m.aria2.TellStatus(gid)
		if err == nil {
			switch st.Status {
			case "complete":
				if len(st.Files) > 0 {
					metaPath = st.Files[0].Path
				}
			case "error":
				msg := st.ErrorMessage
				if msg == "" {
					msg = "errorCode=" + st.ErrorCode
				}
				return nil, fmt.Errorf("元数据获取失败: %s", msg)
			}
			if metaPath != "" {
				break
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	if metaPath == "" {
		_ = m.aria2.Remove(gid)
		return nil, fmt.Errorf("元数据获取超时（%s），磁力可能无源", magnetResolveTimeout)
	}
	data, err := os.ReadFile(metaPath)
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("读取元数据失败: %w", err)
	}
	name, files, total, err := torrentInfo(data)
	if err != nil {
		return nil, fmt.Errorf("解析种子失败: %w", err)
	}
	_ = total
	id := fmt.Sprintf("mg%d", time.Now().UnixNano())
	cachePut(id, data)
	return &MagnetInfo{
		ResolveID: id, Name: name, Files: files, TotalLength: total,
		SingleFile: len(files) == 1,
	}, nil
}

// MagnetSubmit 用户确认后开始下载：select-file 选择文件（空=全部）；
// 单文件且 newName 非空时用 out 改保存文件名（多文件的根目录名 aria2 改不了）。
func (m *Manager) MagnetSubmit(resolveID, saveDir string, selected []int, newName string) (*SubmitResult, error) {
	data, ok := cacheGet(resolveID)
	if !ok {
		return nil, fmt.Errorf("解析结果已过期（10 分钟），请重新解析")
	}
	name, files, _, err := torrentInfo(data)
	if err != nil {
		return nil, fmt.Errorf("解析种子失败: %w", err)
	}
	if saveDir == "" {
		saveDir = m.DefaultDir()
	}
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return nil, fmt.Errorf("创建保存目录失败: %w", err)
	}
	opts := map[string]any{"dir": saveDir}
	if len(selected) > 0 && len(selected) < len(files) {
		idx := make([]string, len(selected))
		for i, s := range selected {
			idx[i] = fmt.Sprint(s)
		}
		opts["select-file"] = strings.Join(idx, ",")
		// 按所选文件重算名义大小（展示用）
		var sel int64
		want := map[int]bool{}
		for _, s := range selected {
			want[s] = true
		}
		for _, f := range files {
			if want[f.Index] {
				sel += f.Length
			}
		}
		_ = sel // 名义大小展示交给前端按勾选计算
	}
	display := name
	if len(files) == 1 && newName != "" {
		opts["out"] = newName
		display = newName
	}
	gid, err := m.aria2.AddTorrent(base64.StdEncoding.EncodeToString(data), opts)
	if err != nil {
		return nil, fmt.Errorf("提交下载失败: %w", err)
	}
	m.applyOptionsOnce()
	t := &Task{Engine: EngineAria2, Gid: gid, URL: "magnet:" + resolveID, Name: display,
		SaveDir: saveDir, Status: StatusPending}
	if err := m.db.InsertTask(t); err != nil {
		return nil, err
	}
	return &SubmitResult{TaskID: t.ID, TaskIDs: []int64{t.ID}, Engine: EngineAria2, Name: display}, nil
}
