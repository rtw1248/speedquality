package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestRetransmissionCountsOnlyTheSampleAndPreservesMissingData(t *testing.T) {
	start := &retransmissionStats{SentBytes: 1_000_000, RetransmittedBytes: 50_000}
	end := &retransmissionStats{SentBytes: 2_000_000, RetransmittedBytes: 70_000}
	delta := retransmissionDelta(start, end)
	if delta == nil || displayRetransmission(&ModeResult{UploadRetransmission: delta}) != "2.00%" {
		t.Fatalf("warmup bytes or retransmits leaked into sample: %+v", delta)
	}
	for _, pair := range [][2]*retransmissionStats{
		{nil, end}, {start, nil}, {start, start}, {end, start},
		{start, {SentBytes: 1_000_001, RetransmittedBytes: 50_010}},
	} {
		if retransmissionDelta(pair[0], pair[1]) != nil {
			t.Fatal("invalid counters became a rate")
		}
	}
	if displayRetransmission(nil) != "—" {
		t.Fatal("missing is not zero")
	}
	for _, sample := range []struct {
		stats retransmissionStats
		text  string
	}{
		{retransmissionStats{1_000_000, 1}, "<0.01%"},
		{retransmissionStats{1_000_000, 0}, "0.00%"},
		{retransmissionStats{1_000_000, 1_000_000}, "100.00%"},
	} {
		if displayRetransmission(&ModeResult{UploadRetransmission: &sample.stats}) != sample.text {
			t.Fatalf("unexpected display for %+v", sample)
		}
	}
}

func TestUploadReadsActualSocketCountersWithoutAdditionalRequests(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux TCP_INFO")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()
	result := runTransferPhase(context.Background(), RequestSpec{
		Method: "POST", URL: server.URL, ContentLength: 100_000_000,
	}, "", false, 80*time.Millisecond, 200*time.Millisecond, 100)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Retransmission == nil || result.Retransmission.SentBytes == 0 {
		t.Fatalf("missing real TCP_INFO upload sample: %+v", result)
	}
}
