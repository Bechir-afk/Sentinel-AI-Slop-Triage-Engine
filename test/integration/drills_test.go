package integration

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// US4 (P4) — Failure drills: every runbook failure scenario proven on the real
// binary. Each drill here is the paired check a docs/RUNBOOK.md scenario names
// (T018 enforces that pairing), so the runbook can never claim a behavior the
// code does not exhibit.

// TestFailOpenDrills (T015): for each downstream failure the pipeline can hit
// (model unreachable, model 5xx, GitHub 5xx through the retry budget) a signed
// slop delivery must still ack 2xx immediately, the gateway must fail open (the
// `failed` counter advances) and — the safety-critical half — it must make ZERO
// PR mutations (FR-009, SC-007). A failing downstream never makes Sentinel speak.
func TestFailOpenDrills(t *testing.T) {
	// Each drill arranges one downstream failure after bring-up. The GitHub-side
	// failure is injected on the DIFF fetch (a read, stage 3), NOT a write: a
	// write-side 5xx is logged non-fatally in act() and never advances `failed`,
	// so a read-stage failure is the honest observable fail-open signal.
	cases := []struct {
		name     string
		delivery string
		arrange  func(m *ModelStub, gh *GitHubStub)
	}{
		{
			name:     "model unreachable",
			delivery: "delivery-drill-model-down",
			// Bring-up sourced the threshold while the model was up; take it down
			// now so triage (stage 4) fails on an unreachable model — the diff
			// fetch (stage 3) still succeeds, isolating the failure to triage.
			arrange: func(m *ModelStub, gh *GitHubStub) { m.Close() },
		},
		{
			name:     "model 5xx",
			delivery: "delivery-drill-model-5xx",
			// The model is reachable but its /predict returns 500 through the
			// triage client's retry budget — stage 4 fails, stage 3 already
			// succeeded, so this isolates the failure to the model call.
			arrange: func(m *ModelStub, gh *GitHubStub) { m.InjectPredictStatus(http.StatusInternalServerError) },
		},
		{
			name:     "github 5xx",
			delivery: "delivery-drill-github-5xx",
			// Inject on the DIFF fetch (stage 3, a read), not a write: act()'s
			// write failures are logged non-fatally and never advance `failed`, so
			// a read-stage 5xx is the honest fail-open signal. The retry budget in
			// the GitHub client is exhausted before the error surfaces.
			arrange: func(m *ModelStub, gh *GitHubStub) { gh.InjectDiffStatus(http.StatusInternalServerError) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModelStub()
			defer model.Close()
			gh := NewGitHubStub()
			defer gh.Close()

			// Boot with both stubs healthy so threshold sourcing (US1) succeeds;
			// arrange injects the failure only afterwards, isolating it to the
			// delivery under test.
			g := boot(t, bootOptions{github: gh, model: model})
			tc.arrange(model, gh)

			req, err := signedRequest(g.base(), g.secret, "pull_request", tc.delivery, openedPayload())
			if err != nil {
				t.Fatalf("build signed request: %v", err)
			}
			// The ack is immediate and 2xx even though the pipeline will fail open
			// downstream — the connection is never held for the failing call.
			if status, err := g.send(req); err != nil || status != http.StatusOK {
				t.Fatalf("POST /webhook: status=%d err=%v", status, err)
			}

			// Observable fail-open signal: `failed` advances to 1 (incFailed fires
			// on a stage-3 or stage-4 error and on nothing else).
			waitFor(t, "failed counter advances", func() bool { return g.statCounter("failed") == 1 })

			// The safety-critical half: a failing downstream makes ZERO PR
			// mutations. Settle briefly to catch any erroneous async write, then
			// assert none landed.
			time.Sleep(200 * time.Millisecond) // ponytail: short settle before the zero-writes assertion; the waitFor above already proved the pipeline reached its fail-open return
			if n := gh.WriteCalls(); n != 0 {
				t.Errorf("%s: fail-open made %d GitHub writes, want 0", tc.name, n)
			}

			assertNoSecretsInLog(t, g, model, gh)
		})
	}
}

