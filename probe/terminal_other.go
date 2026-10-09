//go:build !linux

package main

func terminalColumns(_ uintptr) int { return 0 }
