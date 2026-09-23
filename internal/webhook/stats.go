// Stats is the gateway's in-memory operational counter set (FR-003). It is
// lock-free (sync/atomic) so the worker pool can increment it without
// contention, and holds counts + a triage-latency summary only — never PR
// content. It resets on restart (no persistence); the stats endpoint that
// exposes it needs no secret and reveals nothing about any PR.
package webhook

import (
	"sync/atomic"
	"time"
)

// Stats holds the lifetime counters and a running triage-latency summary.
// Counter meaning:
//   - Received: a delivery was accepted and enqueued for triage.
//   - Triaged:  the model returned a verdict (Flagged + Skipped).
//   - Flagged:  verdict was slop above threshold → label + comment written.
//   - Skipped:  verdict was not actionable (legit, or below threshold).
//   - Failed:   the pipeline errored (diff fetch or model call) → fail-open.
type Stats struct {
	received atomic.Int64
	triaged  atomic.Int64
	flagged  atomic.Int64
	skipped  atomic.Int64
	failed   atomic.Int64

	// Triage-latency summary in nanoseconds: a sum + count give the mean, and
	// a monotonic max gives the worst case. atomically maintained.
	latencySumNanos atomic.Int64
	latencyCount    atomic.Int64
	latencyMaxNanos atomic.Int64
}

func (s *Stats) incReceived() { s.received.Add(1) }
func (s *Stats) incFlagged()  { s.triaged.Add(1); s.flagged.Add(1) }
func (s *Stats) incSkipped()  { s.triaged.Add(1); s.skipped.Add(1) }
func (s *Stats) incFailed()   { s.failed.Add(1) }

// observeLatency records one triage duration into the latency summary.
func (s *Stats) observeLatency(d time.Duration) {
	n := d.Nanoseconds()
	s.latencySumNanos.Add(n)
	s.latencyCount.Add(1)
	// Compare-and-swap loop to keep a lock-free running max.
	for {
		cur := s.latencyMaxNanos.Load()
		if n <= cur || s.latencyMaxNanos.CompareAndSwap(cur, n) {
			return
		}
	}
}

// Snapshot is the JSON shape returned by the /stats endpoint.
type Snapshot struct {
	Received      int64   `json:"received"`
	Triaged       int64   `json:"triaged"`
	Flagged       int64   `json:"flagged"`
	Skipped       int64   `json:"skipped"`
	Failed        int64   `json:"failed"`
	TriageMeanMS  float64 `json:"triage_mean_ms"`
	TriageMaxMS   float64 `json:"triage_max_ms"`
	TriageSamples int64   `json:"triage_samples"`
}

// Snapshot reads the counters into a consistent-enough point-in-time view.
// Counters are read independently (not under one lock), so a snapshot taken
// mid-burst may be momentarily off by one across fields; that is acceptable for
// an operational gauge and avoids serializing the hot path.
func (s *Stats) Snapshot() Snapshot {
	count := s.latencyCount.Load()
	var meanMS float64
	if count > 0 {
		meanMS = float64(s.latencySumNanos.Load()) / float64(count) / 1e6
	}
	return Snapshot{
		Received:      s.received.Load(),
		Triaged:       s.triaged.Load(),
		Flagged:       s.flagged.Load(),
		Skipped:       s.skipped.Load(),
		Failed:        s.failed.Load(),
		TriageMeanMS:  meanMS,
		TriageMaxMS:   float64(s.latencyMaxNanos.Load()) / 1e6,
		TriageSamples: count,
	}
}
