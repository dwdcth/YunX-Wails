// 无界面验证入口：经 Wails 服务（CoreService）所用的同一个 yunxcore 封装调 DLL 并打印结果。
// 用途：在没有 GUI 的环境（CI / 本沙箱）证明「Go → C ABI → 核心 Kotlin 逻辑」链路通。
//
//	go run ./cmd/yunx-call
//
// 环境变量 YUNX_DEBUG_CHECK=1 时追加「下载调试」扩展冒烟：
// 开 downloadDebug → 对不存在的任务 id 查 download_debug_info / 调 download_debug_preempt
//（均应优雅 ok:false，不崩，preempt 有显式断言）→ 还原开关。
// 环境变量 YUNX_C12_CHECK=1 时追加「多账号下载（C1）/多源分片（C2）功能门」冒烟：
// 四种开关组合写入读回 + 不存在 fid 触发 share_download / share_folder_download
//（应优雅 ok:false「尚未解析任何分享」，不崩）→ 还原开关。
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"yunxwailsdemo/yunxcore"
)

func main() {
	core, err := yunxcore.Open()
	if err != nil {
		fmt.Println("open core:", err)
		os.Exit(1)
	}
	fmt.Println("PING", core.Ping())
	for _, req := range []string{
		`{"cmd":"version"}`,
		`{"cmd":"parse_share","text":"https://pan.quark.cn/s/abc123XYZ 提取码：a1b2"}`,
		`{"cmd":"kv_put","key":"smoke","value":"hello"}`,
		`{"cmd":"kv_get","key":"smoke"}`,
		`{"cmd":"key_roundtrip"}`,
	} {
		resp, err := core.Call(req)
		if err != nil {
			fmt.Println("CALL ERR", err)
			os.Exit(1)
		}
		fmt.Printf("REQ %s\nRESP %s\n", req, resp)
	}
	if os.Getenv("YUNX_DEBUG_CHECK") == "1" {
		debugCheck(core)
	}
	if os.Getenv("YUNX_C12_CHECK") == "1" {
		c12Check(core)
	}
	fmt.Println("YUNX_CALL_DONE")
}

// 下载调试扩展冒烟：开关写入（含运行时联动）→ 不存在任务的 debug_info / debug_preempt 优雅失败 → 还原开关。
func debugCheck(core *yunxcore.Core) {
	fmt.Println("DEBUG_CHECK begin")
	for _, req := range []string{
		`{"cmd":"settings_set","key":"downloadDebug","value":"true"}`,
		`{"cmd":"download_debug_info","taskId":999999}`,
		`{"cmd":"download_debug_export","taskId":999999}`,
		`{"cmd":"download_debug_preempt","taskId":999999,"key":"0-1048576"}`,
		`{"cmd":"settings_set","key":"downloadDebug","value":"false"}`,
	} {
		resp, err := core.Call(req)
		if err != nil {
			fmt.Println("CALL ERR", err)
			os.Exit(1)
		}
		fmt.Printf("REQ %s\nRESP %s\n", req, resp)
	}
	// 断言：preempt 不存在的任务必须优雅 ok:false（不崩、带 error 说明）
	preemptResp, err := core.Call(`{"cmd":"download_debug_preempt","taskId":999999,"key":"0-1048576"}`)
	if err != nil {
		fmt.Println("PREEMPT CALL ERR", err)
		os.Exit(1)
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(preemptResp), &r) != nil || r.OK || r.Error == "" {
		fmt.Printf("PREEMPT 断言失败（应 ok:false + error）: %s\n", preemptResp)
		os.Exit(1)
	}
	fmt.Println("PREEMPT 断言通过（不存在任务优雅 ok:false）")
	fmt.Println("DEBUG_CHECK done")
}

// C1/C2 功能门冒烟：multiAccountDownload（C1）× multiSourceDownload（C2）四种组合写入读回，
// 每种组合下用不存在的 fid 触发 share_download / share_folder_download —— 未解析会话时都应
// 优雅 ok:false（error 含「尚未解析任何分享」），不崩，证明命令面与开关联动无异常。
// 真实多源取链需要现成分享链接与多账号，留给 GUI 实测。收尾把两个开关还原为默认关。
func c12Check(core *yunxcore.Core) {
	fmt.Println("C12_CHECK begin")
	for _, combo := range []struct{ c1, c2 string }{
		{"false", "false"}, {"true", "false"}, {"true", "true"}, {"false", "true"},
	} {
		fmt.Printf("C12_CHECK 组合 C1=%s C2=%s\n", combo.c1, combo.c2)
		for _, req := range []string{
			`{"cmd":"settings_set","key":"multiAccountDownload","value":"` + combo.c1 + `"}`,
			`{"cmd":"settings_set","key":"multiSourceDownload","value":"` + combo.c2 + `"}`,
		} {
			resp, err := core.Call(req)
			if err != nil {
				fmt.Println("CALL ERR", err)
				os.Exit(1)
			}
			fmt.Printf("REQ %s\nRESP %s\n", req, resp)
		}
		// 读回断言：settings_list 反映刚写入的开关值
		listResp, err := core.Call(`{"cmd":"settings_list"}`)
		if err != nil {
			fmt.Println("CALL ERR", err)
			os.Exit(1)
		}
		var s struct {
			OK       bool `json:"ok"`
			Settings struct {
				MultiAccountDownload bool `json:"multiAccountDownload"`
				MultiSourceDownload  bool `json:"multiSourceDownload"`
			} `json:"settings"`
		}
		if json.Unmarshal([]byte(listResp), &s) != nil || !s.OK ||
			fmt.Sprint(s.Settings.MultiAccountDownload) != combo.c1 ||
			fmt.Sprint(s.Settings.MultiSourceDownload) != combo.c2 {
			fmt.Printf("C12_CHECK 读回断言失败: %s\n", listResp)
			os.Exit(1)
		}
		fmt.Println("C12_CHECK 读回断言通过")
		// 开关组合下触发下载命令：无会话 → 优雅 ok:false（不崩）
		for _, req := range []string{
			`{"cmd":"share_download","fid":"c12-nonexistent-fid","fname":"c12.bin"}`,
			`{"cmd":"share_folder_download","fid":"c12-nonexistent-fid","fname":"c12dir","saveDir":"/tmp"}`,
		} {
			resp, err := core.Call(req)
			if err != nil {
				fmt.Println("CALL ERR", err)
				os.Exit(1)
			}
			fmt.Printf("REQ %s\nRESP %s\n", req, resp)
			var r struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if json.Unmarshal([]byte(resp), &r) != nil || r.OK || r.Error == "" {
				fmt.Printf("C12_CHECK 断言失败（应 ok:false + error）: %s\n", resp)
				os.Exit(1)
			}
		}
	}
	// 还原两个开关为默认关（隔离 prefs 环境里也保持干净）
	for _, req := range []string{
		`{"cmd":"settings_set","key":"multiAccountDownload","value":"false"}`,
		`{"cmd":"settings_set","key":"multiSourceDownload","value":"false"}`,
	} {
		resp, err := core.Call(req)
		if err != nil {
			fmt.Println("CALL ERR", err)
			os.Exit(1)
		}
		fmt.Printf("REQ %s\nRESP %s\n", req, resp)
	}
	fmt.Println("C12_CHECK 断言全部通过（四组合写入读回 + 无会话下载优雅失败）")
	fmt.Println("C12_CHECK done")
}
