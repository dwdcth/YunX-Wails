// 无界面验证入口：经 Wails 服务（CoreService）所用的同一个 yunxcore 封装调 DLL 并打印结果。
// 用途：在没有 GUI 的环境（CI / 本沙箱）证明「Go → C ABI → 核心 Kotlin 逻辑」链路通。
//
//	go run ./cmd/yunx-call
//
// 环境变量 YUNX_DEBUG_CHECK=1 时追加「下载调试」扩展冒烟：
// 开 downloadDebug → 对不存在的任务 id 查 download_debug_info（应优雅 ok:false，不崩）→ 还原开关。
package main

import (
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
	fmt.Println("YUNX_CALL_DONE")
}

// 下载调试扩展冒烟：开关写入（含运行时联动）→ 不存在任务的 debug_info 优雅失败 → 还原开关。
func debugCheck(core *yunxcore.Core) {
	fmt.Println("DEBUG_CHECK begin")
	for _, req := range []string{
		`{"cmd":"settings_set","key":"downloadDebug","value":"true"}`,
		`{"cmd":"download_debug_info","taskId":999999}`,
		`{"cmd":"download_debug_export","taskId":999999}`,
		`{"cmd":"settings_set","key":"downloadDebug","value":"false"}`,
	} {
		resp, err := core.Call(req)
		if err != nil {
			fmt.Println("CALL ERR", err)
			os.Exit(1)
		}
		fmt.Printf("REQ %s\nRESP %s\n", req, resp)
	}
	fmt.Println("DEBUG_CHECK done")
}
