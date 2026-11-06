//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// processAlive 检查 PID 是否存活（Windows：tasklist 按 PID 过滤查询）。
// CSV 行形如 "yunx-demo.exe","1234",...，带引号匹配避免 123 误命中 1234。
// TODO(Windows): 可改用 OpenProcess + GetExitCodeProcess API 免去 spawn 子进程（当前实现够用）。
func processAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"`+strconv.Itoa(pid)+`"`)
}
