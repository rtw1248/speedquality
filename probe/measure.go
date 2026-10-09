package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	preheatDuration = 2 * time.Second
	limiterBurst    = 64 * 1024
)

type Report struct {
	Version         int                 `json:"version"`
	LeaseID         string              `json:"lease_id"`
	StartedAt       int64               `json:"started_at"`
	CompletedAt     int64               `json:"completed_at"`
	Region          Region              `json:"region"`
	Family          string              `json:"family"`
	DurationSeconds int                 `json:"duration_seconds"`
	TargetMbps      int                 `json:"target_mbps"`
	Modes           []string            `json:"modes"`
	Results         []MeasurementResult `json:"results"`
}

type MeasurementResult struct {
	Carrier string      `json:"carrier"`
	Label   string      `json:"label"`
	NodeID  string      `json:"node_id,omitempty"`
	Address string      `json:"address,omitempty"`
	Port    int         `json:"port,omitempty"`
	Latency float64     `json:"latency_ms,omitempty"`
	Status  string      `json:"status"`
	Error   string      `json:"error,omitempty"`
	Single  *ModeResult `json:"single,omitempty"`
	Multi   *ModeResult `json:"multi,omitempty"`
}

type ModeResult struct {
	DownloadMbps  float64 `json:"download_mbps"`
	UploadMbps    float64 `json:"upload_mbps"`
	DownloadBytes int64   `json:"download_bytes"`
	UploadBytes   int64   `json:"upload_bytes"`
}

type atomicCounter struct{ value atomic.Int64 }

func (counter *atomicCounter) Add(value int) { counter.value.Add(int64(value)) }
func (counter *atomicCounter) Load() int64   { return counter.value.Load() }

type byteRateLimiter struct {
	mu             sync.Mutex
	bytesPerSecond float64
	burst          float64
	tokens         float64
	last           time.Time
}

func newByteRateLimiter(targetMbps int) *byteRateLimiter {
	rate := float64(targetMbps) * 1_000_000 / 8
	return &byteRateLimiter{
		bytesPerSecond: rate,
		burst:          limiterBurst,
		tokens:         limiterBurst,
		last:           time.Now(),
	}
}

