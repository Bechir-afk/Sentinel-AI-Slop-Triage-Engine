package webhook

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestLedgerClaimOnce(t *testing.T) {
	l := NewLedger(16)
	if !l.Claim("d1") {
		t.Fatal("first Claim should be new")
	}
	if l.Claim("d1") {
		t.Error("second Claim of same id should be a duplicate")
	}
	if !l.Claim("d2") {
		t.Error("a distinct id should be new")
	}
}

func TestLedgerEviction(t *testing.T) {
	l := NewLedger(2)
	if !l.Claim("a") || !l.Claim("b") {
		t.Fatal("a and b should both be new")
	}
	if !l.Claim("c") {
		t.Fatal("c should be new (evicts oldest, a)")
	}
	// After c: seen={b,c}, a evicted. Check b is still present FIRST — a
	// duplicate Claim returns early without mutating the ring.
	if l.Claim("b") {
		t.Error("b should still be a duplicate (not yet evicted)")
	}
	// a was evicted → claimable again (this re-add evicts b, which is fine now).
	if !l.Claim("a") {
		t.Error("a should be new again after eviction")
	}
}

func TestLedgerConcurrentClaimExactlyOnce(t *testing.T) {
	l := NewLedger(64)
	const goroutines = 100
	var wins int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // line them up so they race on the same id
			if l.Claim("same") {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Errorf("exactly one Claim should win, got %d", wins)
	}
}

func TestLedgerZeroCapacityIsUsable(t *testing.T) {
	l := NewLedger(0) // clamped to 1
	if !l.Claim("x") {
		t.Fatal("first Claim should be new")
	}
	if l.Claim("x") {
		t.Error("immediate re-Claim should be a duplicate")
	}
	// Capacity 1: any new id evicts the previous.
	if !l.Claim("y") {
		t.Error("y should be new")
	}
	if !l.Claim("x") {
		t.Error("x should be claimable again after y evicts it")
	}
}
