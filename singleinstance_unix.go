//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// processAlive 检查 PID 是否存活（POSIX：kill -0 语义，只探测不投递信号）。
// EPERM（进程存在但属其他用户）也视为存活，避免误判他人进程为陈旧锁。
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
