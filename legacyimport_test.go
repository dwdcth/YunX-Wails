// legacyimport_test.go：HOME 隔离的纯 Go 功能冒烟（不碰 yunxcore / .so / 真实用户目录）。
//
// 场景：伪造一套「原版」数据（yunx.db 占位 + credential.key + kv/ + files/ + prefs 树）
// → Status available=true → Decide(true) 导入 → 断言目标文件齐全、prefs 树就位、标记存在
// → Status available=false/asked=true → Decide 再调 ok:false。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withSandboxHome 把 HOME / YUNX_DESKTOP_DATA_DIR / YUNX_PREFS_NODE 全部指进临时沙箱，
// 保证测试绝不触碰真实 ~/.yunx-desktop 与 ~/.java/.userPrefs（安全红线）。
func withSandboxHome(t *testing.T) (home string, sourceDir string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	sourceDir = filepath.Join(home, ".yunx-desktop")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("YUNX_DESKTOP_DATA_DIR", filepath.Join(home, ".yunx-wails"))
	t.Setenv("YUNX_PREFS_NODE", "yunx-wails")
	return home, sourceDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// fakeLegacyTree 伪造原版数据：库 + 密钥 + kv/ + files/ + 应跳过的目录/文件 + prefs 树。
func fakeLegacyTree(t *testing.T, home, sourceDir string) {
	t.Helper()
	writeFile(t, filepath.Join(sourceDir, "yunx.db"), "FAKE-DB") // 0 字节/占位即可，导入只认存在性
	writeFile(t, filepath.Join(sourceDir, "yunx.db-wal"), "FAKE-WAL")
	writeFile(t, filepath.Join(sourceDir, "credential.key"), "FAKE-KEY")
	writeFile(t, filepath.Join(sourceDir, "credential.key.unreadable"), "FAKE")
	writeFile(t, filepath.Join(sourceDir, "yunx-instance.lock"), "")
	writeFile(t, filepath.Join(sourceDir, "kv", "a.txt"), "kv-a")
	writeFile(t, filepath.Join(sourceDir, "files", "yunx-desktop.log"), "log-line")
	writeFile(t, filepath.Join(sourceDir, "files", "sub", "b.log"), "log-b")
	writeFile(t, filepath.Join(sourceDir, "cache", "download_tmp", "chunk.bin"), "chunk")
	writeFile(t, filepath.Join(sourceDir, "diagnostic_logs", "diag.zip"), "zip")
	// prefs 树（模拟 java.util.prefs 文件布局：节点目录内 prefs.xml + 转义名子节点）
	prefsRoot := filepath.Join(home, ".java", ".userPrefs")
	writeFile(t, filepath.Join(prefsRoot, "yunx", "prefs.xml"), "<?xml version='1.0'?><!DOCTYPE map SYSTEM 'http://java.sun.com/dtd/preferences.dtd'><map/><root type='user'/>")
	writeFile(t, filepath.Join(prefsRoot, "yunx", "settings", "prefs.xml"), "settings-xml")
	writeFile(t, filepath.Join(prefsRoot, "yunx", "misc", "prefs.xml"), "misc-xml")
}

func mustJSON(t *testing.T, raw string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		t.Fatalf("JSON 解析失败 %q: %v", raw, err)
	}
}

