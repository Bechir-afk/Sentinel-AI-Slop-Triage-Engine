// Package webhook parses GitHub pull_request events and orchestrates the
// triage pipeline (SYSTEM_FLOW stages 2-5).
package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"sentinel/internal/config"
	"sentinel/internal/triage"
)

// actionable lists the pull_request actions that carry new or changed code
// worth triaging. Other actions (labeled, closed, assigned, ...) are ignored.
var actionable = map[string]bool{
	"opened":      true,
	"reopened":    true,
	"synchronize": true,
}

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
	} `json:"pull_request"`
	Repository struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`
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
}

// Handler wires config + collaborators into an http.Handler for /webhook.
type Handler struct {
	cfg     config.Config
	gh      GitHub
	triager Triager
}

// New builds a webhook handler.
func New(cfg config.Config, gh GitHub, triager Triager) *Handler {
	return &Handler{cfg: cfg, gh: gh, triager: triager}
}

// ServeHTTP runs stages 2-5. It always responds 200 for well-formed events it
// accepts, and fails open (200, no action) on downstream errors so a flaky AI
// call or GitHub hiccup never blocks a legitimate contributor. The raw body is
// injected by the verify middleware after HMAC checking.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	// Stage 2: parse payload.
	var ev event
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !actionable[ev.Action] {
		writeOK(w, "ignored action: "+ev.Action)
		return
	}

	owner, repo, num := ev.Repository.Owner.Login, ev.Repository.Name, ev.Number
	log.Printf("triaging %s/%s#%d by %s: %q", owner, repo, num, ev.PullRequest.User.Login, ev.PullRequest.Title)

	// Stage 3: fetch diff. Fail open on error.
	diff, err := h.gh.FetchDiff(ctx, owner, repo, num)
	if err != nil {
		log.Printf("fetch diff failed for %s/%s#%d: %v", owner, repo, num, err)
		writeOK(w, "diff fetch failed; skipped")
		return
	}

	// Stage 4: triage. Fail open on error.
	res, err := h.triager.Analyze(ctx, ev.PullRequest.Title, diff)
	if err != nil {
		log.Printf("triage failed for %s/%s#%d: %v", owner, repo, num, err)
		writeOK(w, "triage failed; skipped")
		return
	}
	log.Printf("verdict %s/%s#%d: is_slop=%t confidence=%.2f", owner, repo, num, res.IsSlop, res.Confidence)

	// Stage 5: act only on high-confidence slop. Never auto-close.
	if !res.IsSlop || res.Confidence < h.cfg.ConfidenceThreshold {
		writeOK(w, "no action")
		return
	}
	h.act(ctx, owner, repo, num, res)
	writeOK(w, "flagged")
}

// act applies the label then posts the comment. Errors are logged, not fatal:
// a successful label with a failed comment is still useful.
func (h *Handler) act(ctx context.Context, owner, repo string, num int, res *triage.Result) {
	if err := h.gh.AddLabel(ctx, owner, repo, num, h.cfg.SlopLabel); err != nil {
		log.Printf("add label failed for %s/%s#%d: %v", owner, repo, num, err)
	}
	if err := h.gh.PostComment(ctx, owner, repo, num, comment(res.Reason)); err != nil {
		log.Printf("post comment failed for %s/%s#%d: %v", owner, repo, num, err)
	}
}

// comment builds the polite explanation posted to a flagged PR.
func comment(reason string) string {
	if reason == "" {
		reason = "the change appears to be low-effort or automatically generated."
	}
	return "👋 Thanks for the contribution! This PR was automatically flagged for human review because " +
		reason + "\n\nA maintainer will take a look. If you believe this was a mistake, please add context explaining the change. — _Sentinel_"
}

func writeOK(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(msg))
}
