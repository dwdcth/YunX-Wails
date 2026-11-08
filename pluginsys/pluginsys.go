// pluginsys.go：类油猴（userscript）插件系统 MVP——纯 Go 包，不含 Wails 依赖。
//
// 一个插件 = <数据目录>/plugins/*.user.js 一个文件，需含 ==UserScript== 元数据块
// （至少 @name + @match）并定义全局函数 yunx_parse(url)。执行模型：
//
//	PluginParse(id, url) → 每次调用新建一个 goja Runtime（无 os/io/fs/require 暴露，
//	脚本之间零共享）→ 注入宿主 API（yunx_fetch/yunx_log/DOM 兼容层/GM_* 兼容层）
//	→ 跑脚本 → 调 yunx_parse(url) → 归一化成 ParseResult JSON。
//
// 由此「新站点解析」退化为「丢一个 .user.js 进插件目录」，无需改 Go 代码、无需重启。
package pluginsys

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// PluginDirName 插件目录名（挂在数据目录下）。
const PluginDirName = "plugins"

// parseTimeout 单次 yunx_parse 的脚本执行上限（含死循环兜底：Interrupt 掐断）。
// var 而非 const：测试里缩短后验证超时机制。
var parseTimeout = 30 * time.Second

// Plugin 已加载的插件（元数据 + 脚本源码）。
type Plugin struct {
	ID        string   `json:"id"`        // 文件名去扩展名（目录内唯一，PluginParse 用它定位）
	Name      string   `json:"name"`      // @name（缺省用文件名）
	Namespace string   `json:"namespace"` // @namespace（缺省空；Tampermonkey 里用于全局唯一，这里仅透出）
	Version   string   `json:"version"`   // @version（缺省 "0.0.0"）
	Match     []string `json:"match"`     // @match（+@include 别名），glob 通配；给 UI 自动选插件用
	Enabled   bool     `json:"enabled"`   // false = 禁用态（文件 .off，不参与解析匹配）
	File      string   `json:"file"`      // 绝对路径
	Source    string   `json:"-"`         // 脚本全文（含元数据块，直接整段跑，块是注释无副作用）
}

// Link 插件解析出的下载链接（喂给普通下载引擎的最终形状）。
type Link struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
	Size string `json:"size,omitempty"`
}

// ParseResult yunx_parse 契约：ok 必填；成功时 title + links；失败时 error。
type ParseResult struct {
	OK    bool   `json:"ok"`
	Title string `json:"title,omitempty"`
	Links []Link `json:"links,omitempty"`
	Error string `json:"error,omitempty"`
}

// Manager 插件管理器：插件目录惰性重扫（每次 List/Parse 重新读目录——解析是纯文本
// 正则，代价可忽略，换来「丢文件即生效」的免重启体验）。并发安全。
type Manager struct {
	dataDir string
	// fetcher 拉取器（默认 httpFetch；测试注入 fixture）。
	fetcher func(url string) (*FetchResult, error)
	// logger 日志落地（默认标准 log；宿主可换）。
	logger *log.Logger
}

// NewManager 数据目录为空时按宿主同源规则解析（YUNX_DESKTOP_DATA_DIR → ~/.yunx-wails）。
func NewManager(dataDir string) *Manager {
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	return &Manager{
		dataDir: dataDir,
		fetcher: httpFetch,
		logger:  log.New(os.Stderr, "[plugin] ", log.LstdFlags|log.Lmsgprefix),
	}
}

// SetFetcher 替换拉取器（测试注入用）。
func (m *Manager) SetFetcher(f func(url string) (*FetchResult, error)) { m.fetcher = f }

// SetLogger 替换日志落地（宿主对接自身日志体系用）。
func (m *Manager) SetLogger(l *log.Logger) {
	if l != nil {
		m.logger = l
	}
}

// defaultDataDir 与 downloadd/main 同源的数据目录解析。
func defaultDataDir() string {
	if v := os.Getenv("YUNX_DESKTOP_DATA_DIR"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".yunx-wails")
	}
	return "."
}

// PluginDir 插件目录绝对路径。
func (m *Manager) PluginDir() string { return filepath.Join(m.dataDir, PluginDirName) }