func (limiter *byteRateLimiter) wait(ctx context.Context, bytes int) error {
	if bytes <= 0 {
		return nil
	}
	limiter.mu.Lock()
	now := time.Now()
	limiter.tokens += now.Sub(limiter.last).Seconds() * limiter.bytesPerSecond
	if limiter.tokens > limiter.burst {
		limiter.tokens = limiter.burst
	}
	limiter.tokens -= float64(bytes)
	limiter.last = now
	waitSeconds := 0.0
	if limiter.tokens < 0 {
		waitSeconds = -limiter.tokens / limiter.bytesPerSecond
	}
	limiter.mu.Unlock()
	if waitSeconds <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(waitSeconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type countingWriter struct {
	ctx     context.Context
	counter *atomicCounter
	limiter *byteRateLimiter
}

func (writer countingWriter) Write(data []byte) (int, error) {
	if err := writer.limiter.wait(writer.ctx, len(data)); err != nil {
		return 0, err
	}
	writer.counter.Add(len(data))
	return len(data), nil
}

type uploadReader struct {
	prefix  []byte
	offset  int
	counter *atomicCounter
	ctx     context.Context
	limiter *byteRateLimiter
	seed    byte
}

func (reader *uploadReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if err := reader.limiter.wait(reader.ctx, len(buffer)); err != nil {
		return 0, err
	}
	written := 0
	if reader.offset < len(reader.prefix) {
		written = copy(buffer, reader.prefix[reader.offset:])
		reader.offset += written
	}
	for index := written; index < len(buffer); index++ {
		reader.seed = reader.seed*33 + 17
		buffer[index] = reader.seed
	}
	reader.counter.Add(len(buffer))
	return len(buffer), nil
}

type rankedCandidate struct {
	candidate Candidate
	latency   float64
	index     int
}

type preparedTarget struct {
	result      MeasurementResult
	candidate   Candidate
	key         string
	activatedAt time.Time
}

type transferProgress func(completed int, stage string)

func runLease(ctx context.Context, lease Lease, progress *progressTracker, now func() time.Time) Report {
	started := time.Now()
	diagnostic(ctx, slog.LevelInfo, "lease.started",
		"lease_id", lease.LeaseID,
		"region", lease.Region.Code,
		"family", lease.Family,
		"target_mbps", lease.TargetMbps,
		"target_groups", len(lease.Targets),
	)
	report := Report{
		Version:         1,
		LeaseID:         lease.LeaseID,
		StartedAt:       now().Unix(),
		Region:          lease.Region,
		Family:          lease.Family,
		DurationSeconds: lease.DurationSeconds,
		TargetMbps:      lease.TargetMbps,
		Modes:           append([]string(nil), lease.Modes...),
	}
	for index, target := range lease.Targets {
		baseProgress := index * 3
		progress.Update(baseProgress, target.Label+" / 连接节点")
		prepared := prepareTarget(ctx, lease.Family, target)
		updateProgress := func(completed int, stage string) {
			progress.Update(baseProgress+completed, target.Label+" / "+stage)
		}
		report.Results = append(
			report.Results,
			runPreparedTarget(ctx, lease, prepared, updateProgress),
		)
		if prepared.key != "" {
			if err := releaseCandidate(prepared.candidate, prepared.key); err != nil {
				diagnostic(ctx, slog.LevelWarn, "candidate.release_failed",
					"lease_id", lease.LeaseID,
					"node_id", prepared.candidate.ID,
					"error", diagnosticError(err),
				)
			} else {
				diagnostic(ctx, slog.LevelInfo, "candidate.released",
					"lease_id", lease.LeaseID,
					"node_id", prepared.candidate.ID,
				)
			}
		}
	}
	report.CompletedAt = now().Unix()
	diagnostic(ctx, slog.LevelInfo, "lease.completed",
		"lease_id", lease.LeaseID,
		"duration_ms", time.Since(started).Milliseconds(),
		"results", len(report.Results),
	)
	return report
}

func prepareTarget(ctx context.Context, family string, target TargetGroup) preparedTarget {
	started := time.Now()
	result := MeasurementResult{Carrier: target.Carrier, Label: target.Label, Status: "failed"}
	ranked := rankCandidates(ctx, family, target.Candidates)
	diagnostic(ctx, slog.LevelInfo, "candidate.ranked",
		"carrier", target.Carrier,
		"candidate_count", len(target.Candidates),
		"reachable_count", len(ranked),
		"duration_ms", time.Since(started).Milliseconds(),
	)
	if len(ranked) == 0 {
		// The lease is already ranked by Core. Reporting its first candidate lets
		// Core avoid serving the same unreachable endpoint on the next run.
		if len(target.Candidates) > 0 {
			result.NodeID = target.Candidates[0].ID
		}
		result.Error = "没有可连接的候选节点"
		return preparedTarget{result: result}
	}
	var activationErrors []string
	for _, item := range ranked {
		if result.NodeID == "" {
			result.NodeID = item.candidate.ID
		}
		activationStarted := time.Now()
		activatedKey, err := activateCandidate(ctx, item.candidate)
		if err != nil {
			diagnostic(ctx, slog.LevelWarn, "candidate.activation_failed",
				"carrier", target.Carrier,
				"node_id", item.candidate.ID,
				"duration_ms", time.Since(activationStarted).Milliseconds(),
				"error", diagnosticError(err),
			)
			activationErrors = append(activationErrors, publicNetworkError(err))
			continue
		}
		diagnostic(ctx, slog.LevelInfo, "candidate.activated",
			"carrier", target.Carrier,
			"node_id", item.candidate.ID,
			"latency_ms", item.latency,
			"duration_ms", time.Since(activationStarted).Milliseconds(),
		)
		result.Latency = item.latency
		result.NodeID = item.candidate.ID
		result.Address = item.candidate.Address
		result.Port = item.candidate.Port
		return preparedTarget{
			result:      result,
			candidate:   item.candidate,
			key:         activatedKey,
			activatedAt: time.Now(),
		}
	}
	result.Error = "节点激活失败"
	if len(activationErrors) > 0 {
		result.Error += ": " + strings.Join(activationErrors, "; ")
	}
	return preparedTarget{result: result}
}

func runPreparedTarget(
	ctx context.Context,
	lease Lease,
	prepared preparedTarget,
	progress transferProgress,
) MeasurementResult {
	result := prepared.result
	if prepared.key == "" {
		progress(3, "节点不可用")
		return result
	}
	progress(1, "等待节点")
	readyAt := prepared.activatedAt.Add(
		time.Duration(prepared.candidate.Activate.ReadyDelayMillis) * time.Millisecond,
	)
	if delay := time.Until(readyAt); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			result.Error = "测速在节点准备完成前被取消"
			return result
		case <-timer.C:
		}
	}
	progress(1, "下载测速")
	diagnostic(ctx, slog.LevelInfo, "transfer.started",
		"lease_id", lease.LeaseID,
		"node_id", prepared.candidate.ID,
		"direction", "download",
		"target_mbps", lease.TargetMbps,
	)
	downStarted := time.Now()
	downMbps, downBytes := runTransferPhase(
		ctx, prepared.candidate.Download, prepared.key, 1, true,
		preheatDuration, time.Duration(lease.DurationSeconds)*time.Second, lease.TargetMbps,
	)
	diagnostic(ctx, slog.LevelInfo, "transfer.completed",
		"lease_id", lease.LeaseID,
		"node_id", prepared.candidate.ID,
		"direction", "download",
		"duration_ms", time.Since(downStarted).Milliseconds(),
		"bytes", downBytes,
		"mbps", round(downMbps, 2),
	)
	progress(2, "上传测速")
	diagnostic(ctx, slog.LevelInfo, "transfer.started",
		"lease_id", lease.LeaseID,
		"node_id", prepared.candidate.ID,
		"direction", "upload",
		"target_mbps", lease.TargetMbps,
	)
	upStarted := time.Now()
	upMbps, upBytes := runTransferPhase(
		ctx, prepared.candidate.Upload, prepared.key, 1, false,
		preheatDuration, time.Duration(lease.DurationSeconds)*time.Second, lease.TargetMbps,
	)
	diagnostic(ctx, slog.LevelInfo, "transfer.completed",
		"lease_id", lease.LeaseID,
		"node_id", prepared.candidate.ID,
		"direction", "upload",
		"duration_ms", time.Since(upStarted).Milliseconds(),
		"bytes", upBytes,
		"mbps", round(upMbps, 2),
	)
	progress(3, "完成")
	result.Single = &ModeResult{
		DownloadMbps:  round(downMbps, 2),
		UploadMbps:    round(upMbps, 2),
		DownloadBytes: downBytes,
		UploadBytes:   upBytes,
	}
	if result.Single.DownloadMbps > 0 && result.Single.UploadMbps > 0 {
		result.Status = "ok"
	} else {
		failedDirections := make([]string, 0, 2)
		if result.Single.DownloadMbps <= 0 {
			failedDirections = append(failedDirections, "下载")
		}
		if result.Single.UploadMbps <= 0 {
			failedDirections = append(failedDirections, "上传")
		}
		result.Error = strings.Join(failedDirections, "、") + "未产生有效数据"
	}
	return result
}

