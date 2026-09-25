// Entity: Integration Harness (003 spec Key Entities) — boots the shipped
// sentinel binary as a black box and tears it down cleanly.
//
// TestMain `go build`s ./cmd/sentinel once to a temp path, then each scenario
// boots it via os/exec with env wiring GITHUB_API_BASE/MODEL_URL at the stubs.
// The harness never imports internal/ — it observes exactly what ships (T006,
// FR-013). It runs identically on Windows and Linux (FR-012): the binary name
// carries the OS-appropriate suffix and ports are chosen by asking the OS for a
// free one, never hard-coded.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// binPath is the built gateway binary, set once by TestMain.
var binPath string

// startupBudget bounds how long bring-up may take before the harness declares
// the gateway unhealthy (US1 SC-001). waitBudget bounds an async completion
// signal (a stub write landing or a /stats counter advancing).
const (
	startupBudget = 20 * time.Second
	waitBudget    = 15 * time.Second
	pollInterval  = 25 * time.Millisecond
)

// TestMain builds the real binary once for the whole package. A build failure
// fails fast with the compiler output rather than every scenario timing out on
// a missing binary.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sentinel-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "sentinel"+exeSuffix())
	// Build from the repo root (two levels up from test/integration).
	build := exec.Command("go", "build", "-o", binPath, "./cmd/sentinel")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build ./cmd/sentinel failed: %v\n%s", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// repoRoot returns the repository root: this file lives at
// test/integration/harness_test.go, so the root is two directories up.
func repoRoot() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(self)))
}

// gateway is one booted sentinel process plus the address it listens on.
type gateway struct {
	t      *testing.T
	cmd    *exec.Cmd
	port   string
	secret string
	logBuf *syncBuffer
}

// bootOptions tunes a single gateway boot. Zero value is the golden-path setup:
// stubs wired, a fresh secret, threshold sourced from the model, no shadow mode,
// no feedback log.
type bootOptions struct {
	github        *GitHubStub
	model         *ModelStub
	secret        string            // GITHUB_WEBHOOK_SECRET; generated if empty
	shadowMode    bool              // SHADOW_MODE=true
	feedbackLog   string            // FEEDBACK_LOG path; unset disables capture
	thresholdEnv  string            // CONFIDENCE_THRESHOLD; unset ⇒ sourced from model
	extraEnv      map[string]string // any additional env overrides
}

// boot builds the env, starts the binary on a free port, and waits for
// /healthz. It registers cleanup so the process is always torn down. A
// port-in-use or a boot that never reaches healthy fails the test with a clear
// message (edge case: port already in use).
func boot(t *testing.T, opts bootOptions) *gateway {
	t.Helper()

	secret := opts.secret
	if secret == "" {
		secret = "test-webhook-secret"
	}
	port, err := freePort()
	if err != nil {
		t.Fatalf("could not find a free port: %v", err)
	}

	env := map[string]string{
		"PORT":                  port,
		"GITHUB_WEBHOOK_SECRET": secret,
		"GITHUB_TOKEN":          "dummy-token-not-used-against-stub",
		"SHADOW_MODE":           boolEnv(opts.shadowMode),
	}
	if opts.github != nil {
		env["GITHUB_API_BASE"] = opts.github.URL()
	}
	if opts.model != nil {
		env["MODEL_URL"] = opts.model.URL()
	}
	if opts.feedbackLog != "" {
		env["FEEDBACK_LOG"] = opts.feedbackLog
	}
	if opts.thresholdEnv != "" {
		env["CONFIDENCE_THRESHOLD"] = opts.thresholdEnv
	}
	for k, v := range opts.extraEnv {
		env[k] = v
	}

	logBuf := &syncBuffer{}
	cmd := exec.Command(binPath)
	cmd.Dir = repoRoot()
	cmd.Env = flatEnv(env)
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf // gateway logs JSON to stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	g := &gateway{t: t, cmd: cmd, port: port, secret: secret, logBuf: logBuf}
	t.Cleanup(g.stop)

	if err := g.waitHealthy(); err != nil {
		// Surface the process log so a boot failure (port in use, bad env) is
		// diagnosable — the log is JSON status lines, never a secret/diff (FR-014).
		t.Fatalf("gateway did not become healthy within %s: %v\n--- gateway log ---\n%s",
			startupBudget, err, logBuf.String())
	}
	return g
}

