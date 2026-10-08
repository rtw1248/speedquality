package main

import (
	"encoding/base64"
	"testing"
	"time"
)

func validTestLease(now time.Time) Lease {
	prefix := base64.StdEncoding.EncodeToString([]byte("upload-prefix"))
	request := func(method, path string) RequestSpec {
		return RequestSpec{Method: method, URL: "http://127.0.0.1:8080" + path}
	}
	return Lease{
		Version: 1, LeaseID: "lease_test_123", IssuedAt: now.Unix() - 1,
		ExpiresAt: now.Unix() + 120, Region: Region{Code: "hb", Name: "湖北"},
		Family: "v4", DurationSeconds: 5, TargetMbps: 200, Modes: []string{"s"},
		Targets: []TargetGroup{{
			Carrier: "ct", Label: "湖北电信", Candidates: []Candidate{{
				ID: "node_test_123", Address: "127.0.0.1", Port: 8080,
				Activate: ActivationSpec{RequestSpec: request("GET", "/activate"), KeyPrefixBytes: 2},
				Download: request("GET", "/download?key={key}&r={nonce}"),
				Upload:   RequestSpec{Method: "POST", URL: "http://127.0.0.1:8080/upload", ContentLength: 1_000_000, BodyPrefixBase64: prefix},
				Release:  request("POST", "/release?key={key}"),
			}},
		}},
	}
}

func TestLeaseValidationAcceptsGenericOperations(t *testing.T) {
	now := time.Now()
	if err := validTestLease(now).validate(now); err != nil {
		t.Fatalf("valid lease rejected: %v", err)
	}
}

func TestLeaseValidationRejectsCrossHostOperation(t *testing.T) {
	now := time.Now()
	lease := validTestLease(now)
	lease.Targets[0].Candidates[0].Download.URL = "https://example.com/file?key={key}"
	if err := lease.validate(now); err == nil {
		t.Fatal("cross-host operation was accepted")
	}
}

func TestLeaseValidationRejectsExcessiveActivationDelay(t *testing.T) {
	now := time.Now()
	lease := validTestLease(now)
	lease.Targets[0].Candidates[0].Activate.ReadyDelayMillis = 10_001
	if err := lease.validate(now); err == nil {
		t.Fatal("excessive activation delay was accepted")
	}
}

func TestLeaseValidationRejectsUnsupportedTargetAndMode(t *testing.T) {
	now := time.Now()
	lease := validTestLease(now)
	lease.TargetMbps = 300
	if err := lease.validate(now); err == nil {
		t.Fatal("unsupported target speed was accepted")
	}
	lease = validTestLease(now)
	lease.Modes = []string{"m"}
	if err := lease.validate(now); err == nil {
		t.Fatal("multi-thread mode was accepted")
	}
}

func TestLeaseValidationRejectsUnsupportedCarrier(t *testing.T) {
	now := time.Now()
	lease := validTestLease(now)
	lease.Targets[0].Carrier = "edu"
	lease.Targets[0].Label = "湖北教育网"
	if err := lease.validate(now); err == nil {
		t.Fatal("unsupported carrier was accepted")
	}
}