func rankCandidates(ctx context.Context, family string, candidates []Candidate) []rankedCandidate {
	results := make(chan rankedCandidate, len(candidates))
	var probes sync.WaitGroup
	for index, candidate := range candidates {
		probes.Add(1)
		go func(index int, candidate Candidate) {
			defer probes.Done()
			latency := tcpLatency(ctx, family, candidate.Address, candidate.Port, 3)
			if latency >= 0 {
				results <- rankedCandidate{candidate: candidate, latency: latency, index: index}
			}
		}(index, candidate)
	}
	probes.Wait()
	close(results)
	ranked := make([]rankedCandidate, 0, len(candidates))
	for result := range results {
		ranked = append(ranked, result)
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].latency == ranked[right].latency {
			return ranked[left].index < ranked[right].index
		}
		return ranked[left].latency < ranked[right].latency
	})
	return ranked
}

func tcpLatency(ctx context.Context, family, address string, port, attempts int) float64 {
	network := "tcp4"
	if family == "v6" {
		network = "tcp6"
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	endpoint := net.JoinHostPort(address, fmt.Sprintf("%d", port))
	var total float64
	var successes int
	for attempt := 0; attempt < attempts; attempt++ {
		started := time.Now()
		connection, err := dialer.DialContext(ctx, network, endpoint)
		if err == nil {
			total += float64(time.Since(started).Microseconds()) / 1000
			successes++
			_ = connection.Close()
		}
	}
	if successes == 0 {
		return -1
	}
	return round(total/float64(successes), 2)
}

func activateCandidate(ctx context.Context, candidate Candidate) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		body, err := performSmallRequest(ctx, candidate.Activate.RequestSpec, "", 6*time.Second)
		if err != nil {
			if attempt == 2 {
				return "", err
			}
			continue
		}
		body = strings.TrimSpace(body)
		if hasAnyPrefix(body, candidate.Activate.RejectPrefixes) {
			return "", errors.New("节点拒绝本次测速")
		}
		if hasAnyPrefix(body, candidate.Activate.RetryPrefixes) {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if len(body) <= candidate.Activate.KeyPrefixBytes {
			continue
		}
		key := strings.TrimSpace(body[candidate.Activate.KeyPrefixBytes:])
		if key != "" && len(key) <= 256 && !strings.ContainsAny(key, "\r\n") {
			return key, nil
		}
	}
	return "", errors.New("节点没有返回有效激活凭据")
}

