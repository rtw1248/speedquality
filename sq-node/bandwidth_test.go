package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBandwidthAdmissionSharedAcrossTiersFamiliesAndScopes(t *testing.T) {
	path, config, signingKey := testConfig(t)
	config.CapacityMbps, config.MaxMbps = 1000, 400
	// Old values must not cap or reserve slots after an explicit migration.
	config.MaxConcurrency, config.ReserveConcurrency = 2, 1
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	admit := func(id string, tier int, scope, source string) (int, string) {
		claims := claimsFor(config, "task-"+id, scope, source, 1000)
		claims.TargetMbps = tier
		req := activateRequest(signActivationToken(t, config, signingKey, claims), source)
		req.RemoteAddr = net.JoinHostPort(source, "12345")
		recorder := httptest.NewRecorder()
		node.server.Handler.ServeHTTP(recorder, req)
		return recorder.Code, strings.TrimSpace(recorder.Body.String())
	}
	for _, task := range []struct {
		id            string
		tier          int
		scope, source string
	}{
		{"bandwidth-a", 400, "public", "127.0.0.1"},
		{"bandwidth-b", 400, "owner", "::1"},
	} {
		if status, _ := admit(task.id, task.tier, task.scope, task.source); status != 200 {
			t.Fatalf("%s: %d", task.id, status)
		}
	}
	status, key := admit("bandwidth-c", 200, "public", "127.0.0.1")
	if status != 200 {
		t.Fatal(status)
	}
	if node.state.reservedBandwidth() != 1000 {
		t.Fatal("budget not fully reserved")
	}
	if status, _ := admit("bandwidth-d", 100, "owner", "::1"); status != 429 {
		t.Fatalf("overbooking: %d", status)
	}
	release := httptest.NewRequest("POST", "/release?key="+key, nil)
	release.RemoteAddr = "127.0.0.1:12345"
	node.release(httptest.NewRecorder(), release)
	node.release(httptest.NewRecorder(), release)
	if node.state.reservedBandwidth() != 800 {
		t.Fatal("duplicate release changed other reservations")
	}
	if status, _ := admit("bandwidth-d", 100, "owner", "::1"); status != 200 {
		t.Fatal(status)
	}
	if status, _ := admit("bandwidth-e", 100, "public", "127.0.0.1"); status != 200 {
		t.Fatal(status)
	}
	persisted, err := readRuntimeState(statePath(path), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.reservedBandwidth() != 1000 {
		t.Fatal("status does not reflect reserved bandwidth")
	}
	node.state.mu.Lock()
	for _, active := range node.state.activations {
		active.ExpiresAt = time.Now().Unix() - 1
	}
	node.state.mu.Unlock()
	node.state.cleanupExpired(config, time.Now())
	if node.state.reservedBandwidth() != 0 {
		t.Fatal("expired capacity leaked")
	}
	if status, _ := admit("bandwidth-after-expiry", 400, "public", "127.0.0.1"); status != 200 {
		t.Fatal(status)
	}
	restarted, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.state.reservedBandwidth() != 0 {
		t.Fatal("restart retained unusable activations")
	}
}

func TestBandwidthConcurrentHTTPActivationAndOnlineIncrease(t *testing.T) {
	path, config, signingKey := testConfig(t)
	config.CapacityMbps = 1000
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	// The same running HTTP server must support a later budget increase.
	config.CapacityMbps = 3000
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var accepted atomic.Int64
	start := make(chan struct{})
	for i := 0; i < 40; i++ {
		source := fmt.Sprintf("127.0.0.%d", i+1)
		token := signActivationToken(t, config, signingKey, claimsFor(config, fmt.Sprintf("bandwidth-parallel-%d", i), "public", source, 1000))
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			recorder := httptest.NewRecorder()
			node.server.Handler.ServeHTTP(recorder, activateRequest(token, source))
			if recorder.Code == http.StatusOK {
				accepted.Add(1)
			} else if recorder.Code != 429 {
				t.Errorf("status %d", recorder.Code)
			}
		}()
	}
	close(start)
	workers.Wait()
	if accepted.Load() != 30 || node.state.reservedBandwidth() != 3000 {
		t.Fatalf("accepted=%d budget=%d", accepted.Load(), node.state.reservedBandwidth())
	}
}

func TestBandwidthMigrationIsExplicitAndPreservesOtherSettings(t *testing.T) {
	path, config, _ := testConfig(t)
	config.NodeID, config.RouteKey, config.APIToken = "", "", ""
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	original, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if original.CapacityMbps != 0 || original.MaxConcurrency != 2 || original.ReserveConcurrency != 1 {
		t.Fatal("legacy policy migrated without confirmation")
	}
	var output bytes.Buffer
	if err := updateLimit(path, []string{"bandwidth", "100"}, &output); err == nil {
		t.Fatal("accepted budget below maximum tier")
	}
	if err := updateLimit(path, []string{"bandwidth", "600"}, &output); err != nil {
		t.Fatal(err)
	}
	migrated, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := original
	expected.CapacityMbps = 600
	expected.MaxConcurrency, expected.ReserveConcurrency = 0, 0
	if migrated != expected {
		t.Fatalf("unexpected migration: %+v", migrated)
	}
	if !strings.Contains(output.String(), "Route Key优先调度") {
		t.Fatal("missing reserved-slot migration explanation")
	}
	if err := updateLimit(path, []string{"concurrency", "3"}, &output); err == nil {
		t.Fatal("new node accepted hidden concurrency limit")
	}
	payload := buildHeartbeatPayload(path, migrated, &runtimeState{persistent: persistentState{Day: time.Now().UTC().Format("2006-01-02")}}, time.Now())
	if payload.CapacityMbps != 600 || !strings.Contains(strings.Join(payload.Capabilities, ","), "capacity.bandwidth.v1") {
		t.Fatalf("missing negotiated capacity: %+v", payload)
	}
}
