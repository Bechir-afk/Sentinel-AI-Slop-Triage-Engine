package integration

import (
	"strings"
	"testing"
)

// US1 (P1) — Bring the stack up and prove it healthy. These scenarios are also
// the Phase 2 checkpoint: they prove the harness boots the shipped binary
// against the stubs and both report healthy.

// T007: both services reach healthy within the startup budget and each /healthz
// carries a build version (FR-002, SC-001); a fresh /stats returns all-zero
// counters, needs no secret, and exposes no PR content (spec US1 scenario 3).
func TestBringUpHealthyAndZeroedStats(t *testing.T) {
	model := NewModelStub()
	defer model.Close()
	gh := NewGitHubStub()
	defer gh.Close()

	g := boot(t, bootOptions{github: gh, model: model})

	// boot() already waited on /healthz; assert the model stub is reachable too
	// (the gateway proved it by sourcing its threshold — see healthCalls > 0).
	if model.HealthCalls() == 0 {
		t.Errorf("gateway never called the model /healthz during bring-up")
	}

	// /healthz carries a build version (a plain `go build` reports "dev").
	ver, err := g.healthVersion()
	if err != nil {
		t.Fatalf("read /healthz version: %v", err)
	}
	if ver == "" {
		t.Errorf("/healthz reported no build version")
	}

	// A fresh /stats: no secret needed (boot's poll used no auth header) and all
	// counters zero before any delivery.
	stats, err := g.stats()
	if err != nil {
		t.Fatalf("read /stats: %v", err)
	}
	for _, k := range []string{"received", "triaged", "flagged", "skipped", "failed"} {
		if v, _ := stats[k].(float64); v != 0 {
			t.Errorf("fresh /stats counter %q = %v, want 0", k, v)
		}
	}
	// Exposes no PR content: the only keys are counters/latency/version — assert
	// nothing that could carry a title, diff, author, or repo leaked in.
	for k := range stats {
		if strings.ContainsAny(k, " ") ||
			strings.Contains(strings.ToLower(k), "title") ||
			strings.Contains(strings.ToLower(k), "diff") ||
			strings.Contains(strings.ToLower(k), "author") ||
			strings.Contains(strings.ToLower(k), "repo") ||
			strings.Contains(strings.ToLower(k), "body") {
			t.Errorf("/stats exposed unexpected key %q (possible PR content leak)", k)
		}
	}

	assertNoSecretsInLog(t, g, model, gh)
}

// T008: boot with CONFIDENCE_THRESHOLD unset and the model stub advertising a
// distinct threshold on /healthz; assert the gateway sourced its operating
// threshold from the model artifact, not the 0.95 fallback (FR-002, spec US1
// scenario 2) — observed via the gateway's startup log line. Confirm harness
// output leaks no secret/diff (FR-014).
func TestBringUpThresholdSourcedFromArtifact(t *testing.T) {
	model := NewModelStub()
	defer model.Close()
	model.SetThreshold(0.73) // distinct, non-fallback value
	gh := NewGitHubStub()
	defer gh.Close()

	// CONFIDENCE_THRESHOLD deliberately unset ⇒ the gateway must source it.
	g := boot(t, bootOptions{github: gh, model: model})

	// The gateway logs "threshold sourced from model artifact" with the value
	// when it sources over the network; the 0.95 fallback logs no such line.
	logs := g.logs()
	if !strings.Contains(logs, "threshold sourced from model artifact") {
		t.Errorf("gateway did not log artifact-sourced threshold; log:\n%s", logs)
	}
	if !strings.Contains(logs, "0.73") {
		t.Errorf("sourced threshold value 0.73 not present in startup log; log:\n%s", logs)
	}
	// It must NOT have fallen back to 0.95.
	if strings.Contains(logs, "using default threshold") {
		t.Errorf("gateway fell back to default threshold instead of sourcing it; log:\n%s", logs)
	}

	assertNoSecretsInLog(t, g, model, gh)
}

// assertNoSecretsInLog enforces the harness's own log-hygiene invariant (FR-014,
// 002 FR-001): the captured gateway output must never contain the webhook
// secret, the GitHub token, or the canned diff body.
func assertNoSecretsInLog(t *testing.T, g *gateway, _ *ModelStub, _ *GitHubStub) {
	t.Helper()
	logs := g.logs()
	if strings.Contains(logs, g.secret) {
		t.Errorf("gateway log leaked the webhook secret")
	}
	if strings.Contains(logs, "dummy-token-not-used-against-stub") {
		t.Errorf("gateway log leaked the GitHub token")
	}
	if strings.Contains(logs, cannedDiff) || strings.Contains(logs, "+new") {
		t.Errorf("gateway log leaked the diff body")
	}
}
