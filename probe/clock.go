package main

import (
	"errors"
	"time"
)

// The runner supplies the HTTPS platform time advanced by Linux uptime. Once a
// probe starts, elapsed time comes from Go's monotonic clock, not the host clock.
type measurementClock struct {
	reference time.Time
	started   time.Time
}

func newMeasurementClock(referenceEpoch int64) (measurementClock, error) {
	if referenceEpoch != 0 && (referenceEpoch < 1_000_000_000 || referenceEpoch > 9_999_999_999) {
		return measurementClock{}, errors.New("reference time must be Unix seconds")
	}
	started := time.Now()
	reference := started
	if referenceEpoch != 0 {
		reference = time.Unix(referenceEpoch, 0)
	}
	return measurementClock{reference: reference, started: started}, nil
}

func (clock measurementClock) Now() time.Time {
	return clock.reference.Add(time.Since(clock.started))
}
