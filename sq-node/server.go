package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const ownershipPath = "/.well-known/speedquality/ownership"

type nodeServer struct {
	configPath string
	state      *runtimeState
	server     *http.Server
	protector  *requestProtector
}

func newNodeServer(path string) (*nodeServer, error) {
	config, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	state, err := loadRuntimeState(statePath(path), time.Now())
	if err != nil {
		return nil, err
	}
	node := &nodeServer{configPath: path, state: state, protector: newRequestProtector(config)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", node.health)
	mux.HandleFunc("GET "+ownershipPath, node.ownership)
	mux.HandleFunc("POST /activate", node.activate)
	mux.HandleFunc("GET /download", node.download)
	mux.HandleFunc("POST /upload", node.upload)
	mux.HandleFunc("POST /release", node.release)
	node.server = &http.Server{
		Addr:              net.JoinHostPort(listenHost(config.ListenAddress), strconv.Itoa(config.Port)),
		Handler:           observeRequests(securityHeaders(node.protector.wrap(mux))),
		ReadHeaderTimeout: 8 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	return node, nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(response, request)
	})
}

func (node *nodeServer) serve(listener net.Listener) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.cleanupLoop(ctx)
	go node.heartbeatLoop(ctx)
	go node.state.lockWatchdog(ctx)
	go diagnosticSignalLoop(ctx)
	config, _ := loadConfig(node.configPath)
	nodeLog(ctx, slog.LevelInfo, "service.started",
		"node_id", shortNodeID(config.NodeID),
		"listen_port", config.Port,
		"version", version,
	)
	var err error
	if listener != nil {
		err = node.server.Serve(listener)
	} else {
		err = node.server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		nodeLog(ctx, slog.LevelInfo, "service.stopped")
		return err
	}
	nodeLog(ctx, slog.LevelError, "service.failed", "error", safeLogError(err))
	return err
}

func (node *nodeServer) shutdown(ctx context.Context) error {
	return node.server.Shutdown(ctx)
}

func (node *nodeServer) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			config, err := loadConfig(node.configPath)
			if err == nil {
				node.state.cleanupExpired(config, now)
			}
		}
	}
}

