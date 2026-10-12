package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPlatformRegistrationHeartbeatAndUnregisterPreserveNodeSetup(t *testing.T) {
	path, config, _ := testConfig(t)
	jwtPublicKey := config.JWTPublicKey
	config.NodeID = ""
	config.RouteKey = ""
	config.APIToken = ""
	config.JWTIssuer = ""
	config.JWTPublicKey = PublicJWK{}
	port := config.Port
	config.ListenAddress = "127.0.0.1"

	var heartbeat heartbeatRequest
	var unregisterAuthorization string
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/nodes/register":
			var value registrationRequest
			if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
				t.Error(err)
			}
			if !strings.HasPrefix(value.Challenge, "sq-owner-") || !value.Authorized {
				t.Errorf("invalid registration payload: %+v", value)
			}
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(registrationResponse{
				NodeID:       "fedcba9876543210fedcba9876543210",
				RouteKey:     "sqn_" + strings.Repeat("r", 32),
				APIToken:     "sqa_" + strings.Repeat("a", 43),
				JWTPublicJWK: jwtPublicKey,
				JWTIssuer:    "platform-issuer", JWTAudience: "sq-node", HeartbeatSecs: 45,
			})
		case "/api/nodes/heartbeat":
			if request.Header.Get("Authorization") != "Bearer sqa_"+strings.Repeat("a", 43) {
				t.Error("heartbeat API token missing")
			}
			if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(response).Encode(heartbeatResponse{
				Status: "ok", JWTPublicJWK: jwtPublicKey,
				JWTIssuer: "platform-issuer", JWTAudience: "sq-node", HeartbeatSecs: 45,
			})
		case "/api/nodes/unregister":
			unregisterAuthorization = request.Header.Get("Authorization")
			_ = json.NewEncoder(response).Encode(map[string]string{"status": "revoked"})
		default:
			http.NotFound(response, request)
		}
	})
	config.PlatformURL = "https://sq.example"
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}

	previousClient := platformHTTPClient
	previousNodeCheck := nodeRespondsLocally
	platformHTTPClient = &http.Client{
		Timeout: 35 * time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Result(), nil
		}),
	}
	nodeRespondsLocally = func(Config) bool { return true }
	t.Cleanup(func() {
		platformHTTPClient = previousClient
		nodeRespondsLocally = previousNodeCheck
	})

	registered, err := registerWithPlatform(path)
	if err != nil {
		t.Fatal(err)
	}
	if registered.NodeID == "" || registered.RouteKey == "" || registered.APIToken == "" {
		t.Fatalf("incomplete registration: %+v", registered)
	}
	if registered.OwnershipChallenge != "" || registered.HeartbeatSeconds != 45 {
		t.Fatalf("registration state was not finalized: %+v", registered)
	}

	state, err := loadRuntimeState(statePath(path), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.persistent.PublicBytes = 1234
	state.persistent.PublicReservedBytes = 5678
	state.persistent.TotalBytes = 2345
	state.persistent.TotalReservedBytes = 6789
	state.persistent.EmergencyStopped = true
	if err := state.saveLocked(); err != nil {
		state.mu.Unlock()
		t.Fatal(err)
	}
	state.mu.Unlock()
	if err := saveGateStatus(gateStatusPath(path), gateStatus{
		Ready: true, ActiveLeases: 1, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := sendHeartbeat(context.Background(), path, state); err != nil {
		t.Fatal(err)
	}
	if heartbeat.PublicBytesToday != 1234 || heartbeat.PublicReserved != 5678 {
		t.Fatalf("heartbeat usage=%+v", heartbeat)
	}
	if heartbeat.TotalBytesToday != 2345 || heartbeat.TotalReserved != 6789 ||
		!heartbeat.EmergencyStopped || !heartbeat.FirewallReady {
		t.Fatalf("heartbeat protection fields=%+v", heartbeat)
	}
	if heartbeat.ProtocolVersion != 1 || len(heartbeat.Capabilities) != 1 ||
		heartbeat.Capabilities[0] != "speed.http.v1" || heartbeat.Availability != "always" ||
		!heartbeat.Available {
		t.Fatalf("heartbeat lifecycle fields=%+v", heartbeat)
	}

	if err := unregisterFromPlatform(path); err != nil {
		t.Fatal(err)
	}
	if unregisterAuthorization == "" {
		t.Fatal("unregister API token missing")
	}
	remaining, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.NodeID != "" || remaining.APIToken != "" || remaining.RouteKey != "" {
		t.Fatalf("credentials were not removed: %+v", remaining)
	}
	if remaining.Port != port || remaining.PublicIPv4 != "8.8.8.8" || remaining.Region != "hb" {
		t.Fatalf("node setup was removed: %+v", remaining)
	}
}

func TestSetupAutoDetectionSuppliesIdentityAndSafeDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	previousDetection := detectNodeSetup
	previousPortCheck := portAvailable
	detectNodeSetup = func(string) (detectedEnvironment, error) {
		return detectedEnvironment{
			IPv4: "8.8.8.8", IPv6: "2001:4860:4860::8888",
			Region: "hb", Carrier: "ct",
		}, nil
	}
	portAvailable = func(int) bool { return true }
	t.Cleanup(func() {
		detectNodeSetup = previousDetection
		portAvailable = previousPortCheck
	})
	var output bytes.Buffer
	arguments, err := prepareSetupArguments(
		path, []string{"--platform", "https://sq.example"},
		bufio.NewReader(strings.NewReader("")), &output, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := initCommandModeNamed(path, arguments, strings.NewReader(""), &output, false, "setup"); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.PublicIPv4 != "8.8.8.8" || config.PublicIPv6 != "2001:4860:4860::8888" ||
		config.Region != "hb" || config.Carrier != "ct" || config.MaxMbps != 200 ||
		config.MaxConcurrency != 5 || config.DailyPublicBytes != 10_000_000_000 ||
		config.DailyTotalBytes != 100_000_000_000 {
		t.Fatalf("auto config=%+v", config)
	}
}

func TestSetupRejectsForeignAddressEvenWhenProvinceIsClaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	previousDetection := detectNodeSetup
	detectNodeSetup = func(string) (detectedEnvironment, error) {
		return detectedEnvironment{
			IPv4: "8.8.8.8", CountryCode: "JP",
		}, nil
	}
	t.Cleanup(func() { detectNodeSetup = previousDetection })
	var output bytes.Buffer
	_, err := prepareSetupArguments(path, []string{
		"--platform", "https://sq.example", "--region", "hb", "--carrier", "ct",
	}, bufio.NewReader(strings.NewReader("")), &output, false)
	if err == nil || !strings.Contains(err.Error(), "不能将海外 IP 登记为国内省份") {
		t.Fatalf("foreign claim error=%v output=%s", err, output.String())
	}
	if strings.Contains(output.String(), "请输入") {
		t.Fatalf("unsupported country prompted for a domestic province: %s", output.String())
	}
}

func TestSetupManualIdentityClaimsRemainUnverifiedAndExplicitIPv6DisableIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	previous := detectNodeSetup
	detectNodeSetup = func(string) (detectedEnvironment, error) {
		return detectedEnvironment{IPv4: "8.8.8.8", IPv6: "2001:4860:4860::8888", CountryCode: "CN"}, nil
	}
	t.Cleanup(func() { detectNodeSetup = previous })
	var output bytes.Buffer
	arguments, err := prepareSetupArguments(path, []string{"--platform", "https://sq.example", "--ipv6=", "--port", "51234"},
		bufio.NewReader(strings.NewReader("湖北\n中国电信\n")), &output, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := initCommandModeNamed(path, arguments, strings.NewReader(""), &output, false, "setup"); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Region != "hb" || config.Carrier != "ct" || config.PublicIPv6 != "" || config.NodeID != "" {
		t.Fatalf("manual identity or disabled family lost: %+v", config)
	}
	printSetupSummary(&output, config)
	for _, expected := range []string{"电信 ct / 联通 cu / 移动 cm", "申报值", "待平台核验"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in %s", expected, output.String())
		}
	}
}

