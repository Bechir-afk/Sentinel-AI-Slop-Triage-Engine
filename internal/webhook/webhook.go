// Package webhook parses GitHub pull_request events and orchestrates the
// triage pipeline (SYSTEM_FLOW stages 2-5).
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/triage"
)

const (
	// triageTimeout bounds a single async triage. The worker uses a fresh
	// context.Background() with this deadline — never the request context —
	// so triage survives the acked connection closing (FR-001/FR-002).
	triageTimeout = 45 * time.Second
	// ledgerCapacity is the dedup window (recent delivery IDs).
	ledgerCapacity = 1024
	// queueCapacity buffers accepted jobs handed to the worker pool.
	queueCapacity = 256
)

// actionable lists the pull_request actions that carry new or changed code
// worth triaging. Other actions (labeled, closed, assigned, ...) are ignored.
var actionable = map[string]bool{
	"opened":      true,
	"reopened":    true,
	"synchronize": true,
}

const (
	eventHeader    = "X-GitHub-Event"    // GitHub's event-type header
	deliveryHeader = "X-GitHub-Delivery" // unique per delivery; the idempotency key
)

// event is the subset of the GitHub pull_request webhook payload we need.
// The payload does NOT contain the diff itself, only enough to fetch it.
type event struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Title string `json:"title"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`
	// Label is populated on "labeled"/"unlabeled" actions — the label added or
	// removed. A maintainer removing the slop label is the disagreement signal.
	Label struct {
		Name string `json:"name"`
	} `json:"label"`
}

// Triager evaluates a code diff. Implemented by *triage.Client.
type Triager interface {
	Analyze(ctx context.Context, title, diff string) (*triage.Result, error)
}

// GitHub performs the read + write GitHub API calls. Implemented by *github.Client.
type GitHub interface {
	FetchDiff(ctx context.Context, owner, repo string, number int) (string, error)
	AddLabel(ctx context.Context, owner, repo string, number int, label string) error
	PostComment(ctx context.Context, owner, repo string, number int, body string) error
	CreateCheckRun(ctx context.Context, owner, repo, headSHA, conclusion, summary string) error
}

// job is one accepted delivery handed from the request goroutine to a worker.
// It carries only what the pipeline needs — no request, no response writer —
// so processing is fully decoupled from the (already acked) connection.
type job struct {
	deliveryID string
	owner      string
	repo       string
	number     int
	title      string
	author     string
	headSHA    string
}

