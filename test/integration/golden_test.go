package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// US2 (P2) — Golden end-to-end path, no live credentials. One signed slop
// delivery → exactly one label + one comment + one Check Run, asserted on the
// real request bytes the recording GitHub stub captured.

// T009: POST a correctly-signed slop "opened" delivery; assert the webhook
// response is 2xx and immediate (sub-second), then waitFor async completion and
// assert the GitHub stub recorded exactly one label add, one comment (text
// contains confidence + model/artifact version), and one Check Run on the PR
// head SHA (FR-003, SC-002).
func TestGoldenPathExactlyThreeWrites(t *testing.T) {
	model := NewModelStub() // default: high-confidence slop
	defer model.Close()
	gh := NewGitHubStub()
	defer gh.Close()

	g := boot(t, bootOptions{github: gh, model: model})

	body := openedPayload()
	req, err := signedRequest(g.base(), g.secret, "pull_request", "delivery-golden-1", body)
	if err != nil {
		t.Fatalf("build signed request: %v", err)
	}

	start := time.Now()
	status, err := g.send(req)
	if err != nil {
		t.Fatalf("POST /webhook: %v", err)
	}
	elapsed := time.Since(start)

	if status != http.StatusOK {
		t.Errorf("webhook status = %d, want 200", status)
	}
	// Immediate ack: the handler enqueues and returns; triage runs async. A
	// generous sub-second bound proves the connection is not held for the model
	// round trip (which itself is fast here, so keep the bar honest but firm).
	if elapsed > 900*time.Millisecond {
		t.Errorf("webhook ack took %s, want immediate (<900ms)", elapsed)
	}

	// Wait on the observable completion signal: the Check Run is the last of the
	// three writes in act(), so its landing means all three ran.
	waitFor(t, "check run recorded", func() bool { return gh.CheckRunCalls() >= 1 })

	if n := gh.LabelCalls(); n != 1 {
		t.Errorf("label adds = %d, want exactly 1", n)
	}
	if n := gh.CommentCalls(); n != 1 {
		t.Errorf("comments = %d, want exactly 1", n)
	}
	if n := gh.CheckRunCalls(); n != 1 {
		t.Errorf("check runs = %d, want exactly 1", n)
	}

	// The label body names our slop label.
	if lb := gh.LastLabelBody(); lb != nil {
		labels, _ := lb["labels"].([]any)
		if len(labels) != 1 || fmt.Sprint(labels[0]) != slopLabel {
			t.Errorf("label body = %v, want [%q]", lb, slopLabel)
		}
	} else {
		t.Errorf("no label body recorded")
	}

	// The comment carries confidence (99%) + artifact version (from the stub).
	cb := gh.LastCommentBody()
	if cb == nil {
		t.Fatalf("no comment body recorded")
	}
	commentText, _ := cb["body"].(string)
	if !strings.Contains(commentText, "99%") {
		t.Errorf("comment missing confidence; got: %q", commentText)
	}
	if !strings.Contains(commentText, "stub-model-1.0") {
		t.Errorf("comment missing artifact/model version; got: %q", commentText)
	}

	// The Check Run targets the PR head SHA and is completed/neutral.
	crb := gh.LastCheckRunBody()
	if crb == nil {
		t.Fatalf("no check run body recorded")
	}
	if got := fmt.Sprint(crb["head_sha"]); got != fxHeadSHA {
		t.Errorf("check run head_sha = %q, want %q", got, fxHeadSHA)
	}
	if got := fmt.Sprint(crb["status"]); got != "completed" {
		t.Errorf("check run status = %q, want completed", got)
	}
	if got := fmt.Sprint(crb["conclusion"]); got != "neutral" {
		t.Errorf("check run conclusion = %q, want neutral", got)
	}

	assertNoSecretsInLog(t, g, model, gh)
}

