package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel/internal/config"
	"sentinel/internal/triage"
)

// fakeGitHub records label/comment calls and lets tests inject a diff error.
type fakeGitHub struct {
	diff        string
	diffErr     error
	labeled     bool
	commented   bool
	labelErr    error
	commentText string
}

func (f *fakeGitHub) FetchDiff(_ context.Context, _, _ string, _ int) (string, error) {
	return f.diff, f.diffErr
}
func (f *fakeGitHub) AddLabel(_ context.Context, _, _ string, _ int, _ string) error {
	f.labeled = true
	return f.labelErr
}
func (f *fakeGitHub) PostComment(_ context.Context, _, _ string, _ int, body string) error {
	f.commented = true
	f.commentText = body
	return nil
}

// fakeTriager returns a fixed verdict or error.
type fakeTriager struct {
	res *triage.Result
	err error
}

func (f *fakeTriager) Analyze(_ context.Context, _, _ string) (*triage.Result, error) {
	return f.res, f.err
}

func testConfig() config.Config {
	return config.Config{ConfidenceThreshold: 0.90, SlopLabel: "needs-human-review"}
}

const openedPayload = `{
	"action":"opened","number":7,
	"pull_request":{"title":"Add feature","user":{"login":"dev"}},
	"repository":{"owner":{"login":"octo"},"name":"repo"}
}`

func post(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSlopAboveThresholdIsFlagged(t *testing.T) {
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.95, Reason: "boilerplate"}}
	h := New(testConfig(), gh, tr)

	rec := post(t, h, openedPayload)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	if !gh.labeled || !gh.commented {
		t.Errorf("expected label+comment; labeled=%v commented=%v", gh.labeled, gh.commented)
	}
	if !strings.Contains(gh.commentText, "boilerplate") {
		t.Errorf("comment missing reason: %q", gh.commentText)
	}
}

func TestSlopBelowThresholdNoAction(t *testing.T) {
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.80}}
	h := New(testConfig(), gh, tr)

	post(t, h, openedPayload)

	if gh.labeled || gh.commented {
		t.Error("low-confidence slop should not trigger action")
	}
}

func TestNotSlopNoAction(t *testing.T) {
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: false, Confidence: 0.99}}
	h := New(testConfig(), gh, tr)

	post(t, h, openedPayload)

	if gh.labeled || gh.commented {
		t.Error("legitimate PR should not trigger action")
	}
}

func TestNonActionableActionIgnored(t *testing.T) {
	gh := &fakeGitHub{diff: "d"}
	tr := &fakeTriager{res: &triage.Result{}}
	h := New(testConfig(), gh, tr)

	body := strings.Replace(openedPayload, `"action":"opened"`, `"action":"closed"`, 1)
	post(t, h, body)

	if gh.labeled || gh.commented {
		t.Error("closed action should be ignored")
	}
}

func TestDiffFetchErrorFailsOpen(t *testing.T) {
	gh := &fakeGitHub{diffErr: errors.New("boom")}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.99}}
	h := New(testConfig(), gh, tr)

	rec := post(t, h, openedPayload)

	if rec.Code != http.StatusOK {
		t.Errorf("fail-open should still return 200, got %d", rec.Code)
	}
	if gh.labeled || gh.commented {
		t.Error("no action expected when diff fetch fails")
	}
}

func TestTriageErrorFailsOpen(t *testing.T) {
	gh := &fakeGitHub{diff: "d"}
	tr := &fakeTriager{err: errors.New("model down")}
	h := New(testConfig(), gh, tr)

	rec := post(t, h, openedPayload)

	if rec.Code != http.StatusOK {
		t.Errorf("fail-open should still return 200, got %d", rec.Code)
	}
	if gh.labeled || gh.commented {
		t.Error("no action expected when triage fails")
	}
}

func TestInvalidPayloadRejected(t *testing.T) {
	h := New(testConfig(), &fakeGitHub{}, &fakeTriager{})
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGetMethodRejected(t *testing.T) {
	h := New(testConfig(), &fakeGitHub{}, &fakeTriager{})
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
