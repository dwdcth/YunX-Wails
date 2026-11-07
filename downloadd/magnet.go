package downloadd

// magnet.go：迅雷式磁力流程——先取元数据，前端弹窗让用户选文件/确认文件名（真名，
// 不是 hash），确认后重提到正式目录（select-file/out）开始下载。
//
// ★ aria2-next（3.0.10 实测）忽略 follow-torrent=false：元数据到手后同一任务直接续成
//   正片下载（写进元数据缓存目录），状态永远停在 active 而非 complete——按「等 complete
//   再读 .torrent」的老方案会永远卡在“正在获取磁力元数据”（GUI 实测病灶：元数据 4 秒
//   就到了，代码却在等一个永远不会来的 complete）。
//   现方案：轮询 tellStatus，totalLength>0 且 files 非空 = 元数据已就位 → 立即掐掉该
//   任务、从 files[] 直接构建文件清单（1 基序号与 select-file 对齐），确认后用原磁力
//   重提（peer 已热，元数据二次获取秒级）。
//   兼容尊重 follow-torrent 的实现（files 只剩一项 .torrent 时读字节走 addTorrent）。

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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

// MagnetInfo 磁力元数据解析结果（resolveId 关联服务端缓存）。
type MagnetInfo struct {
	ResolveID    string        `json:"resolveId"`
	Name         string        `json:"name"`
	Files        []TorrentFile `json:"files"`
	TotalLength  int64         `json:"totalLength"`
	SingleFile   bool          `json:"singleFile"`
}

// magnetResolveTimeout 元数据获取上限（冷 DHT 可能很慢，给足）
const magnetResolveTimeout = 3 * time.Minute

// magnetCache resolveId → 解析结果（10 分钟过期，前端确认后 MagnetSubmit 消费）。
var magnetCache = struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}{m: map[string]cacheEntry{}}

type cacheEntry struct {
	magnet string
	info   *MagnetInfo
	data   []byte     // 拿到过 .torrent 字节时留存（尊重 follow-torrent 的引擎路径）；aria2-next 下为空
	at     time.Time // 写入时间（10 分钟过期）
}

func cachePut(id string, e cacheEntry) {
	magnetCache.mu.Lock()
	defer magnetCache.mu.Unlock()
	// 顺手清理过期项
	for k, old := range magnetCache.m {
		if time.Since(old.at) > 10*time.Minute {
			delete(magnetCache.m, k)
		}
	}
	e.at = time.Now()
	magnetCache.m[id] = e
}

func cacheGet(id string) (cacheEntry, bool) {
	magnetCache.mu.Lock()
	defer magnetCache.mu.Unlock()
	e, ok := magnetCache.m[id]
	if !ok || time.Since(e.at) > 10*time.Minute {
		return cacheEntry{}, false
	}
	return e, true
}