// T010: send the same X-GitHub-Delivery twice within one process; assert the
// outward writes occur exactly once — idempotent redelivery (FR-004, SC-003).
func TestGoldenPathIdempotentRedelivery(t *testing.T) {
	model := NewModelStub()
	defer model.Close()
	gh := NewGitHubStub()
	defer gh.Close()

	g := boot(t, bootOptions{github: gh, model: model})

	body := openedPayload()
	const deliveryID = "delivery-dupe-1"

	// First delivery: process fully (wait on the Check Run landing).
	req1, _ := signedRequest(g.base(), g.secret, "pull_request", deliveryID, body)
	if status, err := g.send(req1); err != nil || status != http.StatusOK {
		t.Fatalf("first delivery: status=%d err=%v", status, err)
	}
	waitFor(t, "first delivery completed", func() bool { return gh.CheckRunCalls() >= 1 })

	// Second delivery, same ID: the ledger claims it as a duplicate and skips.
	req2, _ := signedRequest(g.base(), g.secret, "pull_request", deliveryID, body)
	if status, err := g.send(req2); err != nil || status != http.StatusOK {
		t.Fatalf("redelivery: status=%d err=%v", status, err)
	}

	// The redelivery is a fast synchronous "duplicate; skipped" — but to prove no
	// second write slips through async, wait until received stops advancing and
	// then assert counts. received counts only accepted (non-dupe) jobs, so it
	// must stay at 1.
	waitFor(t, "received counter stable at 1", func() bool { return g.statCounter("received") == 1 })
	// Give any (erroneous) second pipeline a chance to write, then assert once.
	time.Sleep(200 * time.Millisecond) // ponytail: a short settle before the exactly-once assertion; not a completion wait (that was the waitFor above)

	if n := gh.LabelCalls(); n != 1 {
		t.Errorf("label adds after redelivery = %d, want 1", n)
	}
	if n := gh.CommentCalls(); n != 1 {
		t.Errorf("comments after redelivery = %d, want 1", n)
	}
	if n := gh.CheckRunCalls(); n != 1 {
		t.Errorf("check runs after redelivery = %d, want 1", n)
	}

	assertNoSecretsInLog(t, g, model, gh)
}

// T011: a delivery whose signature does not match the raw body → 401 and zero
// GitHub/model calls; a body over MAX_BODY_BYTES → 413 before any HMAC/parse
// work (FR-005, SC-004). Trust boundary, on the real binary.
func TestGoldenPathTrustBoundary(t *testing.T) {
	model := NewModelStub()
	defer model.Close()
	gh := NewGitHubStub()
	defer gh.Close()

	// Small body cap so we can exceed it cheaply without a multi-MB payload.
	g := boot(t, bootOptions{
		github:   gh,
		model:    model,
		extraEnv: map[string]string{"MAX_BODY_BYTES": "1024"},
	})

	// --- bad signature → 401, zero downstream calls ---
	body := openedPayload()
	bad, err := badSignedRequest(g.base(), g.secret, "pull_request", "delivery-bad-sig", body)
	if err != nil {
		t.Fatalf("build bad-signed request: %v", err)
	}
	status, err := g.send(bad)
	if err != nil {
		t.Fatalf("POST bad-signed: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("bad signature status = %d, want 401", status)
	}
	// Zero GitHub or model calls: verification is first at the trust boundary.
	// Settle briefly, then assert nothing fired.
	time.Sleep(200 * time.Millisecond)
	if n := gh.DiffCalls() + gh.WriteCalls(); n != 0 {
		t.Errorf("bad signature triggered %d GitHub calls, want 0", n)
	}
	if n := model.PredictCalls(); n != 0 {
		t.Errorf("bad signature triggered %d model calls, want 0", n)
	}

	// --- oversized body → 413 before HMAC ---
	big := make([]byte, 4096) // > 1024 cap
	for i := range big {
		big[i] = 'a'
	}
	// Sign it correctly: the point is the size gate must fire BEFORE HMAC, so
	// even a valid signature must not save an oversized body.
	oversized, err := signedRequest(g.base(), g.secret, "pull_request", "delivery-oversized", big)
	if err != nil {
		t.Fatalf("build oversized request: %v", err)
	}
	status, err = g.send(oversized)
	if err != nil {
		t.Fatalf("POST oversized: %v", err)
	}
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body status = %d, want 413", status)
	}

	assertNoSecretsInLog(t, g, model, gh)
}
