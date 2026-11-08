package main

// pluginsys_host_test.go：内置插件分发单测——首启写入、不覆盖用户改动、可删恢复。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallBuiltinPlugins(t *testing.T) {
	dir := t.TempDir()
	installBuiltinPlugins(dir)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("内置插件未安装: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == "dytt8899.user.js" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺 dytt8899.user.js（安装了 %d 个）: %v", len(entries), entries)
	}

	// 已存在不覆盖：用户改过的文件保持原样
	p := filepath.Join(dir, "dytt8899.user.js")
	before, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(before, []byte("\n// 用户本地改动")...), 0644); err != nil {
		t.Fatal(err)
	}
	installBuiltinPlugins(dir)
	after, _ := os.ReadFile(p)
	if string(after) == string(before) {
		t.Fatal("覆盖了用户改动")
	}

	// 删除后重装恢复出厂版
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	installBuiltinPlugins(dir)
	restored, err := os.ReadFile(p)
	if err != nil || string(restored) != string(before) {
		t.Fatalf("删除重装未恢复出厂版（err=%v）", err)
	}
}
