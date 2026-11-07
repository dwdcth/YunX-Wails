package downloadd

// magnet_integration_test.go：磁力全链路集成测试——真实 aria2-next + 真实网络。
// 链路：MagnetResolve（元数据 → 文件清单）→ 选其中一个文件 → MagnetSubmit(select-file)
// → 等完成 → 磁盘校验。同时守护两处历史修复：
//   ① --bt-tracker 必须随启动参数生效（Hooks 无 Call 时 ApplyGlobalOptions 全程跳过，
//      getGlobalOption 里的 bt-tracker 只可能来自启动参数——正是 5c1489b 修的路径）；
//   ② select-file 选择性下载（多文件种子只落所选文件，且文件名是真名不是 hash）。
// 磁力源：Big Buck Bunny（Blender 开源电影，webtorrent 常驻示例种子，做种充足）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMagnetResolveAndDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("集成测试需外网（tracker/peer），-short 跳过")
	}
	bin := findAria2Bin(t)
	if bin == "" {
		t.Skip("未找到 aria2-next（设 YUNX_ARIA2_BIN 或装到 ~/.local/share/yunx-desktop/tools/），跳过")
	}
	t.Setenv("YUNX_ARIA2_BIN", bin)

	const magnet = "magnet:?xt=urn:btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&dn=Big%20Buck%20Bunny"

	dataDir := t.TempDir()
	m, err := Init(dataDir, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// dumpAria2LogOnFail 失败时带上 aria2 日志尾，第一现场不丢。
	dumpAria2LogOnFail := func() {
		if b, err := os.ReadFile(filepath.Join(dataDir, "aria2.log")); err == nil {
			lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
			if len(lines) > 80 {
				lines = lines[len(lines)-80:]
			}
			t.Logf("---- aria2.log 尾 %d 行 ----\n%s", len(lines), strings.Join(lines, "\n"))
		}
	}

	// ---- 1. 元数据解析（真实 tracker/DHT）----
	t.Logf("解析磁力: %s（DHT 冷启动可能需 1-3 分钟）", magnet)
	info, err := m.MagnetResolve(magnet)
	if err != nil {
		dumpAria2LogOnFail()
		t.Fatalf("磁力元数据解析失败: %v", err)
	}
	t.Logf("种子名: %s  文件数: %d  总大小: %s", info.Name, len(info.Files), humanSize(info.TotalLength))
	for _, f := range info.Files {
		t.Logf("  [%d] %s  (%s)", f.Index, f.Path, humanSize(f.Length))
	}
	if len(info.Files) == 0 {
		t.Fatal("文件清单为空")
	}

	// ---- 2. bt-tracker 启动参数守护断言 ----
	// 本测试 Hooks 无 Call → ApplyGlobalOptions 全程跳过 → getGlobalOption 里的
	// bt-tracker 只可能来自 --bt-tracker 启动参数（磁力元数据下载从第一秒就有 tracker）。
	opts, err := m.aria2.GetGlobalOption()
	if err != nil {
		t.Fatalf("getGlobalOption: %v", err)
	}
	if tr := opts["bt-tracker"]; tr == "" {
		dumpAria2LogOnFail()
		t.Fatal("bt-tracker 为空——启动参数未生效（回归：磁力元数据阶段没有 tracker）")
	} else {
		show := tr
		if len(show) > 120 {
			show = show[:120] + "…"
		}
		t.Logf("bt-tracker（启动参数生效）: %s", show)
	}

	// ---- 3. 选一个文件：多文件选最大的，单文件选它自己 ----
	sel := info.Files[0]
	for _, f := range info.Files {
		if f.Length > sel.Length {
			sel = f
		}
	}
	t.Logf("选定文件: [%d] %s (%s)", sel.Index, sel.Path, humanSize(sel.Length))

	// ---- 4. 提交（select-file 只下所选）----
	saveDir := filepath.Join(t.TempDir(), "downloads")
	res, err := m.MagnetSubmit(info.ResolveID, saveDir, []int{sel.Index}, "")
	if err != nil {
		dumpAria2LogOnFail()
		t.Fatalf("提交下载失败: %v", err)
	}
	t.Logf("已提交 taskId=%d engine=%s name=%s saveDir=%s", res.TaskID, res.Engine, res.Name, saveDir)

	// ---- 5. 等完成：10s 一报进度；downloaded 持续为 0 超过 4 分钟即判卡死 ----
	//（抓「磁力速度 0」回归，不干等完整超时）
	deadline := time.Now().Add(12 * time.Minute)
	zeroSince := time.Now()
	lastLog := time.Now()
	var done, last *Task
poll:
	for time.Now().Before(deadline) {
		task, err := m.db.GetTask(res.TaskID)
		if err != nil || task == nil {
			dumpAria2LogOnFail()
			t.Fatalf("任务 %d 消失: %v", res.TaskID, err)
		}
		last = task
		switch task.Status {
		case StatusDone:
			done = task
			break poll
		case StatusError:
			dumpAria2LogOnFail()
			t.Fatalf("下载失败: %s", task.Err)
		}
		if task.Downloaded > 0 {
			zeroSince = time.Now()
		} else if time.Since(zeroSince) > 4*time.Minute {
			dumpAria2LogOnFail()
			t.Fatalf("速度为 0 超过 4 分钟（downloaded=%d）——磁力 peer 连通性回归", task.Downloaded)
		}
		if time.Since(lastLog) >= 10*time.Second {
			t.Logf("下载中: %d%%  %s/%s  %s/s  status=%s", task.Progress,
				humanSize(task.Downloaded), humanSize(task.Size), humanSize(task.Speed), task.Status)
			lastLog = time.Now()
		}
		time.Sleep(2 * time.Second)
	}
	if done == nil {
		dumpAria2LogOnFail()
		t.Fatalf("下载超时未完成（最后状态 %+v）", last)
	}
	t.Logf("下载完成: %s  大小 %s  用时见上方进度日志", done.Name, humanSize(done.Size))

	// ---- 6. 磁盘校验：所选文件存在、文件名是真名、大小一致 ----
	if !walkFindSize(saveDir, sel.Path, sel.Length) {
		dumpAria2LogOnFail()
		listDirTree(t, saveDir)
		t.Fatalf("未在 %s 找到所选文件 %s（期望大小 %s）", saveDir, sel.Path, humanSize(sel.Length))
	}
	t.Logf("磁盘校验通过: %s 下 %s（%s）", saveDir, filepath.Base(sel.Path), humanSize(sel.Length))
}

// humanSize 字节数 → 人类可读（测试日志用）。
func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// walkFindSize 在 root 下找 basename 与所选文件一致且大小相符的落盘文件
//（单文件种子直接落 dir/，多文件落 dir/<种子名>/<相对路径>，按名匹配两种都覆盖）。
func walkFindSize(root, relPath string, want int64) bool {
	base := filepath.Base(strings.ReplaceAll(relPath, "\\", "/"))
	found := false
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(p) == base {
			if fi, e := d.Info(); e == nil && fi.Size() == want {
				found = true
			}
		}
		return nil
	})
	return found
}

// listDirTree 失败诊断：打印目录树。
func listDirTree(t *testing.T, root string) {
	t.Helper()
	t.Logf("---- %s 目录树 ----", root)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			t.Logf("  DIR  %s", p)
		} else if fi, e := d.Info(); e == nil {
			t.Logf("  FILE %s (%d)", p, fi.Size())
		}
		return nil
	})
}
