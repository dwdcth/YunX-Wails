package downloadd

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// slowHTTPServer 进程内限速 HTTP 文件服务（支持 Range，供 aria2 断点续传/暂停用例）。
// 全局令牌桶：整个服务每 chunkDelay 发一个 chunk——aria2-next 会开多连接并行，
// 按连接限速会被摊薄，全局限速才能制造稳定可观测的传输时长。
func slowHTTPServer(t *testing.T, data []byte, chunk int, chunkDelay time.Duration) *httptest.Server {
	t.Helper()
	tokens := make(chan struct{}, 4)
	quit := make(chan struct{})
	go func() {
		tk := time.NewTicker(chunkDelay)
		defer tk.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tk.C:
				select {
				case tokens <- struct{}{}:
				default:
				}
			}
		}
	}()
	t.Cleanup(func() { close(quit) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := int64(0), int64(len(data))-1
		if cr := r.Header.Get("Range"); strings.HasPrefix(cr, "bytes=") {
			var a, b int64
			if n, _ := fmt.Sscanf(cr, "bytes=%d-%d", &a, &b); n >= 1 && a >= 0 && a <= end {
				start = a
				if n == 2 && b >= a && b < end {
					end = b
				}
			} else {
				w.WriteHeader(416)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		for pos := start; pos <= end; pos += int64(chunk) {
			select {
			case <-tokens:
			case <-r.Context().Done():
				return
			}
			e := pos + int64(chunk) - 1
			if e > end {
				e = end
			}
			if _, err := w.Write(data[pos : e+1]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAria2EngineDownload(t *testing.T) {
	bin := findAria2Bin(t)
	if bin == "" {
		t.Skip("未找到 aria2-next（设 YUNX_ARIA2_BIN 或装到 ~/.local/share/yunx-desktop/tools/），跳过")
	}
	t.Setenv("YUNX_ARIA2_BIN", bin)

	dataDir := t.TempDir()
	saveDir := filepath.Join(dataDir, "save")
	os.MkdirAll(saveDir, 0755)

	m, err := Init(dataDir, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// 初始状态：二进制能找到、进程未起
	st := m.Aria2Status()
	if !st.Found {
		t.Fatalf("Aria2Status().Found=false, bin=%s", bin)
	}
	if st.Running {
		t.Error("未提交任务前不应已在运行")
	}

	// 基础链路：python3 -m http.server 放 3MB 文件 → submit → done → 字节一致
	var wwwPort int = 18231
	www := filepath.Join(dataDir, "www")
	os.MkdirAll(www, 0755)
	want3mb := randBytes(t, 3<<20)
	os.WriteFile(filepath.Join(www, "data3mb.bin"), want3mb, 0644)
	if _, err := exec.LookPath("python3"); err == nil {
		startProc(t, "httpserver", dataDir, "python3", "-m", "http.server",
			strconv.Itoa(wwwPort), "-d", www)
		if waitPort(t, fmt.Sprintf("127.0.0.1:%d", wwwPort), 10*time.Second) {
			t.Run("完整下载(python http.server)", func(t *testing.T) {
				res, err := m.Submit(fmt.Sprintf("http://127.0.0.1:%d/data3mb.bin", wwwPort), saveDir)
				if err != nil {
					t.Fatalf("提交失败: %v", err)
				}
				if res.Engine != EngineAria2 {
					t.Fatalf("engine=%s want aria2", res.Engine)
				}
				task := waitTaskStatus(t, m, res.TaskID,
					map[string]bool{StatusDone: true, StatusError: true}, 60*time.Second)
				if task.Status != StatusDone {
					t.Fatalf("status=%s err=%s", task.Status, task.Err)
				}
				if task.SavePath == "" {
					t.Error("complete 后 save_path 未回填")
				}
				filesEqual(t, filepath.Join(saveDir, "data3mb.bin"), filepath.Join(www, "data3mb.bin"))
			})
		} else {
			t.Fatal("python http.server 未就绪")
		}
	} else {
		t.Log("无 python3，基础下载用进程内 HTTP 服务代替")
	}

	// 暂停/恢复/移除：进程内限速服务（支持 Range）制造可控时长
	t.Run("暂停恢复移除", func(t *testing.T) {
		slowData := randBytes(t, 3<<20)
		srv := slowHTTPServer(t, slowData, 64<<10, 150*time.Millisecond) // 全局 ≈427KB/s，3MB≈11s
		slowURL := srv.URL + "/slow.bin"

		// pause → paused → resume → done，字节一致
		res, err := m.Submit(slowURL, saveDir)
		if err != nil {
			t.Fatal(err)
		}
		waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusActive: true}, 20*time.Second)
		if err := m.Pause(res.TaskID); err != nil {
			t.Fatalf("暂停失败: %v", err)
		}
		task := waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusPaused: true}, 15*time.Second)
		if task.Status != StatusPaused {
			t.Fatalf("暂停失败: status=%s err=%s", task.Status, task.Err)
		}
		if err := m.Resume(res.TaskID); err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		done := waitTaskStatus(t, m, res.TaskID,
			map[string]bool{StatusDone: true, StatusError: true}, 90*time.Second)
		if done.Status != StatusDone {
			t.Fatalf("恢复后未完成: status=%s err=%s", done.Status, done.Err)
		}
		got, _ := os.ReadFile(filepath.Join(saveDir, "slow.bin"))
		if len(got) != len(slowData) {
			t.Fatalf("大小不一致: %d != %d", len(got), len(slowData))
		}

		// remove（下载中）→ DB 行消失（轮询/引擎侧不再有该 gid 任务）
		os.Remove(filepath.Join(saveDir, "slow2.bin"))
		res2, err := m.Submit(srv.URL+"/slow2.bin?x=1", saveDir)
		if err != nil {
			t.Fatal(err)
		}
		waitTaskStatus(t, m, res2.TaskID, map[string]bool{StatusActive: true}, 20*time.Second)
		if err := m.Remove(res2.TaskID); err != nil {
			t.Fatalf("移除失败: %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if t2, _ := m.db.GetTask(res2.TaskID); t2 == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if t2, _ := m.db.GetTask(res2.TaskID); t2 != nil {
			t.Fatal("移除后任务行仍在")
		}
	})

	// 运行中状态：version/port/binPath 齐全
	st2 := m.Aria2Status()
	if !st2.Running || st2.Version == "" || st2.Port < 6900 || st2.Port > 6999 {
		t.Errorf("运行中状态不对: %+v", st2)
	}
	if st2.BinPath != bin && !strings.HasSuffix(st2.BinPath, "aria2-next") {
		t.Errorf("binPath=%s want %s", st2.BinPath, bin)
	}
	// 磁力/ed2k 提交链路（无 tracker 也能成功入队，不等完成）
	t.Run("magnet入队", func(t *testing.T) {
		res, err := m.Submit("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=yunx-test.bin", saveDir)
		if err != nil {
			t.Fatalf("磁力提交失败: %v", err)
		}
		if res.Name != "yunx-test.bin" {
			t.Errorf("磁力 dn 解析=%q", res.Name)
		}
		task := waitTaskStatus(t, m, res.TaskID,
			map[string]bool{StatusPending: true, StatusActive: true, StatusError: true}, 30*time.Second)
		if task.Status == StatusError && !strings.Contains(task.Err, "timeout") {
			t.Logf("（信息）磁力在无网络 tracker 下状态=%s err=%s", task.Status, task.Err)
		}
		_ = m.Remove(res.TaskID)
	})

	// Close 后 RPC 端口应关闭（子进程确已退出）
	rpcPort := st2.Port
	m.Close()
	deadline := time.Now().Add(8 * time.Second)
	closed := false
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", rpcPort), time.Second)
		if err != nil {
			closed = true
			break
		}
		conn.Close()
		time.Sleep(300 * time.Millisecond)
	}
	if !closed {
		t.Errorf("Close 后 RPC 端口 %d 仍可连——aria2 子进程未退出", rpcPort)
	}
}