// Handler wires config + collaborators into an http.Handler for /webhook.
// Deliveries are acknowledged immediately and processed by a worker pool, so
// a slow model or GitHub call never holds the webhook connection open.
type Handler struct {
	cfg     config.Config
	gh      GitHub
	triager Triager

	ledger    *Ledger
	stats     Stats
	feedback  *FeedbackLog // nil when FEEDBACK_LOG is unset (capture disabled)
	jobs      chan job
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

// Stats returns a point-in-time snapshot of the operational counters for the
// /stats endpoint (FR-003).
func (h *Handler) Stats() Snapshot { return h.stats.Snapshot() }

// New builds a webhook handler. Call Start to launch the worker pool before
// serving, and Shutdown to drain it on exit. A non-empty feedbackPath enables
// the out-of-band maintainer-disagreement log (FR-015); "" disables capture.
func New(cfg config.Config, gh GitHub, triager Triager) *Handler {
	h := &Handler{
		cfg:     cfg,
		gh:      gh,
		triager: triager,
		ledger:  NewLedger(ledgerCapacity),
		jobs:    make(chan job, queueCapacity),
	}
	if cfg.FeedbackLogPath != "" {
		h.feedback = NewFeedbackLog(cfg.FeedbackLogPath)
	}
	return h
}

// Start launches the worker pool (idempotent). Worker count comes from config
// (clamped to at least 1). Workers run until Shutdown closes the queue. The
// feedback-log writer (when enabled) is started alongside.
func (h *Handler) Start() {
	h.startOnce.Do(func() {
		if h.feedback != nil {
			h.feedback.Start()
		}
		workers := h.cfg.WorkerCount
		if workers < 1 {
			workers = 1
		}
		for i := 0; i < workers; i++ {
			h.wg.Add(1)
			go h.worker()
		}
	})
}

// Shutdown stops accepting new jobs and waits for in-flight triage to drain,
// bounded by ctx. It is safe to call once; a second call is a no-op. The
// feedback log is flushed after workers drain so any queued signal is written.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.stopOnce.Do(func() { close(h.jobs) })

	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		if h.feedback != nil {
			h.feedback.Close()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// worker consumes jobs until the queue is closed, running each on a fresh,
// bounded context so triage outlives the acked HTTP connection. The delivery ID
// rides on the context so the model call carries the same correlation ID (FR-002).
func (h *Handler) worker() {
	defer h.wg.Done()
	for j := range h.jobs {
		ctx, cancel := context.WithTimeout(context.Background(), triageTimeout)
		ctx = triage.WithCorrelationID(ctx, j.deliveryID)
		h.process(ctx, j)
		cancel()
	}
}

// ServeHTTP validates and enqueues, then acks immediately (FR-001). Parsing,
// event/action gating, bot-author skip, and dedup all happen synchronously so
// the response is honest about what was accepted; the diff fetch, model call,
// and GitHub writes (stages 3-5) run later on a worker via process. It fails
// open on every downstream error so a flaky model or GitHub hiccup never
// blocks a legitimate contributor. The raw body is injected by the verify
// middleware after HMAC checking.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Only pull_request events carry code to triage. Any other (or missing) event
	// type is acknowledged and ignored with zero API calls (FR-003).
	if ev := r.Header.Get(eventHeader); ev != "pull_request" {
		writeOK(w, "ignored event: "+ev)
		return
	}
	// The delivery ID is the idempotency key. A pull_request event without one
	// is malformed.
	deliveryID := r.Header.Get(deliveryHeader)
	if deliveryID == "" {
		http.Error(w, "missing "+deliveryHeader, http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	// Stage 2: parse payload.
	var ev event
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	// Maintainer disagreement: removing the slop label is the feedback signal
	// (FR-015). It arrives as a pull_request "unlabeled" action — not an
	// actionable triage action — so capture it before the actionable gate drops
	// it, and before the bot-author skip (the actor here is the maintainer, not
	// the PR author). Only OUR slop label counts; any other removed label is a
	// normal non-actionable event.
	if ev.Action == "unlabeled" && ev.Label.Name == h.cfg.SlopLabel {
		if !h.ledger.Claim(deliveryID) { // dedup redeliveries like any other event
			writeOK(w, "duplicate delivery; skipped")
			return
		}
		writeOK(w, h.recordDisagreement(deliveryID, ev))
		return
	}

	if !actionable[ev.Action] {
		writeOK(w, "ignored action: "+ev.Action)
		return
	}

	author := ev.PullRequest.User.Login
	// Skip PRs opened by bots: a bot's output is not the low-effort human
	// contribution Sentinel triages, and flagging it is noise (FR-005).
	if isBot(author) {
		writeOK(w, "ignored bot author: "+author)
		return
	}

	// Idempotency: the first Claim for a delivery ID wins; a redelivery is
	// acked but not reprocessed, so no second label/comment is written.
	if !h.ledger.Claim(deliveryID) {
		writeOK(w, "duplicate delivery; skipped")
		return
	}

	j := job{
		deliveryID: deliveryID,
		owner:      ev.Repository.Owner.Login,
		repo:       ev.Repository.Name,
		number:     ev.Number,
		title:      ev.PullRequest.Title,
		author:     author,
		headSHA:    ev.PullRequest.Head.SHA,
	}

	// Non-blocking enqueue: a saturated queue fails open rather than holding the
	// connection (constitution: fail-open). ponytail: a dropped job is not
	// retried — the ceiling is queueCapacity vs burst; the upgrade path is a
	// larger buffer or backpressure with a 503 (which GitHub would redeliver).
	select {
	case h.jobs <- j:
		h.stats.incReceived()
		writeOK(w, "accepted")
	default:
		slog.Warn("queue full; job dropped",
			"delivery", deliveryID, "owner", j.owner, "repo", j.repo, "pr", j.number)
		writeOK(w, "queue full; skipped")
	}
}

// recordDisagreement captures a maintainer removing the slop label as one
// out-of-band feedback signal (FR-015) and returns the ack message. It is
// off the request-critical path: the Record hand-off is non-blocking and the
// file write happens on the feedback log's own goroutine, so the request still
// acks fast and holds no state.
//
// We fire only on removal of OUR configured slop label (checked by the caller),
// which in normal operation only Sentinel applies — so a removed slop label is
// evidence Sentinel flagged the PR, without needing per-PR memory. The signal
// records the honest fact "our slop label was removed"; it never fabricates a
// confidence number for a verdict the stateless gateway can't recall (spec edge
// case: never fabricate an original verdict).
//
// ponytail: confidence is not persisted at flag time (the gateway is
// stateless), so the signal carries PR + verdict + type but omits confidence —
// the offline ml/ join can recover it from the original flag comment if needed.
// The upgrade path is stamping the verdict into a persistent flag record if
// confidence-in-signal is ever required.
func (h *Handler) recordDisagreement(deliveryID string, ev event) string {
	pr := fmt.Sprintf("%s/%s#%d", ev.Repository.Owner.Login, ev.Repository.Name, ev.Number)
	lg := slog.With("delivery", deliveryID, "pr", pr, "disagreement", "label-removed")

	if h.feedback == nil {
		lg.Info("slop label removed; feedback capture disabled (FEEDBACK_LOG unset)")
		return "feedback capture disabled"
	}

	ok := h.feedback.Record(Signal{
		PR:               pr,
		OriginalVerdict:  "slop",
		DisagreementType: "label-removed",
		TS:               time.Now().UTC().Format(time.RFC3339),
	})
	if !ok {
		lg.Warn("feedback buffer full; signal dropped")
		return "feedback buffer full; skipped"
	}
	lg.Info("recorded maintainer disagreement")
	return "feedback recorded"
}

// process runs stages 3-5 for one accepted job on a worker goroutine. Every
// failure path is fail-open (log + return, PR untouched). A per-delivery logger
// carries the correlation ID onto every line so a single PR's journey is
// traceable end to end (FR-002); the diff body is never logged (FR-001).
func (h *Handler) process(ctx context.Context, j job) {
	lg := slog.With(
		"delivery", j.deliveryID,
		"owner", j.owner, "repo", j.repo, "pr", j.number,
	)
	lg.Info("triaging", "author", j.author, "title", j.title)

	// Stage 3: fetch diff. The latency clock spans the fetch + model call so the
	// summary reflects real end-to-end triage cost.
	start := time.Now()
	diff, err := h.gh.FetchDiff(ctx, j.owner, j.repo, j.number)
	if err != nil {
		h.stats.incFailed()
		lg.Error("fetch diff failed; failing open", "err", err)
		return
	}

	// Stage 4: triage.
	res, err := h.triager.Analyze(ctx, j.title, diff)
	if err != nil {
		h.stats.incFailed()
		lg.Error("triage failed; failing open", "err", err)
		return
	}
	h.stats.observeLatency(time.Since(start))
	lg.Info("verdict", "is_slop", res.IsSlop, "confidence", res.Confidence)

	// Stage 5: act only on high-confidence slop. Never auto-close.
	if !res.IsSlop || res.Confidence < h.cfg.ConfidenceThreshold {
		h.stats.incSkipped()
		return
	}
	h.stats.incFlagged()

	// Shadow mode: the full pipeline ran and produced a flag, but we write
	// nothing outward — log the would-be action and stop. Lets an operator
	// observe verdicts on real traffic before Sentinel ever speaks (FR-005).
	if h.cfg.ShadowMode {
		lg.Info("shadow mode: would flag (no label/comment/check-run written)",
			"confidence", res.Confidence, "reason", res.Reason)
		return
	}
	h.act(ctx, lg, j, res)
}

// isBot reports whether a GitHub login is a bot account. GitHub App bots carry
// the "[bot]" suffix on their login.
func isBot(login string) bool {
	return strings.HasSuffix(login, "[bot]")
}

// act applies the label, posts the comment, then creates the observational Check
// Run. Each is logged, not fatal: a successful label with a failed comment is
// still useful, and the Check Run is the least critical of the three, so its
// failure must not block the label/comment path (FR-014).
func (h *Handler) act(ctx context.Context, lg *slog.Logger, j job, res *triage.Result) {
	if err := h.gh.AddLabel(ctx, j.owner, j.repo, j.number, h.cfg.SlopLabel); err != nil {
		lg.Error("add label failed", "err", err)
	}
	if err := h.gh.PostComment(ctx, j.owner, j.repo, j.number, comment(res)); err != nil {
		lg.Error("post comment failed", "err", err)
	}
	// Check Run: a first-class verdict in the PR's checks list, alongside the
	// label + comment. Observational only — conclusion is "neutral", never a
	// failing/blocking status, so it can never gate the PR (FR-014). GitHub keys
	// it by (name, head_sha); a redelivery updates the run rather than stacking.
	// Skipped when the payload carried no head SHA (nothing to attach to).
	if j.headSHA == "" {
		lg.Warn("no head SHA in payload; skipping check run")
		return
	}
	if err := h.gh.CreateCheckRun(ctx, j.owner, j.repo, j.headSHA, "neutral", checkRunSummary(res)); err != nil {
		lg.Error("create check run failed; label/comment unaffected", "err", err)
	}
}

// comment builds the polite explanation posted to a flagged PR. It states the
// model's confidence and the artifact version alongside the reason so every
// action is auditable and a maintainer can report a bad call precisely (FR-006).
func comment(res *triage.Result) string {
	reason := res.Reason
	if reason == "" {
		reason = "the change appears to be low-effort or automatically generated."
	}
	version := res.Version
	if version == "" {
		version = "unknown"
	}
	return "👋 Thanks for the contribution! This PR was automatically flagged for human review because " +
		reason +
		fmt.Sprintf("\n\n_Model confidence: %.0f%% · artifact: `%s`_", res.Confidence*100, version) +
		"\n\nA maintainer will take a look. If you believe this was a mistake, please add context explaining the change. — _Sentinel_"
}

// checkRunSummary is the markdown body of the observational Check Run: the same
// verdict facts as the comment (reason, confidence, artifact version), phrased
// for the checks panel. It is informational only — the Check Run never gates the
// PR (FR-014).
func checkRunSummary(res *triage.Result) string {
	reason := res.Reason
	if reason == "" {
		reason = "the change appears to be low-effort or automatically generated."
	}
	version := res.Version
	if version == "" {
		version = "unknown"
	}
	return fmt.Sprintf(
		"Sentinel flagged this PR for human review because %s\n\n"+
			"- **Confidence:** %.0f%%\n- **Artifact:** `%s`\n\n"+
			"_This is an observational check, not a required status — it does not block merging._",
		reason, res.Confidence*100, version)
}

func writeOK(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(msg))
}
