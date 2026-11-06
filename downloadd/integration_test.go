package downloadd

import (
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// ---- 集成测试共用帮手（全部落 /tmp 隔离目录，绝不碰 ~/.yunx-wails 等真实目录）----

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return buf
}

// waitPort 等端口可连（服务就绪探测）。
func waitPort(t *testing.T, addr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// startProc 起外部进程（日志进 <dir>/proc-<tag>.log），失败时打印日志内容。
func startProc(t *testing.T, tag, dir string, name string, args ...string) *exec.Cmd {
	t.Helper()
	logF, err := os.Create(fmt.Sprintf("%s/proc-%s.log", dir, tag))
	if err != nil {
		t.Fatalf("建 %s 日志: %v", tag, err)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := cmd.Start(); err != nil {
		logF.Close()
		t.Fatalf("启动 %s: %v（%s %v）", tag, err, name, args)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return cmd
}

// waitTaskStatus 轮询 DB 等任务进入期望状态集合之一；返回终态任务；超时 Fatal。
func waitTaskStatus(t *testing.T, m *Manager, id int64, want map[string]bool, timeout time.Duration) *Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, err := m.db.GetTask(id)
		if err != nil || task == nil {
			t.Fatalf("任务 %d 消失: %v", id, err)
		}
		if want[task.Status] {
			return task
		}
		time.Sleep(200 * time.Millisecond)
	}
	task, _ := m.db.GetTask(id)
	t.Fatalf("等待任务 %d 状态 %v 超时（当前 %+v）", id, want, task)
	return nil
}

// findAria2Bin 测试环境定位 aria2-next（YUNX_ARIA2_BIN > 内置安装路径 > PATH）。
func findAria2Bin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("YUNX_ARIA2_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := home + "/.local/share/yunx-desktop/tools/aria2-next"
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("aria2-next"); err == nil {
		return p
	}
	return ""
}

func filesEqual(t *testing.T, a, b string) {
	t.Helper()
	da, err := os.ReadFile(a)
	if err != nil {
		t.Fatalf("读 %s: %v", a, err)
	}
	db, err := os.ReadFile(b)
	if err != nil {
		t.Fatalf("读 %s: %v", b, err)
	}
	if len(da) != len(db) {
		t.Fatalf("文件大小不一致: %s=%d %s=%d", a, len(da), b, len(db))
	}
	for i := range da {
		if da[i] != db[i] {
			t.Fatalf("文件内容不一致: 首个差异 offset=%d", i)
		}
	}
}
