//go:build !windows

package downloadd

import (
	"os"
	"syscall"
)

// termSignal 优雅终止信号（unix 用 SIGTERM，windows 直接 Kill）。
func termSignal() os.Signal { return syscall.SIGTERM }
