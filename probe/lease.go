package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maxLeaseBytes      = 256 * 1024
	maxFutureClockSkew = 5 * time.Minute
)

type Lease struct {
	Version         int           `json:"version"`
	LeaseID         string        `json:"lease_id"`
	IssuedAt        int64         `json:"issued_at"`
	ExpiresAt       int64         `json:"expires_at"`
	Region          Region        `json:"region"`
	Family          string        `json:"family"`
	DurationSeconds int           `json:"duration_seconds"`
	TargetMbps      int           `json:"target_mbps"`
	Modes           []string      `json:"modes"`
	Targets         []TargetGroup `json:"targets"`
}

type Region struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type TargetGroup struct {
	Carrier    string      `json:"carrier"`
	Label      string      `json:"label"`
	Candidates []Candidate `json:"candidates"`
}

type Candidate struct {
	ID       string         `json:"id"`
	Address  string         `json:"address"`
	Port     int            `json:"port"`
	Activate ActivationSpec `json:"activate"`
	Download RequestSpec    `json:"download"`
	Upload   RequestSpec    `json:"upload"`
	Release  RequestSpec    `json:"release"`
}

type RequestSpec struct {
	Method           string            `json:"method"`
	URL              string            `json:"url"`
	Headers          map[string]string `json:"headers,omitempty"`
	BodyPrefixBase64 string            `json:"body_prefix_base64,omitempty"`
	ContentLength    int64             `json:"content_length,omitempty"`
}

type ActivationSpec struct {
	RequestSpec
	KeyPrefixBytes   int      `json:"key_prefix_bytes"`
	ReadyDelayMillis int      `json:"ready_delay_ms,omitempty"`
	RejectPrefixes   []string `json:"reject_prefixes,omitempty"`
	RetryPrefixes    []string `json:"retry_prefixes,omitempty"`
}

func loadLease(path string, now time.Time) (Lease, error) {
	file, err := os.Open(path)
	if err != nil {
		return Lease{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLeaseBytes+1))
	if err != nil {
		return Lease{}, err
	}
	if len(data) > maxLeaseBytes {
		return Lease{}, errors.New("lease exceeds 256 KiB")
	}
	var lease Lease
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lease); err != nil {
		return Lease{}, fmt.Errorf("invalid lease JSON: %w", err)
	}
	if err := lease.validate(now); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (lease Lease) validate(now time.Time) error {
	if lease.Version != 1 {
		return fmt.Errorf("unsupported lease version: %d", lease.Version)
	}
	if !validIdentifier(lease.LeaseID, 8, 96) {
		return errors.New("invalid lease id")
	}
	if lease.IssuedAt <= 0 || lease.ExpiresAt <= lease.IssuedAt {
		return errors.New("invalid lease lifetime")
	}
	nowUnix := now.Unix()
	if lease.IssuedAt > nowUnix+int64(maxFutureClockSkew/time.Second) {
		return errors.New("lease issue time is in the future")
	}
	if lease.ExpiresAt <= nowUnix {
		return errors.New("lease has expired")
	}
	if lease.ExpiresAt-lease.IssuedAt > 15*60 {
		return errors.New("lease lifetime is too long")
	}
	if !validIdentifier(lease.Region.Code, 2, 4) || strings.TrimSpace(lease.Region.Name) == "" {
		return errors.New("invalid lease region")
	}
	if lease.Family != "v4" && lease.Family != "v6" {
		return errors.New("invalid address family")
	}
	if lease.DurationSeconds != 5 {
		return errors.New("duration must be 5 seconds")
	}
	if lease.TargetMbps != 100 && lease.TargetMbps != 200 && lease.TargetMbps != 400 {
		return errors.New("target speed must be 100, 200, or 400 Mbps")
	}
	if len(lease.Modes) != 1 || lease.Modes[0] != "s" {
		return errors.New("only single-thread mode is supported")
	}
	if len(lease.Targets) < 1 || len(lease.Targets) > 3 {
		return errors.New("invalid target group count")
	}
	seenCarriers := map[string]bool{}
	for _, target := range lease.Targets {
		if !validCarrier(target.Carrier) || strings.TrimSpace(target.Label) == "" {
			return errors.New("invalid target group")
		}
		if seenCarriers[target.Carrier] {
			return fmt.Errorf("duplicate carrier: %s", target.Carrier)
		}
		seenCarriers[target.Carrier] = true
		if len(target.Candidates) < 1 || len(target.Candidates) > 4 {
			return fmt.Errorf("invalid candidate count for %s", target.Carrier)
		}
		for _, candidate := range target.Candidates {
			if err := candidate.validate(lease.Family); err != nil {
				return fmt.Errorf("candidate %s: %w", candidate.ID, err)
			}
		}
	}
	return nil
}

