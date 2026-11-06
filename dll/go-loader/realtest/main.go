// 真实链路测试（用户授权：使用真实 ~/.yunx-desktop 数据与真实账号，真网络真下载）。
//
// 流程：注入真实 machine-id（/etc/machine-id，与 App 同源，保证密钥可解）
// → accounts_list（真实库）→ 按优先级挑已登录平台 → cloud_list 根目录（必要时下探一层）
// → 选 8~15MB 的文件 → cloud_download 到 /tmp/yunx-realtest-dl → 轮询到完成
// → 校验文件大小 → download_remove(deleteLocal) 清掉测试任务 → PASS/FAIL。
//
// ★ 与 go-loader 不同：本程序【不隔离】数据目录， deliberately 触碰真实数据；
//   但只做：读账号/列目录/下载到临时目录/删除自己建的下载任务。不动账号、不动云盘文件。
package main

/*
#include <stdlib.h>
#include "libyunxcore.h"
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"runtime"
	"unsafe"
)

func call(thread *C.graal_isolatethread_t, req string) string {
	cReq := C.CString(req)
	defer C.free(unsafe.Pointer(cReq))
	out := make([]byte, 1<<22)
	n := C.yunx_call(thread, (*C.char)(unsafe.Pointer(cReq)), (*C.char)(unsafe.Pointer(&out[0])), C.int(len(out)))
	if n < 0 {
		return "<buffer too small>"
	}
	return string(out[:n])
}

type accountRow struct {
	Platform string `json:"platform"`
	Nickname string `json:"nickname"`
}
type fileRow struct {
	Fid      string `json:"fid"`
	Fname    string `json:"fname"`
	Fsize    int64  `json:"fsize"`
	Isdir    bool   `json:"isdir"`
	FidToken string `json:"fidToken"`
}

var PLATFORM_PRIORITY = []string{"quark", "uc", "xunlei", "baidu", "c139", "pan123", "pan115", "guangya", "ilanzou", "lanzou"}

func main() {
	runtime.LockOSThread()
	var isolate *C.graal_isolate_t
	var thread *C.graal_isolatethread_t
	if rc := C.graal_create_isolate(nil, &isolate, &thread); rc != 0 {
		fmt.Println("FAIL: graal_create_isolate")
		os.Exit(1)
	}

	fail := func(format string, a ...any) {
		fmt.Printf("FAIL: "+format+"\n", a...)
		os.Exit(1)
	}
	mustOK := func(cmd, desc string) string {
		resp := call(thread, cmd)
		fmt.Printf("[REQ] %s\n[RESP] %.220s\n", cmd, resp)
		if !strings.Contains(resp, `"ok":true`) {
			fail("%s 失败: %s", desc, resp)
		}
		return resp
	}

	// 1) 真实 machine-id（与 App 同源）
	guid, _ := os.ReadFile("/etc/machine-id")
	id := strings.TrimSpace(string(guid))
	if id == "" {
		fail("读不到 /etc/machine-id")
	}
	mustOK(fmt.Sprintf(`{"cmd":"host_set_machine_id","id":%q}`, id), "注入机器标识")

	// 2) 真实账号
	accResp := mustOK(`{"cmd":"accounts_list"}`, "读取真实账号")
	var acc struct {
		Accounts []accountRow `json:"accounts"`
	}
	_ = json.Unmarshal([]byte(accResp), &acc)
	if len(acc.Accounts) == 0 {
		fail("真实库中没有账号")
	}
	platforms := map[string]int{}
	for _, a := range acc.Accounts {
		platforms[a.Platform]++
		fmt.Printf("已登录: %-8s %s\n", a.Platform, a.Nickname)
	}

	// 3) 按优先级找 ~10MB 文件（窗口逐级放宽：[8,15] → [5,30] → ≥1MB；目录下探最多 20 个、深度 2）
	const saveDir = "/tmp/yunx-realtest-dl"
	_ = os.MkdirAll(saveDir, 0o755)
	windows := [][2]int64{{8_000_000, 15_000_000}, {5_000_000, 30_000_000}, {1_000_000, 1 << 62}}
	pickIn := func(files []fileRow, lo, hi int64) *fileRow {
		var cands []fileRow
		for _, f := range files {
			if !f.Isdir && f.Fsize >= lo && f.Fsize <= hi {
				cands = append(cands, f)
			}
		}
		sort.Slice(cands, func(i, j int) bool { // 最接近 10MB 的
			di, dj := cands[i].Fsize-10_000_000, cands[j].Fsize-10_000_000
			if di < 0 { di = -di }
			if dj < 0 { dj = -dj }
			return di < dj
		})
		if len(cands) == 0 { return nil }
		return &cands[0]
	}

	listFiles := func(platform, dirFid string) []fileRow {
		resp := call(thread, fmt.Sprintf(`{"cmd":"cloud_list","platform":%q,"dirFid":%q}`, platform, dirFid))
		if !strings.Contains(resp, `"ok":true`) {
			fmt.Printf("  [%s] 列目录失败（跳过该平台）: %.140s\n", platform, resp)
			return nil
		}
		var r struct{ Files []fileRow `json:"files"` }
		_ = json.Unmarshal([]byte(resp), &r)
		return r.Files
	}

	var chosen *fileRow
	var chosenPlatform string
	for _, p := range PLATFORM_PRIORITY {
		if platforms[p] == 0 { continue }
		root := listFiles(p, "")
		if root == nil { continue } // 该平台列目录失败（如 Cookie 过期）→ 跳过
		fmt.Printf("%s 根目录 %d 项\n", p, len(root))
		for _, w := range windows {
			if f := pickIn(root, w[0], w[1]); f != nil { chosen, chosenPlatform = f, p; break }
			// 下探（深度 1：最多 20 个子目录；深度 2：每个再 5 个）
			probed := 0
			for _, d := range root {
				if chosen != nil || !d.Isdir || probed >= 20 { continue }
				probed++
				l1 := listFiles(p, d.Fid)
				fmt.Printf("  下探 %s/%s: %d 项\n", p, d.Fname, len(l1))
				if f := pickIn(l1, w[0], w[1]); f != nil { chosen, chosenPlatform = f, p; break }
				sub2 := 0
				for _, d2 := range l1 {
					if chosen != nil || !d2.Isdir || sub2 >= 5 { continue }
					sub2++
					l2 := listFiles(p, d2.Fid)
					fmt.Printf("    下探 %s/%s/%s: %d 项\n", p, d.Fname, d2.Fname, len(l2))
					if f := pickIn(l2, w[0], w[1]); f != nil { chosen, chosenPlatform = f, p; break }
				}
			}
			if chosen != nil { break }
		}
		if chosen != nil { break }
	}
	if chosen == nil {
		fail("所有已登录平台的根目录/一级子目录都没有 8~15MB 的文件，改大范围或手动指定")
	}
	fmt.Printf("选中: [%s] %s (%.1f MB) fid=%s\n", chosenPlatform, chosen.Fname, float64(chosen.Fsize)/1e6, chosen.Fid)

	// 4) 真下载
	dlResp := mustOK(fmt.Sprintf(
		`{"cmd":"cloud_download","platform":%q,"fid":%q,"fname":%q,"fsize":%d,"fidToken":%q,"saveDir":%q}`,
		chosenPlatform, chosen.Fid, chosen.Fname, chosen.Fsize, chosen.FidToken, saveDir), "提交下载")
	taskID := regexp.MustCompile(`"taskId":(\d+)`).FindStringSubmatch(dlResp)
	if taskID == nil { fail("无 taskId: %s", dlResp) }
	fmt.Println("taskId =", taskID[1])

	// 5) 轮询到完成
	deadline := time.Now().Add(180 * time.Second)
	status := -1
	var last string
	for time.Now().Before(deadline) {
		time.Sleep(700 * time.Millisecond)
		last = call(thread, `{"cmd":"download_list"}`)
		m := regexp.MustCompile(`"taskId":` + taskID[1] + `,.{0,400}?"status":(\d)`).FindStringSubmatch(last)
		if m != nil {
			fmt.Sscanf(m[1], "%d", &status)
			if status == 3 || status == 4 { break }
		}
	}
	snap := regexp.MustCompile(`\{[^{}]*"taskId":` + taskID[1] + `[^{}]*\}`).FindString(last)
	fmt.Printf("最终状态: %d 快照: %.200s\n", status, snap)
	if status != 3 { fail("下载未完成（status=%d）", status) }

	// 6) 校验文件
	local := filepath.Join(saveDir, chosen.Fname)
	st, err := os.Stat(local)
	if err != nil { fail("文件不存在: %v", err) }
	if st.Size() != chosen.Fsize { fail("大小不符: 本地 %d vs 云端 %d", st.Size(), chosen.Fsize) }
	fmt.Printf("文件校验 OK: %s (%d 字节)\n", local, st.Size())

	// 7) 清理测试任务（含本地文件）
	mustOK(fmt.Sprintf(`{"cmd":"download_remove","taskId":%s,"deleteLocal":true}`, taskID[1]), "清理测试任务")

	fmt.Println("REALTEST_PASS")
}
