package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	progressBarWidth    = 20
	progressTipInterval = 8 * time.Second
)

type usageTip struct {
	text   string
	weight int
}

type progressTipState struct {
	Text          string `json:"text"`
	ElapsedMillis int64  `json:"elapsed_millis"`
}

type progressTracker struct {
	writer       io.Writer
	total        int
	prefix       string
	enabled      bool
	startedAt    time.Time
	stop         chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
	tips         []usageTip
	tipStatePath string
	tipOffset    time.Duration

	mu        sync.Mutex
	completed int
	label     string
	frame     int
	tipShown  bool
	tipSlot   int
	tip       string
}

func terminalProgressEnabled() bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func newProgressTracker(writer io.Writer, total int, prefix string, enabled bool, tips []usageTip, tipStatePath string) *progressTracker {
	tracker := &progressTracker{
		writer: writer, total: total, prefix: prefix, enabled: enabled && total > 0,
		startedAt: time.Now(), stop: make(chan struct{}), done: make(chan struct{}),
		tips: append([]usageTip(nil), tips...), tipSlot: -1,
		tipStatePath: tipStatePath,
	}
	if !tracker.enabled {
		close(tracker.done)
		return tracker
	}
	tracker.loadTipState()
	go tracker.loop()
	return tracker
}

func (tracker *progressTracker) loop() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	defer close(tracker.done)
	for {
		select {
		case <-ticker.C:
			tracker.render()
		case <-tracker.stop:
			return
		}
	}
}

func (tracker *progressTracker) Update(completed int, label string) {
	if !tracker.enabled {
		return
	}
	tracker.mu.Lock()
	if completed < 0 {
		completed = 0
	}
	if completed > tracker.total {
		completed = tracker.total
	}
	tracker.completed = completed
	tracker.label = label
	tracker.mu.Unlock()
}

func (tracker *progressTracker) Finish() {
	if !tracker.enabled {
		return
	}
	tracker.stopOnce.Do(func() {
		close(tracker.stop)
		<-tracker.done
		tracker.saveTipState(time.Since(tracker.startedAt))
		if tracker.tipShown {
			fmt.Fprint(tracker.writer, "\r\x1b[2K\x1b[1A")
		}
		fmt.Fprint(tracker.writer, "\r\x1b[2K")
	})
}

func (tracker *progressTracker) render() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.frame++
	elapsed := time.Since(tracker.startedAt)
	line := formatProgressLine(
		tracker.completed,
		tracker.total,
		tracker.prefix,
		tracker.label,
		elapsed,
		tracker.frame,
	)
	width := progressTerminalWidth(tracker.writer) - 1
	if tracker.tipShown {
		fmt.Fprint(tracker.writer, "\r\x1b[1A")
	}
	fmt.Fprintf(tracker.writer, "\r\x1b[2K%s", truncateProgressLine(line, width))
	if tip := tracker.currentTip(elapsed); tip != "" {
		fmt.Fprintf(tracker.writer, "\n\r\x1b[2K%s", truncateProgressLine("提示："+tip, width))
		tracker.tipShown = true
	}
}

func progressTips(nodeQualityEnabled bool) []usageTip {
	tips := []usageTip{
		{"用 -p hb,bj 选择省份（最多 5 个）；-l 查看省份列表", 4},
		{"报告页支持复制文本、NodeSeek 和 Markdown", 2},
		{"用 -s 100 / 200 / 400 选择限速档位", 1},
		{"默认测 IPv4/IPv6；-v4 或 -v6 可单独测", 1},
		{"速度后的 ✓ 表示达到所选档位，并非峰值", 1},
		{"测速不会安装系统软件或启动后台服务", 1},
		{"用 -l 查看地区代码，-h 查看完整用法", 1},
	}
	if nodeQualityEnabled {
		tips = append(tips, usageTip{"用 --nq 报告链接 关联 NodeQuality 报告", 4})
	}
	return tips
}

