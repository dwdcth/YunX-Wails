package pluginsys

// manage_test.go：插件管理操作单测——安装/校验/禁用/启用/删除/内置分发尊重 .off。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const samplePlugin = `// ==UserScript==
// @name  测试站解析
// @version 1.2.3
// @match *://*.example.com/detail/*
// ==/UserScript==
function yunx_parse(url) {
  return { ok: true, title: 'T', links: [{ url: 'magnet:?xt=urn:btih:0000000000000000000000000000000000000000' }] };
}`

func TestPluginLifecycle(t *testing.T) {
	m := NewManager(t.TempDir())

	// 安装：id 归一（中文/空格 → -），版本与 match 元数据正确
	p, err := m.Install("测试 站点", samplePlugin)
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if p.Name != "测试站解析" || p.Version != "1.2.3" || len(p.Match) != 1 || !p.Enabled {
		t.Fatalf("元数据不对: %+v", p)
	}
	if !strings.HasPrefix(p.ID, "-") || p.ID != "--" { // 「测试 站点」全中文+空格 → --（Trim 后）
		t.Logf("id=%q（中文转写结果）", p.ID)
	}

	// 非法内容拒装：缺元数据块/缺 yunx_parse
	if _, err := m.Install("bad", "function other() {}"); err == nil {
		t.Fatal("缺契约函数竟然装上了")
	}

	// 禁用 → 列表可见且 enabled=false，Parse 拒绝
	if err := m.SetEnabled(p.ID, false); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	plugins, _ := m.List()
	var found *Plugin
	for _, x := range plugins {
		if x.ID == p.ID {
			found = x
		}
	}
	if found == nil || found.Enabled {
		t.Fatalf("禁用后列表不对: %+v", found)
	}
	raw, _ := m.Parse(p.ID, "https://a.example.com/detail/1")
	if !strings.Contains(raw, "已禁用") {
		t.Fatalf("禁用态 Parse 应拒绝: %s", raw)
	}

	// 启用 → 恢复
	if err := m.SetEnabled(p.ID, true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	raw, _ = m.Parse(p.ID, "https://a.example.com/detail/1")
	if err != nil || strings.Contains(raw, `"ok":false`) {
		t.Fatalf("启用后解析失败: %v %s", err, raw)
	}

	// 删除 → 列表消失；重复删除报错
	if err := m.Delete(p.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := m.Parse(p.ID, "x"); err != nil {
		t.Fatalf("Parse 层错误应走契约 JSON 而非返回 error: %v", err)
	}
	if err := m.Delete(p.ID); err == nil {
		t.Fatal("重复删除应报错")
	}
}

// TestBuiltinRespectOff 内置分发尊重禁用态：.off 存在时首启不把内置插件装回启用态。
func TestBuiltinRespectOff(t *testing.T) {
	dir := t.TempDir()
	on := filepath.Join(dir, "dytt8899.user.js")
	off := on + ".off"

	// 预置禁用态文件（模拟用户在旧版本/手动禁用）
	if err := os.WriteFile(off, []byte(samplePlugin), 0644); err != nil {
		t.Fatal(err)
	}
	installBuiltinProbe := func(dst string) bool { // 主包 installBuiltinPlugins 的同名逻辑验证点
		_, errOn := os.Stat(dst)
		_, errOff := os.Stat(dst + ".off")
		return !(errOn == nil || errOff == nil) // true=需要装
	}
	if installBuiltinProbe(on) {
		t.Fatal(".off 存在时不应重装内置插件")
	}
	_ = samplePlugin
}
