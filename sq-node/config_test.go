package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHelpIncludesSetupAndSetupHelpDoesNotWriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	t.Setenv("SQ_NODE_CONFIG", path)
	var output bytes.Buffer
	if err := run([]string{"--help"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "sq-node setup") {
		t.Fatalf("main help does not include setup: %s", output.String())
	}
	output.Reset()
	err := run([]string{"setup", "--help"}, strings.NewReader(""), &output)
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(output.String(), "-platform") {
		t.Fatalf("setup help error=%v output=%s", err, output.String())
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("setup --help unexpectedly wrote config: %v", statErr)
	}
}

func TestConfigValidationAndByteLimits(t *testing.T) {
	config := defaultConfig()
	config.Port = 51234
	config.PublicIPv4 = "8.8.8.8"
	config.Region = "hb"
	config.Carrier = "ct"
	if err := config.validate(false); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for _, address := range []string{
		"127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.0.1", "192.0.2.1", "198.18.0.1",
	} {
		invalid := config
		invalid.PublicIPv4 = address
		if err := invalid.validate(false); err == nil {
			t.Fatalf("non-public address %s was accepted", address)
		}
	}
	invalid := config
	invalid.ReserveConcurrency = invalid.MaxConcurrency
	if err := invalid.validate(false); err == nil {
		t.Fatal("reserve concurrency equal to total was accepted")
	}
	for _, platform := range []string{
		"http://sq.example", "https://user@sq.example", "https://sq.example/path",
	} {
		invalid = config
		invalid.PlatformURL = platform
		if err := invalid.validate(false); err == nil {
			t.Fatalf("invalid platform URL %q was accepted", platform)
		}
	}

	cases := map[string]int64{
		"0": 0, "500MB": 500_000_000, "20GB": 20_000_000_000, "1TB": 1_000_000_000_000,
	}
	for input, expected := range cases {
		actual, err := parseByteLimit(input)
		if err != nil || actual != expected {
			t.Fatalf("parseByteLimit(%q)=%d, %v", input, actual, err)
		}
	}
	for _, input := range []string{"-1GB", "text", "10001TB"} {
		if _, err := parseByteLimit(input); err == nil {
			t.Fatalf("invalid byte limit %q was accepted", input)
		}
	}
}

func TestRegisteredNodeIdentityCannotChangeLocally(t *testing.T) {
	path, original, _ := testConfig(t)
	var output bytes.Buffer
	err := initCommandMode(
		path,
		[]string{"--ipv4", "9.9.9.9"},
		strings.NewReader(""),
		&output,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "sq-node unregister") {
		t.Fatalf("identity change error=%v", err)
	}
	remaining, loadErr := loadConfig(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if remaining.PublicIPv4 != original.PublicIPv4 || remaining.NodeID != original.NodeID {
		t.Fatalf("registered identity changed: %+v", remaining)
	}
}

func TestConfigAndStateUsePrivateFilesAndConservativeRestartAccounting(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "node.json")
	config := defaultConfig()
	config.Port = 51234
	config.PublicIPv4 = "8.8.8.8"
	config.Region = "hb"
	config.Carrier = "ct"
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%o", info.Mode().Perm())
	}

	now := time.Date(2026, 9, 28, 1, 30, 0, 0, time.FixedZone("CST", 8*3600))
	state, err := loadRuntimeState(statePath(path), now)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.persistent.PublicBytes = 100
	state.persistent.PublicReservedBytes = 50
	state.persistent.TotalBytes = 200
	state.persistent.TotalReservedBytes = 75
	state.persistent.EmergencyStopped = true
	if err := state.saveLocked(); err != nil {
		state.mu.Unlock()
		t.Fatal(err)
	}
	state.mu.Unlock()
	reloaded, err := loadRuntimeState(statePath(path), now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, used, reserved, totalUsed, totalReserved, emergencyStopped := reloaded.snapshot(now)
	if used != 150 || reserved != 0 {
		t.Fatalf("restart accounting used=%d reserved=%d", used, reserved)
	}
	if totalUsed != 275 || totalReserved != 0 || !emergencyStopped {
		t.Fatalf("total restart accounting used=%d reserved=%d emergency=%t", totalUsed, totalReserved, emergencyStopped)
	}
	if reloaded.persistent.Day != "2026-09-27" {
		t.Fatalf("quota day must be UTC, got %s", reloaded.persistent.Day)
	}
}

func TestReadRuntimeStateDoesNotSettleLiveReservations(t *testing.T) {
	path, _, _ := testConfig(t)
	now := time.Now()
	state, err := loadRuntimeState(statePath(path), now)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.persistent.PublicBytes = 100
	state.persistent.PublicReservedBytes = 50
	state.persistent.TotalBytes = 200
	state.persistent.TotalReservedBytes = 75
	if err := state.saveLocked(); err != nil {
		state.mu.Unlock()
		t.Fatal(err)
	}
	state.mu.Unlock()

	readOnly, err := readRuntimeState(statePath(path), now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, publicUsed, publicReserved, totalUsed, totalReserved, _ := readOnly.snapshot(now)
	if publicUsed != 100 || publicReserved != 50 || totalUsed != 200 || totalReserved != 75 {
		t.Fatalf("read-only snapshot settled reservations: %d %d %d %d",
			publicUsed, publicReserved, totalUsed, totalReserved)
	}
	reloaded, err := readRuntimeState(statePath(path), now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, publicUsed, publicReserved, totalUsed, totalReserved, _ = reloaded.snapshot(now)
	if publicUsed != 100 || publicReserved != 50 || totalUsed != 200 || totalReserved != 75 {
		t.Fatalf("read-only snapshot changed disk state: %d %d %d %d",
			publicUsed, publicReserved, totalUsed, totalReserved)
	}
}

func TestRestartChargesPublicReservationOnceToTotalQuota(t *testing.T) {
	path, _, _ := testConfig(t)
	now := time.Now()
	state, err := readRuntimeState(statePath(path), now)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.persistent.PublicReservedBytes = 500
	state.persistent.TotalReservedBytes = 500
	if err := state.saveLocked(); err != nil {
		state.mu.Unlock()
		t.Fatal(err)
	}
	state.mu.Unlock()

	reloaded, err := loadRuntimeState(statePath(path), now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, publicUsed, publicReserved, totalUsed, totalReserved, _ := reloaded.snapshot(now)
	if publicUsed != 500 || totalUsed != 500 || publicReserved != 0 || totalReserved != 0 {
		t.Fatalf("restart accounting public=%d/%d total=%d/%d",
			publicUsed, publicReserved, totalUsed, totalReserved)
	}
}

func TestTelemetryShowsOperationalFieldsWithoutCredentialsOrAddresses(t *testing.T) {
	path, config, _ := testConfig(t)
	if err := saveGateStatus(gateStatusPath(path), gateStatus{
		Ready: true, ActiveLeases: 2, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := telemetryCommand(path, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"daily_total_bytes", "total_bytes_today", "firewall_ready"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("telemetry omitted %s: %s", expected, text)
		}
	}
	for _, secret := range []string{config.APIToken, config.RouteKey, config.PublicIPv4, "client_ip"} {
		if strings.Contains(text, secret) {
			t.Fatalf("telemetry exposed %q: %s", secret, text)
		}
	}
}
