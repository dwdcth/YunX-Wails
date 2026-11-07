// pluginsys_test.go：单元 + 端到端测试。
//
// 真实页面 fixture：testdata/dytt.html（https://www.dytt8899.com/i/119283.html 实拉后
// 转 UTF-8 存档）与 testdata/dytt_gb2312.html（原始 gb2312 字节，测字符集归一）。
// 插件本体 fixture：testdata/dytt8899.user.js（与仓库 plugins/dytt8899.user.js 同源拷贝，
// go:embed 嵌入——测试不依赖数据目录）。
//
// 网络用例默认跳过：go test ./pluginsys/ -pluginsys.live 才实拉 dytt。
package pluginsys

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "embed"
)

//go:embed testdata/dytt.html
var fixtureHTML string

//go:embed testdata/dytt8899.user.js
var dyttPluginSrc string

var live = flag.Bool("pluginsys.live", false, "跑真实网络用例（实拉 dytt8899 详情页）")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

// ---- 元数据解析 ----

func TestParseUserScriptMeta(t *testing.T) {
	src := `// ==UserScript==
// @name         电影天堂解析
// @namespace    yunx/plugins
// @version      0.1.0
// @match        *://www.dytt8899.com/i/*
// @match        *://*.dytt8899.com/i/*
// @run-at       document-idle
// @grant        none
// ==/UserScript==
function yunx_parse(url) { return {ok:true}; }
`
	meta := ParseUserScriptMeta(src)
	if got := meta["name"]; len(got) != 1 || got[0] != "电影天堂解析" {
		t.Fatalf("name = %v", got)
	}
	if got := meta["match"]; len(got) != 2 {
		t.Fatalf("match 数量 = %v", got)
	}
	if got := meta["version"]; len(got) != 1 || got[0] != "0.1.0" {
		t.Fatalf("version = %v", got)
	}
	// 未消费键也透出
	if got := meta["run-at"]; len(got) != 1 || got[0] != "document-idle" {
		t.Fatalf("run-at = %v", got)
	}
	// 无元数据块 → 空 map
	if m := ParseUserScriptMeta("function yunx_parse(u){}"); len(m) != 0 {
		t.Fatalf("无块应返回空: %v", m)
	}
}

func TestMatchURL(t *testing.T) {
	cases := []struct {
		pattern, url string
		want         bool
	}{
		{"*://www.dytt8899.com/i/*", "https://www.dytt8899.com/i/119283.html", true},
		{"*://www.dytt8899.com/i/*", "https://www.dytt8899.com/i/119283/", true},
		{"*://www.dytt8899.com/i/*", "https://www.dytt8899.com/html/gndy/1.html", false},
		{"*://*.dytt8899.com/i/*", "https://m.dytt8899.com/i/1.html", true},
		{"https://a.com/*", "https://a.com/", true},
		{"https://a.com/*", "http://a.com/", false},
	}
	for _, c := range cases {
		if got := MatchURL(c.pattern, c.url); got != c.want {
			t.Errorf("MatchURL(%q, %q) = %v, want %v", c.pattern, c.url, got, c.want)
		}
	}
	if !Matches(nil, "https://anything") {
		t.Error("无 @match 应视为全匹配")
	}
}

func TestLoadPlugin(t *testing.T) {
	// 缺 yunx_parse 契约 → 拒载
	if _, err := LoadPlugin("bad.user.js", "// ==UserScript==\n// @name x\n// ==/UserScript==\n"); err == nil {
		t.Fatal("缺 yunx_parse 应报错")
	}
	// @name 缺省 → 用文件名
	p, err := LoadPlugin("fallback.user.js", "function yunx_parse(u){}")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "fallback" || p.ID != "fallback" || p.Version != "0.0.0" {
		t.Fatalf("fallback 元数据异常: %+v", p)
	}
	// 非法文件名（路径分隔符）→ 拒载
	if _, err := LoadPlugin("../evil.user.js", "function yunx_parse(u){}"); err == nil {
		t.Fatal("路径分隔符文件名应报错")
	}
}

// ---- DOM 兼容层（用 fixture 走完整 yunx_parse 栈）----
//
// 契约归一会丢掉非契约字段，探针脚本把断言数据 JSON.stringify 后借 title 字段
// 私带出来（走全公开 API 栈：fake fetcher → document 自动跟随 → DOM 查询 → 归一化）。