// waitHealthy polls /healthz until it returns 200 or the startup budget expires.
func (g *gateway) waitHealthy() error {
	deadline := time.Now().Add(startupBudget)
	client := &http.Client{Timeout: 2 * time.Second}
	url := g.base() + "/healthz"
	var lastErr error
	for time.Now().Before(deadline) {
		// If the process died (e.g. missing required env), stop polling early.
		if g.cmd.ProcessState != nil && g.cmd.ProcessState.Exited() {
			return fmt.Errorf("process exited during startup: %s", g.cmd.ProcessState)
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("healthz status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(pollInterval)
	}
	return lastErr
}

// stop terminates the gateway. Kill (not a graceful signal) keeps teardown fast
// and identical on Windows and POSIX — the process holds no durable state worth
// draining in a test.
func (g *gateway) stop() {
	if g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_, _ = g.cmd.Process.Wait()
	}
}

// base is the gateway's http://127.0.0.1:PORT.
func (g *gateway) base() string { return "http://127.0.0.1:" + g.port }

// send performs an HTTP request against the gateway and returns the response
// status. The body is drained and discarded.
func (g *gateway) send(req *http.Request) (int, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// stats fetches and decodes /stats. Counters are the observable signal waitFor
// polls for async completion.
func (g *gateway) stats() (map[string]any, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(g.base() + "/stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// healthVersion fetches /healthz and returns the reported build version.
func (g *gateway) healthVersion() (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(g.base() + "/healthz")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return "", err
	}
	return m["version"], nil
}

// logs returns the captured process output so far (JSON status lines).
func (g *gateway) logs() string { return g.logBuf.String() }

// waitFor polls cond until it returns true or waitBudget expires. This is the
// determinism primitive (edge case): scenarios wait on an observable signal — a
// stub write landing or a /stats counter advancing — never a fixed sleep, so the
// suite is not flaky under CI load.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out after %s waiting for: %s", waitBudget, what)
}

// statCounter reads one integer counter out of /stats (JSON numbers decode as
// float64). Missing counter reads as 0.
func (g *gateway) statCounter(name string) int {
	m, err := g.stats()
	if err != nil {
		return 0
	}
	if v, ok := m[name].(float64); ok {
		return int(v)
	}
	return 0
}

// --- env + port + log helpers ---

// freePort asks the OS for an unused TCP port and releases it. There is a
// tiny race between release and the gateway binding it; in practice the OS does
// not immediately hand the same port to another listener, and a genuine
// collision surfaces as a clear "did not become healthy" failure rather than a
// hang. ponytail: accept the well-known ephemeral-port TOCTOU rather than build
// a bind-handoff protocol; the upgrade path is passing a pre-bound listener FD.
func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

// flatEnv merges the given overrides onto the current environment as a KEY=VALUE
// slice for exec.Cmd. Starting from os.Environ keeps PATH etc. so `go`-built
// binaries and the OS loader still work.
func flatEnv(overrides map[string]string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(overrides))
	// Drop any inherited copy of a key we override, so ours wins on all platforms.
	for _, kv := range base {
		k := kv[:strings.IndexByte(kv, '=')]
		if _, ok := overrides[k]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

func boolEnv(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// syncBuffer is a concurrency-safe buffer capturing the gateway's stderr, which
// is written from the process's I/O goroutine while a test reads it. Guarding
// with a mutex avoids a data race between the two (go test -race).
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
