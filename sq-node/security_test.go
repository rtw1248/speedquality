package main

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestSecurityPreflightParsers(t *testing.T) {
	settings := parseSSHDSettings("PermitRootLogin yes\nPasswordAuthentication yes\n")
	if settings["permitrootlogin"] != "yes" || settings["passwordauthentication"] != "yes" {
		t.Fatalf("unexpected ssh settings: %#v", settings)
	}
	ports := exposedHighRiskPorts(
		"LISTEN 0 4096 0.0.0.0:2375 0.0.0.0:*\n" +
			"LISTEN 0 128 127.0.0.1:6379 0.0.0.0:*\n" +
			"LISTEN 0 128 [::]:23 [::]:*\n",
	)
	if !reflect.DeepEqual(ports, []int{2375, 23}) {
		t.Fatalf("unexpected exposed ports: %#v", ports)
	}
}

func TestSecurityPreflightTreatsPasswordLoginAsSevere(t *testing.T) {
	previousLookPath := securityLookPath
	previousCommand := securityCommand
	securityLookPath = func(string) (string, error) { return "/usr/sbin/nft", nil }
	securityCommand = func(name string, arguments ...string) ([]byte, error) {
		switch name {
		case "nft":
			return []byte("table inet filter {}"), nil
		case "sshd":
			return []byte("permitrootlogin no\npasswordauthentication yes\nkbdinteractiveauthentication no\n"), nil
		case "ss":
			return nil, nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
	t.Cleanup(func() {
		securityLookPath = previousLookPath
		securityCommand = previousCommand
	})

	config := defaultConfig()
	config.Port = 51234
	report := runSecurityPreflight(config)
	for _, finding := range report.Findings {
		if finding.Code == "ssh_public_password" && finding.Severity == "severe" {
			return
		}
	}
	t.Fatalf("password login was not severe: %#v", report.Findings)
}

func TestSecurityPreflightMissingNFTIsSevere(t *testing.T) {
	previousLookPath := securityLookPath
	previousCommand := securityCommand
	securityLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	securityCommand = func(name string, arguments ...string) ([]byte, error) {
		if name == "sshd" {
			return []byte("permitrootlogin no\npasswordauthentication no\nkbdinteractiveauthentication no\n"), nil
		}
		if name == "ss" {
			return nil, nil
		}
		return nil, errors.New("unexpected command: " + strings.Join(append([]string{name}, arguments...), " "))
	}
	t.Cleanup(func() {
		securityLookPath = previousLookPath
		securityCommand = previousCommand
	})

	config := defaultConfig()
	config.Port = 51234
	if report := runSecurityPreflight(config); !report.severe() {
		t.Fatalf("missing nft was not severe: %#v", report.Findings)
	}
}
