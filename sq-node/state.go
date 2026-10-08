package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type persistentState struct {
	Day                 string           `json:"day"`
	PublicBytes         int64            `json:"public_bytes"`
	PublicReservedBytes int64            `json:"public_reserved_bytes"`
	TotalBytes          int64            `json:"total_bytes"`
	TotalReservedBytes  int64            `json:"total_reserved_bytes"`
	ActiveReservations  int              `json:"active_reservations"`
	PublicReservations  int              `json:"public_active_reservations"`
	EmergencyStopped    bool             `json:"emergency_stopped"`
	UsedJWTIDs          map[string]int64 `json:"used_jti"`
}

type activation struct {
	Key                  string
	SourceIP             string
	Scope                string
	JWTID                string
	TargetMbps           int
	MaxBytesPerDirection int64
	DownloadBytes        int64
	UploadBytes          int64
	ExpiresAt            int64
	ReservedBytes        int64
	Released             bool
	DownloadActive       bool
	UploadActive         bool
}

type runtimeState struct {
	mu          monitoredMutex
	path        string
	persistent  persistentState
	activations map[string]*activation
	lastSaveAt  atomic.Int64
	lastSaveMS  atomic.Int64
	lastSaveOK  atomic.Bool
}

func readRuntimeState(path string, now time.Time) (*runtimeState, error) {
	state := &runtimeState{
		path: path,
		persistent: persistentState{
			Day:        now.UTC().Format("2006-01-02"),
			UsedJWTIDs: map[string]int64{},
		},
		activations: map[string]*activation{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return nil, err
	}
	if len(data) > 2*1024*1024 {
		return nil, errors.New("节点状态文件过大")
	}
	if err := json.Unmarshal(data, &state.persistent); err != nil {
		return nil, err
	}
	if state.persistent.UsedJWTIDs == nil {
		state.persistent.UsedJWTIDs = map[string]int64{}
	}
	state.rotateDayLocked(now)
	state.pruneJWTIDsLocked(now.Unix())
	return state, nil
}

func loadRuntimeState(path string, now time.Time) (*runtimeState, error) {
	state, err := readRuntimeState(path, now)
	if err != nil {
		return nil, err
	}
	// A restart loses active request accounting. Charge outstanding reservations
	// conservatively so a restart cannot reset the public daily limit.
	state.persistent.PublicBytes += state.persistent.PublicReservedBytes
	state.persistent.PublicReservedBytes = 0
	state.persistent.TotalBytes += state.persistent.TotalReservedBytes
	state.persistent.TotalReservedBytes = 0
	if state.persistent.TotalBytes < state.persistent.PublicBytes {
		state.persistent.TotalBytes = state.persistent.PublicBytes
	}
	state.persistent.ActiveReservations = 0
	state.persistent.PublicReservations = 0
	if err := state.saveLocked(); err != nil {
		return nil, err
	}
	return state, nil
}

func (state *runtimeState) saveLocked() (saveErr error) {
	started := time.Now()
	ok := false
	defer func() {
		duration := time.Since(started)
		state.lastSaveAt.Store(time.Now().Unix())
		state.lastSaveMS.Store(duration.Milliseconds())
		state.lastSaveOK.Store(ok)
		if saveErr != nil {
			nodeLog(context.Background(), slog.LevelError, "state.persist_failed",
				"error", safeLogError(saveErr),
			)
		}
		if duration >= 100*time.Millisecond {
			nodeLog(context.Background(), slog.LevelWarn, "state.persist_slow",
				"duration_ms", duration.Milliseconds(),
			)
		}
	}()
	data, err := json.MarshalIndent(state.persistent, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(state.path), 0o700); err != nil {
		return err
	}
	temporary := state.path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, state.path); err != nil {
		return err
	}
	ok = true
	return nil
}

func (state *runtimeState) rotateDayLocked(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if state.persistent.Day == day {
		return
	}
	state.persistent.Day = day
	state.persistent.PublicBytes = 0
	state.persistent.PublicReservedBytes = 0
	state.persistent.TotalBytes = 0
	state.persistent.TotalReservedBytes = 0
	state.persistent.ActiveReservations = 0
	state.persistent.PublicReservations = 0
}

func (state *runtimeState) pruneJWTIDsLocked(now int64) {
	for id, expiresAt := range state.persistent.UsedJWTIDs {
		if expiresAt <= now {
			delete(state.persistent.UsedJWTIDs, id)
		}
	}
}

func (state *runtimeState) cleanupExpired(config Config, now time.Time) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.rotateDayLocked(now)
	state.pruneJWTIDsLocked(now.Unix())
	expired := 0
	for key, active := range state.activations {
		if active.ExpiresAt > now.Unix() {
			continue
		}
		state.finishActivationLocked(active)
		delete(state.activations, key)
		expired++
	}
	if expired > 0 {
		if err := state.saveLocked(); err != nil {
			nodeLog(context.Background(), slog.LevelError, "activation.cleanup_persist_failed",
				"expired", expired,
				"error", safeLogError(err),
			)
		} else {
			nodeLog(context.Background(), slog.LevelInfo, "activation.expired_cleanup",
				"expired", expired,
			)
		}
	}
}

func (state *runtimeState) lockWatchdog(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	dumped := false
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			held, report := state.mu.stalled(now)
			if held == 0 {
				dumped = false
			}
			if report {
				nodeLog(ctx, slog.LevelError, "state.lock_stalled", "held_ms", held.Milliseconds())
				if !dumped {
					logGoroutineDump(ctx, "state_lock_stalled")
					dumped = true
				}
			}
		}
	}
}

func (state *runtimeState) finishActivationLocked(active *activation) {
	if active.Released {
		return
	}
	active.Released = true
	state.persistent.TotalReservedBytes -= active.ReservedBytes
	if state.persistent.TotalReservedBytes < 0 {
		state.persistent.TotalReservedBytes = 0
	}
	state.persistent.TotalBytes += active.DownloadBytes + active.UploadBytes
	if state.persistent.ActiveReservations > 0 {
		state.persistent.ActiveReservations--
	}
	if active.Scope == "public" {
		state.persistent.PublicReservedBytes -= active.ReservedBytes
		if state.persistent.PublicReservedBytes < 0 {
			state.persistent.PublicReservedBytes = 0
		}
		state.persistent.PublicBytes += active.DownloadBytes + active.UploadBytes
		if state.persistent.PublicReservations > 0 {
			state.persistent.PublicReservations--
		}
	}
}

func (state *runtimeState) snapshot(now time.Time) (
	active int,
	publicActive int,
	publicBytes int64,
	publicReservedBytes int64,
	totalBytes int64,
	totalReservedBytes int64,
	emergencyStopped bool,
) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.rotateDayLocked(now)
	active = state.persistent.ActiveReservations
	publicActive = state.persistent.PublicReservations
	return active, publicActive, state.persistent.PublicBytes, state.persistent.PublicReservedBytes,
		state.persistent.TotalBytes, state.persistent.TotalReservedBytes, state.persistent.EmergencyStopped
}

func (state *runtimeState) setEmergencyStopped(stopped bool) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.persistent.EmergencyStopped = stopped
	if stopped {
		for key, active := range state.activations {
			state.finishActivationLocked(active)
			delete(state.activations, key)
		}
	}
	return state.saveLocked()
}

func (state *runtimeState) persistenceSnapshot() (savedAt int64, durationMS int64, ok bool) {
	return state.lastSaveAt.Load(), state.lastSaveMS.Load(), state.lastSaveOK.Load()
}
