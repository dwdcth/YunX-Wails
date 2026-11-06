//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"syscall"
)

// processAlive 检查 PID 是否存活（POSIX：kill -0 语义，只探测不投递信号）。
// EPERM（进程存在但属其他用户）也视为存活，避免误判他人进程为陈旧锁。
// ★ 僵尸进程例外：kill -0 对僵尸也成功（/proc 条目还在），但僵尸已死不该挡锁——
// 僵尸的 /proc/<pid>/cmdline 为空，据此判死。
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	if pid == os.Getpid() {
		return true // 自身（防御：不该发生）
	}
	data, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if readErr != nil {
		return false // Linux 无 /proc 时保守判死（本应用目标平台均有 /proc）
	}
	return len(bytes.TrimSpace(data)) > 0
}
