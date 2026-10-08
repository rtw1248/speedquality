package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	slowLockWait = 250 * time.Millisecond
	slowLockHold = 500 * time.Millisecond
	stalledLock  = 2 * time.Second
)

type requestContextKey struct{}

var (
	requestSequence atomic.Uint64
	nodeLogger      = newNodeLogger()
	logURLPattern   = regexp.MustCompile(`(?i)https?://[^\s"']+`)
	logIPv4Pattern  = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	logIPv6Pattern  = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{0,4}:){2,}[0-9a-f]{0,4}\b`)
	logTokenPattern = regexp.MustCompile(`\b(?:sqn|sqa)_[A-Za-z0-9_-]{16,}\b`)
	logJWTPattern   = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
)

func newNodeLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQ_NODE_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func nodeLog(ctx context.Context, level slog.Level, event string, attributes ...any) {
	values := []any{"event", event}
	if requestID, _ := ctx.Value(requestContextKey{}).(string); requestID != "" {
		values = append(values, "request_id", requestID)
	}
	nodeLogger.Log(ctx, level, event, append(values, attributes...)...)
}

func shortDiagnosticID(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:6])
}

func shortNodeID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func safeLogError(err error) string {
	if err == nil {
		return ""
	}
	return safeLogText(err.Error())
}

func safeLogText(input string) string {
	value := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, input)
	value = logURLPattern.ReplaceAllString(value, "[redacted-url]")
	value = logIPv4Pattern.ReplaceAllString(value, "[redacted-ip]")
	value = logIPv6Pattern.ReplaceAllString(value, "[redacted-ip]")
	value = logTokenPattern.ReplaceAllString(value, "[redacted-credential]")
	value = logJWTPattern.ReplaceAllString(value, "[redacted-jwt]")
	if len(value) > 300 {
		value = value[:300]
	}
	return value
}

func newRequestID() string {
	return fmt.Sprintf("node-%x", requestSequence.Add(1))
}

func logGoroutineDump(ctx context.Context, reason string) {
	buffer := make([]byte, 1024*1024)
	length := runtime.Stack(buffer, true)
	truncated := length == len(buffer)
	buffer = buffer[:length]
	const chunkSize = 12 * 1024
	chunks := (len(buffer) + chunkSize - 1) / chunkSize
	dumpID := newRequestID()
	for index := 0; index < chunks; index++ {
		end := (index + 1) * chunkSize
		if end > len(buffer) {
			end = len(buffer)
		}
		nodeLog(ctx, slog.LevelError, "runtime.goroutine_dump",
			"dump_id", dumpID,
			"reason", reason,
			"chunk", index+1,
			"chunks", chunks,
			"truncated", truncated,
			"stack", string(buffer[index*chunkSize:end]),
		)
	}
}

func diagnosticSignalLoop(ctx context.Context) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	defer signal.Stop(signals)
	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			logGoroutineDump(ctx, "sigusr1")
		}
	}
}

type observedResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (writer *observedResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *observedResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	written, err := writer.ResponseWriter.Write(data)
	writer.bytes += int64(written)
	return written, err
}

func (writer *observedResponseWriter) Flush() {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func observeRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		requestID := newRequestID()
		ctx := context.WithValue(request.Context(), requestContextKey{}, requestID)
		request = request.WithContext(ctx)
		response.Header().Set("X-Request-ID", requestID)
		observed := &observedResponseWriter{ResponseWriter: response}
		defer func() {
			if recovered := recover(); recovered != nil {
				nodeLog(ctx, slog.LevelError, "http.request_panic",
					"method", request.Method,
					"path", request.URL.Path,
					"panic", safeLogText(fmt.Sprint(recovered)),
					"duration_ms", time.Since(started).Milliseconds(),
				)
				panic(recovered)
			}
			if observed.status == 0 {
				observed.status = http.StatusOK
			}
			if request.URL.Path != "/healthz" || observed.status >= 400 {
				nodeLog(ctx, slog.LevelInfo, "http.request_completed",
					"method", request.Method,
					"path", request.URL.Path,
					"status", observed.status,
					"response_bytes", observed.bytes,
					"duration_ms", time.Since(started).Milliseconds(),
				)
			}
		}()
		next.ServeHTTP(observed, request)
	})
}

type monitoredMutex struct {
	lock        sync.Mutex
	metadata    sync.Mutex
	acquiredAt  time.Time
	lastStalled time.Time
}

func (mutex *monitoredMutex) Lock() {
	started := time.Now()
	mutex.lock.Lock()
	waited := time.Since(started)
	mutex.metadata.Lock()
	mutex.acquiredAt = time.Now()
	mutex.metadata.Unlock()
	if waited >= slowLockWait {
		nodeLog(context.Background(), slog.LevelWarn, "state.lock_wait_slow",
			"wait_ms", waited.Milliseconds(),
		)
	}
}

func (mutex *monitoredMutex) Unlock() {
	mutex.metadata.Lock()
	held := time.Since(mutex.acquiredAt)
	mutex.acquiredAt = time.Time{}
	mutex.metadata.Unlock()
	mutex.lock.Unlock()
	if held >= slowLockHold {
		nodeLog(context.Background(), slog.LevelWarn, "state.lock_held_slow",
			"held_ms", held.Milliseconds(),
		)
	}
}

func (mutex *monitoredMutex) stalled(now time.Time) (time.Duration, bool) {
	mutex.metadata.Lock()
	defer mutex.metadata.Unlock()
	if mutex.acquiredAt.IsZero() {
		return 0, false
	}
	held := now.Sub(mutex.acquiredAt)
	if held < stalledLock || (!mutex.lastStalled.IsZero() && now.Sub(mutex.lastStalled) < 5*time.Second) {
		return held, false
	}
	mutex.lastStalled = now
	return held, true
}
