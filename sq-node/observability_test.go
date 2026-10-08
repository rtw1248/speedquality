package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSafeLogErrorRedactsSensitiveValues(t *testing.T) {
	input := errors.New(
		"POST https://192.0.2.10/activate?key=secret from 2001:db8::1 " +
			"sqn_abcdefghijklmnopqrstuvwx sqa_abcdefghijklmnopqrstuvwxyz123456 " +
			"eyJabc.def.ghi",
	)
	output := safeLogError(input)
	for _, secret := range []string{
		"192.0.2.10", "2001:db8", "sqn_", "sqa_", "eyJabc", "secret",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("log output leaked %q: %q", secret, output)
		}
	}
}

func TestMonitoredMutexReportsStalledHolder(t *testing.T) {
	var mutex monitoredMutex
	mutex.Lock()
	held, report := mutex.stalled(time.Now().Add(3 * time.Second))
	mutex.Unlock()
	if !report || held < stalledLock {
		t.Fatalf("held=%v report=%t", held, report)
	}
}
