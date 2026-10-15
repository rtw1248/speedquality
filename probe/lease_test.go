package main

import (
	"encoding/base64"
	"strings"
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

func TestLeaseValidationAllowsFiveMinuteSlowClock(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	lease := validTestLease(now)
	lease.IssuedAt = now.Add(maxFutureClockSkew).Unix()
	lease.ExpiresAt = lease.IssuedAt + 120
	if err := lease.validate(now); err != nil {
		t.Fatalf("lease within clock skew tolerance rejected: %v", err)
	}
}

func TestLeaseValidationRejectsClockFurtherThanFiveMinutesBehind(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	lease := validTestLease(now)
	lease.IssuedAt = now.Add(maxFutureClockSkew + time.Second).Unix()
	lease.ExpiresAt = lease.IssuedAt + 120
	if err := lease.validate(now); err == nil {
		t.Fatal("lease beyond clock skew tolerance was accepted")
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

func TestRequestValidationAcceptsEquivalentIPv6Text(t *testing.T) {
	spec := RequestSpec{
		Method: "GET",
		URL:    "http://[2001:db8::1]:8080/download?key={key}",
	}
	if err := validateRequestSpec(spec, "2001:0db8:0:0:0:0:0:1", 8080); err != nil {
		t.Fatalf("equivalent IPv6 address rejected: %v", err)
	}
	if err := validateRequestSpec(spec, "2001:db8::2", 8080); err == nil {
		t.Fatal("different IPv6 address was accepted")
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
	lease.Targets[0].Carrier = "unknown"
	lease.Targets[0].Label = "湖北教育网"
	if err := lease.validate(now); err == nil {
		t.Fatal("unsupported carrier was accepted")
	}
}

func TestEducationLeaseAndNodeInfoValidation(t *testing.T) {
	now := time.Now()
	lease := validTestLease(now)
	lease.Targets[0].Carrier = "edu"
	info := &NodeInfo{City: "武汉", CityStatus: "verified", Source: "community", Proof: strings.Repeat("a", 64)}
	lease.Targets[0].Candidates[0].NodeInfo = info
	if err := lease.validate(now); err != nil {
		t.Fatal(err)
	}
	for _, city := range []string{"武汉\x1b[31m", "1.2.3.4", "<script>", strings.Repeat("武", 49)} {
		info.City = city
		if err := lease.validate(now); err == nil {
			t.Fatalf("unsafe city accepted: %q", city)
		}
	}
}

func TestFriendlyNodeNameAndLegacyDescription(t *testing.T) {
	result := MeasurementResult{NodeUsed: true, NodeID: strings.Repeat("a", 32), NodeInfo: &NodeInfo{
		City: "武汉", CityStatus: "verified", Source: "third_party", Name: "武汉电信1号",
	}}
	for family, expected := range map[string]string{
		"v4": "武汉电信1号（IPv4） · 第三方",
		"v6": "武汉电信1号（IPv6） · 第三方",
	} {
		if got := reportNodeDescription(result, family); got != expected {
			t.Fatalf("friendly name for %s: %q", family, got)
		}
	}
	if result.NodeInfo.Name != "武汉电信1号" {
		t.Fatal("display formatting changed the signed name")
	}
	result.NodeInfo.Name = ""
	if got := reportNodeDescription(result, "v4"); got != "武汉 · aaaaaaaaaaaa · 第三方" {
		t.Fatalf("legacy description: %q", got)
	}
	result.NodeUsed = false
	if got := reportNodeDescription(result, "v4"); got != "" {
		t.Fatalf("unused node: %q", got)
	}
}
