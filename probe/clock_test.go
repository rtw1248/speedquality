package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReferenceClockValidatesLeasesDespiteHostClockSkew(t *testing.T) {
	for _, offset := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
		platformTime := time.Now().Add(offset)
		lease := validTestLease(platformTime)
		if err := lease.validate(time.Now()); err == nil {
			t.Fatal("fixture must be invalid against the host clock")
		}
		clock, err := newMeasurementClock(platformTime.Unix())
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.validate(clock.Now()); err != nil {
			t.Fatalf("valid lease rejected with platform reference: %v", err)
		}
		// A cached platform timestamp must keep advancing so it cannot keep a
		// lease alive after its real expiry.
		clock.started = clock.started.Add(-2 * time.Minute)
		if err := lease.validate(clock.Now()); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired lease accepted with platform reference: %v", err)
		}
	}
}

func TestReportTimestampsUseReferenceClock(t *testing.T) {
	const referenceEpoch int64 = 2_000_000_000
	clock, err := newMeasurementClock(referenceEpoch)
	if err != nil {
		t.Fatal(err)
	}
	clock.started = clock.started.Add(-10 * time.Second)
	// No targets: verify timestamps without transferring data to any node.
	report := runLease(context.Background(), Lease{}, nil, clock.Now)
	if report.StartedAt < referenceEpoch+10 || report.CompletedAt < report.StartedAt ||
		report.CompletedAt > referenceEpoch+15 {
		t.Fatalf("report timestamps did not follow platform time: %d / %d", report.StartedAt, report.CompletedAt)
	}
}

func TestReferenceClockRejectsMalformedEpoch(t *testing.T) {
	for _, epoch := range []int64{-1, 1, 2_000_000_000_000} {
		if _, err := newMeasurementClock(epoch); err == nil {
			t.Fatalf("invalid reference time accepted: %d", epoch)
		}
	}
}
