package main

import (
	"fmt"
	"testing"
	"time"
)

func TestRequestProtectorBoundsSourceBuckets(t *testing.T) {
	protector := newRequestProtector(defaultConfig())
	now := time.Now()
	for index := 0; index < 4096; index++ {
		if !protector.allow(fmt.Sprintf("source-%d", index), 1, now) {
			t.Fatalf("bucket %d was rejected before the limit", index)
		}
	}
	if protector.allow("source-overflow", 1, now) {
		t.Fatal("new source was accepted after the bucket limit")
	}
	if protector.allow("source-0", 1, now) {
		t.Fatal("existing source exceeded its request limit")
	}
	if !protector.allow("source-after-window", 1, now.Add(rateWindow)) {
		t.Fatal("expired buckets were not pruned")
	}
	if len(protector.buckets) != 1 {
		t.Fatalf("bucket count after pruning=%d", len(protector.buckets))
	}
}
