// legacyimport.go：「从原版（Compose 版 YunX-Desktop）导入数据」服务 —— 纯 Go 实现，不触碰核心。
//
// 背景：本应用数据目录已独立为 ~/.yunx-wails（main.go 里把 YUNX_DESKTOP_DATA_DIR 默认值
// 指过去），与原版 ~/.yunx-desktop 分家、可双开。首启时前端先调 LegacyImportStatus 探测
// 原版数据是否可导入、弹窗询问，用户决定后调 LegacyImportDecide 执行或跳过 —— 绝不自动导入。
//
// ★ 安全红线：原版目录（~/.yunx-desktop 与 ~/.java/.userPrefs/yunx）是用户真实数据，
//   本文件对它们只读，绝不写、绝不删。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// decisionMarker 决定标记文件名（放目标数据目录根）：存在 = 已问过用户，不再询问。
const decisionMarker = ".import-decided"

// filesDirCopyLimit files/（应用日志等杂项文件）单文件大小上限：超限跳过。
// core 的 filesDir 只放应用日志/崩溃转储（AppContext.filesDir 注释：杂项文件目录），正常
// 都是文本小文件；上限纯防御（比如用户塞了巨大崩溃转储/别的大文件），避免导入卡半天。
const filesDirCopyLimit = 64 << 20 // 64MB

// LegacyImportStatus 首启探测：原版数据可否导入、是否已询问过。
// 返回 JSON：{available, asked, sourceDir, targetDir}。
//   - available：原版 ~/.yunx-desktop/yunx.db 存在 且 目标无 yunx.db 且 尚未做过决定
//     （.import-decided 标记不存在）。三个条件缺一不可：已决定过（导入/跳过）就不该再问。
//   - asked：目标目录存在 .import-decided 标记。
func (s *CoreService) LegacyImportStatus() string {
	sourceDir := legacySourceDir()
	targetDir := dataDirTarget()
	asked := decisionExists(targetDir)
	available := !asked &&
		fileExists(filepath.Join(sourceDir, "yunx.db")) &&
		!fileExists(filepath.Join(targetDir, "yunx.db"))
	return marshalMust(map[string]any{
		"available": available,
		"asked":     asked,
		"sourceDir": sourceDir,
		"targetDir": targetDir,
	})
}

// LegacyImportDecide 用户决定后的执行入口（doImport=true 导入 / false 跳过）。
// 返回 JSON：{ok, imported, copied[], skippedFiles[], prefsCopied, prefsSkippedReason?, error?, partial?}。
// 拷贝中途失败：保留已拷贝文件、partial=true、不写决定标记（下次启动仍可询问/重试，
// 重试策略 = 覆盖同名文件继续拷）。
func (s *CoreService) LegacyImportDecide(doImport bool) string {
	targetDir := dataDirTarget()
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return errorJSON(fmt.Sprintf("创建数据目录失败: %v", err))
	}
	if st := parseStatus(s.LegacyImportStatus()); !st.Available {
		return errorJSON("当前无可导入数据（原版数据不存在 / 已导入过 / 已决定跳过）")
	}
	if !doImport {
		if err := writeDecision(targetDir, "skipped"); err != nil {
			return errorJSON(fmt.Sprintf("写入决定标记失败: %v", err))
		}
		return marshalMust(map[string]any{"ok": true, "imported": false})
	}

	res, err := runImport(legacySourceDir(), targetDir)
	if err != nil {
		// 失败不写标记：下次启动仍可重试（重试=覆盖同名文件继续拷）
		res.OK = false
		res.Error = err.Error()
		res.Partial = true
		return marshalMust(res)
	}
	if err := writeDecision(targetDir, "imported from "+legacySourceDir()); err != nil {
		return marshalMust(&importResult{
			OK: false, Error: fmt.Sprintf("导入完成但写决定标记失败: %v", err), Partial: true,
			Copied: res.Copied, SkippedFiles: res.SkippedFiles, PrefsCopied: res.PrefsCopied,
		})
	}
	res.OK = true
	res.Imported = true
	return marshalMust(res)
}

// ---- 状态/结果结构 ----

type importStatus struct {
	Available bool   `json:"available"`
	Asked     bool   `json:"asked"`
	SourceDir string `json:"sourceDir"`
	TargetDir string `json:"targetDir"`
}

func parseStatus(raw string) importStatus {
	var st importStatus
	_ = json.Unmarshal([]byte(raw), &st)
	return st
}

type importResult struct {
	OK               bool     `json:"ok"`
	Imported         bool     `json:"imported"`
	Copied           []string `json:"copied"`
	SkippedFiles     []string `json:"skippedFiles"`
	PrefsCopied      bool     `json:"prefsCopied"`
	PrefsSkipReason  string   `json:"prefsSkippedReason,omitempty"`
	Error            string   `json:"error,omitempty"`
	Partial          bool     `json:"partial,omitempty"`
}

// ---- 路径推导（与 core AppContext / main.go setDefaultDataDir 同源规则）----

// legacySourceDir 原版数据目录（只读！）。
func legacySourceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".yunx-desktop")
}

// dataDirTarget 本应用数据目录：YUNX_DESKTOP_DATA_DIR（main.go 已默认指向 ~/.yunx-wails）。
func dataDirTarget() string {
	if v := os.Getenv("YUNX_DESKTOP_DATA_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".yunx-wails")
}

// prefsUserRoot java.util.prefs 的用户根（Linux FileSystemPreferences：$HOME/.java/.userPrefs）。
// 必须与 System.getProperty("user.home") 同源 —— Go 侧即 $HOME（os.UserHomeDir）。
func prefsUserRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".java", ".userPrefs")
}

