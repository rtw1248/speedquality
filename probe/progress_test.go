package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatProgressLine(t *testing.T) {
	line := formatProgressLine(3, 9, "湖北 IPv4", "湖北联通 / 下载测速", 12*time.Second, 1, false)
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

func TestProgressTrackerFinishesOnOneLine(t *testing.T) {
	var output bytes.Buffer
	tracker := newProgressTracker(&output, 9, "湖北 IPv4", true)
	tracker.Update(4, "湖北联通 / 上传测速")
	tracker.Finish("测速完成")
	text := output.String()
	if !strings.Contains(text, "100% 湖北 IPv4 / 测速完成") ||
		!strings.HasSuffix(text, "\n") {
		t.Fatalf("finished progress = %q", text)
	}
}
