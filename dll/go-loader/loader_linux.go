//go:build linux

package main

/*
#cgo CFLAGS: -I${SRCDIR}/../build/native
#cgo LDFLAGS: -L${SRCDIR}/../build/native -lyunxcore -Wl,-rpath,${SRCDIR}/../build/native
*/
import "C"
