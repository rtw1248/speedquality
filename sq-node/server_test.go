package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(t *testing.T) (string, Config, *ecdsa.PrivateKey) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	coordinate := func(value *big.Int) string {
		data := value.FillBytes(make([]byte, 32))
		return base64.RawURLEncoding.EncodeToString(data)
	}
	config := defaultConfig()
	config.PlatformURL = "https://sq.example"
	config.ListenAddress = "127.0.0.1"
	config.Port = 51234
	config.PublicIPv4 = "8.8.8.8"
	config.Region = "hb"
	config.Carrier = "ct"
	config.MaxMbps = 200
	config.MaxConcurrency = 2
	config.ReserveConcurrency = 1
	config.DailyPublicBytes = 2_000_000_000
	config.AccessMode = "public"
	config.NodeID = "0123456789abcdef0123456789abcdef"
	config.RouteKey = "sqn_" + strings.Repeat("r", 32)
	config.APIToken = "sqa_" + strings.Repeat("a", 43)
	config.JWTIssuer = "test-issuer"
	config.JWTAudience = "sq-node"
	config.JWTPublicKey = PublicJWK{
		Kty: "EC", Crv: "P-256", Alg: "ES256", Kid: "test-key",
		X: coordinate(privateKey.X), Y: coordinate(privateKey.Y),
	}
	path := filepath.Join(t.TempDir(), "node.json")
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	return path, config, privateKey
}

func signActivationToken(t *testing.T, config Config, key *ecdsa.PrivateKey, claims ActivationClaims) string {
	t.Helper()
	header, _ := json.Marshal(jwtHeader{Algorithm: "ES256", Type: "JWT", KeyID: config.JWTPublicKey.Kid})
	payload, _ := json.Marshal(claims)
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	input := encodedHeader + "." + encodedPayload
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func claimsFor(config Config, id, scope, source string, maxBytes int64) ActivationClaims {
	now := time.Now().Unix()
	return ActivationClaims{
		Issuer: config.JWTIssuer, Audience: config.JWTAudience, Subject: config.NodeID,
		JWTID: id, Scope: scope, ClientIP: source, TargetMbps: 100,
		MaxBytesPerDirection: maxBytes, DurationSeconds: 5,
		IssuedAt: now, NotBefore: now - 1, ExpiresAt: now + 180,
	}
}

func activateRequest(token, source string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://node.test/activate", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.RemoteAddr = source + ":12345"
	return request
}

func activate(t *testing.T, node *nodeServer, token, source string) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	node.activate(recorder, activateRequest(token, source))
	return recorder.Code, strings.TrimSpace(recorder.Body.String())
}

func TestActivationJWTReplayAndSourceBinding(t *testing.T) {
	path, config, privateKey := testConfig(t)
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	token := signActivationToken(t, config, privateKey, claimsFor(
		config, "activation-jti-0001", "public", "127.0.0.1", 1024,
	))
	status, key := activate(t, node, token, "127.0.0.1")
	if status != http.StatusOK || key == "" {
		t.Fatalf("activation status=%d key=%q", status, key)
	}
	status, _ = activate(t, node, token, "127.0.0.1")
	if status != http.StatusConflict {
		t.Fatalf("replayed JWT status=%d", status)
	}

	wrongSource := signActivationToken(t, config, privateKey, claimsFor(
		config, "activation-jti-0002", "owner", "8.8.4.4", 1024,
	))
	status, _ = activate(t, node, wrongSource, "127.0.0.1")
	if status != http.StatusForbidden {
		t.Fatalf("source mismatch status=%d", status)
	}
}

