package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const progressBarWidth = 20

type progressTracker struct {
	writer    io.Writer
	total     int
	prefix    string
	enabled   bool
	startedAt time.Time
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once

	mu        sync.Mutex
	completed int
	label     string
	frame     int
}

func terminalProgressEnabled() bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func newProgressTracker(writer io.Writer, total int, prefix string, enabled bool) *progressTracker {
	tracker := &progressTracker{
		writer: writer, total: total, prefix: prefix, enabled: enabled && total > 0,
		startedAt: time.Now(), stop: make(chan struct{}), done: make(chan struct{}),
	}
	if !tracker.enabled {
		close(tracker.done)
		return tracker
	}
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
			tracker.render(false)
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

func (tracker *progressTracker) Finish(label string) {
	if !tracker.enabled {
		return
	}
	tracker.Update(tracker.total, label)
	tracker.stopOnce.Do(func() { close(tracker.stop) })
	<-tracker.done
	tracker.render(true)
	fmt.Fprintln(tracker.writer)
}

func (tracker *progressTracker) render(final bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.frame++
	line := formatProgressLine(
		tracker.completed,
		tracker.total,
		tracker.prefix,
		tracker.label,
		time.Since(tracker.startedAt),
		tracker.frame,
		final,
	)
	fmt.Fprintf(tracker.writer, "\r\x1b[2K%s", line)
}

func formatProgressLine(completed, total int, prefix, label string, elapsed time.Duration, frame int, final bool) string {
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
	if final {
		return fmt.Sprintf("[%s] %3d%% %s  %ds", bar, percent, status, int(elapsed.Seconds()))
	}
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
