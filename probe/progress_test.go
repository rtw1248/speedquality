package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatProgressLine(t *testing.T) {
	line := formatProgressLine(3, 9, "湖北 IPv4", "湖北联通 / 下载测速", 12*time.Second, 1)
	for _, expected := range []string{
		"[======>",
		" 33%",
		"湖北 IPv4 / 湖北联通 / 下载测速",
		"12s",
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("progress line lacks %q: %q", expected, line)
		}
	}
}

func TestProgressTrackerClearsLineWhenFinished(t *testing.T) {
	var output bytes.Buffer
	tracker := newProgressTracker(&output, 9, "湖北 IPv4", true)
	tracker.Update(4, "湖北联通 / 上传测速")
	tracker.Finish()
	text := output.String()
	if strings.Contains(text, "100%") || !strings.HasSuffix(text, "\r\x1b[2K") {
		t.Fatalf("finished progress = %q", text)
	}
}
