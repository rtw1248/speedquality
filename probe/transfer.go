package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const transferStartTimeout = 10 * time.Second

var (
	errTransferStartTimeout = errors.New("transfer start timeout")
	errTransferEnded        = errors.New("transfer ended before measurement completed")
	errTransferNoData       = errors.New("no data during measurement")
)

type transferMeasurement struct {
	Mbps           float64
	Bytes          int64
	SetupDuration  time.Duration
	SampleDuration time.Duration
	TotalDuration  time.Duration
	Err            error
}

type transferCompletion struct {
	finishedAt time.Time
	err        error
}

// Each phase uses exactly one request and one connection. Connection/first-data
// waiting is bounded independently, then the same stream warms up and is sampled.
func runTransferPhase(
	parent context.Context,
	spec RequestSpec,
	key string,
	download bool,
	preheat time.Duration,
	duration time.Duration,
	targetMbps int,
) (result transferMeasurement) {
	started := time.Now()
	ctx, cancel := context.WithCancel(parent)
	counter := &atomicCounter{started: make(chan time.Time, 1)}
	limiter := newByteRateLimiter(targetMbps)
	done := make(chan transferCompletion, 1)
	go func() {
		var err error
		if download {
			err = downloadWorker(ctx, spec, key, counter, limiter)
		} else {
			err = uploadWorker(ctx, spec, key, counter, limiter)
		}
		done <- transferCompletion{finishedAt: time.Now(), err: err}
	}()
	var completion *transferCompletion
	var sampleStarted time.Time
	var baseline int64
	defer func() {
		// Snapshot before cancellation/worker cleanup, which must not enter the
		// throughput denominator or add bytes from after the measurement window.
		ended := time.Now()
		if !sampleStarted.IsZero() {
			result.Bytes = counter.Load() - baseline
			result.SampleDuration = ended.Sub(sampleStarted)
		}
		if result.SetupDuration == 0 {
			result.SetupDuration = ended.Sub(started)
		}
		result.TotalDuration = ended.Sub(started)
		cancel()
		if completion == nil {
			cleanup := time.NewTimer(time.Second)
			defer cleanup.Stop()
			select {
			case value := <-done:
				completion = &value
			case <-cleanup.C:
				if result.Err == nil {
					result.Err = errors.New("transfer did not stop")
				}
			}
		}
		if result.Err == nil && parent.Err() != nil {
			result.Err = parent.Err()
		}
		if result.Err == nil && completion != nil {
			// A timer and a server rejection/EOF may become ready together. An
			// earlier termination must never be mistaken for our own cancellation.
			if completion.finishedAt.Before(sampleStarted.Add(duration)) ||
				(!errors.Is(completion.err, context.Canceled) && !errors.Is(completion.err, errTransferEnded)) {
				result.Err = completion.err
			}
		}
		if result.Err == nil && (result.Bytes <= 0 || result.SampleDuration <= 0) {
			result.Err = errTransferNoData
		}
		if result.Err == nil {
			result.Mbps = float64(result.Bytes) * 8 / 1_000_000 / result.SampleDuration.Seconds()
		}
	}()

	setupTimer := time.NewTimer(transferStartTimeout)
	defer setupTimer.Stop()
	var firstData time.Time
	select {
	case firstData = <-counter.started:
		result.SetupDuration = firstData.Sub(started)
	case value := <-done:
		completion, result.Err = &value, value.err
		return
	case <-setupTimer.C:
		result.Err = errTransferStartTimeout
		return
	case <-parent.Done():
		result.Err = parent.Err()
		return
	}
	setupTimer.Stop()

	warmupTimer := time.NewTimer(time.Until(firstData.Add(preheat)))
	defer warmupTimer.Stop()
	select {
	case <-warmupTimer.C:
	case value := <-done:
		completion, result.Err = &value, value.err
		return
	case <-parent.Done():
		result.Err = parent.Err()
		return
	}
	baseline = counter.Load()
	sampleStarted = time.Now()
	sampleTimer := time.NewTimer(duration)
	defer sampleTimer.Stop()
	select {
	case <-sampleTimer.C:
	case value := <-done:
		completion, result.Err = &value, value.err
	case <-parent.Done():
		result.Err = parent.Err()
	}
	return
}

func measurementHTTPClient() *http.Client {
	client := transferHTTPClient()
	// Redirects would measure an unselected endpoint and create a second
	// connection. Return the response so its status becomes an explicit failure.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

func downloadWorker(ctx context.Context, spec RequestSpec, key string, counter *atomicCounter, limiter *byteRateLimiter) error {
	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(spec.Method),
		replaceTemplates(spec.URL, key, fmt.Sprintf("%d", time.Now().Unix())), nil)
	if err != nil {
		return err
	}
	applyHeaders(request, spec.Headers, key)
	response, err := measurementHTTPClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	_, err = io.CopyBuffer(countingWriter{ctx: ctx, counter: counter, limiter: limiter}, response.Body, make([]byte, limiterBurst))
	if err != nil {
		return err
	}
	// Even an HTTP 200 containing a tiny error page is not a full measurement.
	return errTransferEnded
}

func uploadWorker(ctx context.Context, spec RequestSpec, key string, counter *atomicCounter, limiter *byteRateLimiter) error {
	prefix, err := base64.StdEncoding.DecodeString(spec.BodyPrefixBase64)
	if err != nil {
		return err
	}
	body := &uploadReader{prefix: prefix, counter: counter, ctx: ctx, limiter: limiter, seed: 1}
	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(spec.Method),
		replaceTemplates(spec.URL, key, fmt.Sprintf("%d-0", time.Now().UnixNano())), io.LimitReader(body, spec.ContentLength))
	if err != nil {
		return err
	}
	request.ContentLength = spec.ContentLength
	applyHeaders(request, spec.Headers, key)
	response, err := measurementHTTPClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	// A final response before our sampling timer means the server ended the
	// upload, including a successful response to an undersized request body.
	return errTransferEnded
}

func publicTransferError(err error) string {
	switch {
	case errors.Is(err, errTransferStartTimeout):
		return "等待测速数据超时"
	case errors.Is(err, errTransferEnded):
		return "节点提前结束传输"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "传输提前中断"
	case errors.Is(err, errTransferNoData):
		return "正式测速期间没有有效数据"
	default:
		return publicNetworkError(err)
	}
}

func logTransferResult(ctx context.Context, leaseID, nodeID, direction string, result transferMeasurement) {
	level, status := slog.LevelInfo, "ok"
	if result.Err != nil {
		level, status = slog.LevelWarn, "failed"
	}
	diagnostic(ctx, level, "transfer.completed",
		"lease_id", leaseID, "node_id", nodeID, "direction", direction, "status", status,
		"duration_ms", result.TotalDuration.Milliseconds(),
		"setup_ms", result.SetupDuration.Milliseconds(),
		"measurement_ms", result.SampleDuration.Milliseconds(),
		"bytes", result.Bytes, "mbps", round(result.Mbps, 2), "error", diagnosticError(result.Err),
	)
}
