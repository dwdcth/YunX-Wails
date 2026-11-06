// 无界面验证入口：经 Wails 服务（CoreService）所用的同一个 yunxcore 封装调 DLL 并打印结果。
// 用途：在没有 GUI 的环境（CI / 本沙箱）证明「Go → C ABI → 核心 Kotlin 逻辑」链路通。
//
//	go run ./cmd/yunx-call
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
	fmt.Println("YUNX_CALL_DONE")
}
