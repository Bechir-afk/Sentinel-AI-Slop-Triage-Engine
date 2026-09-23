package webhook

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/triage"
)

// fakeGitHub records label/comment calls (concurrency-safe: workers run in
// their own goroutines) and lets tests inject a diff error.
type fakeGitHub struct {
	mu          sync.Mutex
	diff        string
	diffErr     error
	labelCount  int
	commentCnt  int
	labelErr    error
	commentText string
}

func (f *fakeGitHub) FetchDiff(_ context.Context, _, _ string, _ int) (string, error) {
	return f.diff, f.diffErr
}
func (f *fakeGitHub) AddLabel(_ context.Context, _, _ string, _ int, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labelCount++
	return f.labelErr
}
func (f *fakeGitHub) PostComment(_ context.Context, _, _ string, _ int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commentCnt++
	f.commentText = body
	return nil
}
func (f *fakeGitHub) labels() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.labelCount
}
func (f *fakeGitHub) comments() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commentCnt
}
func (f *fakeGitHub) comment() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commentText
}

// fakeTriager returns a fixed verdict or error.
type fakeTriager struct {
	res *triage.Result
	err error
}

func (f *fakeTriager) Analyze(_ context.Context, _, _ string) (*triage.Result, error) {
	return f.res, f.err
}

// blockingTriager blocks in Analyze until release is closed, so a test can
// measure ack latency while triage is still stuck.
type blockingTriager struct{ release chan struct{} }

func (b *blockingTriager) Analyze(ctx context.Context, _, _ string) (*triage.Result, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &triage.Result{}, nil
}

func testConfig() config.Config {
	return config.Config{ConfidenceThreshold: 0.90, SlopLabel: "needs-human-review", WorkerCount: 2}
}

const openedPayload = `{
	"action":"opened","number":7,
	"pull_request":{"title":"Add feature","user":{"login":"dev"}},
	"repository":{"owner":{"login":"octo"},"name":"repo"}
}`

// newStarted builds a handler with its worker pool running.
func newStarted(cfg config.Config, gh GitHub, tr Triager) *Handler {
	h := New(cfg, gh, tr)
	h.Start()
	return h
}

// drain shuts the pool down and waits for in-flight jobs, so post-drain
// assertions see the completed pipeline. Fails the test if drain times out.
func drain(t *testing.T, h *Handler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

// postHeaders posts body with the given event type and delivery ID.
func postHeaders(t *testing.T, h *Handler, event, delivery, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// post is the common case: a pull_request event with a fixed delivery ID.
func post(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	return postHeaders(t, h, "pull_request", "test-delivery-1", body)
}

func TestSlopAboveThresholdIsFlagged(t *testing.T) {
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.95, Reason: "boilerplate"}}
	h := newStarted(testConfig(), gh, tr)

	rec := post(t, h, openedPayload)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	drain(t, h)

	if gh.labels() != 1 || gh.comments() != 1 {
		t.Errorf("expected 1 label+1 comment; labels=%d comments=%d", gh.labels(), gh.comments())
	}
	if !strings.Contains(gh.comment(), "boilerplate") {
		t.Errorf("comment missing reason: %q", gh.comment())
	}
}

func TestSlopBelowThresholdNoAction(t *testing.T) {
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.80}}
	h := newStarted(testConfig(), gh, tr)

	post(t, h, openedPayload)
	drain(t, h)

	if gh.labels() > 0 || gh.comments() > 0 {
		t.Error("low-confidence slop should not trigger action")
	}
}

func TestNotSlopNoAction(t *testing.T) {
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: false, Confidence: 0.99}}
	h := newStarted(testConfig(), gh, tr)

	post(t, h, openedPayload)
	drain(t, h)

	if gh.labels() > 0 || gh.comments() > 0 {
		t.Error("legitimate PR should not trigger action")
	}
}