func (node *nodeServer) heartbeatLoop(ctx context.Context) {
	consecutiveFailures := 0
	for {
		config, err := loadConfig(node.configPath)
		if err == nil && config.NodeID != "" && config.APIToken != "" {
			err = sendHeartbeat(ctx, node.configPath, node.state)
			if err != nil {
				consecutiveFailures++
				if consecutiveFailures == 1 || consecutiveFailures%5 == 0 {
					nodeLog(ctx, slog.LevelWarn, "heartbeat.failed",
						"node_id", shortNodeID(config.NodeID),
						"consecutive_failures", consecutiveFailures,
						"error", safeLogError(err),
					)
				}
			} else if consecutiveFailures > 0 {
				nodeLog(ctx, slog.LevelInfo, "heartbeat.recovered",
					"node_id", shortNodeID(config.NodeID),
					"previous_failures", consecutiveFailures,
				)
				consecutiveFailures = 0
			}
		} else if err != nil {
			consecutiveFailures++
			if consecutiveFailures == 1 || consecutiveFailures%5 == 0 {
				nodeLog(ctx, slog.LevelError, "heartbeat.config_failed",
					"consecutive_failures", consecutiveFailures,
					"error", safeLogError(err),
				)
			}
		}
		seconds := 60
		if err == nil {
			seconds = config.HeartbeatSeconds
			if !currentGateStatus(node.configPath, time.Now()).Ready && seconds > 5 {
				seconds = 5
			}
		}
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (node *nodeServer) health(response http.ResponseWriter, _ *http.Request) {
	_, err := loadConfig(node.configPath)
	if err != nil {
		http.Error(response, "configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status":           "ok",
		"protocol_version": nodeProtocolVersion,
	})
}

func (node *nodeServer) ownership(response http.ResponseWriter, _ *http.Request) {
	config, err := loadConfig(node.configPath)
	if err != nil || config.OwnershipChallenge == "" {
		http.NotFound(response, nil)
		return
	}
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(response, config.OwnershipChallenge+"\n")
}

func (node *nodeServer) activate(response http.ResponseWriter, request *http.Request) {
	reject := func(status int, reason, message string, attributes ...any) {
		nodeLog(request.Context(), slog.LevelWarn, "activation.rejected",
			append([]any{"reason", reason, "status", status}, attributes...)...,
		)
		http.Error(response, message, status)
	}
	config, err := loadConfig(node.configPath)
	if err != nil || config.validate(true) != nil {
		reject(http.StatusServiceUnavailable, "configuration_unavailable", "node is not registered")
		return
	}
	if config.AccessMode == "paused" {
		reject(http.StatusServiceUnavailable, "node_paused", "node is paused", "node_id", shortNodeID(config.NodeID))
		return
	}
	_, _, _, _, _, _, emergencyStopped := node.state.snapshot(time.Now())
	if emergencyStopped {
		reject(http.StatusServiceUnavailable, "emergency_stopped", "node is emergency stopped", "node_id", shortNodeID(config.NodeID))
		return
	}
	if !availableAt(config, time.Now()) {
		response.Header().Set("Retry-After", "300")
		reject(http.StatusServiceUnavailable, "outside_schedule", "node is outside its availability schedule", "node_id", shortNodeID(config.NodeID))
		return
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		reject(http.StatusUnauthorized, "missing_token", "missing activation token")
		return
	}
	claims, err := verifyActivationJWT(strings.TrimPrefix(authorization, "Bearer "), config, time.Now())
	if err != nil {
		reject(http.StatusUnauthorized, "invalid_token", "invalid activation token")
		return
	}
	taskID := shortDiagnosticID(claims.JWTID)
	if claims.Scope == "public" && config.AccessMode != "public" {
		reject(http.StatusForbidden, "public_access_disabled", "public access is disabled", "task_id", taskID)
		return
	}
	sourceIP := requestIP(request)
	if !sameIP(sourceIP, claims.ClientIP) {
		reject(http.StatusForbidden, "source_mismatch", "source address mismatch", "task_id", taskID)
		return
	}
	key, err := randomURLToken(24)
	if err != nil {
		reject(http.StatusInternalServerError, "key_generation_failed", "unable to allocate activation", "task_id", taskID)
		return
	}
	reservedBytes := claims.MaxBytesPerDirection * 2
	now := time.Now()
	node.state.mu.Lock()
	node.state.rotateDayLocked(now)
	node.state.pruneJWTIDsLocked(now.Unix())
	if _, used := node.state.persistent.UsedJWTIDs[claims.JWTID]; used {
		node.state.mu.Unlock()
		reject(http.StatusConflict, "token_replayed", "activation token was already used", "task_id", taskID)
		return
	}
	active, publicActive := 0, 0
	for _, item := range node.state.activations {
		if item.Released || item.ExpiresAt <= now.Unix() {
			continue
		}
		active++
		if item.Scope == "public" {
			publicActive++
		}
	}
	if active >= config.MaxConcurrency || (claims.Scope == "public" &&
		publicActive >= config.MaxConcurrency-config.ReserveConcurrency) {
		node.state.mu.Unlock()
		response.Header().Set("Retry-After", "30")
		reject(http.StatusTooManyRequests, "capacity_full", "node capacity is full",
			"task_id", taskID,
			"active", active,
			"public_active", publicActive,
			"max_concurrency", config.MaxConcurrency,
		)
		return
	}
	if claims.Scope == "public" && (config.DailyPublicBytes == 0 ||
		node.state.persistent.PublicBytes+node.state.persistent.PublicReservedBytes+reservedBytes > config.DailyPublicBytes) {
		publicBytes := node.state.persistent.PublicBytes
		publicReservedBytes := node.state.persistent.PublicReservedBytes
		node.state.mu.Unlock()
		response.Header().Set("Retry-After", "3600")
		reject(http.StatusTooManyRequests, "public_quota_exhausted", "daily public quota is exhausted",
			"task_id", taskID,
			"public_bytes", publicBytes,
			"public_reserved_bytes", publicReservedBytes,
		)
		return
	}
	if node.state.persistent.TotalBytes+node.state.persistent.TotalReservedBytes+reservedBytes > config.DailyTotalBytes {
		totalBytes := node.state.persistent.TotalBytes
		totalReservedBytes := node.state.persistent.TotalReservedBytes
		node.state.mu.Unlock()
		response.Header().Set("Retry-After", "3600")
		reject(http.StatusTooManyRequests, "total_quota_exhausted", "daily total quota is exhausted",
			"task_id", taskID,
			"total_bytes", totalBytes,
			"total_reserved_bytes", totalReservedBytes,
		)
		return
	}
	item := &activation{
		Key: key, SourceIP: sourceIP, Scope: claims.Scope, JWTID: claims.JWTID,
		TargetMbps: claims.TargetMbps, MaxBytesPerDirection: claims.MaxBytesPerDirection,
		ExpiresAt: claims.ExpiresAt, ReservedBytes: reservedBytes,
	}
	node.state.activations[key] = item
	node.state.persistent.UsedJWTIDs[claims.JWTID] = claims.ExpiresAt
	node.state.persistent.TotalReservedBytes += reservedBytes
	node.state.persistent.ActiveReservations++
	if claims.Scope == "public" {
		node.state.persistent.PublicReservedBytes += reservedBytes
		node.state.persistent.PublicReservations++
	}
	if err := node.state.saveLocked(); err != nil {
		delete(node.state.activations, key)
		delete(node.state.persistent.UsedJWTIDs, claims.JWTID)
		node.state.persistent.TotalReservedBytes -= reservedBytes
		node.state.persistent.ActiveReservations--
		if claims.Scope == "public" {
			node.state.persistent.PublicReservedBytes -= reservedBytes
			node.state.persistent.PublicReservations--
		}
		node.state.mu.Unlock()
		reject(http.StatusInternalServerError, "state_persist_failed", "unable to persist activation",
			"task_id", taskID,
			"error", safeLogError(err),
		)
		return
	}
	node.state.mu.Unlock()
	nodeLog(request.Context(), slog.LevelInfo, "activation.accepted",
		"node_id", shortNodeID(config.NodeID),
		"task_id", taskID,
		"scope", claims.Scope,
		"target_mbps", claims.TargetMbps,
		"active", active+1,
		"public_active", publicActive+boolToInt(claims.Scope == "public"),
		"reserved_bytes", reservedBytes,
	)
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(response, key+"\n")
}

func (node *nodeServer) beginTransfer(request *http.Request, direction string) (*activation, int64, error) {
	key := request.URL.Query().Get("key")
	if key == "" || len(key) > 256 {
		return nil, 0, errors.New("missing key")
	}
	node.state.mu.Lock()
	defer node.state.mu.Unlock()
	item := node.state.activations[key]
	if item == nil || item.Released || item.ExpiresAt <= time.Now().Unix() ||
		!sameIP(item.SourceIP, requestIP(request)) {
		return nil, 0, errors.New("invalid key")
	}
	if direction == "download" {
		if item.DownloadActive {
			return nil, 0, errors.New("download already active")
		}
		item.DownloadActive = true
		return item, item.DownloadBytes, nil
	}
	if item.UploadActive {
		return nil, 0, errors.New("upload already active")
	}
	item.UploadActive = true
	return item, item.UploadBytes, nil
}

func (node *nodeServer) endTransfer(item *activation, direction string) int64 {
	node.state.mu.Lock()
	defer node.state.mu.Unlock()
	if direction == "download" {
		item.DownloadActive = false
		return item.DownloadBytes
	} else {
		item.UploadActive = false
		return item.UploadBytes
	}
}

func (node *nodeServer) download(response http.ResponseWriter, request *http.Request) {
	item, initialBytes, err := node.beginTransfer(request, "download")
	if err != nil {
		nodeLog(request.Context(), slog.LevelWarn, "transfer.rejected",
			"direction", "download", "reason", err.Error())
		http.Error(response, "invalid activation", http.StatusForbidden)
		return
	}
	started := time.Now()
	taskID := shortDiagnosticID(item.JWTID)
	nodeLog(request.Context(), slog.LevelInfo, "transfer.started",
		"task_id", taskID, "direction", "download", "target_mbps", item.TargetMbps)
	defer func() {
		finalBytes := node.endTransfer(item, "download")
		nodeLog(request.Context(), slog.LevelInfo, "transfer.completed",
			"task_id", taskID,
			"direction", "download",
			"bytes", finalBytes-initialBytes,
			"duration_ms", time.Since(started).Milliseconds(),
			"context_error", safeLogError(request.Context().Err()),
		)
	}()
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Encoding", "identity")
	response.Header().Set("Cache-Control", "no-store, no-transform")
	buffer := make([]byte, 64*1024)
	if _, err := rand.Read(buffer); err != nil {
		nodeLog(request.Context(), slog.LevelError, "transfer.random_failed",
			"task_id", taskID, "error", safeLogError(err))
		http.Error(response, "random source unavailable", http.StatusInternalServerError)
		return
	}
	for {
		node.state.mu.Lock()
		remaining := item.MaxBytesPerDirection - item.DownloadBytes
		if item.Released || remaining <= 0 || item.ExpiresAt <= time.Now().Unix() {
			node.state.mu.Unlock()
			return
		}
		writeSize := int64(len(buffer))
		if remaining < writeSize {
			writeSize = remaining
		}
		node.state.mu.Unlock()
		mutateBuffer(buffer)
		written, writeErr := response.Write(buffer[:writeSize])
		if written > 0 {
			node.state.mu.Lock()
			item.DownloadBytes += int64(written)
			total := item.DownloadBytes
			node.state.mu.Unlock()
			paceTransfer(request.Context(), started, total-initialBytes, item.TargetMbps)
		}
		if writeErr != nil {
			return
		}
		if flusher, ok := response.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (node *nodeServer) upload(response http.ResponseWriter, request *http.Request) {
	item, initialBytes, err := node.beginTransfer(request, "upload")
	if err != nil {
		nodeLog(request.Context(), slog.LevelWarn, "transfer.rejected",
			"direction", "upload", "reason", err.Error())
		http.Error(response, "invalid activation", http.StatusForbidden)
		return
	}
	started := time.Now()
	taskID := shortDiagnosticID(item.JWTID)
	nodeLog(request.Context(), slog.LevelInfo, "transfer.started",
		"task_id", taskID, "direction", "upload", "target_mbps", item.TargetMbps)
	defer func() {
		finalBytes := node.endTransfer(item, "upload")
		nodeLog(request.Context(), slog.LevelInfo, "transfer.completed",
			"task_id", taskID,
			"direction", "upload",
			"bytes", finalBytes-initialBytes,
			"duration_ms", time.Since(started).Milliseconds(),
			"context_error", safeLogError(request.Context().Err()),
		)
	}()
	buffer := make([]byte, 64*1024)
	for {
		node.state.mu.Lock()
		remaining := item.MaxBytesPerDirection - item.UploadBytes
		if item.Released || remaining <= 0 || item.ExpiresAt <= time.Now().Unix() {
			node.state.mu.Unlock()
			break
		}
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining
		}
		node.state.mu.Unlock()
		read, readErr := request.Body.Read(buffer[:readSize])
		if read > 0 {
			node.state.mu.Lock()
			item.UploadBytes += int64(read)
			total := item.UploadBytes
			node.state.mu.Unlock()
			paceTransfer(request.Context(), started, total-initialBytes, item.TargetMbps)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return
			}
			break
		}
	}
	response.WriteHeader(http.StatusNoContent)
}

func (node *nodeServer) release(response http.ResponseWriter, request *http.Request) {
	key := request.URL.Query().Get("key")
	released := false
	taskID := ""
	var persistErr error
	node.state.mu.Lock()
	if item := node.state.activations[key]; item != nil && sameIP(item.SourceIP, requestIP(request)) {
		taskID = shortDiagnosticID(item.JWTID)
		node.state.finishActivationLocked(item)
		delete(node.state.activations, key)
		persistErr = node.state.saveLocked()
		released = true
	}
	node.state.mu.Unlock()
	level := slog.LevelInfo
	if persistErr != nil {
		level = slog.LevelError
	}
	nodeLog(request.Context(), level, "activation.released",
		"task_id", taskID,
		"found", released,
		"persist_error", safeLogError(persistErr),
	)
	response.WriteHeader(http.StatusNoContent)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requestIP(request *http.Request) string {
	return directRequestIP(request)
}

func directRequestIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return strings.Trim(request.RemoteAddr, "[]")
	}
	return strings.Trim(host, "[]")
}

func sameIP(left, right string) bool {
	leftIP := net.ParseIP(strings.Trim(left, "[]"))
	rightIP := net.ParseIP(strings.Trim(right, "[]"))
	return leftIP != nil && rightIP != nil && leftIP.Equal(rightIP)
}

func mutateBuffer(buffer []byte) {
	if len(buffer) == 0 {
		return
	}
	seed := byte(time.Now().UnixNano())
	for index := 0; index < len(buffer); index += 4096 {
		buffer[index] ^= seed + byte(index/4096)
	}
}

func paceTransfer(ctx context.Context, started time.Time, bytes int64, mbps int) {
	if mbps <= 0 || bytes <= 0 {
		return
	}
	expected := time.Duration(float64(bytes*8) / float64(mbps*1_000_000) * float64(time.Second))
	wait := expected - time.Since(started)
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func listenForConfig(config Config) (net.Listener, error) {
	host := listenHost(config.ListenAddress)
	internalOnly := os.Getenv("SQ_NODE_INTERNAL_ONLY") == "1"
	if internalOnly {
		host = "127.0.0.1"
	}
	endpoint := net.JoinHostPort(host, strconv.Itoa(config.Port))
	listener, err := net.Listen("tcp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("无法监听 %s: %w", endpoint, err)
	}
	limited := limitConnections(listener, maximum(32, config.MaxConcurrency*12))
	if internalOnly {
		return &trustedProxyListener{Listener: limited, apiToken: config.APIToken}, nil
	}
	return limited, nil
}
