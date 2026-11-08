package pluginsys

// pluginsys_net_test.go：真实网络集成测试——电影天堂插件解析真实详情页。
// 站点是 GBK 编码 + 可能有反爬，这里守护的是「插件脚本 + fetch 转码 + DOM 兼容层」全链路。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDyttParseRealPage 电影天堂真实详情页解析：标题 + 下载链接（magnet/ftp/ed2k/torrent）。
func TestDyttParseRealPage(t *testing.T) {
	if testing.Short() {
		t.Skip("集成测试需外网，-short 跳过")
	}
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, PluginDirName), 0755); err != nil {
		t.Fatal(err)
	}
	// 仓库自带插件源（ ../../../plugins/dytt8899.user.js 相对本文件）
	src, err := os.ReadFile(filepath.Join("..", "plugins", "dytt8899.user.js"))
	if err != nil {
		t.Skipf("未找到仓库插件源: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, PluginDirName, "dytt8899.user.js"), src, 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(dataDir)
	plugins, problems := m.List()
	for _, p := range problems {
		t.Logf("插件告警: %s", p)
	}
	if len(plugins) == 0 {
		t.Fatal("插件未加载")
	}
	t.Logf("已加载插件: id=%s name=%s v%s match=%v", plugins[0].ID, plugins[0].Name, plugins[0].Version, plugins[0].Match)

	// 页面可用 YUNX_DYTT_PAGE 覆盖（换一部片验证时免改代码）
	page := "https://www.dytt8899.com/i/119463.html"
	if v := os.Getenv("YUNX_DYTT_PAGE"); v != "" {
		page = v
	}
	if !Matches(plugins[0].Match, page) {
		t.Fatalf("@match 未命中 %s（match=%v）", page, plugins[0].Match)
	}
	raw, err := m.Parse(plugins[0].ID, page)
	if err != nil {
		t.Fatalf("Parse 执行错误: %v", err)
	}
	var res ParseResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("契约 JSON 不合法: %v\n%s", err, raw)
	}
	t.Logf("解析结果: ok=%v title=%q links=%d error=%q", res.OK, res.Title, len(res.Links), res.Error)
	if !res.OK {
		t.Fatalf("解析失败: %s", res.Error)
	}
	if res.Title == "" {
		t.Error("标题为空")
	}
	if len(res.Links) == 0 {
		t.Fatal("未解析出下载链接")
	}
	proto := regexp.MustCompile(`(?i)^(magnet:|ftp://|ed2k:|https?://.*\.torrent)`)
	for _, l := range res.Links {
		t.Logf("  链接: %s  名字: %q", l.URL, l.Name)
		if !proto.MatchString(l.URL) {
			t.Errorf("链接协议不认识: %s", l.URL)
		}
		if strings.Contains(l.URL, "jianpian://") {
			t.Errorf("jianpian:// 剥壳失败: %s", l.URL)
		}
	}
}