func validCarrier(value string) bool {
	return value == "ct" || value == "cu" || value == "cm"
}

func (candidate Candidate) validate(family string) error {
	if !validIdentifier(candidate.ID, 8, 96) {
		return errors.New("invalid id")
	}
	ip := net.ParseIP(candidate.Address)
	if ip == nil {
		return errors.New("address must be an IP literal")
	}
	if (family == "v4") != (ip.To4() != nil) {
		return errors.New("address does not match lease family")
	}
	if candidate.Port < 1 || candidate.Port > 65535 {
		return errors.New("invalid port")
	}
	if candidate.Activate.KeyPrefixBytes < 0 || candidate.Activate.KeyPrefixBytes > 32 {
		return errors.New("invalid activation key prefix")
	}
	if candidate.Activate.ReadyDelayMillis < 0 || candidate.Activate.ReadyDelayMillis > 10_000 {
		return errors.New("invalid activation ready delay")
	}
	operations := []struct {
		name string
		spec RequestSpec
	}{
		{"activate", candidate.Activate.RequestSpec},
		{"download", candidate.Download},
		{"upload", candidate.Upload},
		{"release", candidate.Release},
	}
	for _, operation := range operations {
		if err := validateRequestSpec(operation.spec, candidate.Address, candidate.Port); err != nil {
			return fmt.Errorf("%s: %w", operation.name, err)
		}
	}
	if !strings.Contains(candidate.Download.URL, "{key}") || !strings.Contains(candidate.Release.URL, "{key}") {
		return errors.New("download and release operations must use the activation key")
	}
	if candidate.Upload.ContentLength < 1 || candidate.Upload.ContentLength > 2_000_000_000 {
		return errors.New("invalid upload content length")
	}
	if candidate.Upload.BodyPrefixBase64 != "" {
		prefix, err := base64.StdEncoding.DecodeString(candidate.Upload.BodyPrefixBase64)
		if err != nil || len(prefix) > 16*1024 {
			return errors.New("invalid upload body prefix")
		}
	}
	return nil
}

func validateRequestSpec(spec RequestSpec, address string, port int) error {
	method := strings.ToUpper(spec.Method)
	if method != "GET" && method != "POST" {
		return errors.New("method must be GET or POST")
	}
	parsed, err := url.Parse(replaceTemplates(spec.URL, "test-key", "1"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return errors.New("invalid URL")
	}
	if !strings.EqualFold(parsed.Hostname(), address) {
		return errors.New("URL host differs from candidate address")
	}
	actualPort := parsed.Port()
	if actualPort == "" {
		if parsed.Scheme == "https" {
			actualPort = "443"
		} else {
			actualPort = "80"
		}
	}
	if actualPort != strconv.Itoa(port) {
		return errors.New("URL port differs from candidate port")
	}
	if len(spec.Headers) > 24 {
		return errors.New("too many headers")
	}
	for name, value := range spec.Headers {
		if name == "" || strings.ContainsAny(name+value, "\r\n") || len(name) > 80 || len(value) > 2048 {
			return errors.New("invalid header")
		}
	}
	return nil
}

func validIdentifier(value string, minLength, maxLength int) bool {
	if len(value) < minLength || len(value) > maxLength {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func replaceTemplates(value, key, nonce string) string {
	value = strings.ReplaceAll(value, "{key}", url.QueryEscape(key))
	return strings.ReplaceAll(value, "{nonce}", url.QueryEscape(nonce))
}
