//go:build linux

package main

import (
	"net"
	"syscall"
	"unsafe"
)

func readTCPBytes(conn *net.TCPConn) *retransmissionStats {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil
	}
	// Linux uapi tcp_info: bytes_sent and bytes_retrans are at offsets 200
	// and 208 on both supported architectures (amd64/arm64). Request only
	// this ABI prefix and check the returned length for older kernels.
	var info struct {
		Prefix       [200]byte
		BytesSent    uint64
		BytesRetrans uint64
	}
	length := uint32(unsafe.Sizeof(info))
	var syscallErr syscall.Errno
	err = raw.Control(func(fd uintptr) {
		_, _, syscallErr = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd,
			syscall.IPPROTO_TCP, syscall.TCP_INFO, uintptr(unsafe.Pointer(&info)),
			uintptr(unsafe.Pointer(&length)), 0)
	})
	if err != nil || syscallErr != 0 || length < uint32(unsafe.Sizeof(info)) {
		return nil
	}
	return &retransmissionStats{info.BytesSent, info.BytesRetrans}
}
