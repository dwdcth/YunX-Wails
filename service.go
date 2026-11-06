package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"yunxwailsdemo/hostenv"
	"yunxwailsdemo/yunxcore"
)

// wailsApp 在 main.go 里 application.New 之后赋值；对话框类方法依赖它
var wailsApp *application.App

// CoreService 是暴露给前端的 Wails 服务（界面方通过生成的 bindings 调用这些方法）。
// 这里只做「参数拼装 + 调 DLL + 原样回 JSON」，业务逻辑全在核心 DLL 里。
type CoreService struct {
	core   *yunxcore.Core
	bridge *browserBridge

	// 核心惰性初始化：NewCoreService 不再立即 yunxcore.Open()（那会瞬间建 isolate→开库→
	// 生成密钥），给「首启先问 LegacyImport、用户决定后再动数据目录」留出窗口；
	// 也不阻塞主线程启动。首次真正用到核心的调用在 ensureCore 里完成 Open + 宿主装配。
	coreOnce sync.Once
	coreErr  error
}

func NewCoreService() (*CoreService, error) {
	return &CoreService{}, nil
}

// ensureCore 首次调用时加载核心并执行启动装配（对齐原 main.go：Open + ApplyHostDefaults）。
// 失败原因记在 coreErr，后续调用按原错误路径返回（不重试，与旧版 Open 失败即 Fatal 的语义一致）。
// ★ Once 体内只允许走「直连」路径（callLoaded / setProxyDirect 等）：若经 coreCall 或公开包装
//   重入 ensureCore，sync.Once 不可重入会自锁（2026-11-06 启动无窗口事故的根因）。
func (s *CoreService) ensureCore() error {
	s.coreOnce.Do(func() {
		core, err := yunxcore.Open()
		if err != nil {
			s.coreErr = err
			return
		}
		s.core = core
		// 宿主环境装配：机器标识（仅 Windows 真值）/ 代理（默认跟随系统）/ ffmpeg（自动探测）
		s.applyHostDefaults()
	})
	return s.coreErr
}

// callLoaded 直连核心通道：调用方须保证核心已就绪（Once 体内与内部帮手专用，避免重入 ensureCore）。
func (s *CoreService) callLoaded(requestJSON string) (string, error) {
	return s.core.Call(requestJSON)
}

// coreCall 所有经核心通道的调用统一入口：先确保核心已加载。
func (s *CoreService) coreCall(requestJSON string) (string, error) {
	if err := s.ensureCore(); err != nil {
		return "", err
	}
	return s.core.Call(requestJSON)
}

// Ping 连通性探针（期望 42）。
func (s *CoreService) Ping() int {
	if err := s.ensureCore(); err != nil {
		return -1 // 核心不可用（保持 int 返回签名；错误细节看日志/Call）
	}
	return s.core.Ping()
}

// Call 通用命令通道：请求/响应都是 JSON 字符串（协议见 dll 模块 DllFacade）。
func (s *CoreService) Call(requestJSON string) (string, error) {
	return s.coreCall(requestJSON)
}

// Version 核心版本：{"cmd":"version"} 的便捷封装。
func (s *CoreService) Version() (string, error) {
	return s.coreCall(`{"cmd":"version"}`)
}

// ParseShare 解析分享文案（纯本地）：返回 {"ok":true,"found":...,"platform":...,"shareId":...,"pwd":...}。
func (s *CoreService) ParseShare(text string) (string, error) {
	req, err := json.Marshal(map[string]string{"cmd": "parse_share", "text": text})
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return s.coreCall(string(req))
}

// ---- 宿主环境（代理 / ffmpeg / 机器标识）：探测在 Go，核心只收结果 ----

// HostStatus 宿主配置概览（设置页展示用）：代理（读正式设置）、ffmpeg 路径、机器标识可用性。
func (s *CoreService) HostStatus() (string, error) {
	if err := s.ensureCore(); err != nil {
		return "", err
	}
	settings, _ := s.settingsSnapshot()
	mode := "direct"
	if v, ok := settings["proxyMode"].(string); ok && v != "" {
		mode = v
	}
	host, _ := settings["proxyHost"].(string)
	port := 0
	if v, ok := settings["proxyPort"].(float64); ok {
		port = int(v)
	}
	ffmpeg, _ := s.kvGet("settings.ffmpegPath")
	sysHost, sysPort := hostenv.ResolveSystemProxy()
	return marshal(map[string]any{
		"proxyMode": mode, "proxyHost": host, "proxyPort": port,
		"systemProxyHost": sysHost, "systemProxyPort": sysPort,
		"ffmpegPath": ffmpeg, "ffmpegAuto": hostenv.FfmpegAuto(),
		"machineIdAvailable": hostenv.MachineId() != "",
	})
}

