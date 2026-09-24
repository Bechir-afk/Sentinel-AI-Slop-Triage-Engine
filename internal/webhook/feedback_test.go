package webhook

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/config"
	"sentinel/internal/triage"
)

// unlabeledPayload builds a pull_request "unlabeled" payload where the named
// label was removed from PR #7 — the maintainer-disagreement signal.
func unlabeledPayload(label string) string {
	return `{
		"action":"unlabeled","number":7,
		"pull_request":{"title":"Add feature","user":{"login":"dev"},"head":{"sha":"abc123"}},
		"repository":{"owner":{"login":"octo"},"name":"repo"},
		"label":{"name":"` + label + `"}
	}`
}

// readSignals returns every JSON line in the feedback log as a decoded Signal.
func readSignals(t *testing.T, path string) []Signal {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open feedback log: %v", err)
	}
	defer f.Close()
	var out []Signal
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var s Signal
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("bad signal line %q: %v", line, err)
		}
		out = append(out, s)
	}
	return out
}

func feedbackConfig(path string) config.Config {
	cfg := testConfig()
	cfg.FeedbackLogPath = path
	return cfg
}

// SC-010 (FR-015): removing the slop label appends exactly one well-formed row
// to the out-of-band log — PR identity, original verdict, disagreement type.
func TestSlopLabelRemovalAppendsOneSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	gh := &fakeGitHub{}
	h := newStarted(feedbackConfig(path), gh, &fakeTriager{})

	rec := postHeaders(t, h, "pull_request", "fb-1", unlabeledPayload("needs-human-review"))
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	drain(t, h) // flushes the feedback log

	sigs := readSignals(t, path)
	if len(sigs) != 1 {
		t.Fatalf("want exactly 1 signal row, got %d", len(sigs))
	}
	s := sigs[0]
	if s.PR != "octo/repo#7" {
		t.Errorf("PR = %q, want octo/repo#7", s.PR)
	}
	if s.OriginalVerdict != "slop" {
		t.Errorf("OriginalVerdict = %q, want slop", s.OriginalVerdict)
	}
	if s.DisagreementType != "label-removed" {
		t.Errorf("DisagreementType = %q, want label-removed", s.DisagreementType)
	}
	if s.TS == "" {
		t.Error("signal missing timestamp")
	}
	// The label removal does zero GitHub work — it's a feedback signal, not a triage.
	if gh.labels() > 0 || gh.comments() > 0 || gh.checks() > 0 {
		t.Error("a label-removal event must trigger no GitHub writes")
	}
}

// Removing a label that is NOT Sentinel's slop label is a normal non-actionable
// event: no signal is recorded (we never fabricate a verdict for a PR we didn't
// flag — spec edge case).
func TestUnrelatedLabelRemovalRecordsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	h := newStarted(feedbackConfig(path), &fakeGitHub{}, &fakeTriager{})

	postHeaders(t, h, "pull_request", "fb-2", unlabeledPayload("enhancement"))
	drain(t, h)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		// The file may exist (opened by the writer) but must contain zero rows.
		if sigs := readSignals(t, path); len(sigs) != 0 {
			t.Errorf("unrelated label removal recorded %d signals, want 0", len(sigs))
		}
	}
}

// A duplicate redelivery of the same removal is recorded once, matching the
// dedup guarantee on the triage path.
func TestDuplicateLabelRemovalRecordedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	h := newStarted(feedbackConfig(path), &fakeGitHub{}, &fakeTriager{})

	for i := 0; i < 3; i++ {
		postHeaders(t, h, "pull_request", "fb-dup", unlabeledPayload("needs-human-review"))
	}
	drain(t, h)

	if sigs := readSignals(t, path); len(sigs) != 1 {
		t.Errorf("same removal ×3 should yield exactly 1 signal, got %d", len(sigs))
	}
}

// With FEEDBACK_LOG unset the feature is disabled: the event is still acked (no
// error) but nothing is persisted and no writer goroutine runs.
func TestLabelRemovalWithFeedbackDisabledIsAcked(t *testing.T) {
	h := newStarted(testConfig(), &fakeGitHub{}, &fakeTriager{}) // no FeedbackLogPath
	rec := postHeaders(t, h, "pull_request", "fb-off", unlabeledPayload("needs-human-review"))
	drain(t, h)
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 even with feedback disabled", rec.Code)
	}
}

// The feedback log is out-of-band and independent of the triage path: a normal
// flagged triage writes to GitHub and touches no feedback file.
func TestFlaggedTriageDoesNotWriteFeedback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	gh := &fakeGitHub{diff: "some diff"}
	tr := &fakeTriager{res: &triage.Result{IsSlop: true, Confidence: 0.99, Reason: "boilerplate"}}
	h := newStarted(feedbackConfig(path), gh, tr)

	post(t, h, openedPayload)
	drain(t, h)

	if gh.labels() != 1 {
		t.Errorf("expected the PR to be flagged; labels=%d", gh.labels())
	}
	if _, err := os.Stat(path); err == nil {
		if sigs := readSignals(t, path); len(sigs) != 0 {
			t.Errorf("a normal triage must not append feedback rows; got %d", len(sigs))
		}
	}
}