func releaseCandidate(candidate Candidate, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := performSmallRequest(ctx, candidate.Release, key, 5*time.Second)
	return err
}

func performSmallRequest(ctx context.Context, spec RequestSpec, key string, timeout time.Duration) (string, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := strings.ToUpper(spec.Method)
	request, err := http.NewRequestWithContext(
		requestContext,
		method,
		replaceTemplates(spec.URL, key, fmt.Sprintf("%d", time.Now().UnixNano())),
		nil,
	)
	if err != nil {
		return "", err
	}
	applyHeaders(request, spec.Headers, key)
	response, err := transferHTTPClient().Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", errors.New("response is too large")
	}
	return string(data), nil
}

func runTransferPhase(
	parent context.Context,
	spec RequestSpec,
	key string,
	threads int,
	download bool,
	preheat time.Duration,
	duration time.Duration,
	targetMbps int,
) (float64, int64) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	counter := &atomicCounter{}
	limiter := newByteRateLimiter(targetMbps)
	var workers sync.WaitGroup
	for index := 0; index < threads; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			if download {
				downloadWorker(ctx, spec, key, worker, counter, limiter)
			} else {
				uploadWorker(ctx, spec, key, worker, counter, limiter)
			}
		}(index)
		time.Sleep(40 * time.Millisecond)
	}
	preheatTimer := time.NewTimer(preheat)
	select {
	case <-parent.Done():
		preheatTimer.Stop()
		cancel()
		waitWorkers(&workers)
		return 0, 0
	case <-preheatTimer.C:
	}
	baseline := counter.Load()
	measurementStart := time.Now()
	measurementTimer := time.NewTimer(duration)
	select {
	case <-parent.Done():
		measurementTimer.Stop()
		cancel()
		waitWorkers(&workers)
		return 0, counter.Load() - baseline
	case <-measurementTimer.C:
	}
	measurementEnd := time.Now()
	measuredBytes := counter.Load() - baseline
	cancel()
	waitWorkers(&workers)
	elapsed := measurementEnd.Sub(measurementStart).Seconds()
	if measuredBytes <= 0 || elapsed <= 0 {
		return 0, measuredBytes
	}
	return float64(measuredBytes) * 8 / 1_000_000 / elapsed, measuredBytes
}

func downloadWorker(
	ctx context.Context,
	spec RequestSpec,
	key string,
	_ int,
	counter *atomicCounter,
	limiter *byteRateLimiter,
) {
	request, err := http.NewRequestWithContext(
		ctx,
		strings.ToUpper(spec.Method),
		replaceTemplates(spec.URL, key, fmt.Sprintf("%d", time.Now().Unix())),
		nil,
	)
	if err != nil {
		return
	}
	applyHeaders(request, spec.Headers, key)
	response, err := transferHTTPClient().Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return
	}
	_, _ = io.CopyBuffer(
		countingWriter{ctx: ctx, counter: counter, limiter: limiter},
		response.Body,
		make([]byte, limiterBurst),
	)
}

func uploadWorker(
	ctx context.Context,
	spec RequestSpec,
	key string,
	worker int,
	counter *atomicCounter,
	limiter *byteRateLimiter,
) {
	prefix, err := base64.StdEncoding.DecodeString(spec.BodyPrefixBase64)
	if err != nil {
		return
	}
	body := &uploadReader{
		prefix: prefix, counter: counter, ctx: ctx, limiter: limiter, seed: byte(worker + 1),
	}
	request, err := http.NewRequestWithContext(
		ctx,
		strings.ToUpper(spec.Method),
		replaceTemplates(spec.URL, key, fmt.Sprintf("%d-%d", time.Now().UnixNano(), worker)),
		io.LimitReader(body, spec.ContentLength),
	)
	if err != nil {
		return
	}
	request.ContentLength = spec.ContentLength
	applyHeaders(request, spec.Headers, key)
	response, err := transferHTTPClient().Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
}

func transferHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}).DialContext,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		MaxIdleConns:          1,
		ResponseHeaderTimeout: 8 * time.Second,
		ExpectContinueTimeout: 0,
		ForceAttemptHTTP2:     false,
	}
	return &http.Client{Transport: transport}
}

func applyHeaders(request *http.Request, headers map[string]string, key string) {
	for name, value := range headers {
		value = strings.ReplaceAll(value, "{key}", key)
		request.Header.Set(name, strings.ReplaceAll(value, "{nonce}", ""))
	}
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func waitWorkers(workers *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

func round(value float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}