// TestMissingArtifactFailsFastWithOneLine (T016, FR-010/SC-008): the real model
// service, pointed at an artifact directory that has no weights, must refuse at
// import with ONE actionable line naming MODEL_PATH and the exact command that
// produces the artifact — never a Hugging Face stack trace that crash-loops with
// no explanation. This exercises model/inference.py::_verify_artifact directly.
//
// The Go ModelStub has no "absent artifact" mode by design (it's a torch-free
// stand-in), so the single-actionable-line contract lives only in the real
// model. We run it in a subprocess. When torch/transformers aren't installed —
// exactly the CI stub-model subset (T019) — the import fails before
// _verify_artifact, so we skip cleanly rather than assert against an environment
// that can't reach the code under test.
//
// The gateway-side half of SC-008 ("the gateway still boots and fails open on a
// delivery") is the same observable behavior already proven by
// TestFailOpenDrills/"model unreachable": a model that exited on an unusable
// artifact is, to the gateway, an unreachable MODEL_URL. It is not re-asserted
// here to avoid a duplicate boot.
func TestMissingArtifactFailsFastWithOneLine(t *testing.T) {
	py := findPython(t)
	modelDir := filepath.Join(repoRoot(), "model")

	// Point the real model at an empty directory: it exists, but carries no
	// config/weights/tokenizer, so _verify_artifact must refuse.
	emptyArtifact := t.TempDir()

	cmd := exec.Command(py, "inference.py")
	cmd.Dir = modelDir // so `import encoding` (a sibling module) resolves
	cmd.Env = append(cmd.Environ(), "MODEL_PATH="+emptyArtifact)
	out, _ := cmd.CombinedOutput()
	stderr := string(out)

	// torch/transformers absent (the CI torch-free subset) → the import at the top
	// of inference.py fails before _verify_artifact runs. That's an environment we
	// don't exercise here, not a contract failure — skip.
	if strings.Contains(stderr, "ModuleNotFoundError") || strings.Contains(stderr, "No module named") {
		t.Skipf("model deps (torch/transformers) not installed; skipping real-model artifact drill:\n%s", stderr)
	}

	// It must have exited non-zero (sys.exit(1)); CombinedOutput's err is set on a
	// non-zero exit, but assert on the stronger, message-level contract below.
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit on missing artifact, got exit 0:\n%s", stderr)
	}

	// The one actionable line names both the offending path and the fix.
	if !strings.Contains(stderr, "MODEL_PATH=") {
		t.Errorf("actionable line missing MODEL_PATH=; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "python ml/train.py --data ml/dataset --out model/model") {
		t.Errorf("actionable line missing the produce-artifact command; got:\n%s", stderr)
	}
	// It is ONE clean message, not a crash-loop stack trace — the whole reason
	// _verify_artifact exists (FR-010).
	if strings.Contains(stderr, "Traceback (most recent call last)") {
		t.Errorf("missing artifact produced a stack trace, want one actionable line:\n%s", stderr)
	}
	if n := strings.Count(stderr, "model artifact unusable"); n != 1 {
		t.Errorf("want exactly one actionable line, found %d:\n%s", n, stderr)
	}
}

// TestCheckRunFailureDoesNotBlockLabelComment (T016, 002 FR-014): the Check Run
// is the least critical of the three writes — observational, never gating. If it
// 5xxs, the label and comment must still land and the delivery must NOT count as
// a failed triage (a write-side error is logged non-fatally, never incFailed).
func TestCheckRunFailureDoesNotBlockLabelComment(t *testing.T) {
	model := NewModelStub() // default: high-confidence slop → would flag
	defer model.Close()
	gh := NewGitHubStub()
	defer gh.Close()

	g := boot(t, bootOptions{github: gh, model: model})
	gh.InjectCheckRunStatus(http.StatusInternalServerError)

	req, err := signedRequest(g.base(), g.secret, "pull_request", "delivery-checkrun-5xx", openedPayload())
	if err != nil {
		t.Fatalf("build signed request: %v", err)
	}
	if status, err := g.send(req); err != nil || status != http.StatusOK {
		t.Fatalf("POST /webhook: status=%d err=%v", status, err)
	}

	// The stub records the Check Run attempt before honoring the injected 5xx, so
	// its landing is the completion signal that act() ran all three writes — even
	// though this one returned 500.
	waitFor(t, "check run attempt recorded", func() bool { return gh.CheckRunCalls() >= 1 })

	// The label and comment (attempted before the Check Run in act()) both landed.
	if n := gh.LabelCalls(); n != 1 {
		t.Errorf("label adds = %d, want 1 (a failing check run must not block the label)", n)
	}
	if n := gh.CommentCalls(); n != 1 {
		t.Errorf("comments = %d, want 1 (a failing check run must not block the comment)", n)
	}

	// A write-side 5xx is non-fatal: the delivery flagged successfully, so `flagged`
	// advances and `failed` does NOT (only stage-3/stage-4 errors fail open).
	if n := g.statCounter("flagged"); n != 1 {
		t.Errorf("flagged = %d, want 1", n)
	}
	if n := g.statCounter("failed"); n != 0 {
		t.Errorf("failed = %d, want 0 (a check-run write error is logged, not failed-open)", n)
	}

	assertNoSecretsInLog(t, g, model, gh)
}

