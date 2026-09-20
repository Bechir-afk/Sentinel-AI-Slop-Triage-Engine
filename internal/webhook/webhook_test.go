package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
