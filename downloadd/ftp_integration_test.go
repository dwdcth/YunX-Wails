package downloadd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// throttleFTPScript 用 pyftpdlib API 写的限速 FTP 服务（CLI 版无 --ratelimit），
// 用于「下载中途暂停/续传/移除」这类需要传输可观测时长的用例。
const throttleFTPScript = `import os, sys
from pyftpdlib.authorizers import DummyAuthorizer
from pyftpdlib.handlers import FTPHandler, ThrottledDTPHandler
from pyftpdlib.servers import FTPServer

root, port, limit_kbs = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
authorizer = DummyAuthorizer()
authorizer.add_user("t", "t", root, perm="elradfmw")
handler = FTPHandler
handler.authorizer = authorizer
handler.passive_ports = None
dtp = ThrottledDTPHandler
dtp.write_limit = limit_kbs * 1024  # 服务器下行（=客户端下载）限速
handler.dtp_handler = dtp
FTPServer(("127.0.0.1", port), handler).serve_forever()
`

func TestFTPEngineDownload(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("无 python3，跳过 FTP 引擎实测")
	}
	// pyftpdlib 可用性检查
	if err := exec.Command("python3", "-c", "import pyftpdlib").Run(); err != nil {
		t.Skip("无 pyftpdlib（pip install --user pyftpdlib），跳过 FTP 引擎实测")
	}

	dataDir := t.TempDir()          // 任务库/aria2 state 隔离目录
	root := filepath.Join(dataDir, "ftp-root")
	saveDir := filepath.Join(dataDir, "save")
	for _, d := range []string{root, saveDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// 5MB 随机文件
	want := randBytes(t, 5<<20)
	if err := os.WriteFile(filepath.Join(root, "big5mb.bin"), want, 0644); err != nil {
		t.Fatal(err)
	}

	port := 22121
	logDir := t.TempDir()
	startProc(t, "pyftp", logDir, "python3", "-m", "pyftpdlib",
		"-p", strconv.Itoa(port), "-d", root, "-u", "t", "-P", "t", "-w")
	if !waitPort(t, fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second) {
		t.Fatal("pyftpdlib 未就绪")
	}

	m, err := Init(dataDir, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	t.Run("完整下载", func(t *testing.T) {
		res, err := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/big5mb.bin", port), saveDir)
		if err != nil {
			t.Fatalf("提交失败: %v", err)
		}
		if res.Engine != EngineFTP {
			t.Fatalf("engine = %s, want ftp", res.Engine)
		}
		task := waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusDone: true, StatusError: true}, 60*time.Second)
		if task.Status != StatusDone {
			t.Fatalf("任务未完成: status=%s err=%s", task.Status, task.Err)
		}
		if task.Size != int64(len(want)) || task.Downloaded != int64(len(want)) || task.Progress != 100 {
			t.Errorf("进度字段不对: %+v", task)
		}
		filesEqual(t, filepath.Join(saveDir, "big5mb.bin"), filepath.Join(root, "big5mb.bin"))
	})

	t.Run("断点续传", func(t *testing.T) {
		// 同内容第二份远端文件；本地预写前一半 → 应从 offset 续传而不是重下
		if err := os.WriteFile(filepath.Join(root, "resume.bin"), want, 0644); err != nil {
			t.Fatal(err)
		}
		half := len(want) / 2
		if err := os.WriteFile(filepath.Join(saveDir, "resume.bin"), want[:half], 0644); err != nil {
			t.Fatal(err)
		}
		res, err := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/resume.bin", port), saveDir)
		if err != nil {
			t.Fatal(err)
		}
		task := waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusDone: true, StatusError: true}, 60*time.Second)
		if task.Status != StatusDone {
			t.Fatalf("续传任务失败: status=%s err=%s", task.Status, task.Err)
		}
		filesEqual(t, filepath.Join(saveDir, "resume.bin"), filepath.Join(root, "resume.bin"))
	})

	t.Run("排队中暂停与恢复", func(t *testing.T) {
		// 两个 worker 先被限速任务占满，第三个任务停在 pending → Pause 应直接置 paused
		slowPort := 22122
		slowRoot := filepath.Join(dataDir, "ftp-slow")
		os.MkdirAll(slowRoot, 0755)
		os.WriteFile(filepath.Join(slowRoot, "slow1.bin"), randBytes(t, 1<<20), 0644)
		os.WriteFile(filepath.Join(slowRoot, "slow2.bin"), randBytes(t, 1<<20), 0644)
		os.WriteFile(filepath.Join(slowRoot, "slow3.bin"), randBytes(t, 1<<20), 0644)
		script := filepath.Join(dataDir, "slow_ftp.py")
		if err := os.WriteFile(script, []byte(throttleFTPScript), 0644); err != nil {
			t.Fatal(err)
		}
		startProc(t, "pyftp-slow", logDir, "python3", script, slowRoot, strconv.Itoa(slowPort), "200")
		if !waitPort(t, fmt.Sprintf("127.0.0.1:%d", slowPort), 10*time.Second) {
			t.Fatal("限速 FTP 未就绪")
		}
		// 1MB @200KB/s ≈ 5s，前两个占满 worker，第三个排队
		r1, _ := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/slow1.bin", slowPort), saveDir)
		r2, _ := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/slow2.bin", slowPort), saveDir)
		id1, id2 := r1.TaskID, r2.TaskID
		res3, err := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/slow3.bin", slowPort), saveDir)
		if err != nil {
			t.Fatal(err)
		}
		waitTaskStatus(t, m, res3.TaskID, map[string]bool{StatusPending: true}, 10*time.Second) // 排队中

		if err := m.Pause(res3.TaskID); err != nil {
			t.Fatalf("暂停排队任务失败: %v", err)
		}
		task := waitTaskStatus(t, m, res3.TaskID, map[string]bool{StatusPaused: true}, 10*time.Second)
		if task.Status != StatusPaused {
			t.Fatalf("排队任务暂停后 status=%s", task.Status)
		}
		if err := m.Resume(res3.TaskID); err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		// 三个任务最终都要完成（1MB@200KB/s 每个约 5s，两 worker → 总预算 60s）
		for _, id := range []int64{id1, id2, res3.TaskID} {
			done := waitTaskStatus(t, m, id, map[string]bool{StatusDone: true, StatusError: true}, 60*time.Second)
			if done.Status != StatusDone {
				t.Errorf("任务 %d: status=%s err=%s", id, done.Status, done.Err)
			}
		}
	})

	t.Run("下载中暂停移除", func(t *testing.T) {
		rmPort := 22123
		rmRoot := filepath.Join(dataDir, "ftp-rm")
		os.MkdirAll(rmRoot, 0755)
		os.WriteFile(filepath.Join(rmRoot, "rm.bin"), randBytes(t, 2<<20), 0644)
		script := filepath.Join(dataDir, "rm_ftp.py")
		os.WriteFile(script, []byte(throttleFTPScript), 0644)
		startProc(t, "pyftp-rm", logDir, "python3", script, rmRoot, strconv.Itoa(rmPort), "300")
		if !waitPort(t, fmt.Sprintf("127.0.0.1:%d", rmPort), 10*time.Second) {
			t.Fatal("限速 FTP(移除用) 未就绪")
		}
		// 暂停（下载中）
		res, err := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/rm.bin", rmPort), saveDir)
		if err != nil {
			t.Fatal(err)
		}
		waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusActive: true}, 10*time.Second)
		if err := m.Pause(res.TaskID); err != nil {
			t.Fatalf("暂停失败: %v", err)
		}
		task := waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusPaused: true}, 15*time.Second)
		if task.Status != StatusPaused {
			t.Fatalf("下载中暂停失败: status=%s err=%s", task.Status, task.Err)
		}
		if st, err := os.Stat(filepath.Join(saveDir, "rm.bin")); err == nil && st.Size() >= 2<<20 {
			t.Errorf("暂停时应只下了部分: %d", st.Size())
		}
		// 恢复 → 完成
		if err := m.Resume(res.TaskID); err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		done := waitTaskStatus(t, m, res.TaskID, map[string]bool{StatusDone: true, StatusError: true}, 60*time.Second)
		if done.Status != StatusDone {
			t.Fatalf("恢复后未完成: status=%s err=%s", done.Status, done.Err)
		}
		filesEqual(t, filepath.Join(saveDir, "rm.bin"), filepath.Join(rmRoot, "rm.bin"))

		// 移除（下载中）→ 行消失，文件保留
		os.Remove(filepath.Join(saveDir, "rm2.bin")) // 清理可能的残留
		os.WriteFile(filepath.Join(rmRoot, "rm2.bin"), randBytes(t, 2<<20), 0644)
		res2, _ := m.Submit(fmt.Sprintf("ftp://t:t@127.0.0.1:%d/rm2.bin", rmPort), saveDir)
		waitTaskStatus(t, m, res2.TaskID, map[string]bool{StatusActive: true}, 10*time.Second)
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
			t.Fatalf("移除后任务行仍在: %+v", t2)
		}
		time.Sleep(1 * time.Second) // 给 worker 落盘时间
		if _, err := os.Stat(filepath.Join(saveDir, "rm2.bin")); err != nil {
			t.Logf("（信息）移除时本地尚无部分文件: %v", err)
		}
	})
}