// loadPlugins 扫描插件目录并解析元数据。目录不存在视为「无插件」（空列表，非错误）。
// 文件按名排序保证列表稳定；单个文件解析失败跳过（记日志，不拖垮整目录）。
func (m *Manager) loadPlugins() ([]*Plugin, []string) {
	dir := m.PluginDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // 目录不存在/不可读：无插件
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".user.js") || strings.HasSuffix(name, ".user.js.off") {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	plugins := make([]*Plugin, 0, len(names))
	var problems []string
	for _, name := range names {
		enabled := !strings.HasSuffix(name, ".off")
		loadName := strings.TrimSuffix(name, ".off") // LoadPlugin 的 id=文件名去 .user.js
		path := filepath.Join(dir, name)             // 路径由目录列举拼出，pluginId 只做白名单查表——无穿越面
		src, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: 读取失败 %v", name, err))
			continue
		}
		p, err := LoadPlugin(loadName, string(src))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		p.File = path
		p.Enabled = enabled
		plugins = append(plugins, p)
	}
	return plugins, problems
}

// List 插件清单（problems 为目录内不可用文件的告警，随调用透出给日志）。
func (m *Manager) List() ([]*Plugin, []string) { return m.loadPlugins() }

// FindByID 按 ID 定位插件（ID=文件名去 .user.js）。
func (m *Manager) FindByID(id string) *Plugin {
	plugins, _ := m.loadPlugins()
	for _, p := range plugins {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Install 安装/更新插件：写入 <插件目录>/<id>.user.js（LoadPlugin 校验契约，
// 非法内容报错不落盘）。id 建议由 @name 转写（英文数字连字符），空则用时间戳。
func (m *Manager) Install(id, source string) (*Plugin, error) {
	id = sanitizePluginID(id)
	if id == "" {
		id = fmt.Sprintf("user%d", time.Now().UnixNano())
	}
	p, err := LoadPlugin(id+".user.js", source)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.PluginDir(), 0755); err != nil {
		return nil, fmt.Errorf("建插件目录: %w", err)
	}
	// 同 .off（禁用态）残留先清，装新版即启用
	_ = os.Remove(filepath.Join(m.PluginDir(), id+".user.js.off"))
	p.File = filepath.Join(m.PluginDir(), id+".user.js")
	p.Enabled = true
	if err := os.WriteFile(p.File, []byte(source), 0644); err != nil {
		return nil, fmt.Errorf("写插件文件: %w", err)
	}
	return p, nil
}

// SetEnabled 启用/禁用：禁用 = 文件改名 <id>.user.js.off（loadPlugins 只扫
// .user.js，天然跳过）；启用 = 改回。内置分发对 .off 也视为已存在不覆盖。
func (m *Manager) SetEnabled(id string, enabled bool) error {
	if !validPluginID(id) {
		return fmt.Errorf("非法插件 id")
	}
	on := filepath.Join(m.PluginDir(), id+".user.js")
	off := on + ".off"
	if enabled {
		if _, err := os.Stat(off); err != nil {
			return fmt.Errorf("插件不存在或未被禁用")
		}
		return os.Rename(off, on)
	}
	if _, err := os.Stat(on); err != nil {
		return fmt.Errorf("插件不存在")
	}
	return os.Rename(on, off)
}

// Delete 占除插件（含禁用态残留）。内置插件删除后重启会恢复出厂版（出厂语义：
// 彻底停用请用禁用）。
func (m *Manager) Delete(id string) error {
	if !validPluginID(id) {
		return fmt.Errorf("非法插件 id")
	}
	on := filepath.Join(m.PluginDir(), id+".user.js")
	off := on + ".off"
	if _, err := os.Stat(on); err != nil {
		if _, err2 := os.Stat(off); err2 != nil {
			return fmt.Errorf("插件不存在")
		}
		return os.Remove(off)
	}
	return os.Remove(on)
}

// sanitizePluginID 插件文件名白名单（防穿越）：仅字母数字._-，其余替换 -；
// @name 优先取其中的 ASCII 段。
func sanitizePluginID(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func validPluginID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`)
}

// Parse 执行插件解析 url。注意：不做 @match 强制（显式指定插件优先，match 是给
// UI 自动选择用的建议值）。错误也走契约 JSON（ok:false + error），error 返回值仅剩
// 序列化失败这类不可恢复场景——前端只需认一种形状。
func (m *Manager) Parse(pluginID, url string) (string, error) {
	p := m.FindByID(pluginID)
	if p == nil {
		return marshalResult(ParseResult{OK: false, Error: fmt.Sprintf("插件不存在: %s", pluginID)}), nil
	}
	if !p.Enabled {
		return marshalResult(ParseResult{OK: false, Error: fmt.Sprintf("插件已禁用: %s", p.Name)}), nil
	}
	res, err := m.runParse(p, url)
	if err != nil {
		return marshalResult(ParseResult{OK: false, Error: err.Error()}), nil
	}
	return marshalResult(*res), nil
}

func marshalResult(r ParseResult) string {
	b, err := json.Marshal(r)
	if err != nil { // 纯结构体序列化，理论不可达；兜底手拼
		return `{"ok":false,"error":"结果序列化失败"}`
	}
	return string(b)
}

// ---- ==UserScript== 元数据块解析 ----

// LoadPlugin 从「文件名 + 脚本源码」解析插件（校验元数据块与 yunx_parse 契约）。
func LoadPlugin(filename, source string) (*Plugin, error) {
	meta := ParseUserScriptMeta(source)
	id := strings.TrimSuffix(filename, ".user.js")
	if id == "" || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("非法插件文件名: %q", filename)
	}
	name := id // @name 缺省用文件名（MVP 宽容：不拒载）
	if ns := meta["name"]; len(ns) > 0 {
		name = ns[0]
	}
	if source == "" || !strings.Contains(source, "yunx_parse") {
		return nil, fmt.Errorf("缺少 yunx_parse 契约函数")
	}
	version := "0.0.0"
	if v := meta["version"]; len(v) > 0 {
		version = v[0]
	}
	match := meta["match"]
	match = append(match, meta["include"]...) // @include 当 @match 的 glob 别名
	return &Plugin{
		ID: id, Name: name, Namespace: firstOr(meta["namespace"], ""),
		Version: version, Match: match, Source: source,
	}, nil
}

func firstOr(ss []string, def string) string {
	if len(ss) > 0 {
		return ss[0]
	}
	return def
}

// metaLineRe 元数据行：`// @key value`（@key 大小写不敏感；value 两侧空白剥掉）。
var metaLineRe = regexp.MustCompile(`(?m)^\s*//\s*@([A-Za-z-]+)\s+(.+?)\s*$`)

// ParseUserScriptMeta 解析 ==UserScript== 块内的 @key → 多值（@match/@include 可重复）。
// 块定界：`// ==UserScript==` 与 `// ==/UserScript==`（允许行内前后空白）。
// 未识别的键（@grant/@require/@run-at/@icon…）也收进 map（无害透出），MVP 不消费。
func ParseUserScriptMeta(source string) map[string][]string {
	out := map[string][]string{}
	start := regexp.MustCompile(`(?m)^\s*//\s*==UserScript==\s*$`).FindStringIndex(source)
	if start == nil {
		return out
	}
	rest := source[start[1]:]
	end := regexp.MustCompile(`(?m)^\s*//\s*==/UserScript==\s*$`).FindStringIndex(rest)
	if end != nil {
		rest = rest[:end[0]]
	}
	for _, m := range metaLineRe.FindAllStringSubmatch(rest, -1) {
		key := strings.ToLower(m[1])
		out[key] = append(out[key], m[2])
	}
	return out
}

// MatchURL Tampermonkey 风格 glob：`*` 通配任意串，整串匹配。
// 例：`*://www.dytt8899.com/i/*` 命中 https://www.dytt8899.com/i/119283.html。
// （真 Tampermonkey 对 host 段有额外规则——如 `*.example.com` 不吃裸域——MVP 按
// 朴素 glob 实现，倾向「多命中」，选择权最终在显式 pluginId。）
func MatchURL(pattern, url string) bool {
	expr := "^" + regexp.QuoteMeta(pattern) + "$"
	expr = strings.ReplaceAll(expr, `\*`, ".*")
	re, err := regexp.Compile(expr)
	if err != nil {
		return false
	}
	return re.MatchString(url)
}

// Matches 任意 pattern 命中即真（无 @match 视为全匹配——显式调用不受限）。
func Matches(patterns []string, url string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if MatchURL(p, url) {
			return true
		}
	}
	return false
}

// ---- goja 执行沙箱 ----

// parseRunner 一次 yunx_parse 执行的全部状态。
type parseRunner struct {
	m    *Manager
	rt   *goja.Runtime
	dom  *domCtx
	logf func(format string, args ...any)
}

// runParse 建沙箱、装 API、跑脚本、调契约函数、归一化结果。
// recover：goja 个别路径（深递归栈溢出等）以 panic 表达，兜住转错误，不炸宿主进程。
func (m *Manager) runParse(p *Plugin, url string) (res *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("插件执行崩溃: %v", r)
		}
	}()
	return m.runParseInner(p, url)
}

