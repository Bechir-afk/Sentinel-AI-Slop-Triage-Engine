package webhook

import "sync"

// Ledger is a bounded, in-memory set of recently seen GitHub delivery IDs. It
// makes webhook processing idempotent across redeliveries: PostComment is not
// idempotent GitHub-side, so a replayed delivery would otherwise post a second
// comment. Oldest entries are evicted first when capacity is reached.
//
// ponytail: process-local only (map + ring, no persistence). A gateway restart
// forgets, so a redelivery arriving after a restart can be reprocessed once.
// The spec accepts this ceiling (no serving-path database); a persistent store
// is the upgrade path if cross-restart dedup is ever required.
type Ledger struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	ring  []string // insertion order, for oldest-first eviction
	pos   int
	count int
}

// NewLedger returns a Ledger holding at most capacity IDs (minimum 1).
func NewLedger(capacity int) *Ledger {
	if capacity < 1 {
		capacity = 1
	}
	return &Ledger{
		seen: make(map[string]struct{}, capacity),
		ring: make([]string, capacity),
	}
}

// Claim records id and reports whether the claim is new. The first Claim for an
// id returns true; later Claims return false until the id is evicted. It is
// safe for concurrent use: for a burst of Claims with the same id, exactly one
// returns true.
func (l *Ledger) Claim(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.seen[id]; ok {
		return false
	}
	if l.count == len(l.ring) {
		delete(l.seen, l.ring[l.pos]) // full: evict the oldest slot
	} else {
		l.count++
	}
	l.ring[l.pos] = id
	l.pos = (l.pos + 1) % len(l.ring)
	l.seen[id] = struct{}{}
	return true
}