func TestSetupOptionsPreserveIdentityAndHiddenLimitsAndRejectBusyPort(t *testing.T) {
	path, config, _ := testConfig(t)
	config.NodeID, config.RouteKey, config.APIToken = "", "", ""
	config.DailyTotalBytes = 20_000_000_000
	config.Availability, config.Timezone = "08:00-18:00", "UTC"
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	previous := portAvailable
	portAvailable = func(port int) bool { return port == 51235 }
	t.Cleanup(func() { portAvailable = previous })
	var output bytes.Buffer
	input := bufio.NewReader(strings.NewReader("51235\n200\n5\n100GB\n2\n2\n"))
	if err := configureSetupInteractive(path, input, &output); err != nil {
		t.Fatal(err)
	}
	updated, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := config
	want.Port, want.MaxMbps, want.MaxConcurrency, want.DailyTotalBytes = 51235, 200, 5, 100_000_000_000
	want.AccessMode, want.UpdateMode = "private", "manual"
	if updated != want {
		t.Fatalf("unexpected changes to existing configuration: got %+v want %+v", updated, want)
	}
	for _, hidden := range []string{"平台地址 [", "省份代码 [", "公网 IPv4 [", "公网 IPv6", "提供者保留并发 [", "每日公共流量", "可用时间", "时区 ["} {
		if strings.Contains(output.String(), hidden) {
			t.Fatalf("unexpected prompt %q: %s", hidden, output.String())
		}
	}
	if err := configureSetupInteractive(path, bufio.NewReader(strings.NewReader("51236\n")), &output); err == nil {
		t.Fatal("busy port accepted")
	}
	remaining, err := loadConfig(path)
	if err != nil || remaining != want {
		t.Fatalf("invalid port changed saved settings: %v %+v", err, remaining)
	}
}

