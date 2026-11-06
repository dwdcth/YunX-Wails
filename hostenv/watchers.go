package hostenv

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// StartKeepAwakeWatcher 下载期防睡眠 + 下载完成通知（对齐原版 KeepAwake / 通知中心进度语义）：
//   - 每 5s 轮询 download_list：有任一 status=1（下载中）任务时，Linux 持有 systemd-inhibit
//     子进程抑制 sleep:idle；同一遍轮询里跟踪任务状态迁移，status 1→3（下载中→已完成）
//     即发系统通知（开关读 settings.notificationShowSpeed，与原版「通知中心下载进度」同一开关）。
//
// call 为核心命令的调用封装；单项能力不可用时静默停用该项，不影响另一项。
func StartKeepAwakeWatcher(call func(requestJSON string) (string, error)) {
	// systemd-inhibit 是 Linux/systemd 专属，先探明可用性再进轮询（Windows 走 TODO 的 API 方案）
	inhibitAvailable := runtime.GOOS == "linux"
	if inhibitAvailable {
		if _, err := exec.LookPath("systemd-inhibit"); err != nil {
			inhibitAvailable = false
		}
	}
	// TODO(Windows): 下载期改用 SetThreadExecutionState(ES_CONTINUOUS|ES_SYSTEM_REQUIRED)，
	//   任务结束恢复 ES_CONTINUOUS（对齐原版 Windows KeepAwakeService），届时在下方同一轮询里挂接。
	notifyAvailable := runtime.GOOS == "linux"
	if notifyAvailable {
		if _, err := exec.LookPath("notify-send"); err != nil {
			notifyAvailable = false
		}
	}
	// TODO(Windows): 下载完成通知改用 Windows 通知中心（PowerShell toast）；macOS 可用 osascript。
	go func() {
		var mu sync.Mutex
		var inhibit *exec.Cmd
		lastStatus := map[int64]int{} // taskId → 上一轮 status（用于识别 1→3 完成迁移）
		activeRe := regexp.MustCompile(`"status":1[,:}]`)
		for {
			time.Sleep(5 * time.Second)
			resp, err := call(`{"cmd":"download_list"}`)
			active := err == nil && strings.Contains(resp, `"tasks":[`) && activeRe.MatchString(resp)
			mu.Lock()
			if active && inhibit == nil {
				inhibit = exec.Command("systemd-inhibit",
					"--what=sleep:idle", "--who=YunX", "--why=下载任务进行中", "sleep", "infinity")
				_ = inhibit.Start()
			} else if !active && inhibit != nil {
				_ = inhibit.Process.Kill()
				_, _ = inhibit.Process.Wait()
				inhibit = nil
			}
			mu.Unlock()
			// 通知与防睡眠消费同一份轮询快照，但独立于 inhibit 是否可用
			if err == nil {
				trackDownloadCompletions(call, resp, lastStatus, notifyAvailable)
			}
		}
	}()
}

// trackDownloadCompletions 解析 download_list 快照，对 status 1→3 的任务发完成通知并刷新状态表。
// 只在「上一轮下载中、这一轮已完成」时通知（启动时已处于完成态的任务不补发，符合通知直觉）。
func trackDownloadCompletions(call func(requestJSON string) (string, error), snapshot string, lastStatus map[int64]int, notifyAvailable bool) {
	var r struct {
		OK    bool `json:"ok"`
		Tasks []struct {
			TaskID   int64  `json:"taskId"`
			FileName string `json:"fileName"`
			Status   int    `json:"status"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(snapshot), &r); err != nil || !r.OK {
		return
	}
	seen := map[int64]bool{}
	for _, t := range r.Tasks {
		seen[t.TaskID] = true
		prev, tracked := lastStatus[t.TaskID]
		lastStatus[t.TaskID] = t.Status
		if !tracked || prev != 1 || t.Status != 3 {
			continue
		}
		if !notificationEnabled(call) {
			continue
		}
		notifyDownloadFinished(t.FileName, notifyAvailable)
	}
	// 清掉已从列表移除的任务，避免长驻进程下状态表无限增长
	for id := range lastStatus {
		if !seen[id] {
			delete(lastStatus, id)
		}
	}
}

// notificationEnabled 读正式设置的通知开关（settings.notificationShowSpeed，即设置页
// 「通知中心下载进度」）；读不到视为开启——与核心 SettingsRepository 默认值一致，不因
// 设置读取失败丢通知。
func notificationEnabled(call func(requestJSON string) (string, error)) bool {
	resp, err := call(`{"cmd":"settings_list"}`)
	if err != nil {
		return true
	}
	var r struct {
		Settings struct {
			NotificationShowSpeed bool `json:"notificationShowSpeed"`
		} `json:"settings"`
	}
	if err := json.Unmarshal([]byte(resp), &r); err != nil {
		return true
	}
	return r.Settings.NotificationShowSpeed
}

// notifyDownloadFinished 发系统级下载完成通知（Linux: notify-send；格式对齐规格文案）。
func notifyDownloadFinished(fileName string, available bool) {
	if !available {
		return
	}
	_ = exec.Command("notify-send", "云析下载完成", fileName+" 已完成").Start()
}

// 分享链接特征（与核心 ShareLinkParser 的平台集对齐，宽松匹配）
var shareLinkRe = regexp.MustCompile(`https?://[^\s"']*(` + strings.Join([]string{
	`pan\.quark\.cn`, `drive\.uc\.cn`, `pan\.baidu\.com`, `yun\.139\.com`, `www\.123pan\.cn`,
	`115\.com`, `guangyapan\.com`, `ilanzou\.com`, `lanzou[a-z]*\.com`, `pan\.xunlei\.com`,
}, "|") + `)[^\s"']*`)

// StartClipboardWatcher 剪贴板分享链接检测（对齐原版 ClipboardLinkPopup 语义）：
// Wayland 用 wl-paste 轮询；命中分享链接且与上次不同时回调（由 main 转成 wails 事件给前端）。
// wl-paste/xclip 均为 Linux 专属，其他平台直接停用。
// TODO(Windows): 改用 `powershell -command Get-Clipboard` 提供同等轮询（对齐原版 Windows 剪贴板监听）。
func StartClipboardWatcher(onShareLink func(link string)) {
	if runtime.GOOS != "linux" {
		return
	}
	read := func() string {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			out, err := exec.Command("wl-paste", "--no-newline").Output()
			if err == nil {
				return string(out)
			}
		}
		if xclip, err := exec.LookPath("xclip"); err == nil {
			_ = xclip
			if out, err := exec.Command("xclip", "-o", "-selection", "clipboard").Output(); err == nil {
				return string(out)
			}
		}
		return ""
	}
	if read() == "" && exec.Command("wl-paste", "--no-newline").Run() != nil {
		return // 无可用剪贴板工具（headless 等），停用
	}
	go func() {
		last := ""
		for {
			time.Sleep(2 * time.Second)
			text := strings.TrimSpace(read())
			if text == "" || text == last {
				continue
			}
			last = text
			if len(text) > 4096 {
				continue
			}
			if link := shareLinkRe.FindString(text); link != "" {
				onShareLink(link)
			}
		}
	}()
}