func TestAccessModesReservedCapacityAndDailyQuota(t *testing.T) {
	path, config, privateKey := testConfig(t)
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	newToken := func(id, scope string, bytes int64) string {
		return signActivationToken(t, config, privateKey, claimsFor(
			config, id, scope, "127.0.0.1", bytes,
		))
	}
	if status, _ := activate(t, node, newToken("public-slot-0001", "public", 1000), "127.0.0.1"); status != 200 {
		t.Fatalf("first public status=%d", status)
	}
	if status, _ := activate(t, node, newToken("public-slot-0002", "public", 1000), "127.0.0.1"); status != 429 {
		t.Fatalf("reserved slot was consumed by public task: status=%d", status)
	}
	if status, _ := activate(t, node, newToken("owner-slot-0001", "owner", 1000), "127.0.0.1"); status != 200 {
		t.Fatalf("owner could not use reserved slot: status=%d", status)
	}

	privatePath, privateConfig, privateKey := testConfig(t)
	privateConfig.AccessMode = "private"
	if err := saveConfig(privatePath, privateConfig); err != nil {
		t.Fatal(err)
	}
	privateNode, _ := newNodeServer(privatePath)
	if status, _ := activate(t, privateNode, signActivationToken(
		t, privateConfig, privateKey, claimsFor(privateConfig, "private-public-01", "public", "127.0.0.1", 100),
	), "127.0.0.1"); status != http.StatusForbidden {
		t.Fatalf("private node accepted public task: status=%d", status)
	}
	if status, _ := activate(t, privateNode, signActivationToken(
		t, privateConfig, privateKey, claimsFor(privateConfig, "private-owner-001", "owner", "127.0.0.1", 100),
	), "127.0.0.1"); status != http.StatusOK {
		t.Fatalf("private node rejected owner: status=%d", status)
	}
	privateConfig.AccessMode = "paused"
	if err := saveConfig(privatePath, privateConfig); err != nil {
		t.Fatal(err)
	}
	if status, _ := activate(t, privateNode, signActivationToken(
		t, privateConfig, privateKey, claimsFor(privateConfig, "paused-owner-0001", "owner", "127.0.0.1", 100),
	), "127.0.0.1"); status != http.StatusServiceUnavailable {
		t.Fatalf("paused node accepted owner: status=%d", status)
	}

	quotaPath, quotaConfig, quotaKey := testConfig(t)
	quotaConfig.MaxConcurrency = 3
	quotaConfig.ReserveConcurrency = 0
	quotaConfig.DailyPublicBytes = 200
	if err := saveConfig(quotaPath, quotaConfig); err != nil {
		t.Fatal(err)
	}
	quotaNode, _ := newNodeServer(quotaPath)
	firstToken := signActivationToken(t, quotaConfig, quotaKey, claimsFor(
		quotaConfig, "quota-public-0001", "public", "127.0.0.1", 60,
	))
	status, activationKey := activate(t, quotaNode, firstToken, "127.0.0.1")
	if status != 200 {
		t.Fatalf("first quota activation status=%d", status)
	}
	secondToken := signActivationToken(t, quotaConfig, quotaKey, claimsFor(
		quotaConfig, "quota-public-0002", "public", "127.0.0.1", 60,
	))
	if status, _ := activate(t, quotaNode, secondToken, "127.0.0.1"); status != 429 {
		t.Fatalf("daily quota status=%d", status)
	}
	release := httptest.NewRequest(http.MethodPost, "/release?key="+activationKey, nil)
	release.RemoteAddr = "127.0.0.1:12345"
	quotaNode.release(httptest.NewRecorder(), release)
	if status, _ := activate(t, quotaNode, secondToken, "127.0.0.1"); status != 200 {
		t.Fatalf("released reservation did not restore quota: status=%d", status)
	}
}

func TestConcurrentActivationsNeverExceedCapacity(t *testing.T) {
	path, config, privateKey := testConfig(t)
	config.MaxConcurrency = 3
	config.ReserveConcurrency = 0
	config.DailyPublicBytes = 10_000_000
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}

	const requests = 20
	start := make(chan struct{})
	var accepted atomic.Int64
	var workers sync.WaitGroup
	for index := 0; index < requests; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			token := signActivationToken(t, config, privateKey, claimsFor(
				config,
				fmt.Sprintf("concurrent-public-%04d", index),
				"public",
				"127.0.0.1",
				1000,
			))
			<-start
			status, _ := activate(t, node, token, "127.0.0.1")
			if status == http.StatusOK {
				accepted.Add(1)
			} else if status != http.StatusTooManyRequests {
				t.Errorf("activation %d returned status %d", index, status)
			}
		}(index)
	}
	close(start)
	workers.Wait()
	if actual := accepted.Load(); actual != int64(config.MaxConcurrency) {
		t.Fatalf("accepted %d concurrent activations, want %d", actual, config.MaxConcurrency)
	}
	active, publicActive, _, _, _, _, _ := node.state.snapshot(time.Now())
	if active != config.MaxConcurrency || publicActive != config.MaxConcurrency {
		t.Fatalf("active=%d public_active=%d want=%d", active, publicActive, config.MaxConcurrency)
	}
}