// findPython locates a WORKING Python interpreter for the real-model artifact
// drill, skipping the test cleanly if none is available. It validates each
// candidate with `--version` because on Windows `python`/`python3` on PATH is
// often the Microsoft Store alias stub — a real file that prints "Python was not
// found" and exits non-zero rather than running anything.
func findPython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python", "py"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "--version").CombinedOutput()
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Python") {
			return p
		}
	}
	t.Skip("no working python interpreter on PATH; skipping real-model artifact drill")
	return ""
}

// TestRunbookScenariosHaveChecks (T018, FR-011, constitution VIII): the machine
// half of "docs match code". docs/RUNBOOK.md names, for every scenario, the
// paired check that proves it. This greps the runbook for those names and fails
// if any names a test function (or the offline Python check) that does not
// exist — so a documented behavior can never outlive the check that backs it.
//
// It verifies existence, not the reverse (a test may exist without a runbook
// row); the point is that the runbook cannot *claim* a check it does not have.
func TestRunbookScenariosHaveChecks(t *testing.T) {
	runbook := filepath.Join(repoRoot(), "docs", "RUNBOOK.md")
	md, err := os.ReadFile(runbook)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}

	// Paired-check names are the only backtick-wrapped tokens that either start
	// with "Test" (a Go test func) or end in ".py" (the offline check). Scenario
	// prose uses backticks for many other things (env vars, endpoints), so anchor
	// on those two shapes rather than every backtick span.
	goNames := uniqueMatches(regexp.MustCompile("`(Test[A-Za-z0-9_]+)`"), string(md))
	pyNames := uniqueMatches(regexp.MustCompile("`([A-Za-z0-9_]+\\.py)`"), string(md))

	// A garbled/empty runbook must not pass vacuously: it documents at least the
	// core drills, so require a sane floor.
	if len(goNames) < 8 {
		t.Fatalf("runbook names only %d Go checks (%v); expected the full drill set — did the tables break?", len(goNames), goNames)
	}

	// The set of Go test functions actually defined in this package.
	defined := definedTestFuncs(t)
	for _, name := range goNames {
		if !defined[name] {
			t.Errorf("RUNBOOK.md names paired check %q, but no such test function exists in test/integration/", name)
		}
	}

	// The offline (Python) checks the runbook cites must exist under ml/tests/.
	for _, py := range pyNames {
		if _, err := os.Stat(filepath.Join(repoRoot(), "ml", "tests", py)); err != nil {
			t.Errorf("RUNBOOK.md names offline check %q, but ml/tests/%s does not exist: %v", py, py, err)
		}
	}
}

// uniqueMatches returns the distinct first-capture-group values of re in s,
// preserving first-seen order (for a stable failure message).
func uniqueMatches(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// definedTestFuncs scans every *_test.go in this package and returns the set of
// top-level Test function names, so the runbook's paired-check names can be
// checked against what actually compiles here.
func definedTestFuncs(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join(repoRoot(), "test", "integration")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read integration dir: %v", err)
	}
	funcRE := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	defined := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range funcRE.FindAllStringSubmatch(string(src), -1) {
			defined[m[1]] = true
		}
	}
	return defined
}
