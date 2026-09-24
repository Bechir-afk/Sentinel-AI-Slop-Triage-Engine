package webhook

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
)

// feedbackBuffer bounds the async signal queue. Maintainer disagreements are
// low-volume (human actions), so a small buffer absorbs any burst; a full
// buffer drops the signal rather than blocking the request path (fail-open).
const feedbackBuffer = 64

// Signal is one maintainer-disagreement record (FR-015). It is the ONLY thing
// Sentinel persists, and it lives out-of-band: written to an append-only log
// off the request path and read only by the offline ml/ pipeline (FR-016).
type Signal struct {
	PR               string  `json:"pr"`                    // owner/repo#number
	OriginalVerdict  string  `json:"original_verdict"`      // "slop" — the label we'd applied was removed
	Confidence       float64 `json:"confidence,omitempty"`  // absent when unknown; a stateless gateway can't recover it
	DisagreementType string  `json:"disagreement_type"`     // "label-removed"
	TS               string  `json:"ts"`                    // RFC3339 UTC
}

// FeedbackLog appends Signals to a file as JSON lines (JSONL), one row per
// event. Writes happen on a single background goroutine draining a buffered
// channel, so the request path never touches the file — it does a non-blocking
// hand-off and acks immediately. This keeps the serving path stateless: the log
// is a file artifact, not in-memory per-PR state (FR-015).
//
// ponytail: append-only local file, no rotation or fsync-per-write. The ceiling
// is a single-host log that grows unbounded; the upgrade path is log rotation or
// shipping rows to a durable store if the loop ever outgrows one box.
type FeedbackLog struct {
	path      string
	ch        chan Signal
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewFeedbackLog returns a log that will append to path. Call Start before
// Record, and Close to drain on shutdown.
func NewFeedbackLog(path string) *FeedbackLog {
	return &FeedbackLog{
		path: path,
		ch:   make(chan Signal, feedbackBuffer),
		done: make(chan struct{}),
	}
}

// Start launches the background writer (idempotent).
func (f *FeedbackLog) Start() {
	f.startOnce.Do(func() { go f.run() })
}

// run opens the log once and appends each Signal as a JSON line until the
// channel is closed. If the file can't be opened it logs and drains the channel
// so senders never block (fail-open: a broken feedback log must not wedge the
// gateway).
func (f *FeedbackLog) run() {
	defer close(f.done)

	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("feedback log open failed; disabling capture", "path", f.path, "err", err)
		for range f.ch { // keep draining so Record's non-blocking send still works
		}
		return
	}
	defer file.Close()

	enc := json.NewEncoder(file) // Encode writes one object + newline → JSONL
	for s := range f.ch {
		if err := enc.Encode(s); err != nil {
			slog.Error("feedback log write failed", "path", f.path, "pr", s.PR, "err", err)
		}
	}
}

// Record hands a Signal to the background writer without blocking. It reports
// false if the buffer is full (the signal is dropped, fail-open) — feedback is
// best-effort and must never hold the request path open.
func (f *FeedbackLog) Record(s Signal) bool {
	select {
	case f.ch <- s:
		return true
	default:
		return false
	}
}

// Close stops the writer and waits for buffered signals to flush (idempotent).
func (f *FeedbackLog) Close() {
	f.stopOnce.Do(func() { close(f.ch) })
	<-f.done
}
