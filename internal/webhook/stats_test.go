package webhook

import (
	"sync"
	"testing"
	"time"
)

// TestStatsZeroSnapshot: a fresh Stats reports all zeros and no divide-by-zero
// on the mean (edge case: /stats queried before any delivery).
func TestStatsZeroSnapshot(t *testing.T) {
	var s Stats
	got := s.Snapshot()
	if got != (Snapshot{}) {
		t.Fatalf("zero snapshot = %+v, want all-zero", got)
	}
}

// TestStatsCounters: flagged/skipped both advance Triaged; Failed is separate.
func TestStatsCounters(t *testing.T) {
	var s Stats
	s.incReceived()
	s.incReceived()
	s.incFlagged() // triaged + flagged
	s.incSkipped() // triaged + skipped
	s.incFailed()

	got := s.Snapshot()
	if got.Received != 2 {
		t.Errorf("Received = %d, want 2", got.Received)
	}
	if got.Triaged != 2 {
		t.Errorf("Triaged = %d, want 2 (flagged+skipped)", got.Triaged)
	}
	if got.Flagged != 1 {
		t.Errorf("Flagged = %d, want 1", got.Flagged)
	}
	if got.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", got.Skipped)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d, want 1", got.Failed)
	}
}

// TestStatsLatency: mean is the arithmetic mean of observed durations and max
// tracks the worst case.
func TestStatsLatency(t *testing.T) {
	var s Stats
	s.observeLatency(10 * time.Millisecond)
	s.observeLatency(30 * time.Millisecond)

	got := s.Snapshot()
	if got.TriageSamples != 2 {
		t.Fatalf("TriageSamples = %d, want 2", got.TriageSamples)
	}
	if got.TriageMeanMS != 20 {
		t.Errorf("TriageMeanMS = %v, want 20", got.TriageMeanMS)
	}
	if got.TriageMaxMS != 30 {
		t.Errorf("TriageMaxMS = %v, want 30", got.TriageMaxMS)
	}
}

// TestStatsConcurrent: the atomic counters are race-free under concurrent
// increment (run with -race). Each of N goroutines records one of each.
func TestStatsConcurrent(t *testing.T) {
	var s Stats
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.incReceived()
			s.incFlagged()
			s.observeLatency(time.Millisecond)
		}()
	}
	wg.Wait()

	got := s.Snapshot()
	if got.Received != n || got.Flagged != n || got.Triaged != n {
		t.Errorf("after %d concurrent ops: received=%d flagged=%d triaged=%d, want %d each",
			n, got.Received, got.Flagged, got.Triaged, n)
	}
	if got.TriageSamples != n {
		t.Errorf("TriageSamples = %d, want %d", got.TriageSamples, n)
	}
}