func TestRegistrationRejectsDuplicateAndClearsFailedChallenge(t *testing.T) {
	path, config, _ := testConfig(t)
	if _, err := registerWithPlatform(path); err == nil || !strings.Contains(err.Error(), "已经注册") {
		t.Fatalf("duplicate registration error=%v", err)
	}

	config.NodeID = ""
	config.RouteKey = ""
	config.APIToken = ""
	config.JWTIssuer = ""
	config.JWTPublicKey = PublicJWK{}
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	previousClient := platformHTTPClient
	previousNodeCheck := nodeRespondsLocally
	platformHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.WriteString(`{"error":"registration unavailable"}`)
		return response.Result(), nil
	})}
	nodeRespondsLocally = func(Config) bool { return true }
	t.Cleanup(func() {
		platformHTTPClient = previousClient
		nodeRespondsLocally = previousNodeCheck
	})
	if _, err := registerWithPlatform(path); err == nil {
		t.Fatal("failed platform registration succeeded")
	}
	remaining, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.OwnershipChallenge != "" {
		t.Fatalf("failed registration left ownership challenge %q", remaining.OwnershipChallenge)
	}
}

func TestTerminalMenuReusesItsBufferedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	t.Setenv("SQ_NODE_CONFIG", path)
	previousPortCheck := portAvailable
	portAvailable = func(int) bool { return true }
	t.Cleanup(func() { portAvailable = previousPortCheck })
	input := bufio.NewReader(strings.NewReader(strings.Join([]string{
		"3",
		"https://sq.example",
		"hb",
		"ct",
		"8.8.8.8",
		"",
		"0",
		"",
	}, "\n")))
	var output bytes.Buffer
	if err := run(nil, input, &output); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(path)
	if err != nil {
		t.Fatalf("menu did not save config: %v\n%s", err, output.String())
	}
	if config.Region != "hb" || config.Carrier != "ct" || config.PublicIPv4 != "8.8.8.8" ||
		config.Port < 50000 || config.Port > 59999 {
		t.Fatalf("menu config=%+v", config)
	}
	if !strings.Contains(output.String(), "配置已保存") {
		t.Fatalf("menu output=%s", output.String())
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions: %v, %v", info, err)
	}
}

func TestSetupSelectsEducationIPv6FromMixedNetworks(t *testing.T) {
	previous := detectNodeSetup
	detectNodeSetup = func(string) (detectedEnvironment, error) {
		return detectedEnvironment{Families: []detectionResponse{
			{Address: "8.8.8.8", Family: "v4", Region: "hb", CountryCode: "CN", Carrier: "ct"},
			{Address: "2001:4860:4860::8888", Family: "v6", Region: "hb", CountryCode: "CN", Carrier: "edu"},
		}}, nil
	}
	t.Cleanup(func() { detectNodeSetup = previous })
	for _, args := range [][]string{{"--carrier", "教育网"}, {"--ipv4", ""}} {
		path := filepath.Join(t.TempDir(), "node.json")
		var output bytes.Buffer
		arguments, err := prepareSetupArguments(path, append(args, "--platform", "https://sq.example", "--port", "51234"), bufio.NewReader(strings.NewReader("")), &output, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := initCommandModeNamed(path, arguments, strings.NewReader(""), &output, false, "setup"); err != nil {
			t.Fatal(err)
		}
		config, err := loadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if config.Carrier != "edu" || config.PublicIPv4 != "" || config.PublicIPv6 != "2001:4860:4860::8888" {
			t.Fatalf("mixed network selected incorrectly: %+v", config)
		}
		printSetupSummary(&output, config)
		if !strings.Contains(output.String(), "暂不参与公共测速") {
			t.Fatal(output.String())
		}
	}
	_, err := prepareSetupArguments(filepath.Join(t.TempDir(), "node.json"), []string{"--platform", "https://sq.example"}, bufio.NewReader(strings.NewReader("")), io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "运营商不同") {
		t.Fatalf("ambiguous networks should require selection: %v", err)
	}
}
