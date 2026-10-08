package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenericNodeLifecycleAndTransfers(t *testing.T) {
	var released atomic.Bool
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/activate":
			_, _ = io.WriteString(writer, "OKtest-key")
		case "/download":
			if request.URL.Query().Get("key") != "test-key" {
				http.Error(writer, "bad key", http.StatusForbidden)
				return
			}
			writer.Header().Set("content-type", "application/octet-stream")
			buffer := make([]byte, 32*1024)
			for {
				if _, err := writer.Write(buffer); err != nil {
					return
				}
				if flusher, ok := writer.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case <-request.Context().Done():
					return
				default:
				}
			}
		case "/upload":
			if request.Header.Get("Key") != "test-key" {
				http.Error(writer, "bad key", http.StatusForbidden)
				return
			}
			_, _ = io.Copy(io.Discard, request.Body)
		case "/release":
			if request.URL.Query().Get("key") == "test-key" {
				released.Store(true)
			}
		default:
			http.NotFound(writer, request)
		}
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets are unavailable: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		ID:      "0123456789abcdef0123456789abcdef",
		Address: parsed.Hostname(),
		Port:    port,
		Activate: ActivationSpec{
			RequestSpec:    RequestSpec{Method: "GET", URL: server.URL + "/activate"},
			KeyPrefixBytes: 2,
		},
		Download: RequestSpec{Method: "GET", URL: server.URL + "/download?key={key}&nonce={nonce}"},
		Upload: RequestSpec{
			Method:        "POST",
			URL:           server.URL + "/upload",
			Headers:       map[string]string{"Key": "{key}"},
			ContentLength: 1_000_000_000,
		},
		Release: RequestSpec{Method: "POST", URL: server.URL + "/release?key={key}"},
	}

	key, err := activateCandidate(context.Background(), candidate)
	if err != nil {
		t.Fatalf("activateCandidate: %v", err)
	}
	if key != "test-key" {
		t.Fatalf("activation key = %q", key)
	}
	downMbps, downBytes := runTransferPhase(
		context.Background(), candidate.Download, key, 1, true,
		100*time.Millisecond, 700*time.Millisecond, 100,
	)
	upMbps, upBytes := runTransferPhase(
		context.Background(), candidate.Upload, key, 1, false,
		100*time.Millisecond, 700*time.Millisecond, 100,
	)
	if downMbps <= 0 || downBytes <= 0 {
		t.Fatalf("download did not produce traffic: %.2f Mbps / %d bytes", downMbps, downBytes)
	}
	if upMbps <= 0 || upBytes <= 0 {
		t.Fatalf("upload did not produce traffic: %.2f Mbps / %d bytes", upMbps, upBytes)
	}
	if downMbps > 105 || upMbps > 105 {
		t.Fatalf("rate limiter exceeded target: download %.2f Mbps, upload %.2f Mbps", downMbps, upMbps)
	}
	releaseCandidate(candidate, key)
	if !released.Load() {
		t.Fatal("release request was not received")
	}
	if strings.Contains(fmt.Sprint(downMbps, upMbps), "NaN") {
		t.Fatal("throughput contains NaN")
	}
}

func TestDisplaySpeedMarksReachedTarget(t *testing.T) {
	if actual := displaySpeed(198, 200); actual != "200Mbps ✓" {
		t.Fatalf("displaySpeed reached target = %q", actual)
	}
	if actual := displaySpeed(195.9, 200); actual != "195.90Mbps" {
		t.Fatalf("displaySpeed below target = %q", actual)
	}
	if speedColor(160, 200) != green || speedColor(159.9, 200) != yellow ||
		speedColor(60, 200) != yellow || speedColor(59.9, 200) != red {
		t.Fatal("speed color thresholds do not match 30% and 80% boundaries")
	}
	if latencyColor(100) != green || latencyColor(100.01) != yellow ||
		latencyColor(200) != yellow || latencyColor(200.01) != red {
		t.Fatal("latency color thresholds do not match 100ms and 200ms boundaries")
	}
}

func TestByteRateLimiterPacesAggregateBytes(t *testing.T) {
	limiter := newByteRateLimiter(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	if err := limiter.wait(ctx, limiterBurst); err != nil {
		t.Fatal(err)
	}
	if err := limiter.wait(ctx, limiterBurst); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if elapsed < 400*time.Millisecond || elapsed > time.Second {
		t.Fatalf("limiter elapsed = %v, want about 524ms", elapsed)
	}
}