func domProbeScript(t *testing.T) string {
	t.Helper()
	return `
// ==UserScript==
// @name domprobe
// @match *://*/*
// ==/UserScript==
function yunx_parse(url) {
  var res = yunx_fetch(url);          // fake fetcher → fixture，document 已自动跟随
  if (res.status !== 200) return {ok:false, error:'fetch '+res.status};
  var out = {};
  out.pageTitle = document.title;
  var zoom = document.querySelector('#Zoom');
  out.hasZoom = zoom !== null;
  out.zoomTag = zoom ? zoom.tag : null;
  var zoomAttr = document.querySelector('div[id=Zoom]');
  out.zoomAttrId = zoomAttr ? zoomAttr.attr('id') : null;
  out.zoomAttrMissing = zoomAttr ? zoomAttr.attr('nope') : 'el-null';   // getAttribute 缺省 → null
  out.zoomTextHead = zoom ? zoom.textContent.replace(/\s+/g,' ').slice(0, 20) : '';
  out.zoomHTMLHead = zoom ? zoom.innerHTML.slice(0, 500) : '';
  var noel = document.querySelector('#not-exist');
  out.missingNull = noel === null;
  var byId = document.getElementById('downlist');
  out.getById = byId !== null && byId.attr('id') === 'downlist';
  out.getByIdUnsafe = document.getElementById('a b') === null;
  var magnet = document.querySelectorAll('a[href^="magnet:"]');
  out.magnetCount = magnet.length;
  var table = byId.querySelector('table');
  out.chained = table !== null && table.tag === 'table';
  out.chainedAll = table ? table.querySelectorAll('a').length : -1;
  out.bodyTag = document.body.tag;
  out.tagNameUpper = magnet.length ? magnet[0].tagName : '';
  out.consoleWorks = (function(){ try { console.log('hi', 1); return true; } catch(e){ return false; } })();
  out.gmWorks = (function(){ var got; GM_xmlhttpRequest({method:'GET', url:url, onload:function(r){ got = r.status; }}); return got === 200; })();
  var doc2 = yunx_dom('<html><head><title>t2</title></head><body><a href="ed2k://x">e</a></body></html>');
  out.explicitDoc = doc2.title + ':' + doc2.querySelectorAll('a').length;
  out.locationHref = location.href;
  return {ok:true, title: JSON.stringify(out), links: []};
}
`
}

func TestDOMShimOnFixture(t *testing.T) {
	res := runProbe(t, domProbeScript(t), fixtureHTML)
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Title), &out); err != nil {
		t.Fatalf("探针数据不合法: %v\n%s", err, res.Title)
	}
	check := func(key string, want any) {
		t.Helper()
		got, ok := out[key]
		if !ok || got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	if !strings.Contains(out["pageTitle"].(string), "寻秦记") {
		t.Errorf("pageTitle = %v", out["pageTitle"])
	}
	check("hasZoom", true)
	check("zoomTag", "div")
	check("zoomAttrId", "Zoom")
	check("zoomAttrMissing", nil)
	check("missingNull", true)
	check("getById", true)
	check("getByIdUnsafe", true)
	check("magnetCount", float64(1)) // JSON 往返后数字统一 float64
	check("chained", true)
	check("chainedAll", float64(1))
	check("bodyTag", "body")
	check("tagNameUpper", "A")
	check("consoleWorks", true)
	check("gmWorks", true)
	check("explicitDoc", "t2:1")
	check("locationHref", "https://www.dytt8899.com/i/119283.html")
	if s, _ := out["zoomTextHead"].(string); !strings.Contains(s, "译") {
		t.Errorf("zoomTextHead = %q", s)
	}
	// fixture 里 #Zoom 开头是 <!--Content Start--> 注释，<img> 在其后
	if s, _ := out["zoomHTMLHead"].(string); !strings.Contains(s, "<img") {
		t.Errorf("zoomHTMLHead = %q", s)
	}
}

// runProbe 通用跑法：临时插件目录写入脚本，fake fetcher 回 fixture，返回归一化契约结果。
func runProbe(t *testing.T, src, body string) ParseResult {
	t.Helper()
	m := NewManager(t.TempDir())
	writePlugin(t, m, "probe.user.js", src)
	m.SetFetcher(func(url string) (*FetchResult, error) {
		return &FetchResult{Status: 200, Headers: map[string]string{"content-type": "text/html"}, Body: body}, nil
	})
	raw, err := m.Parse("probe", "https://www.dytt8899.com/i/119283.html")
	if err != nil {
		t.Fatal(err)
	}
	var res ParseResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("契约 JSON 不合法: %v\n%s", err, raw)
	}
	if !res.OK {
		t.Fatalf("probe 失败: %s", res.Error)
	}
	return res
}