func TestLegacyImportEndToEnd(t *testing.T) {
	home, sourceDir := withSandboxHome(t)
	fakeLegacyTree(t, home, sourceDir)
	targetDir := filepath.Join(home, ".yunx-wails")
	svc := &CoreService{}

	// 1. 首启探测：可导入、未问过
	var st importStatus
	mustJSON(t, svc.LegacyImportStatus(), &st)
	if !st.Available || st.Asked {
		t.Fatalf("初始 Status 应 available=true asked=false，实际 %+v（raw=%s）", st, svc.LegacyImportStatus())
	}
	if st.SourceDir != sourceDir || st.TargetDir != targetDir {
		t.Fatalf("目录推导不符：source=%q target=%q", st.SourceDir, st.TargetDir)
	}

	// 2. 决定导入：目标文件齐全、prefs 树就位、标记存在
	var res importResult
	mustJSON(t, svc.LegacyImportDecide(true), &res)
	if !res.OK || !res.Imported || res.Partial {
		t.Fatalf("Decide(true) 应成功: %+v", res)
	}
	for _, rel := range []string{
		"yunx.db", "yunx.db-wal", "credential.key",
		"kv/a.txt", "files/yunx-desktop.log", "files/sub/b.log",
	} {
		if b, err := os.ReadFile(filepath.Join(targetDir, rel)); err != nil || len(b) == 0 {
			t.Fatalf("导入后缺文件或为空: %s (err=%v)", rel, err)
		}
	}
	// 跳过项绝不出现在目标
	for _, rel := range []string{"cache", "diagnostic_logs", "yunx-instance.lock", "credential.key.unreadable"} {
		if _, err := os.Lstat(filepath.Join(targetDir, rel)); err == nil {
			t.Fatalf("应跳过却出现在目标: %s", rel)
		}
	}
	// prefs 树就位（源树必须原样保留）
	dstPrefs := filepath.Join(home, ".java", ".userPrefs", "yunx-wails")
	for _, rel := range []string{"prefs.xml", "settings/prefs.xml", "misc/prefs.xml"} {
		if b, err := os.ReadFile(filepath.Join(dstPrefs, rel)); err != nil || len(b) == 0 {
			t.Fatalf("prefs 目标缺文件: %s (err=%v)", rel, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(home, ".java", ".userPrefs", "yunx", "settings", "prefs.xml")); err != nil || string(b) != "settings-xml" {
		t.Fatalf("prefs 源被改动（红线！）: err=%v", err)
	}
	if !decisionExists(targetDir) {
		t.Fatal("导入成功后应有决定标记")
	}

	// 3. 再问：available=false / asked=true；再决定：ok:false
	mustJSON(t, svc.LegacyImportStatus(), &st)
	if st.Available || !st.Asked {
		t.Fatalf("导入后 Status 应 available=false asked=true，实际 %+v", st)
	}
	var again struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	mustJSON(t, svc.LegacyImportDecide(true), &again)
	if again.OK {
		t.Fatalf("已决定过后 Decide 应 ok:false，实际 %+v", again)
	}
}

func TestLegacyImportSkip(t *testing.T) {
	home, sourceDir := withSandboxHome(t)
	fakeLegacyTree(t, home, sourceDir)
	targetDir := filepath.Join(home, ".yunx-wails")
	svc := &CoreService{}

	var res importResult
	mustJSON(t, svc.LegacyImportDecide(false), &res)
	if !res.OK || res.Imported {
		t.Fatalf("Decide(false) 应 ok:true imported:false: %+v", res)
	}
	if !decisionExists(targetDir) {
		t.Fatal("跳过也应写决定标记（不再询问）")
	}
	if _, err := os.Lstat(filepath.Join(targetDir, "yunx.db")); err == nil {
		t.Fatal("跳过时不应拷任何数据文件")
	}
	var st importStatus
	mustJSON(t, svc.LegacyImportStatus(), &st)
	if st.Available || !st.Asked {
		t.Fatalf("跳过后 Status 应 available=false asked=true，实际 %+v", st)
	}
}

func TestLegacyImportNoSource(t *testing.T) {
	home, _ := withSandboxHome(t) // 沙箱 home 里没有 .yunx-desktop
	svc := &CoreService{}
	var st importStatus
	mustJSON(t, svc.LegacyImportStatus(), &st)
	if st.Available {
		t.Fatal("无原版数据时 available 应为 false")
	}
	var res struct {
		OK bool `json:"ok"`
	}
	mustJSON(t, svc.LegacyImportDecide(true), &res)
	if res.OK {
		t.Fatal("无可导入数据时 Decide 应 ok:false")
	}
	_ = home
}

func TestLegacyImportLargeFileSkipped(t *testing.T) {
	home, sourceDir := withSandboxHome(t)
	fakeLegacyTree(t, home, sourceDir)
	// files/ 下塞一个超限文件（稀疏文件不占实际磁盘）
	big := filepath.Join(sourceDir, "files", "huge.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(filesDirCopyLimit + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	svc := &CoreService{}
	var res importResult
	mustJSON(t, svc.LegacyImportDecide(true), &res)
	if !res.OK || !res.Imported {
		t.Fatalf("超限文件只应跳过不应失败: %+v", res)
	}
	found := false
	for _, s := range res.SkippedFiles {
		if s == "huge.bin（超过大小上限）" {
			found = true
		}
	}
	if !found {
		t.Fatalf("超限文件应记入 skippedFiles: %+v", res.SkippedFiles)
	}
	if _, err := os.Lstat(filepath.Join(dataDirTarget(), "files", "huge.bin")); err == nil {
		t.Fatal("超限文件不应被拷贝")
	}
}

func TestLegacyImportPrefsTargetExists(t *testing.T) {
	home, sourceDir := withSandboxHome(t)
	fakeLegacyTree(t, home, sourceDir)
	// 目标 prefs 节点已存在且非空 → 跳过并注明
	writeFile(t, filepath.Join(home, ".java", ".userPrefs", "yunx-wails", "existing.xml"), "x")

	svc := &CoreService{}
	var res importResult
	mustJSON(t, svc.LegacyImportDecide(true), &res)
	if !res.OK {
		t.Fatalf("prefs 目标存在只应跳过 prefs 不应失败: %+v", res)
	}
	if res.PrefsCopied || res.PrefsSkipReason == "" {
		t.Fatalf("应注明 prefs 跳过原因: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(home, ".java", ".userPrefs", "yunx-wails", "settings")); err == nil {
		t.Fatal("目标 prefs 已存在时不应拷入源树")
	}
}
