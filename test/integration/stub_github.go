// Entity: GitHub Stub (003 spec Key Entities) — a recording httptest server
// standing in for the GitHub REST API.
//
// It records the *bytes* the gateway actually sends (method, path, parsed body)
// for every outward write — label add, comment, Check Run — and the diff fetch,
// so scenarios assert on real request payloads, not on a fake's method calls
// (T003). It returns a canned diff for the fetch, exposes per-call-type counts
// and last-payload accessors, and a per-endpoint 5xx injector for the drills.
//
// Log hygiene (FR-014): the recorder holds parsed request bodies in memory for
// test assertions only; it never logs them, and the canned diff it returns is a
// fixed constant, never echoed to stdout.
package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
)

// cannedDiff is what GET pulls/{n} returns — a fixed, non-secret unified diff.
// The gateway forwards (title, diff) to the model; the stub model ignores it.
const cannedDiff = `diff --git a/foo.txt b/foo.txt
index 0000000..1111111 100644
--- a/foo.txt
+++ b/foo.txt
@@ -1 +1 @@
-old
+new
`

// recordedCall is one captured request: what the gateway sent, in bytes.
type recordedCall struct {
	Method string
	Path   string
	Body   map[string]any // parsed JSON body (nil for the diff GET)
}

// endpoint classifies a recorded call so accessors can count by type.
type endpoint int

const (
	epLabel endpoint = iota
	epComment
	epCheckRun
	epDiff
)

// Path shapes from internal/github/github.go. owner/repo are PathEscape'd but
// match [^/]+ for our fixtures; {n} is digits.
var (
	reDiff     = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/\d+$`)
	reLabel    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/\d+/labels$`)
	reComment  = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/\d+/comments$`)
	reCheckRun = regexp.MustCompile(`^/repos/[^/]+/[^/]+/check-runs$`)
)

// GitHubStub records every call the gateway makes and can inject failures.
type GitHubStub struct {
	Server *httptest.Server

	mu      sync.Mutex
	calls   []recordedCall
	inject  map[endpoint]int // endpoint -> status code to force (>=400); absent = normal
}

// NewGitHubStub starts a recording GitHub API stub.
func NewGitHubStub() *GitHubStub {
	g := &GitHubStub{inject: map[endpoint]int{}}
	g.Server = httptest.NewServer(http.HandlerFunc(g.route))
	return g
}

func (g *GitHubStub) route(w http.ResponseWriter, r *http.Request) {
	ep, ok := classify(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Diff fetch: record the GET, return the canned diff (or injected failure).
	if ep == epDiff {
		g.record(recordedCall{Method: r.Method, Path: r.URL.Path})
		if code := g.injected(epDiff); code >= 400 {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github.v3.diff")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, cannedDiff)
		return
	}

	// Outward writes: parse and record the exact body, then honor any injection.
	var body map[string]any
	if b, err := io.ReadAll(r.Body); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	g.record(recordedCall{Method: r.Method, Path: r.URL.Path, Body: body})

	if code := g.injected(ep); code >= 400 {
		w.WriteHeader(code)
		return
	}
	// A 201 with a small JSON object is enough for the gateway's client.
	writeJSON(w, http.StatusCreated, map[string]any{"id": 1})
}

func classify(r *http.Request) (endpoint, bool) {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && reDiff.MatchString(p):
		return epDiff, true
	case r.Method == http.MethodPost && reLabel.MatchString(p):
		return epLabel, true
	case r.Method == http.MethodPost && reComment.MatchString(p):
		return epComment, true
	case r.Method == http.MethodPost && reCheckRun.MatchString(p):
		return epCheckRun, true
	}
	return 0, false
}

func (g *GitHubStub) record(c recordedCall) {
	g.mu.Lock()
	g.calls = append(g.calls, c)
	g.mu.Unlock()
}

func (g *GitHubStub) injected(ep endpoint) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inject[ep]
}

// URL is the base the gateway's GITHUB_API_BASE points at.
func (g *GitHubStub) URL() string { return g.Server.URL }

// Close shuts the stub down (deferred by the harness).
func (g *GitHubStub) Close() { g.Server.Close() }

// --- accessors (counts + last payload per call type) ---

func (g *GitHubStub) countBy(ep endpoint) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if e, ok := classifyRecorded(c); ok && e == ep {
			n++
		}
	}
	return n
}

func (g *GitHubStub) lastBody(ep endpoint) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := len(g.calls) - 1; i >= 0; i-- {
		if e, ok := classifyRecorded(g.calls[i]); ok && e == ep {
			return g.calls[i].Body
		}
	}
	return nil
}

// classifyRecorded re-derives the endpoint of a stored call (method + path).
func classifyRecorded(c recordedCall) (endpoint, bool) {
	switch {
	case c.Method == http.MethodGet && reDiff.MatchString(c.Path):
		return epDiff, true
	case c.Method == http.MethodPost && reLabel.MatchString(c.Path):
		return epLabel, true
	case c.Method == http.MethodPost && reComment.MatchString(c.Path):
		return epComment, true
	case c.Method == http.MethodPost && reCheckRun.MatchString(c.Path):
		return epCheckRun, true
	}
	return 0, false
}

// LabelCalls / CommentCalls / CheckRunCalls / DiffCalls — the observable
// signals scenarios wait on and assert exact counts against.
func (g *GitHubStub) LabelCalls() int    { return g.countBy(epLabel) }
func (g *GitHubStub) CommentCalls() int  { return g.countBy(epComment) }
func (g *GitHubStub) CheckRunCalls() int { return g.countBy(epCheckRun) }
func (g *GitHubStub) DiffCalls() int     { return g.countBy(epDiff) }

// WriteCalls is the total outward mutations (label+comment+check-run) — the
// "zero writes" assertion for shadow mode and fail-open reads this.
func (g *GitHubStub) WriteCalls() int {
	return g.LabelCalls() + g.CommentCalls() + g.CheckRunCalls()
}

// LastLabelBody / LastCommentBody / LastCheckRunBody return the parsed JSON the
// gateway last POSTed to each endpoint (confidence + version assertions).
func (g *GitHubStub) LastLabelBody() map[string]any    { return g.lastBody(epLabel) }
func (g *GitHubStub) LastCommentBody() map[string]any  { return g.lastBody(epComment) }
func (g *GitHubStub) LastCheckRunBody() map[string]any { return g.lastBody(epCheckRun) }

// --- injectors (drills) ---

// InjectLabelStatus / InjectCommentStatus / InjectCheckRunStatus / InjectDiffStatus
// force the given status (>=400) on that endpoint; pass 0 to clear.
func (g *GitHubStub) InjectLabelStatus(code int)    { g.setInject(epLabel, code) }
func (g *GitHubStub) InjectCommentStatus(code int)  { g.setInject(epComment, code) }
func (g *GitHubStub) InjectCheckRunStatus(code int) { g.setInject(epCheckRun, code) }
func (g *GitHubStub) InjectDiffStatus(code int)     { g.setInject(epDiff, code) }

func (g *GitHubStub) setInject(ep endpoint, code int) {
	g.mu.Lock()
	if code == 0 {
		delete(g.inject, ep)
	} else {
		g.inject[ep] = code
	}
	g.mu.Unlock()
}