func writePlugin(t *testing.T, m *Manager, name, src string) {
	t.Helper()
	dir := m.PluginDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- dytt8899 插件端到端（fixture，无网络）----

func TestDyttPluginEndToEnd(t *testing.T) {
	m := NewManager(t.TempDir())
	writePlugin(t, m, "dytt8899.user.js", dyttPluginSrc)
	m.SetFetcher(func(url string) (*FetchResult, error) {
		return &FetchResult{Status: 200, Headers: map[string]string{"content-type": "text/html; charset=gb2312"}, Body: fixtureHTML}, nil
	})

	// List 能看到（含元数据）
	plugins, problems := m.List()
	if len(problems) > 0 {
		t.Fatalf("插件目录告警: %v", problems)
	}
	if len(plugins) != 1 || plugins[0].ID != "dytt8899" {
		t.Fatalf("List = %+v", plugins)
	}
	p := plugins[0]
	if p.Name != "电影天堂解析 (dytt8899.com)" || p.Version != "0.1.0" {
		t.Errorf("元数据: name=%q version=%q", p.Name, p.Version)
	}
	if !Matches(p.Match, "https://www.dytt8899.com/i/119283.html") {
		t.Errorf("@match 未命中: %v", p.Match)
	}

	raw, err := m.Parse("dytt8899", "https://www.dytt8899.com/i/119283.html")
	if err != nil {
		t.Fatal(err)
	}
	var res ParseResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("契约 JSON: %v\n%s", err, raw)
	}
	if !res.OK {
		t.Fatalf("解析失败: %s", res.Error)
	}
	if res.Title != "寻秦记2025" {
		t.Errorf("title = %q, want 寻秦记2025", res.Title)
	}
	if len(res.Links) < 1 {
		t.Fatalf("links 为空")
	}
	var magnet, ftp int
	for _, l := range res.Links {
		t.Logf("link: %s | %s", l.URL, l.Name)
		switch {
		case strings.HasPrefix(l.URL, "magnet:"):
			magnet++
			if !strings.Contains(l.URL, "btih:727d706c60e583031dca6a59f6dfde0ca3f4bca6") {
				t.Errorf("magnet btih 异常: %s", l.URL)
			}
			if !strings.Contains(l.Name, "寻秦记") {
				t.Errorf("magnet name(dn=) 未取到: %q", l.Name)
			}
		case strings.HasPrefix(l.URL, "ftp://"):
			ftp++
			if !strings.Contains(l.URL, "a.gbl.114s.com") {
				t.Errorf("ftp 链接异常: %s", l.URL)
			}
		default:
			t.Errorf("意外链接类型: %s", l.URL)
		}
	}
	if magnet != 1 || ftp != 1 {
		t.Errorf("期望 1 magnet + 1 ftp，实得 magnet=%d ftp=%d", magnet, ftp)
	}
}

func TestParsePluginMissing(t *testing.T) {
	m := NewManager(t.TempDir())
	raw, err := m.Parse("nope", "https://x/")
	if err != nil {
		t.Fatalf("缺插件应走 ok:false JSON 而非 error: %v", err)
	}
	if !strings.Contains(raw, `"ok":false`) || !strings.Contains(raw, "插件不存在") {
		t.Errorf("raw = %s", raw)
	}
}

func TestParseTimeoutInterrupt(t *testing.T) {
	old := parseTimeout
	parseTimeout = 300 * time.Millisecond
	defer func() { parseTimeout = old }()

	m := NewManager(t.TempDir())
	writePlugin(t, m, "loop.user.js", "function yunx_parse(u){ while(true){} }")
	raw, err := m.Parse("loop", "https://x/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "超时") && !strings.Contains(raw, "interrupt") {
		t.Errorf("应报超时: %s", raw)
	}
}

// ---- fetch：字符集归一 ----

//go:embed testdata/dytt_gb2312.html
var fixtureGB []byte

func TestDecodeGBK(t *testing.T) {
	got := decodeToUTF8(fixtureGB, "text/html; charset=gb2312")
	if !strings.Contains(got, "寻秦记") {
		t.Error("header 声明 gb2312 未归一到 UTF-8")
	}
	// 无 header 声明 → meta 嗅探兜底
	got = decodeToUTF8(fixtureGB, "")
	if !strings.Contains(got, "寻秦记") {
		t.Error("meta 嗅探 gb2312 未归一到 UTF-8")
	}
	// UTF-8 直通
	if got := decodeToUTF8([]byte("<title>ok</title>"), ""); got != "<title>ok</title>" {
		t.Errorf("UTF-8 应原样: %q", got)
	}
	// BOM 剥除
	if got := decodeToUTF8([]byte("\xEF\xBB\xBF<title>ok</title>"), ""); got != "<title>ok</title>" {
		t.Errorf("BOM 未剥除: %q", got)
	}
}

func TestFetchSchemeGuard(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://x/f", "magnet:?xt=1", "javascript:alert(1)"} {
		if _, err := httpFetch(u); err == nil {
			t.Errorf("%s 应被协议白名单拒绝", u)
		}
	}
}

// ---- 真实网络（默认跳过：go test ./pluginsys/ -pluginsys.live）----

func TestLiveDytt8899(t *testing.T) {
	if !*live {
		t.Skip("默认跳过真实网络用例；启用: go test ./pluginsys/ -pluginsys.live")
	}
	m := NewManager(t.TempDir())
	writePlugin(t, m, "dytt8899.user.js", dyttPluginSrc)

	raw, err := m.Parse("dytt8899", "https://www.dytt8899.com/i/119283.html")
	if err != nil {
		t.Fatal(err)
	}
	var res ParseResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("契约 JSON: %v\n%s", err, raw)
	}
	if !res.OK {
		t.Fatalf("实测解析失败: %s", res.Error)
	}
	t.Logf("实测 title=%q links=%d", res.Title, len(res.Links))
	for _, l := range res.Links {
		t.Logf("实测 link: %s | %s", l.URL, l.Name)
	}
	if len(res.Links) < 1 {
		t.Fatal("实测 links 为空")
	}
}
