package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
)

type diagnosticContextKey struct{}

func withDiagnosticLog(ctx context.Context, path string) (context.Context, io.Closer, error) {
	if path == "" {
		return ctx, nil, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return ctx, nil, err
	}
	logger := slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return context.WithValue(ctx, diagnosticContextKey{}, logger), file, nil
}

func diagnostic(ctx context.Context, level slog.Level, event string, attributes ...any) {
	logger, _ := ctx.Value(diagnosticContextKey{}).(*slog.Logger)
	if logger == nil {
		return
	}
	logger.Log(ctx, level, event, append([]any{"event", event}, attributes...)...)
}

func diagnosticError(err error) string {
	if err == nil {
		return ""
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return diagnosticError(urlError.Err)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "network_unreachable"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "timeout"
	}
	message := err.Error()
	if strings.HasPrefix(message, "HTTP ") || message == "response is too large" ||
		message == "节点拒绝本次测速" || message == "节点没有返回有效激活凭据" {
		return message
	}
	return fmt.Sprintf("%T", err)
}

func publicNetworkError(err error) string {
	switch diagnosticError(err) {
	case "deadline_exceeded", "timeout":
		return "连接超时"
	case "canceled":
		return "连接已取消"
	case "connection_refused":
		return "连接被拒绝"
	case "connection_reset":
		return "连接被重置"
	case "network_unreachable":
		return "网络不可达"
	case "节点拒绝本次测速", "节点没有返回有效激活凭据":
		return diagnosticError(err)
	default:
		if strings.HasPrefix(diagnosticError(err), "HTTP ") {
			return "节点返回 " + diagnosticError(err)
		}
		return "节点连接失败"
	}
}