func (tracker *progressTracker) currentTip(elapsed time.Duration) string {
	slot := int((elapsed + tracker.tipOffset) / progressTipInterval)
	if slot != tracker.tipSlot {
		tracker.tip = progressTip(tracker.tips, tracker.tip, rand.IntN)
		tracker.tipSlot = slot
	}
	return tracker.tip
}

func (tracker *progressTracker) loadTipState() {
	if tracker.tipStatePath == "" {
		return
	}
	file, err := os.Open(tracker.tipStatePath)
	if err != nil {
		return
	}
	defer file.Close()
	var state progressTipState
	if json.NewDecoder(io.LimitReader(file, 4096)).Decode(&state) != nil ||
		state.ElapsedMillis < 0 || state.ElapsedMillis >= progressTipInterval.Milliseconds() {
		return
	}
	for _, tip := range tracker.tips {
		if tip.text == state.Text && tip.weight > 0 {
			tracker.tip = state.Text
			tracker.tipOffset = time.Duration(state.ElapsedMillis) * time.Millisecond
			tracker.tipSlot = 0
			return
		}
	}
}

func (tracker *progressTracker) saveTipState(elapsed time.Duration) {
	if tracker.tipStatePath == "" || len(tracker.tips) == 0 {
		return
	}
	state := progressTipState{
		Text:          tracker.currentTip(elapsed),
		ElapsedMillis: ((elapsed + tracker.tipOffset) % progressTipInterval).Milliseconds(),
	}
	encoded, err := json.Marshal(state)
	if err == nil {
		// Optional display state stays in the Bash run's private temporary directory.
		_ = os.WriteFile(tracker.tipStatePath, encoded, 0o600)
	}
}

func progressTip(tips []usageTip, previous string, draw func(int) int) string {
	total := 0
	for _, tip := range tips {
		if tip.text != previous && tip.weight > 0 {
			total += tip.weight
		}
	}
	if total == 0 {
		return previous
	}
	choice := draw(total)
	for _, tip := range tips {
		if tip.text == previous || tip.weight <= 0 {
			continue
		}
		if choice < tip.weight {
			return tip.text
		}
		choice -= tip.weight
	}
	return previous
}

func progressTerminalWidth(writer io.Writer) int {
	if file, ok := writer.(interface{ Fd() uintptr }); ok {
		if columns := terminalColumns(file.Fd()); columns > 0 {
			return columns
		}
	}
	if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns > 0 && columns <= 4096 {
		return columns
	}
	return 80
}

func truncateProgressLine(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(text) <= width {
		return text
	}
	suffix := "..."
	if width < len(suffix) {
		return strings.Repeat(".", width)
	}
	var clipped strings.Builder
	remaining := width - len(suffix)
	for _, character := range text {
		remaining -= displayWidth(string(character))
		if remaining < 0 {
			break
		}
		clipped.WriteRune(character)
	}
	return clipped.String() + suffix
}

func formatProgressLine(completed, total int, prefix, label string, elapsed time.Duration, frame int) string {
	if total < 1 {
		total = 1
	}
	if completed < 0 {
		completed = 0
	}
	if completed > total {
		completed = total
	}
	percent := completed * 100 / total
	filled := completed * progressBarWidth / total
	bar := strings.Repeat("=", filled)
	if filled < progressBarWidth {
		bar += ">" + strings.Repeat(" ", progressBarWidth-filled-1)
	} else {
		bar = strings.Repeat("=", progressBarWidth)
	}
	status := strings.TrimSpace(strings.Join([]string{prefix, label}, " / "))
	spinner := []byte{'|', '/', '-', '\\'}
	return fmt.Sprintf(
		"[%s] %3d%% %s  %ds %c",
		bar,
		percent,
		status,
		int(elapsed.Seconds()),
		spinner[frame%len(spinner)],
	)
}