// MagnetResolve 取磁力元数据：提交只到元数据阶段的任务，轮询到文件清单即掐掉返回。
// 同步等待（调用方是 Go 服务方法，不占核心命令通道）。
func (m *Manager) MagnetResolve(magnet string) (*MagnetInfo, error) {
	if !strings.HasPrefix(strings.ToLower(magnet), "magnet:") {
		return nil, fmt.Errorf("不是磁力链接")
	}
	// 每次解析独立元数据目录：任务掐掉是异步的，残留清理不与下一次解析互相踩
	metaDir := filepath.Join(m.dataDir, "cache", fmt.Sprintf("magnet-meta-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		return nil, fmt.Errorf("建元数据目录: %w", err)
	}
	gid, err := m.addMetaTask(magnet, metaDir)
	if err != nil {
		return nil, fmt.Errorf("提交元数据下载失败: %w", err)
	}

	deadline := time.Now().Add(magnetResolveTimeout)
	var st *aria2Status
	for time.Now().Before(deadline) {
		st, err = m.aria2.TellStatus(gid)
		if err == nil {
			if st.Status == "error" {
				_ = m.aria2.Remove(gid)
				msg := st.ErrorMessage
				if msg == "" {
					msg = "errorCode=" + st.ErrorCode
				}
				return nil, fmt.Errorf("元数据获取失败: %s", msg)
			}
			if metaReady(st) {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !metaReady(st) {
		_ = m.aria2.Remove(gid)
		return nil, fmt.Errorf("元数据获取超时（%s），磁力可能无源", magnetResolveTimeout)
	}

	// 元数据就位——立即掐任务（aria2-next 会继续把正片写进 metaDir，这正是要防的）。
	// 摘除是异步的（BT 要等 peer 收尾）：等 info-hash 真正消失再返回，否则紧随的
	// MagnetSubmit 重提同磁力会撞「already being downloaded」。上限 5s，等不到也放行
	//（reAddMagnet 兜底再摘再试）。
	_ = m.aria2.Remove(gid)
	hash := infoHashOf(magnet)
	quitDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(quitDeadline) {
		if m.aria2.findByInfoHash(hash) == "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	// 异步清残留（Remove 生效有延迟，稍候再删整个目录）
	go func() {
		time.Sleep(3 * time.Second)
		_ = os.RemoveAll(metaDir)
	}()

	// 兼容路径：尊重 follow-torrent=false 的实现落盘一个 .torrent（files 只剩这一项）
	if len(st.Files) == 1 && strings.HasSuffix(st.Files[0].Path, ".torrent") {
		if data, e := os.ReadFile(st.Files[0].Path); e == nil && len(data) > 0 {
			if name, files, total, e2 := torrentInfo(data); e2 == nil && len(files) > 0 {
				id := fmt.Sprintf("mg%d", time.Now().UnixNano())
				info := &MagnetInfo{Name: name, Files: files, TotalLength: total,
					SingleFile: len(files) == 1, ResolveID: id}
				cachePut(id, cacheEntry{magnet: magnet, info: info, data: data})
				return info, nil
			}
		}
	}

	info := infoFromFiles(st, metaDir, magnet)
	if info == nil {
		return nil, fmt.Errorf("元数据已获取但文件清单为空")
	}
	id := fmt.Sprintf("mg%d", time.Now().UnixNano())
	info.ResolveID = id
	cachePut(id, cacheEntry{magnet: magnet, info: info})
	return info, nil
}

// addMetaTask 提交只到元数据阶段的任务；同 info-hash 旧任务先摘再重试。
func (m *Manager) addMetaTask(magnet, metaDir string) (string, error) {
	opts := map[string]any{
		"dir": metaDir, "follow-torrent": false,
		"auto-file-renaming": false, "allow-overwrite": true,
	}
	gid, err := m.aria2.AddURI([]string{magnet}, opts)
	if err == nil {
		return gid, nil
	}
	if strings.Contains(err.Error(), "already being downloaded") {
		if g := m.aria2.findByInfoHash(infoHashOf(magnet)); g != "" {
			_ = m.aria2.Remove(g)
			time.Sleep(500 * time.Millisecond)
			if gid, err = m.aria2.AddURI([]string{magnet}, opts); err == nil {
				return gid, nil
			}
		}
	}
	return "", err
}

// metaReady 元数据是否已就位：totalLength>0 且 files 非空。
// aria2-next 元数据阶段 total=0；到手后 total=正片大小、files 为真实清单。
// 尊重 follow-torrent 的实现则到 complete（files 只剩一项 .torrent）。
func metaReady(st *aria2Status) bool {
	if st == nil {
		return false
	}
	if st.Status == "complete" && len(st.Files) > 0 {
		return true
	}
	total, _ := strconv.ParseInt(st.TotalLength, 10, 64)
	return total > 0 && len(st.Files) > 0
}

// infoFromFiles 从 tellStatus 的 files[] 构建清单：路径剥掉 metaDir 前缀，
// 多文件再剥掉种子根目录（与 bencode 口径一致：path 为根内相对路径，name 为根目录名）。
func infoFromFiles(st *aria2Status, metaDir, magnet string) *MagnetInfo {
	if len(st.Files) == 0 {
		return nil
	}
	files := make([]TorrentFile, 0, len(st.Files))
	var total int64
	for i, f := range st.Files {
		if f.Path == "" {
			continue
		}
		rel := strings.TrimPrefix(strings.ReplaceAll(f.Path, "\\", "/"), strings.ReplaceAll(metaDir, "\\", "/")+"/")
		if rel == "" || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
			continue
		}
		length, _ := strconv.ParseInt(f.Length, 10, 64)
		files = append(files, TorrentFile{Index: i + 1, Path: rel, Length: length})
		total += length
	}
	if len(files) == 0 {
		return nil
	}
	info := &MagnetInfo{Files: files, TotalLength: total}
	if len(files) == 1 && !strings.Contains(files[0].Path, "/") {
		// 单文件种子：文件直接落在 dir 下，文件名即种子名
		info.Name = files[0].Path
		info.SingleFile = true
		return info
	}
	// 多文件：剥公共根目录（bencode 的 info.name，aria2 落盘为 dir/<name>/…）
	root := ""
	if i := strings.Index(files[0].Path, "/"); i > 0 {
		root = files[0].Path[:i]
	}
	sameRoot := root != ""
	for _, f := range files {
		if !strings.HasPrefix(f.Path, root+"/") {
			sameRoot = false
			break
		}
	}
	if sameRoot {
		info.Name = root
		for i := range files {
			files[i].Path = strings.TrimPrefix(files[i].Path, root+"/")
		}
	} else {
		info.Name = dnOf(magnet)
		if info.Name == "" {
			info.Name = "未命名种子"
		}
	}
	return info
}

// dnOf 磁力链接的 dn= 显示名（URL 解码）。
func dnOf(magnet string) string {
	q := magnet
	if i := strings.Index(magnet, "?"); i >= 0 {
		q = magnet[i+1:]
	}
	for _, kv := range strings.FieldsFunc(q, func(r rune) bool { return r == '&' }) {
		if strings.HasPrefix(strings.ToLower(kv), "dn=") {
			if v, err := url.QueryUnescape(kv[3:]); err == nil {
				return v
			}
			return kv[3:]
		}
	}
	return ""
}

// MagnetSubmit 用户确认后开始下载：select-file 选择文件（空=全部）；
// 单文件且 newName 非空时用 out 改保存文件名（多文件的根目录名 aria2 改不了）。
// aria2-next 路径用原磁力重提（元数据二次获取，peer 已热秒级）；
// 拿到过 .torrent 字节则直接 addTorrent。
func (m *Manager) MagnetSubmit(resolveID, saveDir string, selected []int, newName string) (*SubmitResult, error) {
	e, ok := cacheGet(resolveID)
	if !ok {
		return nil, fmt.Errorf("解析结果已过期（10 分钟），请重新解析")
	}
	if saveDir == "" {
		saveDir = m.DefaultDir()
	}
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return nil, fmt.Errorf("创建保存目录失败: %w", err)
	}
	files := e.info.Files
	opts := map[string]any{"dir": saveDir}
	if len(selected) > 0 && len(selected) < len(files) {
		idx := make([]string, len(selected))
		for i, s := range selected {
			idx[i] = fmt.Sprint(s)
		}
		opts["select-file"] = strings.Join(idx, ",")
	}
	display := e.info.Name
	if e.info.SingleFile && newName != "" {
		opts["out"] = newName
		display = newName
	}
	var gid string
	var err error
	if len(e.data) > 0 {
		gid, err = m.reAddMagnet(e.magnet,
			func() (string, error) {
				return m.aria2.AddTorrent(base64.StdEncoding.EncodeToString(e.data), opts)
			})
	} else {
		gid, err = m.reAddMagnet(e.magnet,
			func() (string, error) {
				return m.aria2.AddURI([]string{e.magnet}, opts)
			})
	}
	if err != nil {
		return nil, fmt.Errorf("提交下载失败: %w", err)
	}
	m.applyOptionsOnce()
	t := &Task{Engine: EngineAria2, Gid: gid, URL: e.magnet, Name: display,
		SaveDir: saveDir, Status: StatusPending}
	if err := m.db.InsertTask(t); err != nil {
		return nil, err
	}
	return &SubmitResult{TaskID: t.ID, TaskIDs: []int64{t.ID}, Engine: EngineAria2, Name: display}, nil
}

// reAddMagnet 提交下载；撞上「同 info-hash 已在下载」时循环摘旧重试
//（摘除异步，BT 收尾可能要几秒），8 秒上限。
func (m *Manager) reAddMagnet(magnet string, add func() (string, error)) (string, error) {
	gid, err := add()
	if err == nil {
		return gid, nil
	}
	if !strings.Contains(err.Error(), "already being downloaded") {
		return "", err
	}
	hash := infoHashOf(magnet)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if old := m.aria2.findByInfoHash(hash); old != "" {
			_ = m.aria2.Remove(old) // remove→forceRemove 兜底
		} else if gid, err = add(); err == nil {
			return gid, nil
		} else if !strings.Contains(err.Error(), "already being downloaded") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", err
}

// infoHashOf 从磁力链接提取 info-hash（xt=urn:btih: 后面的十六进制串）
func infoHashOf(magnet string) string {
	lower := strings.ToLower(magnet)
	idx := strings.Index(lower, "urn:btih:")
	if idx < 0 { return "" }
	rest := magnet[idx+9:]
	end := strings.IndexAny(rest, "&")
	if end > 0 { rest = rest[:end] }
	return strings.TrimSpace(rest)
}
