// removefile_integration_test.go：RemoveFile（移除任务同时删除本地文件）集成测试——
// aria2 引擎（真实 aria2-next 子进程 + 本地 http.server）与 FTP 引擎（pyftpdlib fixture）。
// 数据全部落 t.TempDir()（/tmp 下），不碰 ~/.yunx-wails 等真实目录。
package downloadd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// assertFileGone 断言文件已不存在（被 RemoveFile 删除）。
func assertFileGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("文件仍存在: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

// assertRowGone 断言任务行已从库里删除。
func assertRowGone(t *testing.T, m *Manager, id int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if t2, _ := m.db.GetTask(id); t2 == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("移除后任务行仍在: id=%d", id)
}

// TestAria2RemoveFile aria2 引擎：下载完成后 RemoveFile → 任务行消失且本地文件被删。
// 用真实 aria2-next（YUNX_ARIA2_BIN > 内置工具目录 > PATH）；文件源用本地 python
// http.server（无 python3 时退回进程内 httptest 服务）。
func TestAria2RemoveFile(t *testing.T) {
	bin := findAria2Bin(t)
	if bin == "" {
		t.Skip("未找到 aria2-next（设 YUNX_ARIA2_BIN 或装到 ~/.local/share/yunx-desktop/tools/），跳过")
	}
	t.Setenv("YUNX_ARIA2_BIN", bin)

	dataDir := t.TempDir()
	saveDir := filepath.Join(dataDir, "save")
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		t.Fatal(err)
	}

	m, err := Init(dataDir, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// 本地文件服务：python3 -m http.server 优先（真实独立进程），无 python3 用进程内服务
	want := randBytes(t, 1<<20)
	fileName := "rmfile.bin"
	var url string
	if _, err := exec.LookPath("python3"); err == nil {
		www := filepath.Join(dataDir, "www")
		if err := os.MkdirAll(www, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(www, fileName), want, 0644); err != nil {
			t.Fatal(err)
		}
		port := 18241
		logDir := t.TempDir()
		startProc(t, "httpserver-rmfile", logDir, "python3", "-m", "http.server",
			strconv.Itoa(port), "-d", www)
		if !waitPort(t, fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second) {
			t.Fatal("python http.server 未就绪")
		}
		url = fmt.Sprintf("http://127.0.0.1:%d/%s", port, fileName)
	} else {
		t.Log("无 python3，用进程内 HTTP 服务代替")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(want)))
			_, _ = w.Write(want)
		}))
		t.Cleanup(srv.Close)
		url = srv.URL + "/" + fileName
	}

	// 下载完成 → 文件在 → RemoveFile → 行与文件都消失
	res, err := m.Submit(url, saveDir)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if res.Engine != EngineAria2 {
		t.Fatalf("engine=%s want aria2", res.Engine)
	}
	task := waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusDone: true, StatusError: true}, 60*time.Second)
	if task.Status != StatusDone {
		t.Fatalf("任务未完成: status=%s err=%s", task.Status, task.Err)
	}
	path := filepath.Join(saveDir, fileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("完成后应有本地文件 %s: %v", path, err)
	}

	if err := m.RemoveFile(res.TaskID); err != nil {
		t.Fatalf("RemoveFile 失败: %v", err)
	}
	assertRowGone(t, m, res.TaskID)
	assertFileGone(t, path)

	// 幂等：任务已不存在时再删不算错
	if err := m.RemoveFile(res.TaskID); err != nil {
		t.Fatalf("重复 RemoveFile 应幂等成功: %v", err)
	}
}

// TestFTPRemoveFile FTP 引擎：下载完成后 RemoveFile → 任务行消失且本地文件被删
//（pyftpdlib fixture，复用 ftp_integration_test 的服务起法）。
func TestFTPRemoveFile(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("无 python3，跳过 FTP 引擎实测")
	}
	if err := exec.Command("python3", "-c", "import pyftpdlib").Run(); err != nil {
		t.Skip("无 pyftpdlib（pip install --user pyftpdlib），跳过 FTP 引擎实测")
	}

	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "ftp-root")
	saveDir := filepath.Join(dataDir, "save")
	for _, d := range []string{root, saveDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	want := randBytes(t, 1<<20)
	if err := os.WriteFile(filepath.Join(root, "rmfile.bin"), want, 0644); err != nil {
		t.Fatal(err)
	}

	port := 22131
	logDir := t.TempDir()
	startProc(t, "pyftp-rmfile", logDir, "python3", "-m", "pyftpdlib",
		"-p", strconv.Itoa(port), "-d", root, "-u", "t", "-P", "t", "-w")
	if !waitPort(t, fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second) {
		t.Fatal("pyftpdlib 未就绪")
	}

	m, err := Init(dataDir, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	res, err := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/rmfile.bin", port), saveDir)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if res.Engine != EngineFTP {
		t.Fatalf("engine=%s want ftp", res.Engine)
	}
	task := waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusDone: true, StatusError: true}, 60*time.Second)
	if task.Status != StatusDone {
		t.Fatalf("任务未完成: status=%s err=%s", task.Status, task.Err)
	}
	if task.SavePath == "" {
		t.Fatal("完成后 save_path 未回填")
	}
	if _, err := os.Stat(task.SavePath); err != nil {
		t.Fatalf("完成后应有本地文件 %s: %v", task.SavePath, err)
	}

	if err := m.RemoveFile(res.TaskID); err != nil {
		t.Fatalf("RemoveFile 失败: %v", err)
	}
	assertRowGone(t, m, res.TaskID)
	assertFileGone(t, task.SavePath)

	// 幂等：任务已不存在时再删不算错（savePath 空或文件缺失均不报错）
	if err := m.RemoveFile(res.TaskID); err != nil {
		t.Fatalf("重复 RemoveFile 应幂等成功: %v", err)
	}
}