func TestTransferByteCeilingsAndSingleDirection(t *testing.T) {
	path, config, privateKey := testConfig(t)
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	token := signActivationToken(t, config, privateKey, claimsFor(
		config, "transfer-limit-0001", "owner", "127.0.0.1", 1024,
	))
	status, key := activate(t, node, token, "127.0.0.1")
	if status != 200 {
		t.Fatalf("activation status=%d", status)
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/download?key="+key, nil)
	downloadRequest.RemoteAddr = "127.0.0.1:12345"
	downloadResponse := httptest.NewRecorder()
	node.download(downloadResponse, downloadRequest)
	if downloadResponse.Body.Len() != 1024 {
		t.Fatalf("downloaded %d bytes", downloadResponse.Body.Len())
	}

	uploadRequest := httptest.NewRequest(
		http.MethodPost, "/upload?key="+key, bytes.NewReader(bytes.Repeat([]byte("x"), 2048)),
	)
	uploadRequest.RemoteAddr = "127.0.0.1:12345"
	uploadResponse := httptest.NewRecorder()
	node.upload(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusNoContent {
		t.Fatalf("upload status=%d", uploadResponse.Code)
	}
	node.state.mu.Lock()
	item := node.state.activations[key]
	if item.DownloadBytes != 1024 || item.UploadBytes != 1024 {
		t.Fatalf("unexpected transfer bytes: download=%d upload=%d", item.DownloadBytes, item.UploadBytes)
	}
	item.DownloadActive = true
	node.state.mu.Unlock()
	duplicate := httptest.NewRecorder()
	node.download(duplicate, downloadRequest)
	if duplicate.Code != http.StatusForbidden {
		t.Fatalf("parallel download status=%d", duplicate.Code)
	}
}

func TestJWTRejectsUnsupportedSpeedTier(t *testing.T) {
	_, config, privateKey := testConfig(t)
	claims := claimsFor(config, "invalid-tier-0001", "owner", "127.0.0.1", 1024)
	claims.TargetMbps = 150
	token := signActivationToken(t, config, privateKey, claims)
	if _, err := verifyActivationJWT(token, config, time.Now()); err == nil {
		t.Fatal("150 Mbps JWT was accepted")
	}
}

func TestTotalDailyQuotaCoversOwnerActivations(t *testing.T) {
	path, config, privateKey := testConfig(t)
	config.DailyPublicBytes = 0
	config.DailyTotalBytes = 1_000_000
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	first := signActivationToken(t, config, privateKey, claimsFor(
		config, "total-quota-owner-0001", "owner", "127.0.0.1", 300_000,
	))
	if status, _ := activate(t, node, first, "127.0.0.1"); status != http.StatusOK {
		t.Fatalf("first owner activation status=%d", status)
	}
	second := signActivationToken(t, config, privateKey, claimsFor(
		config, "total-quota-owner-0002", "owner", "127.0.0.1", 300_000,
	))
	if status, _ := activate(t, node, second, "127.0.0.1"); status != http.StatusTooManyRequests {
		t.Fatalf("owner activation beyond total quota status=%d", status)
	}
}

func TestAnonymousHealthIsMinimalAndPreAuthRequestsAreLimited(t *testing.T) {
	path, _, _ := testConfig(t)
	node, err := newNodeServer(path)
	if err != nil {
		t.Fatal(err)
	}
	health := httptest.NewRecorder()
	node.server.Handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "http://node.test/healthz", nil))
	var value map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 2 || value["status"] != "ok" || value["protocol_version"] != float64(1) {
		t.Fatalf("unexpected health response: %#v", value)
	}
	for attempt := 0; attempt < 20; attempt++ {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://node.test/activate", nil)
		request.RemoteAddr = "192.0.2.1:1234"
		node.server.Handler.ServeHTTP(response, request)
	}
	limited := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://node.test/activate", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	node.server.Handler.ServeHTTP(limited, request)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("pre-auth limiter status=%d", limited.Code)
	}
}

func TestListenHostSupportsBracketedIPv6(t *testing.T) {
	for input, expected := range map[string]string{"[::]": "::", "::": "::", "0.0.0.0": "0.0.0.0"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			if actual := listenHost(input); actual != expected {
				t.Fatalf("listenHost(%q)=%q", input, actual)
			}
		})
	}
}
