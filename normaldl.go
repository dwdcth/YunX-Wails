// normaldl.go：「普通下载」服务方法（CoreService）——HTTP/磁力/BT/种子/ed2k/Metalink 走
// aria2-next 子进程，FTP/FTPS 走 Go 内置引擎；任务存独立 SQLite（<dataDir>/downloads.db，
// 与核心的 yunx.db 分家）。网盘下载不走这里（仍由核心内置引擎负责）。
//
// 返回值风格与 service.go 一致：JSON 字符串（前端经 bindings 拿到后自行解析）。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"yunxwailsdemo/downloadd"
)

// normalDL 进程级普通下载管理器（main 里 normalDownloadInit 注入；测试可不注入）。
var (
	normalDLOnce sync.Once
	normalDL     *downloadd.Manager
	normalDLErr  error
)

// normalDownloadInit main 启动时调用：读数据目录（main 已在进程最早处设好 env），
// 注入宿主回调（核心 KV / 命令通道），初始化任务库与两个引擎。惰性单例，失败不阻断启动
//（首个下载调用时把错误报给前端）。
func normalDownloadInit(s *CoreService) {
	normalDLOnce.Do(func() {
		dataDir := os.Getenv("YUNX_DESKTOP_DATA_DIR")
		if dataDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dataDir = filepath.Join(home, ".yunx-wails")
			}
		}
		m, err := downloadd.Init(dataDir, downloadd.Hooks{
			// 经核心通道读 KV（settings.aria2Path 等）；核心惰性初始化，首次调用才真正加载
			KvGet: func(key string) (string, bool) {
				if err := s.ensureCore(); err != nil {
					return "", false
				}
				return s.kvGet(key)
			},
			// 核心通用命令通道（DefaultDir 经 settings_list 读 downloadDirUri）
			Call: s.coreCall,
		})
		if err != nil {
			normalDLErr = err
			fmt.Printf("普通下载引擎初始化失败: %v\n", err)
			return
		}
		normalDL = m
		fmt.Printf("普通下载引擎就绪: 任务库 %s\n", filepath.Join(dataDir, "downloads.db"))
	})
}

// normalDownloadShutdown 应用退出时调用（幂等）：优雅停 aria2 子进程、停 FTP worker。
func normalDownloadShutdown() {
	if normalDL != nil {
		normalDL.Close()
		normalDL = nil
	}
}

// normalDLReady 惰性获取管理器（未初始化时也走一次 Init——兜底 main 注入遗漏/测试场景）。
func normalDLReady() (*downloadd.Manager, error) {
	if normalDL == nil && normalDLErr == nil {
		normalDownloadInit(&CoreService{})
	}
	if normalDLErr != nil {
		return nil, fmt.Errorf("普通下载引擎不可用: %w", normalDLErr)
	}
	if normalDL == nil {
		return nil, fmt.Errorf("普通下载引擎不可用")
	}
	return normalDL, nil
}

// NormalDownloadSubmit 提交普通下载（source：http/https/ftp/ftps/magnet/ed2k 链接，
// 本地或 URL 的 .torrent/.metalink 文件路径）。saveDir 空时用默认下载目录（核心 downloadDirUri）。
// 返回 {ok, taskId, taskIds, engine, name, error?}；metalink 多文件时 taskIds 有多条。
func (s *CoreService) NormalDownloadSubmit(source, saveDir string) (string, error) {
	m, err := normalDLReady()
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	res, err := m.Submit(source, saveDir)
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	return marshal(map[string]any{
		"ok": true, "taskId": res.TaskID, "taskIds": res.TaskIDs,
		"engine": res.Engine, "name": res.Name,
	})
}

// NormalDownloadList 全量任务列表（含 ftp 无 gid 任务），created_at 倒序。
// 返回 {ok, tasks:[{id,engine,gid,url,name,saveDir,savePath,size,downloaded,progress,speed,status,err,createdAt,updatedAt}]}。
func (s *CoreService) NormalDownloadList() (string, error) {
	m, err := normalDLReady()
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	tasks, err := m.List()
	if err != nil {
		return errorJSON("读取任务列表失败: " + err.Error()), nil
	}
	if tasks == nil {
		tasks = []downloadd.Task{}
	}
	return marshal(map[string]any{"ok": true, "tasks": tasks})
}

// NormalDownloadPause 暂停任务。返回 {ok, error?}。
func (s *CoreService) NormalDownloadPause(id int64) (string, error) {
	return normalDLOp(func(m *downloadd.Manager) error { return m.Pause(id) })
}

// NormalDownloadResume 恢复任务（FTP 从断点续传）。
func (s *CoreService) NormalDownloadResume(id int64) (string, error) {
	return normalDLOp(func(m *downloadd.Manager) error { return m.Resume(id) })
}

// NormalDownloadRemove 移除任务（不删已下载文件——对齐云盘 remove 语义，删文件走前端单独入口）。
func (s *CoreService) NormalDownloadRemove(id int64) (string, error) {
	return normalDLOp(func(m *downloadd.Manager) error { return m.Remove(id) })
}

// NormalDownloadPauseAll 全部暂停。
func (s *CoreService) NormalDownloadPauseAll() (string, error) {
	return normalDLOp(func(m *downloadd.Manager) error { m.PauseAll(); return nil })
}

// NormalDownloadResumeAll 全部恢复。
func (s *CoreService) NormalDownloadResumeAll() (string, error) {
	return normalDLOp(func(m *downloadd.Manager) error { m.ResumeAll(); return nil })
}

// NormalDownloadRemoveAll 全部移除（不删已下载文件）。
func (s *CoreService) NormalDownloadRemoveAll() (string, error) {
	return normalDLOp(func(m *downloadd.Manager) error { m.RemoveAll(); return nil })
}

// Aria2Status aria2 引擎状态：{running, version, port, binPath, found}。
// found=false 表示本机找不到 aria2-next 二进制，UI 引导用户去设置 aria2Path。
func (s *CoreService) Aria2Status() (string, error) {
	m, err := normalDLReady()
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	st := m.Aria2Status()
	return marshal(map[string]any{
		"ok": true, "running": st.Running, "version": st.Version,
		"port": st.Port, "binPath": st.BinPath, "found": st.Found,
	})
}

// NormalDownloadShutdown 手动停引擎（应用退出钩子也会调；幂等）。优雅 kill aria2 子进程。
func (s *CoreService) NormalDownloadShutdown() (string, error) {
	normalDownloadShutdown()
	return `{"ok":true}`, nil
}

// normalDLOp 操作类方法的统一包装（错误转 JSON）。
func normalDLOp(op func(m *downloadd.Manager) error) (string, error) {
	m, err := normalDLReady()
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	if err := op(m); err != nil {
		return errorJSON(err.Error()), nil
	}
	b, _ := json.Marshal(map[string]any{"ok": true})
	return string(b), nil
}