// SetProxy 配置代理并注入核心（公开入口：先确保核心就绪）。mode：direct / system（Go 探测）/ manual。
// 选择持久化到正式设置（yunx/settings，与原版互通），启动时由 applyHostDefaults 重新生效。
func (s *CoreService) SetProxy(mode, host string, port int) (string, error) {
	if err := s.ensureCore(); err != nil {
		return "", err
	}
	return s.setProxyDirect(mode, host, port)
}

// setProxyDirect SetProxy 的直连实现（核心已就绪时可用；Once 装配路径走这里防重入）。
func (s *CoreService) setProxyDirect(mode, host string, port int) (string, error) {
	appliedHost, appliedPort := "", 0
	switch mode {
	case "manual":
		if host == "" || port <= 0 {
			return "", fmt.Errorf("manual 代理需要 host 与 port")
		}
		appliedHost, appliedPort = host, port
	case "system":
		if h, p := hostenv.ResolveSystemProxy(); h != "" {
			appliedHost, appliedPort = h, p
		}
	case "direct":
	default:
		return "", fmt.Errorf("未知代理模式：%s", mode)
	}
	req := map[string]any{"cmd": "host_set_proxy", "port": appliedPort}
	if appliedHost != "" {
		req["host"] = appliedHost
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	resp, err := s.callLoaded(string(raw))
	if err != nil {
		return resp, err
	}
	// 持久化到正式设置（与原版 SettingsRepository 同一存储）
	for k, v := range map[string]string{
		"proxyMode": mode, "proxyHost": host, "proxyPort": strconv.Itoa(port),
	} {
		if r, err := s.callLoaded(fmt.Sprintf(`{"cmd":"settings_set","key":%q,"value":%q}`, k, v)); err != nil || !strings.Contains(r, `"ok":true`) {
			return resp, fmt.Errorf("settings_set(%s) 失败: %s", k, r)
		}
	}
	return resp, nil
}

// SetFfmpeg 注入外部 ffmpeg 路径（公开入口：先确保核心就绪）。
// path 为空 = 自动探测系统 PATH；探测不到则清除注入（核心回退内置探测）。
func (s *CoreService) SetFfmpeg(path string) (string, error) {
	if err := s.ensureCore(); err != nil {
		return "", err
	}
	return s.setFfmpegDirect(path)
}

// setFfmpegDirect SetFfmpeg 的直连实现（Once 装配路径走这里防重入）。
func (s *CoreService) setFfmpegDirect(path string) (string, error) {
	if path == "" {
		path = hostenv.FfmpegAuto()
	}
	req := map[string]any{"cmd": "host_set_ffmpeg", "path": path}
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	resp, err := s.callLoaded(string(raw))
	if err != nil {
		return resp, err
	}
	s.kvPut("settings.ffmpegPath", path)
	return resp, nil
}

// ApplyHostDefaults 启动装配（公开入口：先确保核心就绪；幂等，重复注入无害）。
func (s *CoreService) ApplyHostDefaults() {
	if s.ensureCore() != nil {
		return
	}
	s.applyHostDefaults()
}

// applyHostDefaults 启动装配的直连实现：机器标识（显式配置 > 系统真值）、代理（正式设置的
// proxyMode，默认 direct）、ffmpeg（显式配置路径）。失败的项记日志不阻断启动。
// ★ 只走直连路径（callLoaded / setProxyDirect / setFfmpegDirect），确保能安全跑在 ensureCore
//   的 Once 体内而不重入死锁。
func (s *CoreService) applyHostDefaults() {
	id, _ := s.kvGet("settings.machineId")
	if id == "" {
		id = hostenv.MachineId()
	}
	if id != "" {
		if _, err := s.callLoaded(`{"cmd":"host_set_machine_id","id":"` + id + `"}`); err == nil {
			fmt.Println("宿主注入：机器标识已注入核心")
		}
	}
	settings, _ := s.settingsSnapshot()
	mode, _ := settings["proxyMode"].(string)
	host, _ := settings["proxyHost"].(string)
	port := 0
	if v, ok := settings["proxyPort"].(float64); ok {
		port = int(v)
	}
	if mode == "" {
		mode = "direct"
	}
	if resp, err := s.setProxyDirect(mode, host, port); err == nil {
		fmt.Printf("宿主注入：代理 mode=%s → %s\n", mode, oneLine(resp))
	}
	if path, _ := s.kvGet("settings.ffmpegPath"); path != "" {
		if resp, err := s.setFfmpegDirect(path); err == nil {
			fmt.Printf("宿主注入：ffmpeg %s → %s\n", path, oneLine(resp))
		}
	}
}

// settingsSnapshot 经核心 settings_list 读正式设置（与原版同一存储；直连，调用方须确保核心就绪）。
func (s *CoreService) settingsSnapshot() (map[string]any, error) {
	raw, err := s.callLoaded(`{"cmd":"settings_list"}`)
	if err != nil {
		return map[string]any{}, err
	}
	var r struct {
		OK       bool           `json:"ok"`
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil || !r.OK {
		return map[string]any{}, fmt.Errorf("settings_list 失败: %s", raw)
	}
	return r.Settings, nil
}

// ---- 认证备份（与原版同格式，跨设备导入导出）----

// AuthBackupExport 打开「另存为」对话框导出认证备份。password 为空 = 明文 JSON（.json）；
// 非空须 ≥8 位（AES-GCM 加密 .yunx）。取消对话框返回 {"ok":true,"canceled":true}。
func (s *CoreService) AuthBackupExport(password string) (string, error) {
	if wailsApp == nil {
		return "", fmt.Errorf("应用尚未就绪")
	}
	if password != "" && len(password) < 8 {
		return errorJSON("备份口令至少 8 位"), nil
	}
	path, err := wailsApp.Dialog.SaveFile().
		SetMessage("导出认证备份").
		AddFilter("云析加密备份 (*.yunx)", "*.yunx").
		AddFilter("JSON 明文 (*.json)", "*.json").
		PromptForSingleSelection()
	if err != nil {
		return errorJSON("对话框失败: " + err.Error()), nil
	}
	if path == "" {
		return `{"ok":true,"canceled":true}`, nil
	}
	req, _ := json.Marshal(map[string]any{"cmd": "auth_export", "path": path, "password": password})
	return s.coreCall(string(req))
}

// AuthBackupImport 打开「打开文件」对话框导入认证备份（.yunx 需提供导出时的口令）。
func (s *CoreService) AuthBackupImport(password string) (string, error) {
	if wailsApp == nil {
		return "", fmt.Errorf("应用尚未就绪")
	}
	path, err := wailsApp.Dialog.OpenFile().
		SetMessage("选择认证备份文件").
		AddFilter("云析认证备份 (*.yunx;*.json)", "*.yunx;*.json").
		PromptForSingleSelection()
	if err != nil {
		return errorJSON("对话框失败: " + err.Error()), nil
	}
	if path == "" {
		return `{"ok":true,"canceled":true}`, nil
	}
	req, _ := json.Marshal(map[string]any{"cmd": "auth_import", "path": path, "password": password})
	return s.coreCall(string(req))
}

// OpenFile 用系统默认程序打开文件（xdg-open / Windows ShellExecute）。
func (s *CoreService) OpenFile(path string) (string, error) {
	if path == "" {
		return `{"ok":false,"error":"路径为空"}`, nil
	}
	cmd := exec.Command("xdg-open", path)
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "start", "", path)
	}
	if err := cmd.Start(); err != nil {
		return errorJSON("打开失败: "+err.Error()), nil
	}
	return `{"ok":true}`, nil
}