func TestNonActionableActionIgnored(t *testing.T) {
	gh := &fakeGitHub{diff: "d"}
	tr := &fakeTriager{res: &triage.Result{}}
	h := newStarted(testConfig(), gh, tr)

	body := strings.Replace(openedPayload, `"action":"opened"`, `"action":"closed"`, 1)
	post(t, h, body)
	drain(t, h)

	if gh.labels() > 0 || gh.comments() > 0 {
		t.Error("closed action should be ignored")
	}
}

func TestDiffFetchErrorFailsOpen(t *testing.T) {
	gh := &fakeGitHub{diffErr: errors.New("boom")}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.99}}
	h := newStarted(testConfig(), gh, tr)

	rec := post(t, h, openedPayload)
	if rec.Code != http.StatusOK {
		t.Errorf("fail-open should still return 200, got %d", rec.Code)
	}
	drain(t, h)

	if gh.labels() > 0 || gh.comments() > 0 {
		t.Error("no action expected when diff fetch fails")
	}
}

func TestTriageErrorFailsOpen(t *testing.T) {
	gh := &fakeGitHub{diff: "d"}
	tr := &fakeTriager{err: errors.New("model down")}
	h := newStarted(testConfig(), gh, tr)

	rec := post(t, h, openedPayload)
	if rec.Code != http.StatusOK {
		t.Errorf("fail-open should still return 200, got %d", rec.Code)
	}
	drain(t, h)

	if gh.labels() > 0 || gh.comments() > 0 {
		t.Error("no action expected when triage fails")
	}
}

func TestInvalidPayloadRejected(t *testing.T) {
	h := newStarted(testConfig(), &fakeGitHub{}, &fakeTriager{})
	defer drain(t, h)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGetMethodRejected(t *testing.T) {
	h := newStarted(testConfig(), &fakeGitHub{}, &fakeTriager{})
	defer drain(t, h)
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestMissingDeliveryIDRejected(t *testing.T) {
	h := newStarted(testConfig(), &fakeGitHub{}, &fakeTriager{})
	defer drain(t, h)
	rec := postHeaders(t, h, "pull_request", "", openedPayload)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing delivery ID", rec.Code)
	}
}

// SC (001 FR-002): a redelivered ID is processed exactly once.
func TestDuplicateDeliveryProcessedOnce(t *testing.T) {
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.99, Reason: "dup"}}
	h := newStarted(testConfig(), gh, tr)

	for i := 0; i < 3; i++ {
		postHeaders(t, h, "pull_request", "same-delivery", openedPayload)
	}
	drain(t, h)

	if gh.labels() != 1 || gh.comments() != 1 {
		t.Errorf("same delivery ×3 should yield exactly 1 label+1 comment; labels=%d comments=%d", gh.labels(), gh.comments())
	}
}

// FR-003: a non-pull_request event does zero GitHub work.
func TestNonPullRequestEventIgnored(t *testing.T) {
	gh := &fakeGitHub{diff: "d"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.99}}
	h := newStarted(testConfig(), gh, tr)

	rec := postHeaders(t, h, "issues", "d-issues", openedPayload)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	drain(t, h)

	if gh.labels() > 0 || gh.comments() > 0 {
		t.Error("issues event should trigger zero GitHub calls")
	}
}

// FR-005: a bot-authored PR is skipped before any GitHub work.
func TestBotAuthorSkipped(t *testing.T) {
	gh := &fakeGitHub{diff: "d"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.99}}
	h := newStarted(testConfig(), gh, tr)

	body := strings.Replace(openedPayload, `"login":"dev"`, `"login":"dependabot[bot]"`, 1)
	rec := postHeaders(t, h, "pull_request", "d-bot", body)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	drain(t, h)

	if gh.labels() > 0 || gh.comments() > 0 {
		t.Error("bot-authored PR should trigger zero GitHub calls")
	}
}