// prefsNodeName DLL 侧 prefs 节点前缀（DllFacade prefsStoreFactory 同源）：默认 yunx-wails，
// YUNX_PREFS_NODE 可覆盖（测试隔离用）。
func prefsNodeName() string {
	if v := os.Getenv("YUNX_PREFS_NODE"); v != "" {
		return v
	}
	return "yunx-wails"
}

// ---- 导入实现 ----

// runImport 执行拷贝（源只读）。清单：
//   - 数据文件：yunx.db / yunx.db-wal / yunx.db-shm / credential.key、kv/ 整目录
//   - files/ 整目录（应用日志等杂项；单文件 >64MB 跳过并记入 skippedFiles）
//   - 跳过：cache/（分片缓存，任务可重下）、diagnostic_logs/、锁文件、credential.key.unreadable
//   - prefs：~/.java/.userPrefs/yunx → <同目录>/<本应用节点>（目标已存在且非空则跳过并注明）
func runImport(sourceDir, targetDir string) (*importResult, error) {
	res := &importResult{Copied: []string{}, SkippedFiles: []string{}}

	// 1. 数据库与密钥（若存在才拷；kv/ 同理）
	dataFiles := []string{"yunx.db", "yunx.db-wal", "yunx.db-shm", "credential.key"}
	for _, name := range dataFiles {
		src := filepath.Join(sourceDir, name)
		if !fileExists(src) {
			continue
		}
		if err := copyFileKeepMode(src, filepath.Join(targetDir, name)); err != nil {
			return res, fmt.Errorf("拷贝 %s 失败: %w", name, err)
		}
		res.Copied = append(res.Copied, name)
	}

	// 2. kv/ 整目录（DLL 的 FileKeyValueStore）
	if err := copyDir(filepath.Join(sourceDir, "kv"), filepath.Join(targetDir, "kv"), res, 0); err != nil {
		return res, err
	}

	// 3. files/ 整目录（应用日志等小杂项；单文件超限跳过）
	if err := copyDir(filepath.Join(sourceDir, "files"), filepath.Join(targetDir, "files"), res, filesDirCopyLimit); err != nil {
		return res, err
	}

	// 4. prefs 树（Linux java.util.prefs 后端文件）
	srcPrefs := filepath.Join(prefsUserRoot(), "yunx")
	dstPrefs := filepath.Join(prefsUserRoot(), prefsNodeName())
	switch {
	case !dirExists(srcPrefs):
		res.PrefsSkipReason = "原版 prefs 不存在（源: " + srcPrefs + "）"
	case prefsNodeName() == "yunx":
		res.PrefsSkipReason = "prefs 节点未分离（YUNX_PREFS_NODE=yunx），跳过避免覆盖原版"
	default:
		// 目标已存在且非空 → 跳过（不合并、不覆盖，避免动用户已产生的设置）
		if entries, err := os.ReadDir(dstPrefs); err == nil && len(entries) > 0 {
			res.PrefsSkipReason = "目标 prefs 节点已存在且非空: " + dstPrefs
		} else if err := copyDir(srcPrefs, dstPrefs, res, 0); err != nil {
			return res, err
		} else {
			res.PrefsCopied = true
		}
	}
	return res, nil
}

// copyDir 递归拷贝目录（源只读）。limit>0 时单文件超限跳过并记入 skippedFiles（相对路径）。
// 目标同名文件直接覆盖（重试语义：失败后重跑把剩余文件补齐）。
func copyDir(src, dst string, res *importResult, limit int64) error {
	if !dirExists(src) {
		return nil // 源目录不存在 = 没东西可拷，不算失败
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("遍历 %s 失败: %w", path, err)
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return fmt.Errorf("相对路径失败: %w", rerr)
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0755)
		}
		if !d.Type().IsRegular() {
			return nil // 符号链接等非常规文件跳过（不跟随，防逃逸）
		}
		if limit > 0 {
			if fi, serr := d.Info(); serr == nil && fi.Size() > limit {
				res.SkippedFiles = append(res.SkippedFiles, rel+"（超过大小上限）")
				return nil
			}
		}
		if err := copyFileKeepMode(path, filepath.Join(dst, rel)); err != nil {
			return fmt.Errorf("拷贝 %s 失败: %w", rel, err)
		}
		res.Copied = append(res.Copied, rel)
		return nil
	})
}

// copyFileKeepMode 单文件拷贝：io.Copy 保内容，权限位跟随源文件。
func copyFileKeepMode(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src) // 源只打开读，绝不写
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".import-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst) // 原子落位：中途失败不留半截文件（重试再拷）
}

// writeDecision 写决定标记（内容：UTC 时间 + 动作/来源）。
func writeDecision(targetDir, note string) error {
	content := fmt.Sprintf("decidedAt=%s\nnote=%s\n", time.Now().UTC().Format(time.RFC3339), note)
	return os.WriteFile(filepath.Join(targetDir, decisionMarker), []byte(content), 0644)
}

func decisionExists(targetDir string) bool {
	return fileExists(filepath.Join(targetDir, decisionMarker))
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func marshalMust(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return errorJSON("序列化失败: " + err.Error())
	}
	return string(b)
}
