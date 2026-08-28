package main

import (
	"testing"
	"time"
)

// TestMintScheduleKeepsNominalRate pins the property the flood journey depends
// on: whatever rate the demo advertises, the (batch, interval) pair actually
// delivers it. The bug this replaces used a 166µs tick for the 6,000 lines/s
// flood rate, which no scheduler keeps, so the producer never outran the frame
// and nothing was ever dropped.
func TestMintScheduleKeepsNominalRate(t *testing.T) {
	for _, rate := range []int{1, 5, 50, 199, 200, 1000, 6000, 100_000} {
		batch, interval := mintSchedule(rate)
		if interval < 5*time.Millisecond && batch != 1 {
			t.Errorf("rate=%d: interval %v is below the 5ms floor", rate, interval)
		}
		if batch < 1 {
			t.Errorf("rate=%d: batch=%d, must mint at least one line per tick", rate, batch)
		}
		// Achieved rate must be within 10% of nominal, in both directions.
		achieved := float64(batch) * float64(time.Second) / float64(interval)
		if achieved < float64(rate)*0.9 || achieved > float64(rate)*1.1 {
			t.Errorf("rate=%d: schedule delivers %.0f lines/s (batch=%d interval=%v), want within 10%%",
				rate, achieved, batch, interval)
		}
	}
}

// TestMintScheduleHandlesNonsenseRates keeps a zero or negative rate from
// dividing by zero in the source loop.
func TestMintScheduleHandlesNonsenseRates(t *testing.T) {
	for _, rate := range []int{0, -1, -6000} {
		batch, interval := mintSchedule(rate)
		if batch < 1 || interval <= 0 {
			t.Errorf("rate=%d: got batch=%d interval=%v, want a usable schedule", rate, batch, interval)
		}
	}
}