func (m *Manager) runParseInner(p *Plugin, url string) (*ParseResult, error) {
	r := &parseRunner{
		m:   m,
		rt:  goja.New(),
		dom: newDomCtx(),
	}
	r.logf = func(format string, args ...any) { m.logger.Printf("[%s] "+format, append([]any{p.ID}, args...)...) }

	// 死循环/长阻塞兜底：超时 Interrupt 掐断（goja 在下个字节码边界抛出）。
	timer := time.AfterFunc(parseTimeout, func() { r.rt.Interrupt(fmt.Sprintf("执行超时（>%s）", parseTimeout)) })
	defer timer.Stop()

	r.installHostAPIs(url)
	if err := r.installDomPrimitives(); err != nil {
		return nil, err
	}
	prog, err := goja.Compile(p.ID, p.Source, false)
	if err != nil {
		return nil, fmt.Errorf("脚本编译失败: %w", err)
	}
	if _, err := r.rt.RunProgram(prog); err != nil {
		return nil, fmt.Errorf("脚本加载失败: %w", err)
	}

	fn, ok := goja.AssertFunction(r.rt.Get("yunx_parse"))
	if !ok {
		return nil, fmt.Errorf("未定义全局函数 yunx_parse(url)")
	}
	v, err := fn(goja.Undefined(), r.rt.ToValue(url))
	if err != nil {
		return nil, fmt.Errorf("yunx_parse 执行失败: %w", err)
	}
	return normalizeResult(v)
}

