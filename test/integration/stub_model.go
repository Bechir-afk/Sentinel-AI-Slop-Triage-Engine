// Package integration is a black-box driver for the shipped sentinel binary.
//
// It never imports internal/ — it `go build`s ./cmd/sentinel, boots it via
// os/exec, points GITHUB_API_BASE/MODEL_URL at the local stubs in this package
// by env, and asserts on the real request bytes the gateway sends. Stubs and
// helpers are plain .go files (not _test.go) so both the CI subset and the local
// full-stack run share them; scenarios are the _test.go files.
//
// Entity: Model Stub (003 spec Key Entities) — an httptest server mirroring the
// real model contract in model/app.py (GET /healthz, POST /predict) with no
// torch, so the gateway's threshold-sourcing + triage paths run unchanged.
package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// modelVerdict mirrors model/app.py PredictResponse exactly: the four fields the
// gateway's triage.Result decodes ({is_slop, confidence, reason, version}).
type modelVerdict struct {
	IsSlop     bool    `json:"is_slop"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Version    string  `json:"version"`
}

// ModelStub stands in for the self-hosted model service. It serves /healthz
// (so the gateway sources its threshold over the network) and /predict (the
// triage verdict). Every knob is guarded by mu so a scenario can retune it
// between deliveries without racing the gateway's worker pool.
type ModelStub struct {
	Server *httptest.Server

	closeOnce sync.Once // Close is idempotent: a drill may take the model down mid-test AND defer a cleanup Close

	mu sync.Mutex
	// health payload
	threshold float64
	artifact  string
	version   string
	// predict payload (default: high-confidence slop, per T002)
	verdict modelVerdict
	// drill injectors
	predictStatus int           // if >=400, /predict returns this instead of a verdict
	healthStatus  int           // if >=400, /healthz returns this
	predictDelay  time.Duration // injected latency before /predict responds
	predictCalls  int
	healthCalls   int
}

// NewModelStub starts a model stub with the T002 default: a reachable model
// advertising a threshold on /healthz and returning high-confidence slop.
func NewModelStub() *ModelStub {
	m := &ModelStub{
		threshold: 0.80, // distinct from the gateway's 0.95 fallback, so US1 can prove sourcing
		artifact:  "stub-artifact-v1",
		version:   "stub-model-1.0",
		verdict: modelVerdict{
			IsSlop:     true,
			Confidence: 0.99,
			Reason:     "stub: high-confidence slop",
			Version:    "stub-model-1.0",
		},
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.route))
	return m
}

func (m *ModelStub) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		m.healthz(w)
	case r.Method == http.MethodPost && r.URL.Path == "/predict":
		m.predict(w)
	default:
		http.NotFound(w, r)
	}
}

func (m *ModelStub) healthz(w http.ResponseWriter) {
	m.mu.Lock()
	m.healthCalls++
	status, thr, art, ver := m.healthStatus, m.threshold, m.artifact, m.version
	m.mu.Unlock()

	if status >= 400 {
		w.WriteHeader(status)
		return
	}
	// Mirror model/app.py GET /healthz exactly.
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"threshold": thr,
		"artifact":  art,
		"version":   ver,
	})
}

func (m *ModelStub) predict(w http.ResponseWriter) {
	m.mu.Lock()
	m.predictCalls++
	status, delay, v := m.predictStatus, m.predictDelay, m.verdict
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if status >= 400 {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// URL is the base the gateway's MODEL_URL points at.
func (m *ModelStub) URL() string { return m.Server.URL }

// Close shuts the stub down (deferred by the harness). Idempotent: the
// model-unreachable drill closes the server to simulate an outage, and the
// per-case cleanup defers a second Close — httptest.Server.Close panics on a
// double call, so guard it with a sync.Once.
func (m *ModelStub) Close() { m.closeOnce.Do(func() { m.Server.Close() }) }

// --- knobs (drills) ---

// SetVerdict tunes the /predict response (verdict, confidence, reason, version).
func (m *ModelStub) SetVerdict(v modelVerdict) {
	m.mu.Lock()
	m.verdict = v
	m.mu.Unlock()
}

// SetThreshold tunes the threshold advertised on /healthz (US1 sourcing proof).
func (m *ModelStub) SetThreshold(t float64) {
	m.mu.Lock()
	m.threshold = t
	m.mu.Unlock()
}

// InjectPredictStatus makes /predict return an error status (model-5xx drill).
// Pass 0 to clear.
func (m *ModelStub) InjectPredictStatus(code int) {
	m.mu.Lock()
	m.predictStatus = code
	m.mu.Unlock()
}

// InjectHealthStatus makes /healthz return an error status. Pass 0 to clear.
func (m *ModelStub) InjectHealthStatus(code int) {
	m.mu.Lock()
	m.healthStatus = code
	m.mu.Unlock()
}

// InjectPredictDelay adds latency before /predict responds (timeout drill).
func (m *ModelStub) InjectPredictDelay(d time.Duration) {
	m.mu.Lock()
	m.predictDelay = d
	m.mu.Unlock()
}

// PredictCalls / HealthCalls report how many times each endpoint was hit — the
// observable signal shadow-mode and fail-open drills wait on.
func (m *ModelStub) PredictCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.predictCalls
}

func (m *ModelStub) HealthCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthCalls
}

// Threshold reports the currently advertised threshold (for assertions).
func (m *ModelStub) Threshold() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.threshold
}

// writeJSON is shared by both stubs — the one JSON-encode helper for this package.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
