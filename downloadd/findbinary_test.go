package downloadd

// 巡检补充单测：FindBinary 定位优先级（kv > env > 数据目录）与 DefaultDir 解析（file:// 剥离/回退链）。
// PATH 与内置工具目录两级依赖宿主环境，不做断言（kv/env 已覆盖优先级行为本身）。

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestAria2(t *testing.T, kv func(string) (string, bool)) *Aria2 {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewAria2(db, dir, Aria2Hooks{KvGet: kv})
}

func TestFindBinaryKvFirst(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "aria2-next")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := newTestAria2(t, func(k string) (string, bool) {
		if k == "settings.aria2Path" {
			return bin, true
		}
		return "", false
	})
	got, ok := a.FindBinary()
	if !ok || got != bin {
		t.Fatalf("kv 显式路径应最优先: got=%s ok=%v", got, ok)
	}
}

func TestFindBinaryKvPointsToMissingFallsThrough(t *testing.T) {
	// kv 指向不存在的文件 → 跳过该级（不返回假路径）
	a := newTestAria2(t, func(k string) (string, bool) {
		if k == "settings.aria2Path" {
			return "/nonexistent/aria2-next", true
		}
		return "", false
	})
	if got, ok := a.FindBinary(); ok && got == "/nonexistent/aria2-next" {
		t.Fatal("不存在的 kv 路径不应被返回")
	}
}

func TestFindBinaryEnv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "aria2-next")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YUNX_ARIA2_BIN", bin)
	a := newTestAria2(t, nil)
	got, ok := a.FindBinary()
	if !ok || got != bin {
		t.Fatalf("env 应命中: got=%s ok=%v", got, ok)
	}
}

func TestFindBinaryDataDirTools(t *testing.T) {
	dir := t.TempDir()
	tools := filepath.Join(dir, "tools")
	if err := os.MkdirAll(tools, 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tools, "aria2-next")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := NewAria2(db, dir, Aria2Hooks{})
	got, ok := a.FindBinary()
	// 宿主 PATH/内置目录可能更早命中：只有拿到的是数据目录路径才断言
	if ok && got != bin {
		t.Skipf("宿主环境命中更高优先级路径: %s", got)
	}
	if !ok {
		t.Fatal("数据目录 tools 下的二进制应被找到")
	}
}

func TestManagerDefaultDir(t *testing.T) {
	dir := t.TempDir()
	m, err := Init(dir, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)

	// 无回调 → 回退链（home/Downloads 或 dataDir/downloads）
	if got := m.DefaultDir(); got == "" {
		t.Fatal("DefaultDir 不应为空")
	}

	// 回调给 file:// 前缀 → 应剥离
	m2, err := Init(t.TempDir(), Hooks{Call: func(string) (string, error) {
		return `{"ok":true,"settings":{"downloadDirUri":"file:///tmp/dl-test"}}`, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if got := m2.DefaultDir(); got != "/tmp/dl-test" {
		t.Fatalf("file:// 前缀应剥离: got=%s", got)
	}

	// 30s 缓存：换回调后短期内仍返回旧值（缓存语义，防高频设置查询）
	m2.hooks.Call = func(string) (string, error) {
		return `{"ok":true,"settings":{"downloadDirUri":"/other"}}`, nil
	}
	if got := m2.DefaultDir(); got != "/tmp/dl-test" {
		t.Fatalf("30s 内应命中缓存: got=%s", got)
	}
}