// OpenDirectory 打开文件所在目录（xdg-open / Windows explorer）。
func (s *CoreService) OpenDirectory(path string) (string, error) {
	if path == "" {
		return `{"ok":false,"error":"路径为空"}`, nil
	}
	dir := filepath.Dir(path)
	cmd := exec.Command("xdg-open", dir)
	if runtime.GOOS == "windows" {
		cmd = exec.Command("explorer", "/select,", path)
	}
	if err := cmd.Start(); err != nil {
		return errorJSON("打开目录失败: "+err.Error()), nil
	}
	return `{"ok":true}`, nil
}

// Shutdown 执行系统关机（Linux: systemctl poweroff / Windows: shutdown /s /t 0）。
func (s *CoreService) Shutdown() (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("shutdown", "/s", "/t", "0")
	} else {
		cmd = exec.Command("systemctl", "poweroff")
	}
	if err := cmd.Start(); err != nil {
		return errorJSON("关机失败: "+err.Error()), nil
	}
	return `{"ok":true}`, nil
}

// ---- 内部帮手 ----

func (s *CoreService) kvGet(key string) (string, bool) {
	raw, err := s.callLoaded(fmt.Sprintf(`{"cmd":"kv_get","key":%q}`, key))
	if err != nil {
		return "", false
	}
	var r struct {
		OK    bool    `json:"ok"`
		Value *string `json:"value"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil || !r.OK || r.Value == nil {
		return "", false
	}
	return *r.Value, true
}

func (s *CoreService) kvPut(key, value string) {
	_, _ = s.callLoaded(fmt.Sprintf(`{"cmd":"kv_put","key":%q,"value":%q}`, key, value))
}

func marshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func oneLine(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func errorJSON(msg string) string {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
	return string(b)
}
