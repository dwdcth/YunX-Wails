package downloadd

// dytt_download_test.go：插件解析 → 普通下载引擎 全链路集成测试（真实网络）。
// 电影天堂详情页 → pluginsys 插件解析出磁力链接 → 迅雷式磁力流程（MagnetResolve →
// MagnetSubmit）→ 真实下载进度。守护「解析出的链接真的能下」这一端到端契约。
//
// 种子健康度是外部因素：磁力无种（peer 不交换元数据）→ Skip 而非 Fail——
// 站点资源死链不是代码回归（119463《用武之地》实测即死链：FTP 假链 EOF + 磁力无种；
// 119809《2010》蓝光有种可下）。默认页选老片（做种年头长），可用 YUNX_DYTT_PAGE 换页。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yunxwailsdemo/pluginsys"
)

func TestDyttParseAndDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("集成测试需外网，-short 跳过")
	}
	bin := findAria2Bin(t)
	if bin == "" {
		t.Skip("未找到 aria2-next，跳过")
	}
	t.Setenv("YUNX_ARIA2_BIN", bin)

	page := "https://www.dytt8899.com/i/119809.html"
	if v := os.Getenv("YUNX_DYTT_PAGE"); v != "" {
		page = v
	}

	// ---- 1. 插件解析真实详情页 ----
	pdata := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pdata, pluginsys.PluginDirName), 0755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "plugins", "dytt8899.user.js"))
	if err != nil {
		t.Skipf("未找到仓库插件源: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pdata, pluginsys.PluginDirName, "dytt8899.user.js"), src, 0644); err != nil {
		t.Fatal(err)
	}
	pm := pluginsys.NewManager(pdata)
	raw, err := pm.Parse("dytt8899", page)
	if err != nil {
		t.Fatalf("插件解析执行错误: %v", err)
	}
	var pres pluginsys.ParseResult
	if err := json.Unmarshal([]byte(raw), &pres); err != nil {
		t.Fatalf("契约 JSON 不合法: %v", err)
	}
	if !pres.OK || len(pres.Links) == 0 {
		t.Skipf("解析失败: %s（站点可能已改版/下架）", pres.Error)
	}
	t.Logf("片名: %s  链接数: %d", pres.Title, len(pres.Links))
	var magnet string
	for _, l := range pres.Links {
		t.Logf("  [%s] %s", l.Name, l.URL)
		if magnet == "" && strings.HasPrefix(strings.ToLower(l.URL), "magnet:") {
			magnet = l.URL
		}
	}
	if magnet == "" {
		t.Skip("页面无磁力链接（改版？）")
	}

	// ---- 2. 迅雷式磁力流程：取元数据（无种 Skip——外部因素不是回归）----
	dataDir := t.TempDir()
	m, err := Init(dataDir, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	info, err := m.MagnetResolve(magnet)
	if err != nil {
		t.Skipf("元数据解析失败（种子可能无源，非代码问题）: %v", err)
	}
	t.Logf("种子: %s  文件数: %d  总大小: %s", info.Name, len(info.Files), humanSize(info.TotalLength))
	for _, f := range info.Files {
		t.Logf("  [%d] %s (%s)", f.Index, f.Path, humanSize(f.Length))
	}

	// ---- 3. 全选提交 → 真实进度（>512KiB 即证明端到端数据流；跨境 peer 可能很慢）----
	saveDir := filepath.Join(t.TempDir(), "downloads")
	res, err := m.MagnetSubmit(info.ResolveID, saveDir, nil, "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	t.Logf("已提交 taskId=%d engine=%s name=%s", res.TaskID, res.Engine, res.Name)
	const want = 512 << 10
	deadline := time.Now().Add(6 * time.Minute)
	zeroSince := time.Now()
	var last *Task
	for time.Now().Before(deadline) {
		task, err := m.db.GetTask(res.TaskID)
		if err != nil || task == nil {
			t.Fatalf("任务消失: %v", err)
		}
		last = task
		if task.Status == StatusError {
			t.Fatalf("下载失败: %s", task.Err)
		}
		if task.Downloaded > want {
			break
		}
		if task.Downloaded > 0 {
			zeroSince = time.Now()
		} else if time.Since(zeroSince) > 4*time.Minute {
			t.Fatalf("速度为 0 超过 4 分钟——磁力 peer 连通性回归")
		}
		time.Sleep(2 * time.Second)
	}
	if last == nil || last.Downloaded <= want {
		t.Fatalf("进度不足（downloaded=%d status=%s）", last.Downloaded, last.Status)
	}
	t.Logf("下载验证通过: %s/%s  %s/s  status=%s（不下载完整文件）",
		humanSize(last.Downloaded), humanSize(last.Size), humanSize(last.Speed), last.Status)

	// ---- 4. 清理（停任务 + 删半成品文件）----
	if err := m.RemoveFile(res.TaskID); err != nil {
		t.Logf("清理告警: %v", err)
	}
}
