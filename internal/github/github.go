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
	"time"
)

// Client talks to the GitHub REST API with a bearer token.
type Client struct {
	http  *http.Client
	token string
	base  string
}

// New returns a Client. base is the API root (e.g. https://api.github.com).
func New(token, base string) *Client {
	return &Client{
		http:  &http.Client{Timeout: 15 * time.Second},
		token: token,
		base:  base,
	}
}

// FetchDiff returns the unified diff for a pull request (stage 3).
func (c *Client) FetchDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.base, owner, repo, number)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch diff: status %d: %s", resp.StatusCode, body)
	}
	return string(body), nil
}

// AddLabel appends a label to the PR without removing existing ones (stage 5).
func (c *Client) AddLabel(ctx context.Context, owner, repo string, number int, label string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels", c.base, owner, repo, number)
	payload := map[string][]string{"labels": {label}}
	return c.postJSON(ctx, url, payload)
}

// PostComment posts a comment on the PR's conversation (stage 5).
func (c *Client) PostComment(ctx context.Context, owner, repo string, number int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.base, owner, repo, number)
	payload := map[string]string{"body": body}
	return c.postJSON(ctx, url, payload)
}

func (c *Client) postJSON(ctx context.Context, url string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, msg)
	}
	return nil
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
