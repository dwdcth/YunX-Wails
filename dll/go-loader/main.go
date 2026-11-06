// Go 宿主冒烟：加载 libyunxcore（GraalVM --shared 产物），经 C ABI 调用核心门面。
//
// 这是 Wails 3 壳调用 DLL 的最小形态（服务层就是把下面的 call 包成方法，见仓库根 yunxcore/service.go）。
// 约定：进程内只创建一个 isolate 并终生持有，退出即回收——不要调 graal_tear_down_isolate。
package main

/*
#include <stdlib.h>
#include "libyunxcore.h"
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

func call(thread *C.graal_isolatethread_t, req string) string {
	cReq := C.CString(req)
	defer C.free(unsafe.Pointer(cReq))
	out := make([]byte, 1<<20)
	n := C.yunx_call(thread, (*C.char)(unsafe.Pointer(cReq)), (*C.char)(unsafe.Pointer(&out[0])), C.int(len(out)))
	if n < 0 {
		return "<buffer too small>"
	}
	return string(out[:n])
}

func main() {
	// ★ 数据目录隔离：冒烟绝不碰 ~/.yunx-desktop 真实数据（曾因未隔离导致密钥被轮换，已恢复）。
	//   必须在第一次 yunx_call 之前设置（AppContext 在核心类首次加载时读环境变量）。
	os.Setenv("YUNX_DESKTOP_DATA_DIR", "/tmp/yunx-go-loader-data")
	os.RemoveAll("/tmp/yunx-go-loader-data")

	// ★ isolate 绑定创建它的 OS 线程：Go 协程会跨线程迁移，必须先锁定，
	//   否则后续调用落到别的 OS 线程时，isolate 按原线程的栈边界做检查会误判栈溢出（实测致命崩溃）。
	runtime.LockOSThread()
	var isolate *C.graal_isolate_t
	var thread *C.graal_isolatethread_t
	if rc := C.graal_create_isolate(nil, &isolate, &thread); rc != 0 {
		fmt.Println("graal_create_isolate failed:", int(rc))
		os.Exit(1)
	}
	fmt.Println("PING", int(C.yunx_ping(thread)))

	cmds := []string{
		`{"cmd":"version"}`,
		`{"cmd":"parse_share","text":"https://pan.quark.cn/s/abc123XYZ 提取码：a1b2"}`,
		`{"cmd":"parse_share","text":"https://pan.baidu.com/s/1qwertyuiop?pwd=zz99"}`,
		`{"cmd":"parse_share","text":"随便一段没有链接的文字"}`,
		`{"cmd":"kv_put","key":"smoke","value":"hello"}`,
		`{"cmd":"kv_get","key":"smoke"}`,
		`{"cmd":"key_roundtrip"}`,
		`{"cmd":"host_set_machine_id","id":"go-loader-machine-id"}`,
		`{"cmd":"host_set_proxy"}`,
		`{"cmd":"host_set_ffmpeg","path":"/nonexistent/ffmpeg"}`,
		`{"cmd":"settings_list"}`,
		`{"cmd":"accounts_list"}`,
		// 仅验命令可达性：auth_export 会读真实 miscPrefs（GitHub Token），native 侧无法隔离 prefs，不做导出冒烟
		`{"cmd":"auth_backup_name","encrypted":true}`,
		`{"cmd":"nope"}`,
	}
	for _, c := range cmds {
		fmt.Printf("REQ %s\nRESP %s\n", c, call(thread, c))
	}

	// https 探针（可选）：YUNX_HTTPS_PROBE=1 时真网络调 update_check（api.github.com），验证 TLS 可用
	if os.Getenv("YUNX_HTTPS_PROBE") == "1" {
		fmt.Println("REQ update_check(https)")
		fmt.Println("RESP " + call(thread, `{"cmd":"update_check"}`))
	}

	downloadSmoke(thread)
	fmt.Println("LOADER_DONE")
}

// downloadSmoke 在本地 Range 服务器上做真实下载端到端：提交 → 轮询 → 字节校验 → 清理。
func downloadSmoke(thread *C.graal_isolatethread_t) {
	payload := make([]byte, 4<<20)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/file.bin", func(w http.ResponseWriter, r *http.Request) {
		total := int64(len(payload))
		from, to := int64(0), total-1
		if rng := r.Header.Get("Range"); rng != "" {
			spec := strings.TrimPrefix(rng, "bytes=")
			parts := strings.SplitN(spec, "-", 2)
			from, _ = strconv.ParseInt(parts[0], 10, 64)
			if len(parts) > 1 && parts[1] != "" {
				to, _ = strconv.ParseInt(parts[1], 10, 64)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, total))
			w.WriteHeader(http.StatusPartialContent)
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Write(payload[from : to+1])
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen failed:", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	dir := "/tmp/yunx-go-loader-dl"
	os.MkdirAll(dir, 0o755)
	submit := call(thread, fmt.Sprintf(
		`{"cmd":"download_submit","url":"http://%s/file.bin","fileName":"go-loader.bin","saveDir":%q}`,
		ln.Addr().String(), dir))
	fmt.Println("REQ download_submit")
	fmt.Println("RESP", submit)
	m := regexp.MustCompile(`"taskId":(\d+)`).FindStringSubmatch(submit)
	if m == nil {
		fmt.Println("DOWNLOAD_VERIFY submit_failed")
		os.Exit(1)
	}
	taskId := m[1]

	deadline := time.Now().Add(90 * time.Second)
	status := -1
	var last string
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		last = call(thread, `{"cmd":"download_list"}`)
		sm := regexp.MustCompile(`"taskId":` + taskId + `,.{0,400}?"status":(\d)`).FindStringSubmatch(last)
		if sm != nil {
			status, _ = strconv.Atoi(sm[1])
			if status == 3 || status == 4 {
				break
			}
		}
	}
	fmt.Println("DOWNLOAD_STATUS final=", status)
	fmt.Println("LIST_SNAPSHOT", last)

	want := sha256.Sum256(payload)
	gotHash := ""
	sizeOK := false
	if b, err := os.ReadFile(dir + "/go-loader.bin"); err == nil {
		sizeOK = len(b) == len(payload)
		h := sha256.Sum256(b)
		gotHash = hex.EncodeToString(h[:8])
	}
	fmt.Printf("DOWNLOAD_VERIFY size_ok=%v content_ok=%v(%s vs %s)\n",
		sizeOK, gotHash == hex.EncodeToString(want[:8]), gotHash, hex.EncodeToString(want[:8]))
	fmt.Println("RESP", call(thread, `{"cmd":"download_remove","taskId":`+taskId+`,"deleteLocal":true}`))
	if status != 3 || !sizeOK {
		os.Exit(1)
	}
}
