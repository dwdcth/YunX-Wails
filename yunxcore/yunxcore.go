// Package yunxcore 是核心原生库（GraalVM --shared 产物）的 Go 封装。
//
// 加载方式：purego 运行时 dlopen（Linux/macOS .so/.dylib、Windows .dll），
// 编译期零 C 依赖——不需要头文件、导入库与 cgo 链接参数，跨平台编译即纯 Go。
//
// 约定（均经实测验证）：
//   - 一个进程只创建一个 isolate，终生持有，退出即回收；不要调 graal_tear_down_isolate。
//   - isolate 绑定「创建它的 OS 线程」：Go 协程会跨 OS 线程迁移，若换了线程再进 isolate，
//     VM 按原线程的栈边界做溢出检查会误判并致命崩溃（StackOverflowError / yellow zone）。
//     因此这里用一个 LockOSThread 的专用协程独占 isolate，所有调用经 channel 串行送入。
package yunxcore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// C ABI 入口（对应 libyunxcore.h；purego 不校验签名，改动需对照头文件）。
// yunx_call 由调用方自备响应缓冲区，返回写入字节数，负数 = 缓冲区不够。
var (
	fnCreateIsolate func(params unsafe.Pointer, isolate *unsafe.Pointer, thread *unsafe.Pointer) int32
	fnPing          func(thread unsafe.Pointer) int32
	fnCall          func(thread unsafe.Pointer, request *byte, response *byte, responseLen int32) int32
)

type request struct {
	kind string // "ping" | "call"
	json string
	resp chan response
}

type response struct {
	code int
	text string
	err  error
}

// Core 是核心库的进程级句柄（零值不可用，用 Open）。
type Core struct {
	reqs chan request
}

var (
	defaultCore *Core
	openOnce    sync.Once
	openErr     error
)

// Open 返回进程级单例（首次调用时在专用线程上加载核心库并创建 isolate）。
func Open() (*Core, error) {
	openOnce.Do(func() {
		ready := make(chan error, 1)
		c := &Core{reqs: make(chan request)}
		go c.serve(ready)
		if err := <-ready; err != nil {
			openErr = err
			return
		}
		defaultCore = c
	})
	return defaultCore, openErr
}

// serve 独占 isolate 的专用协程：锁定 OS 线程后加载核心库、创建 isolate，
// 并在此线程上执行全部调用（isolate 终生绑定本线程）。
func (c *Core) serve(ready chan<- error) {
	runtime.LockOSThread()
	handle, err := purego.Dlopen(findLibrary(), purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		ready <- fmt.Errorf("加载核心库: %w", err)
		return
	}
	for _, s := range []struct {
		name string
		fn   any
	}{
		{"graal_create_isolate", &fnCreateIsolate},
		{"yunx_ping", &fnPing},
		{"yunx_call", &fnCall},
	} {
		addr, err := purego.Dlsym(handle, s.name)
		if err != nil {
			ready <- fmt.Errorf("核心库缺符号 %s: %w", s.name, err)
			return
		}
		purego.RegisterFunc(s.fn, addr)
	}
	var isolate, thread unsafe.Pointer
	if rc := fnCreateIsolate(nil, &isolate, &thread); rc != 0 {
		ready <- errors.New("graal_create_isolate failed")
		return
	}
	ready <- nil
	for req := range c.reqs {
		switch req.kind {
		case "ping":
			req.resp <- response{code: int(fnPing(thread))}
		case "call":
			text, err := callOnThread(thread, req.json)
			req.resp <- response{text: text, err: err}
		}
	}
}

func callOnThread(thread unsafe.Pointer, requestJSON string) (string, error) {
	req := append([]byte(requestJSON), 0) // C 侧按 NUL 结尾字符串读取
	out := make([]byte, 4<<20)
	n := fnCall(thread, &req[0], &out[0], int32(len(out)))
	if n < 0 {
		return "", errors.New("yunx_call: response buffer too small")
	}
	return string(out[:n]), nil
}

func (c *Core) do(req request) response {
	req.resp = make(chan response, 1)
	c.reqs <- req
	return <-req.resp
}

// Ping 连通性探针：恒为 42。
func (c *Core) Ping() int {
	return c.do(request{kind: "ping"}).code
}

// Call 执行一条 JSON 命令（协议见 dll 模块的 DllFacade），返回响应 JSON。
func (c *Core) Call(requestJSON string) (string, error) {
	r := c.do(request{kind: "call", json: requestJSON})
	return r.text, r.err
}

// findLibrary 解析核心库路径，依次尝试：
//  1. $YUNX_CORE_LIB（显式指定，.so/.dll/.dylib 全路径）
//  2. 可执行文件同目录（便携安装布局）
//  3. 可执行文件 ../dll/build/native（仓库布局：bin/yunx → dll/build/native）
//  4. ./dll/build/native（开发模式：仓库根 go run ./cmd/yunx-call）
//  5. 裸文件名（交给系统加载器按默认路径找）
func findLibrary() string {
	name := "libyunxcore.so"
	switch runtime.GOOS {
	case "windows":
		name = "yunxcore.dll"
	case "darwin":
		name = "libyunxcore.dylib"
	}
	if p := os.Getenv("YUNX_CORE_LIB"); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, p := range []string{
			filepath.Join(dir, name),
			filepath.Join(dir, "..", "dll", "build", "native", name),
		} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	if p := filepath.Join("dll", "build", "native", name); fileExists(p) {
		return p
	}
	return name
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
