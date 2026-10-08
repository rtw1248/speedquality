package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSignedUpdateManifestAndVersionComparison(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	manifestValue := releaseManifest{
		Version: "v1.2.3",
		Assets: map[string]releaseAsset{
			"sq-node-linux-" + runtime.GOARCH: {SHA256: hex.EncodeToString(digest[:]), Size: 1},
		},
	}
	manifest, _ := json.Marshal(manifestValue)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	previousKey := releasePublicKeyBase64
	releasePublicKeyBase64 = base64.StdEncoding.EncodeToString(publicKey)
	t.Cleanup(func() { releasePublicKeyBase64 = previousKey })
	if err := verifyReleaseManifest(manifest, []byte(signature)); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseManifest(append(manifest, ' '), []byte(signature)); err == nil {
		t.Fatal("modified manifest passed signature verification")
	}
	if comparison, err := compareReleaseVersions("v1.2.2", "v1.2.3"); err != nil || comparison != -1 {
		t.Fatalf("comparison=%d err=%v", comparison, err)
	}
}

func TestCheckNodeUpdateRequiresSignedSameOriginManifest(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	digest := sha256.Sum256([]byte("x"))
	manifestValue := releaseManifest{
		Version: "v1.2.3",
		Assets: map[string]releaseAsset{
			"sq-node-linux-" + runtime.GOARCH: {SHA256: hex.EncodeToString(digest[:]), Size: 1},
		},
	}
	manifest, _ := json.Marshal(manifestValue)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	previousKey := releasePublicKeyBase64
	previousClient := updateHTTPClient
	releasePublicKeyBase64 = base64.StdEncoding.EncodeToString(publicKey)
	updateHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := ""
		switch request.URL.Path {
		case "/api/nodes/update":
			body = `{"channel":"stable","version":"v1.2.3","manifest_url":"https://sq.example/bin/v1.2.3/release-manifest.json","signature_url":"https://sq.example/bin/v1.2.3/release-manifest.json.sig"}`
		case "/bin/v1.2.3/release-manifest.json":
			body = string(manifest)
		case "/bin/v1.2.3/release-manifest.json.sig":
			body = signature
		default:
			return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() {
		releasePublicKeyBase64 = previousKey
		updateHTTPClient = previousClient
	})
	config := defaultConfig()
	config.PlatformURL = "https://sq.example"
	update, err := checkNodeUpdate(config)
	if err != nil {
		t.Fatal(err)
	}
	if update.manifest.Version != "v1.2.3" {
		t.Fatalf("update=%+v", update)
	}
}

func TestInstallNodeUpdateRestartsNodeBeforeGateway(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "sq-node")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls, restore := prepareInstallUpdateTest(t, executable, []byte("new"), false)
	defer restore()

	config := defaultConfig()
	config.PlatformURL = "https://sq.example"
	update := testVerifiedUpdate([]byte("new"))
	if err := installNodeUpdate("unused", config, update); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "new" {
		t.Fatalf("installed binary=%q", written)
	}
	expected := []string{
		"stop speedquality-node-gate.service",
		"restart speedquality-node.service",
		"restart speedquality-node-gate.service",
	}
	if !reflect.DeepEqual(*calls, expected) {
		t.Fatalf("systemctl calls=%q", *calls)
	}
}

func TestInstallNodeUpdateRollsBackNodeAndGateway(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "sq-node")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls, restore := prepareInstallUpdateTest(t, executable, []byte("new"), true)
	defer restore()

	config := defaultConfig()
	config.PlatformURL = "https://sq.example"
	err := installNodeUpdate("unused", config, testVerifiedUpdate([]byte("new")))
	if err == nil || !strings.Contains(err.Error(), "已回滚") {
		t.Fatalf("expected successful rollback error, got %v", err)
	}
	written, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(written) != "old" {
		t.Fatalf("rolled back binary=%q", written)
	}
	expected := []string{
		"stop speedquality-node-gate.service",
		"restart speedquality-node.service",
		"restart speedquality-node-gate.service",
		"stop speedquality-node-gate.service",
		"restart speedquality-node.service",
		"restart speedquality-node-gate.service",
	}
	if !reflect.DeepEqual(*calls, expected) {
		t.Fatalf("systemctl calls=%q", *calls)
	}
}

func testVerifiedUpdate(binary []byte) verifiedUpdate {
	digest := sha256.Sum256(binary)
	asset := "sq-node-linux-" + runtime.GOARCH
	details := releaseAsset{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(binary))}
	return verifiedUpdate{
		manifest: releaseManifest{Version: "v1.2.3", Assets: map[string]releaseAsset{asset: details}},
		asset:    asset,
		details:  details,
	}
}

func prepareInstallUpdateTest(
	t *testing.T,
	executable string,
	binary []byte,
	failFirstGatewayRestart bool,
) (*[]string, func()) {
	t.Helper()
	previousClient := updateHTTPClient
	previousExecutable := updateExecutablePath
	previousSystemctl := updateSystemctl
	previousResponds := updateNodeResponds
	previousInstalled := updateServiceInstalled
	previousSleep := updateSleep

	updateHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(string(binary))),
		}, nil
	})}
	updateExecutablePath = func() (string, error) { return executable, nil }
	updateServiceInstalled = func(path string) bool {
		return path == nodeServiceUnitPath || path == gateServiceUnitPath
	}
	updateNodeResponds = func(Config) bool { return true }
	updateSleep = func(_ time.Duration) {}
	var calls []string
	gatewayRestarts := 0
	updateSystemctl = func(arguments ...string) ([]byte, error) {
		call := strings.Join(arguments, " ")
		calls = append(calls, call)
		if call == "restart speedquality-node-gate.service" {
			gatewayRestarts++
			if failFirstGatewayRestart && gatewayRestarts == 1 {
				return []byte("simulated gateway failure"), errors.New("exit status 1")
			}
		}
		return nil, nil
	}

	return &calls, func() {
		updateHTTPClient = previousClient
		updateExecutablePath = previousExecutable
		updateSystemctl = previousSystemctl
		updateNodeResponds = previousResponds
		updateServiceInstalled = previousInstalled
		updateSleep = previousSleep
	}
}
