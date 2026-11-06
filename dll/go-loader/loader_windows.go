//go:build windows

package main

// Windows：链接 native-image 产出的导入库 yunxcore.lib（与 yunxcore.dll 同目录）；
// 运行 loader.exe 时把 yunxcore.dll 放在同目录或 PATH 里。
/*
#cgo CFLAGS: -I${SRCDIR}/../build/native
#cgo LDFLAGS: -L${SRCDIR}/../build/native -lyunxcore
*/
import "C"
