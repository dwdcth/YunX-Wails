//go:build windows

package downloadd

import "os"

// termSignal 优雅终止信号（windows 无 SIGTERM 语义，直接强杀）。
func termSignal() os.Signal { return os.Kill }