// normalizeResult 契约归一：接受 JS 对象或 JSON 字符串；links 截断至 200 条、
// 字段 trim；ok 缺省按「有 links」推断（宽容：不少脚本只想吐链接）。
func normalizeResult(v goja.Value) (*ParseResult, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, fmt.Errorf("yunx_parse 返回空值")
	}
	var res ParseResult
	if s, ok := v.Export().(string); ok { // JSON 字符串形态
		if err := json.Unmarshal([]byte(s), &res); err != nil {
			return nil, fmt.Errorf("返回的 JSON 字符串不合法: %w", err)
		}
	} else { // 对象形态：Export → map → JSON → 结构体（走标准序列化容忍缺字段）
		b, err := json.Marshal(v.Export())
		if err != nil {
			return nil, fmt.Errorf("返回值不可序列化: %w", err)
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return nil, fmt.Errorf("返回值形状不符契约: %w", err)
		}
	}
	if !res.OK && len(res.Links) > 0 {
		res.OK = true
	}
	if !res.OK && res.Error == "" {
		res.Error = "ok=false 且未给出 error"
	}
	res.Title = strings.TrimSpace(res.Title)
	if len(res.Links) > 200 {
		res.Links = res.Links[:200]
	}
	for i := range res.Links {
		res.Links[i].URL = strings.TrimSpace(res.Links[i].URL)
		res.Links[i].Name = strings.TrimSpace(res.Links[i].Name)
	}
	return &res, nil
}

// installHostAPIs 注入宿主全局（yunx_* / GM_* / console / location / window）。
//
// 兼容层说明：GM_xmlhttpRequest 为同步执行后立刻回调 onload（真油猴是异步事件循环，
// 这里没有事件循环——同步回调能覆盖「onload 里填结果」的最常见写法，报告详述）。
func (r *parseRunner) installHostAPIs(targetURL string) {
	rt, m := r.rt, r.m

	// yunx_fetch 的 Go 侧（带下划线前缀，JS 预置层包一层让 document 自动跟随）
	rt.Set("__yunx_fetch", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		res, err := m.fetcher(url)
		if err != nil {
			r.logf("fetch %s 失败: %v", url, err)
			return rt.ToValue(map[string]any{"status": 0, "headers": map[string]string{}, "body": "", "error": err.Error()})
		}
		o := rt.NewObject()
		_ = o.Set("status", res.Status)
		_ = o.Set("headers", res.Headers)
		_ = o.Set("body", res.Body)
		return o
	})

	// yunx_log(...)：任意个参数空格拼接打到 Go 日志（带插件 ID 前缀）。
	rt.Set("yunx_log", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, a.String())
		}
		r.logf("%s", strings.Join(parts, " "))
		return goja.Undefined()
	})

	prelude := `
function yunx_fetch(url) {
  var r = __yunx_fetch(url);
  if (r && typeof r.body === 'string' && r.body) {
    try { document = yunx_dom(r.body); } catch (e) { /* DOM 更新失败不影响取值 */ }
  }
  return r;
}
// GM_* 兼容层（类油猴脚本的常用子集）
var GM_log = yunx_log;
function GM_xmlhttpRequest(opts) {
  if (!opts || !opts.url) return { status: 0, responseText: '' };
  var r = yunx_fetch(opts.url);
  var resp = { status: r.status || 0, responseText: r.body || '', responseHeaders: r.headers || {}, finalUrl: opts.url };
  if (typeof opts.onload === 'function') opts.onload(resp); // 同步回调（无事件循环）
  return resp;
}
var console = { log: yunx_log, info: yunx_log, warn: yunx_log, error: yunx_log, debug: yunx_log };
var location = { href: %q };
var window = { location: location };
`
	if _, err := rt.RunString(fmt.Sprintf(prelude, targetURL)); err != nil {
		r.logf("宿主 API 预置层加载失败: %v", err)
	}
}
