package main

import (
	"bytes"
	"regexp"
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
	tracker := newProgressTracker(&output, 9, "湖北 IPv4", true, nil)
	tracker.Update(4, "湖北联通 / 上传测速")
	tracker.Finish()
	text := output.String()
	if strings.Contains(text, "100%") || !strings.HasSuffix(text, "\r\x1b[2K") {
		t.Fatalf("finished progress = %q", text)
	}
}

func TestProgressTipsMatchFeatureAvailabilityAndRotate(t *testing.T) {
	for _, tip := range progressTips(false) {
		if strings.Contains(tip.text, "--nq") {
			t.Fatal("disabled NodeQuality binding is advertised")
		}
	}
	tips := progressTips(true)
	for _, previous := range []string{"", tips[0].text, tips[len(tips)-1].text} {
		counts := map[string]int{}
		total := 0
		for _, tip := range tips {
			if tip.text != previous {
				total += tip.weight
			}
		}
		for choice := 0; choice < total; choice++ {
			selected := progressTip(tips, previous, func(n int) int {
				if n != total {
					t.Fatalf("draw range = %d, expected %d", n, total)
				}
				return choice
			})
			counts[selected]++
		}
		for _, tip := range tips {
			want := tip.weight
			if tip.text == previous {
				want = 0
			}
			if counts[tip.text] != want {
				t.Fatalf("tip %q has %d chances, expected %d", tip.text, counts[tip.text], want)
			}
		}
	}
	if tips[0].weight <= tips[2].weight || !strings.Contains(tips[len(tips)-1].text, "--nq") {
		t.Fatal("province and NodeQuality tips should have higher priority")
	}
	tracker := &progressTracker{tips: tips, tipSlot: -1}
	first := tracker.currentTip(0)
	if tracker.currentTip(7*time.Second) != first {
		t.Fatal("tip changed before the reading interval ended")
	}
	if tracker.currentTip(8*time.Second) == first {
		t.Fatal("consecutive tips repeated")
	}
}

func TestProgressTipsFitNarrowTerminalsAndClearOnFinish(t *testing.T) {
	t.Setenv("COLUMNS", "40")
	var output bytes.Buffer
	tracker := newProgressTracker(&output, 9, "湖北 IPv4", true, progressTips(true))
	tracker.Update(4, "湖北联通 / 上传测速")
	tracker.render()
	tracker.render()
	tracker.Finish()
	text := output.String()
	if !strings.Contains(text, "提示：") {
		t.Fatalf("tip was not displayed: %q", text)
	}
	if !strings.Contains(text, "\r\x1b[1A\r\x1b[2K") ||
		!strings.HasSuffix(text, "\r\x1b[2K\x1b[1A\r\x1b[2K") {
		t.Fatalf("tips did not refresh and clear in place: %q", text)
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`).ReplaceAllString(text, "")
	for _, line := range strings.FieldsFunc(plain, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if displayWidth(line) >= 40 {
			t.Fatalf("progress wraps on a narrow terminal: %q", line)
		}
	}
	tracker.Finish()
	if output.String() != text {
		t.Fatal("repeated Finish moved the terminal cursor again")
	}

	var redirected bytes.Buffer
	disabled := newProgressTracker(&redirected, 9, "湖北 IPv4", false, progressTips(true))
	disabled.Update(4, "上传测速")
	disabled.Finish()
	if redirected.Len() != 0 {
		t.Fatal("disabled progress polluted redirected output")
	}
}
