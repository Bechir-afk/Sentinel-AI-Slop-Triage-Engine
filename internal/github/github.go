// Package github is the REST client for SYSTEM_FLOW stages 3 and 5: fetch a
// PR's diff, and (on high-confidence slop) add a label and post a comment.
// Plain net/http — only three calls, so the go-github dependency is avoided.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client talks to the GitHub REST API with a bearer token.
type Client struct {
	http       *http.Client
	token      string
	base       string
	maxRetries int
}

// New returns a Client. base is the API root (e.g. https://api.github.com).
// maxRetries bounds transient-error retries (5xx / rate-limit); 0 disables them.
func New(token, base string, maxRetries int) *Client {
	return &Client{
		http:       &http.Client{Timeout: 15 * time.Second},
		token:      token,
		base:       base,
		maxRetries: maxRetries,
	}
}

// baseBackoff is the first retry delay; it doubles each attempt. Kept small so a
// retry sequence fits inside the worker's triage budget on a healthy blip.
const baseBackoff = 500 * time.Millisecond

// FetchDiff returns the unified diff for a pull request (stage 3).
func (c *Client) FetchDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.base, esc(owner), esc(repo), number)

	body, status, err := c.doRetrying(ctx, func() (*http.Request, error) {
		req, err := c.newRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github.v3.diff")
		return req, nil
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("fetch diff: status %d: %s", status, body)
	}
	return string(body), nil
}

// AddLabel appends a label to the PR without removing existing ones (stage 5).
func (c *Client) AddLabel(ctx context.Context, owner, repo string, number int, label string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels", c.base, esc(owner), esc(repo), number)
	payload := map[string][]string{"labels": {label}}
	return c.postJSON(ctx, url, payload)
}

// PostComment posts a comment on the PR's conversation (stage 5).
func (c *Client) PostComment(ctx context.Context, owner, repo string, number int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.base, esc(owner), esc(repo), number)
	payload := map[string]string{"body": body}
	return c.postJSON(ctx, url, payload)
}

// checkRunName is the fixed name Sentinel gives its Check Run. GitHub keys check
// runs by (name, head_sha), so a redelivery for the same commit updates the
// existing run instead of stacking duplicates.
const checkRunName = "Sentinel AI-slop triage"

// CreateCheckRun posts an observational Check Run on the PR head commit reporting
// the verdict (stage 5, alongside the label + comment). It is always
// status=completed; the caller passes a non-failing conclusion ("neutral") so
// the run NEVER becomes a required or blocking status — Sentinel reports a
// verdict, it does not gate the PR (FR-014). A crafted owner/repo is escaped like
// every other path (FR-010).
func (c *Client) CreateCheckRun(ctx context.Context, owner, repo, headSHA, conclusion, summary string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/check-runs", c.base, esc(owner), esc(repo))
	payload := map[string]any{
		"name":       checkRunName,
		"head_sha":   headSHA,
		"status":     "completed",
		"conclusion": conclusion,
		"output":     map[string]string{"title": checkRunName, "summary": summary},
	}
	return c.postJSON(ctx, url, payload)
}

// esc path-escapes an owner or repo before it is interpolated into a REST URL.
// GitHub owner/repo names are constrained, but the values arrive from the webhook
// payload (a trust boundary), so a crafted "../" or slash-bearing segment must not
// be able to reshape the path (FR-010).
func esc(s string) string { return url.PathEscape(s) }

func (c *Client) postJSON(ctx context.Context, url string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body, status, err := c.doRetrying(ctx, func() (*http.Request, error) {
		// Fresh body reader per attempt: a retried request must re-read the body.
		req, err := c.newRequest(ctx, http.MethodPost, url, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("POST %s: status %d: %s", url, status, body)
	}
	return nil
}

// doRetrying sends the request built by mk and retries transient failures —
// transport errors, 5xx, and rate-limit 403/429 — up to c.maxRetries times with
// exponential backoff. It never sleeps past the context deadline: a backoff that
// would exceed the remaining triage budget short-circuits and returns the last
// result, so the caller fails open promptly rather than blocking on a dead
// GitHub (FR-009). mk is called once per attempt so each retry gets a fresh
// request (and a re-readable body).
func (c *Client) doRetrying(ctx context.Context, mk func() (*http.Request, error)) (body []byte, status int, err error) {
	backoff := baseBackoff
	for attempt := 0; ; attempt++ {
		var req *http.Request
		req, err = mk()
		if err != nil {
			return nil, 0, err
		}

		var resp *http.Response
		resp, err = c.http.Do(req)
		if err == nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			status = resp.StatusCode
			if !retryable(status) {
				return body, status, nil
			}
		}
		// Out of retries → return the last outcome (error or retryable status).
		if attempt >= c.maxRetries {
			return body, status, err
		}

		wait := backoff
		if hint := retryAfter(resp); hint > wait {
			wait = hint // honor the server's Retry-After when it asks for longer
		}
		// Never sleep past the caller's deadline: if the budget can't cover the
		// wait, stop now and let the caller fail open with the last result.
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= wait {
			return body, status, err
		}
		select {
		case <-ctx.Done():
			return body, status, ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

// retryable reports whether an HTTP status is worth retrying: any 5xx, or the
// 403/429 GitHub uses for rate limiting (a retry-after signal is honored in
// retryAfter). A 403 without rate-limit headers is still retried once under the
// bound — cheap, and secondary rate limits sometimes omit them.
func retryable(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests || status == http.StatusForbidden
}

// retryAfter extracts a delay from GitHub's throttling headers: Retry-After
// (seconds) or the time until x-ratelimit-reset (unix seconds). Returns 0 when
// neither is present or usable.
func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if v := resp.Header.Get("x-ratelimit-reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Until(time.Unix(unix, 0)); d > 0 {
				return d
			}
		}
	}
	return 0
}

func (c *Client) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return req, nil
}
