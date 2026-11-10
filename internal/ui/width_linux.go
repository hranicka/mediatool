//go:build linux

package ui

import (
	"os"
	"syscall"
	"unsafe"
)

// terminalWidth returns the column count of the terminal attached to f,
// or 0 when f is not a terminal.
func terminalWidth(f *os.File) int {
	if f == nil {
		return 0
	}
	var ws struct{ Row, Col, Xpixel, Ypixel uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.Col)
}
