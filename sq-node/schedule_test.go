package main

import (
	"testing"
	"time"
)

func TestAvailabilitySupportsDaytimeAndCrossMidnightWindows(t *testing.T) {
	config := defaultConfig()
	config.Timezone = "UTC"
	config.Availability = "08:00-12:00,22:00-02:00"
	for _, item := range []struct {
		hour      int
		available bool
	}{{7, false}, {8, true}, {12, false}, {23, true}, {1, true}, {3, false}} {
		now := time.Date(2026, 9, 29, item.hour, 0, 0, 0, time.UTC)
		if actual := availableAt(config, now); actual != item.available {
			t.Fatalf("hour=%d available=%t want=%t", item.hour, actual, item.available)
		}
	}
	config.Availability = "08:00-08:00"
	if err := config.validate(false); err == nil {
		t.Fatal("equal schedule endpoints were accepted")
	}
}

func TestConservativeNodeDefaults(t *testing.T) {
	config := defaultConfig()
	if config.MaxMbps != 100 || config.MaxConcurrency != 1 || config.ReserveConcurrency != 0 ||
		config.DailyPublicBytes != 10_000_000_000 || config.AccessMode != "public" ||
		config.UpdateMode != "automatic" || config.UpdateChannel != "stable" {
		t.Fatalf("unexpected defaults: %+v", config)
	}
}
