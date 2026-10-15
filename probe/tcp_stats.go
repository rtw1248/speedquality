package main

import (
	"fmt"
	"net"
	"sync"
)

// TCP payload bytes, including repeated transmissions. These counters belong
// to one upload socket; they never describe the peer's download retransmits.
type retransmissionStats struct {
	SentBytes          uint64 `json:"sent_bytes"`
	RetransmittedBytes uint64 `json:"retransmitted_bytes"`
}

func retransmissionDelta(start, end *retransmissionStats) *retransmissionStats {
	if start == nil || end == nil || end.SentBytes <= start.SentBytes ||
		end.RetransmittedBytes < start.RetransmittedBytes {
		return nil
	}
	value := &retransmissionStats{end.SentBytes - start.SentBytes, end.RetransmittedBytes - start.RetransmittedBytes}
	if value.RetransmittedBytes > value.SentBytes {
		return nil
	}
	return value
}

func displayRetransmission(mode *ModeResult) string {
	if mode == nil || mode.UploadRetransmission == nil || mode.UploadRetransmission.SentBytes == 0 {
		return "—"
	}
	stats := mode.UploadRetransmission
	percent := float64(stats.RetransmittedBytes) / float64(stats.SentBytes) * 100
	if stats.RetransmittedBytes > 0 && percent < 0.01 {
		return "<0.01%"
	}
	return fmt.Sprintf("%.2f%%", percent)
}

func retransmissionColor(mode *ModeResult) string {
	if mode == nil || mode.UploadRetransmission == nil || mode.UploadRetransmission.SentBytes == 0 {
		return "\x1b[90m"
	}
	stats := mode.UploadRetransmission
	percent := float64(stats.RetransmittedBytes) / float64(stats.SentBytes) * 100
	if percent < 1 {
		return green
	}
	if percent < 5 {
		// Match the existing speed/latency warning color on SSH clients that
		// support basic ANSI colors but not 24-bit RGB escape sequences.
		return yellow
	}
	return red
}

type tcpStatsTracker struct {
	mu    sync.Mutex
	conn  *net.TCPConn
	dials int
}

func (tracker *tcpStatsTracker) connected(conn net.Conn) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.dials++
	tracker.conn, _ = conn.(*net.TCPConn)
}

func (tracker *tcpStatsTracker) snapshot() *retransmissionStats {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	// A reconnect would reset counters. Never combine two connections.
	if tracker.dials != 1 || tracker.conn == nil {
		return nil
	}
	return readTCPBytes(tracker.conn)
}
