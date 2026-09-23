package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchDiff(t *testing.T) {
	const wantDiff = "diff --git a/x b/x\n+hello\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/repo/pulls/7" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github.v3.diff" {
			t.Errorf("Accept = %s", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %s", got)
		}
		io.WriteString(w, wantDiff)
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 0)
	got, err := c.FetchDiff(context.Background(), "octo", "repo", 7)
	if err != nil {
		t.Fatalf("FetchDiff: %v", err)
	}
	if got != wantDiff {
		t.Errorf("diff = %q, want %q", got, wantDiff)
	}
}

func TestFetchDiffError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 0)
	if _, err := c.FetchDiff(context.Background(), "octo", "repo", 7); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func TestAddLabel(t *testing.T) {
	var gotBody map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/repo/issues/7/labels" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 0)
	if err := c.AddLabel(context.Background(), "octo", "repo", 7, "needs-human-review"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if len(gotBody["labels"]) != 1 || gotBody["labels"][0] != "needs-human-review" {
		t.Errorf("labels payload = %v", gotBody)
	}
}

func TestPostComment(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/repo/issues/7/comments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 0)
	if err := c.PostComment(context.Background(), "octo", "repo", 7, "hi"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotBody["body"] != "hi" {
		t.Errorf("comment body = %q", gotBody["body"])
	}
}

// FR-010: owner/repo come from the webhook payload (a trust boundary). A
// slash-bearing owner must be path-escaped, not able to reshape the request path.
func TestFetchDiffEscapesOwnerRepo(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		io.WriteString(w, "diff")
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 0)
	if _, err := c.FetchDiff(context.Background(), "octo/../evil", "re po", 7); err != nil {
		t.Fatalf("FetchDiff: %v", err)
	}
	// The "/" and space must be percent-encoded so the segments stay one path
	// segment each — no traversal, no injected path.
	if want := "/repos/octo%2F..%2Fevil/re%20po/pulls/7"; gotPath != want {
		t.Errorf("escaped path = %q, want %q", gotPath, want)
	}
}

// SC-007: two 503s then a 200 → the retry recovers and the diff comes back.
func TestFetchDiffRetriesThenSucceeds(t *testing.T) {
	const wantDiff = "diff --git a/x b/x\n+ok\n"
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, wantDiff)
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 2)
	c.http.Timeout = 2 * time.Second
	got, err := c.FetchDiff(context.Background(), "octo", "repo", 7)
	if err != nil {
		t.Fatalf("FetchDiff after 2×503: %v", err)
	}
	if got != wantDiff {
		t.Errorf("diff = %q, want %q", got, wantDiff)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("server calls = %d, want 3 (2 fail + 1 success)", n)
	}
}

// SC-007: 503 for the whole budget → the error is returned so the caller (the
// webhook worker) fails open. Retries are exhausted, never infinite.
func TestFetchDiffFailsOpenAfterExhausting(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 2)
	c.http.Timeout = 2 * time.Second
	if _, err := c.FetchDiff(context.Background(), "octo", "repo", 7); err == nil {
		t.Fatal("expected error after retries exhausted, got nil")
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("server calls = %d, want 3 (initial + 2 retries)", n)
	}
}

// FR-009: a backoff that would outlast the remaining triage budget short-circuits
// — the client returns promptly instead of sleeping past the deadline.
func TestRetryNeverSleepsPastDeadline(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Budget far smaller than baseBackoff (500ms): the first retry's wait can't
	// fit, so doRetrying must give up after the initial attempt.
	c := New("tok", srv.URL, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.FetchDiff(ctx, "octo", "repo", 7); err == nil {
		t.Fatal("expected error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("took %v; should short-circuit rather than sleep a full backoff", elapsed)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("server calls = %d, want 1 (no retry fits the budget)", n)
	}
}

// FR-009: Retry-After is honored — a 429 with a sub-budget Retry-After retries,
// and the client waits at least that long.
func TestRetryHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		json.NewDecoder(r.Body).Decode(&map[string][]string{})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("tok", srv.URL, 2)
	c.http.Timeout = 3 * time.Second
	start := time.Now()
	if err := c.AddLabel(context.Background(), "octo", "repo", 7, "x"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 1*time.Second {
		t.Errorf("waited %v; should honor Retry-After: 1s", elapsed)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("server calls = %d, want 2", n)
	}
}
