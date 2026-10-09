package main

import (
	"syscall"
	"unsafe"
)

func terminalColumns(fd uintptr) int {
	var size struct {
		rows, columns, xPixels, yPixels uint16
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0
	}
	return int(size.columns)
}