// syncBuffer is a mutex-guarded buffer: worker goroutines log concurrently, so
// the test's slog sink must be safe for concurrent writes and reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test and restores it afterward.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	prev := slog.Default()
	sb := &syncBuffer{}
	slog.SetDefault(slog.New(slog.NewJSONHandler(sb, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sb
}

// numberedPayload builds a pull_request "opened" payload with a specific PR
// number and title so a scripted fake can route behavior per delivery.
func numberedPayload(number int, title string) string {
	return `{
		"action":"opened","number":` + strconv.Itoa(number) + `,
		"pull_request":{"title":"` + title + `","user":{"login":"dev"}},
		"repository":{"owner":{"login":"octo"},"name":"repo"}
	}`
}

// routingGitHub errors FetchDiff for one PR number (to drive the failed path)
// and otherwise returns a fixed diff; label/comment counting is inherited.
type routingGitHub struct {
	fakeGitHub
	failOnNumber int
}

func (r *routingGitHub) FetchDiff(_ context.Context, _, _ string, number int) (string, error) {
	if number == r.failOnNumber {
		return "", errors.New("boom")
	}
	return r.diff, nil
}

// routingTriager flags PRs whose title is "slop", passes everything else.
type routingTriager struct{}

func (routingTriager) Analyze(_ context.Context, title, _ string) (*triage.Result, error) {
	if title == "slop" {
		return &triage.Result{IsSlop: true, Confidence: 0.99, Reason: "boilerplate"}, nil
	}
	return &triage.Result{IsSlop: false, Confidence: 0.99}, nil
}

// SC-003: counters match an injected mix of flagged / skipped / failed triages.
func TestStatsAdvanceAcrossPaths(t *testing.T) {
	gh := &routingGitHub{fakeGitHub: fakeGitHub{diff: "some diff"}, failOnNumber: 3}
	h := newStarted(config.Config{ConfidenceThreshold: 0.90, SlopLabel: "x", WorkerCount: 2}, gh, routingTriager{})

	postHeaders(t, h, "pull_request", "d1", numberedPayload(1, "slop"))  // flagged
	postHeaders(t, h, "pull_request", "d2", numberedPayload(2, "legit")) // skipped (not slop)
	postHeaders(t, h, "pull_request", "d3", numberedPayload(3, "slop"))  // failed (diff error)
	drain(t, h)

	s := h.Stats()
	if s.Received != 3 {
		t.Errorf("Received = %d, want 3", s.Received)
	}
	if s.Flagged != 1 {
		t.Errorf("Flagged = %d, want 1", s.Flagged)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", s.Skipped)
	}
	if s.Failed != 1 {
		t.Errorf("Failed = %d, want 1", s.Failed)
	}
	if s.Triaged != 2 {
		t.Errorf("Triaged = %d, want 2 (flagged+skipped; the failed one never got a verdict)", s.Triaged)
	}
	if s.TriageSamples != 2 {
		t.Errorf("TriageSamples = %d, want 2 (latency recorded only on a completed verdict)", s.TriageSamples)
	}
}

// SC-001/SC-003: a processed delivery's logs carry the delivery ID and NEVER
// contain the diff body.
func TestLogsCarryDeliveryIDAndNeverLeakDiff(t *testing.T) {
	logs := captureLogs(t)
	const diffMarker = "DIFF_BODY_MARKER_must_never_be_logged"
	gh := &fakeGitHub{diff: diffMarker}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.99, Reason: "boilerplate"}}
	h := newStarted(testConfig(), gh, tr)

	postHeaders(t, h, "pull_request", "trace-me-123", openedPayload)
	drain(t, h)

	out := logs.String()
	if !strings.Contains(out, "trace-me-123") {
		t.Errorf("log output missing delivery ID; got:\n%s", out)
	}
	if strings.Contains(out, diffMarker) {
		t.Errorf("log output LEAKED the diff body:\n%s", out)
	}
}

// FR-001: the handler acks fast even while triage is blocked.
func TestAckIsFastWhileTriageBlocks(t *testing.T) {
	gh := &fakeGitHub{diff: "d"}
	bt := &blockingTriager{release: make(chan struct{})}
	h := newStarted(testConfig(), gh, bt)

	start := time.Now()
	rec := post(t, h, openedPayload)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("ack took %v; expected sub-second while triage blocks", elapsed)
	}

	close(bt.release) // let the worker finish so drain doesn't wait on it
	drain(t, h)
}
