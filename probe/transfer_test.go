package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func streamTestDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/octet-stream")
	buffer := make([]byte, 32*1024)
	for {
		if _, err := w.Write(buffer); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		if r.Context().Err() != nil {
			return
		}
	}
}

func TestTransferWaitsForFirstBodyDataBeforeWarmup(t *testing.T) {
	var requests, connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		// Headers alone are not a ready data stream. The previous timing would
		// finish the entire test before this first body byte arrived.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(650 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		streamTestDownload(w, r)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	result := runTransferPhase(context.Background(), RequestSpec{Method: "GET", URL: server.URL}, "", true,
		100*time.Millisecond, 400*time.Millisecond, 100)
	if result.Err != nil || result.Mbps < 90 || result.Mbps > 105 {
		t.Fatalf("delayed stream did not receive a full measurement: %+v", result)
	}
	if result.SetupDuration < 600*time.Millisecond || result.SampleDuration < 400*time.Millisecond ||
		result.TotalDuration < 1100*time.Millisecond {
		t.Fatalf("setup/warmup/sample timing was conflated: %+v", result)
	}
	if requests.Load() != 1 || connections.Load() != 1 {
		t.Fatalf("measurement opened multiple streams: requests=%d connections=%d", requests.Load(), connections.Load())
	}
}

func TestTransferRejectsIncompleteDownload(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		name := "finite_200_body"
		if truncated {
			name = "truncated_body"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if truncated {
					w.Header().Set("Content-Length", "1000000000")
				}
				_, _ = w.Write(make([]byte, 4096))
				w.(http.Flusher).Flush()
				select {
				case <-time.After(200 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				_, _ = w.Write(make([]byte, 4096))
				w.(http.Flusher).Flush()
			}))
			defer server.Close()
			result := runTransferPhase(context.Background(), RequestSpec{Method: "GET", URL: server.URL}, "", true,
				50*time.Millisecond, 600*time.Millisecond, 100)
			want := errTransferEnded
			if truncated {
				want = io.ErrUnexpectedEOF
			}
			if !errors.Is(result.Err, want) || result.Mbps != 0 || result.Bytes == 0 {
				t.Fatalf("incomplete transfer became a valid low speed: %+v", result)
			}
		})
	}
}

func TestTransferRejectsSmallErrorPageAndEmptySuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = io.WriteString(w, "<html><body>node unavailable</body></html>")
				}
			}))
			defer server.Close()
			result := runTransferPhase(context.Background(), RequestSpec{Method: "GET", URL: server.URL}, "", true,
				50*time.Millisecond, 400*time.Millisecond, 100)
			if !errors.Is(result.Err, errTransferEnded) || result.Mbps != 0 {
				t.Fatalf("small/empty response became a valid speed: %+v", result)
			}
		})
	}
}

func TestTransferRejectsHTTPErrorAndDoesNotFollowRedirect(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "/another-node?key=private-key")
				w.WriteHeader(status)
			}))
			defer server.Close()
			result := runTransferPhase(context.Background(), RequestSpec{Method: "GET", URL: server.URL + "?key=private-key"}, "", true,
				50*time.Millisecond, 400*time.Millisecond, 100)
			if result.Err == nil || !strings.HasPrefix(diagnosticError(result.Err), "HTTP ") || result.Mbps != 0 || requests.Load() != 1 {
				t.Fatalf("HTTP response was accepted/retried: result=%+v requests=%d", result, requests.Load())
			}
			if strings.Contains(publicTransferError(result.Err), "private-key") {
				t.Fatal("public error leaked node credentials")
			}
		})
	}
}

func TestUploadRejectsFinalResponseDuringMeasurement(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.CopyN(io.Discard, r.Body, 4_000_000)
				w.Header().Set("Connection", "close")
				w.WriteHeader(status)
			}))
			defer server.Close()
			result := runTransferPhase(context.Background(), RequestSpec{Method: "POST", URL: server.URL, ContentLength: 900_000_000}, "", false,
				50*time.Millisecond, 700*time.Millisecond, 100)
			if result.Err == nil || result.Mbps != 0 || result.Bytes == 0 {
				t.Fatalf("early upload response became a valid speed: %+v", result)
			}
			if status == http.StatusTooManyRequests && diagnosticError(result.Err) != "HTTP 429" {
				t.Fatalf("lost upload rejection reason: %+v", result)
			}
			if status == http.StatusNoContent && !errors.Is(result.Err, errTransferEnded) {
				t.Fatalf("early success was not identified: %+v", result)
			}
		})
	}
}

func TestTransferRetainsGenuinelySlowStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := w.Write(make([]byte, 512)); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer server.Close()
	result := runTransferPhase(context.Background(), RequestSpec{Method: "GET", URL: server.URL}, "", true,
		100*time.Millisecond, 500*time.Millisecond, 200)
	if result.Err != nil || result.Mbps < 0.04 || result.Mbps > 0.13 {
		t.Fatalf("valid low throughput was filtered: %+v", result)
	}
}

func TestTransferRejectsStreamWithNoMeasuredData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 4096))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	result := runTransferPhase(context.Background(), RequestSpec{Method: "GET", URL: server.URL}, "", true,
		50*time.Millisecond, 250*time.Millisecond, 100)
	if !errors.Is(result.Err, errTransferNoData) || result.Mbps != 0 {
		t.Fatalf("stalled measurement was accepted: %+v", result)
	}
}

func TestTransferCancellationDuringSetupAndSampling(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "setup"
		if stream {
			name = "sampling"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					streamTestDownload(w, r)
				} else {
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			started := time.Now()
			result := runTransferPhase(ctx, RequestSpec{Method: "GET", URL: server.URL}, "", true,
				50*time.Millisecond, time.Second, 100)
			if !errors.Is(result.Err, context.DeadlineExceeded) || result.Mbps != 0 || time.Since(started) > time.Second {
				t.Fatalf("canceled measurement was accepted or did not stop: %+v", result)
			}
		})
	}
}

func TestTransferFirstDataWaitHasIndependentLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	started := time.Now()
	result := runTransferPhase(context.Background(), RequestSpec{Method: "GET", URL: server.URL}, "", true,
		50*time.Millisecond, 250*time.Millisecond, 100)
	if !errors.Is(result.Err, errTransferStartTimeout) || result.Mbps != 0 || time.Since(started) > transferStartTimeout+2*time.Second {
		t.Fatalf("first-data wait was not bounded: %+v", result)
	}
}

func TestPreparedTargetKeepsSuccessfulDirectionAndSanitizedFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "private-node-detail private-key", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "diagnostic.jsonl")
	ctx, log, err := withDiagnosticLog(context.Background(), logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	result := runPreparedTarget(ctx, Lease{LeaseID: "test_lease", DurationSeconds: 5, TargetMbps: 100}, preparedTarget{
		key: "private-key", result: MeasurementResult{Status: "failed", NodeUsed: true, NodeID: "node-id"},
		candidate: Candidate{ID: "node-id",
			Download: RequestSpec{Method: "GET", URL: server.URL + "/download?key={key}"},
			Upload:   RequestSpec{Method: "POST", URL: server.URL + "/upload?key={key}", ContentLength: 900_000_000},
		},
	}, func(int, string) {})
	if result.Status != "failed" || result.Single.DownloadMbps != 0 || result.Single.UploadMbps < 90 ||
		result.Error != "下载：节点返回 HTTP 503" {
		t.Fatalf("valid direction lost or invalid direction accepted: %+v", result)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{server.URL, "private-key", "private-node-detail"} {
		if strings.Contains(string(data), private) || strings.Contains(result.Error, private) {
			t.Fatalf("diagnostic output leaked %q", private)
		}
	}
	completed := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["event"] == "transfer.completed" {
			completed++
			if entry["setup_ms"] == nil || entry["measurement_ms"] == nil || entry["status"] == nil {
				t.Fatalf("transfer diagnostics lack separate timing/status: %v", entry)
			}
		}
	}
	if scanner.Err() != nil || completed != 2 {
		t.Fatalf("incomplete diagnostics: completed=%d error=%v", completed, scanner.Err())
	}
}
