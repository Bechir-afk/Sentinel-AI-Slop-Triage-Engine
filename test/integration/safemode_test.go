package integration

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// US3 (P3) — Safe-mode and feedback paths proven end to end on the real binary.
// Shadow mode runs the full pipeline but writes nothing; a maintainer removing
// the slop label lands exactly one out-of-band feedback row and zero GitHub
// writes. The offline fold-in round-trip is the Python half (T014).

// T012: boot with SHADOW_MODE=true; POST a signed high-confidence-slop delivery;
// waitFor the flagged counter to advance and assert the model stub WAS called,
// yet the GitHub stub recorded ZERO label/comment/Check Run writes (FR-006,
// SC-005).
func TestShadowModeFlagsButNeverWrites(t *testing.T) {
	model := NewModelStub() // default: high-confidence slop → would flag
	defer model.Close()
	gh := NewGitHubStub()
	defer gh.Close()

	g := boot(t, bootOptions{github: gh, model: model, shadowMode: true})

	body := openedPayload()
	req, err := signedRequest(g.base(), g.secret, "pull_request", "delivery-shadow-1", body)
	if err != nil {
		t.Fatalf("build signed request: %v", err)
	}
	if status, err := g.send(req); err != nil || status != http.StatusOK {
		t.Fatalf("POST /webhook: status=%d err=%v", status, err)
	}

	// Observable completion: the full pipeline ran, so flagged advances to 1 —
	// shadow mode still counts the would-be flag, it just doesn't write it.
	waitFor(t, "flagged counter advances", func() bool { return g.statCounter("flagged") == 1 })

	// The model WAS consulted (the pipeline is real; only the outward write is
	// suppressed). The diff fetch also happened.
	if model.PredictCalls() < 1 {
		t.Errorf("model predict calls = %d, want >=1 (pipeline must run in shadow mode)", model.PredictCalls())
	}
	if gh.DiffCalls() < 1 {
		t.Errorf("diff fetches = %d, want >=1 (pipeline must fetch the diff)", gh.DiffCalls())
	}

	// Give any (erroneous) outward write a chance to land, then assert none did.
	time.Sleep(200 * time.Millisecond) // ponytail: short settle before the zero-writes assertion; the waitFor above already proved the pipeline completed
	if n := gh.WriteCalls(); n != 0 {
		t.Errorf("shadow mode made %d GitHub writes, want 0", n)
	}

	assertNoSecretsInLog(t, g, model, gh)
}

// T013: boot with FEEDBACK_LOG set to a temp file; POST a signed `unlabeled`
// delivery removing the slop label; assert exactly one well-formed row (PR
// identity, original verdict, disagreement type, timestamp) is appended off the
// request path and ZERO GitHub writes occur (FR-007, SC-006 first half). Assert
// the row carries no diff body (FR-014).
func TestFeedbackRowOnLabelRemoval(t *testing.T) {
	model := NewModelStub()
	defer model.Close()
	gh := NewGitHubStub()
	defer gh.Close()

	feedbackLog := filepath.Join(t.TempDir(), "feedback.jsonl")
	g := boot(t, bootOptions{github: gh, model: model, feedbackLog: feedbackLog})

	body := unlabeledPayload() // maintainer removed our slop label
	req, err := signedRequest(g.base(), g.secret, "pull_request", "delivery-unlabeled-1", body)
	if err != nil {
		t.Fatalf("build signed request: %v", err)
	}
	if status, err := g.send(req); err != nil || status != http.StatusOK {
		t.Fatalf("POST /webhook: status=%d err=%v", status, err)
	}

	// Observable completion: the row lands on the feedback log's own goroutine,
	// so waitFor the file to carry exactly one line rather than sleeping.
	var rows []map[string]any
	waitFor(t, "one feedback row appended", func() bool {
		rows = readFeedbackRows(t, feedbackLog)
		return len(rows) >= 1
	})

	if len(rows) != 1 {
		t.Fatalf("feedback rows = %d, want exactly 1: %v", len(rows), rows)
	}
	row := rows[0]

	// Well-formed: PR identity, original verdict, disagreement type, timestamp.
	if got := stringField(row, "pr"); got != prIdentity() {
		t.Errorf("row pr = %q, want %q", got, prIdentity())
	}
	if got := stringField(row, "original_verdict"); got != "slop" {
		t.Errorf("row original_verdict = %q, want %q", got, "slop")
	}
	if got := stringField(row, "disagreement_type"); got != "label-removed" {
		t.Errorf("row disagreement_type = %q, want %q", got, "label-removed")
	}
	if ts := stringField(row, "ts"); ts == "" {
		t.Errorf("row ts is empty; want an RFC3339 timestamp")
	} else if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("row ts = %q is not RFC3339: %v", ts, err)
	}

	// The feedback signal never carries PR content: no diff body, no title. A
	// stateless gateway records only the disagreement fact (FR-014).
	for k, v := range row {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if strings.Contains(s, "+new") || strings.Contains(s, cannedDiff) {
			t.Errorf("feedback row field %q leaked diff body: %q", k, s)
		}
	}

	// The feedback path is out-of-band: an `unlabeled` event triggers no triage
	// and no outward GitHub writes at all.
	time.Sleep(200 * time.Millisecond) // ponytail: short settle to catch any erroneous async write
	if n := gh.WriteCalls(); n != 0 {
		t.Errorf("label removal triggered %d GitHub writes, want 0", n)
	}
	if n := model.PredictCalls(); n != 0 {
		t.Errorf("label removal triggered %d model calls, want 0 (feedback is not triage)", n)
	}

	assertNoSecretsInLog(t, g, model, gh)
}

// readFeedbackRows reads the JSONL feedback log and returns each decoded row. A
// missing file (writer goroutine hasn't created it yet) reads as zero rows, so
// the caller's waitFor keeps polling rather than failing.
func readFeedbackRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil // not created yet
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("feedback row is not valid JSON: %q: %v", line, err)
		}
		rows = append(rows, m)
	}
	return rows
}

// stringField pulls a string field out of a decoded JSON row (missing → "").
func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
