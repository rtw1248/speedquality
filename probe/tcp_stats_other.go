//go:build !linux

package main

import "net"

func readTCPBytes(_ *net.TCPConn) *retransmissionStats { return nil }
