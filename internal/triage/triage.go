// Package triage is SYSTEM_FLOW stage 4: send a PR's title and diff to the
// self-hosted model service and get back a strict-JSON verdict. Plain stdlib
// net/http POST to {MODEL_URL}/predict, so the gateway stays dependency-free
// and testable offline. No external AI API.
package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxDiffBytes caps how much diff is sent to the model. Oversized diffs are
// truncated to bound latency and token cost; the tail rarely changes a
// slop/legit verdict.
const maxDiffBytes = 50 * 1024

// Result is the model's verdict, matching the /predict response contract.
type Result struct {
	IsSlop     bool    `json:"is_slop"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// Client calls the model service /predict endpoint.
type Client struct {
	http *http.Client
	base string
}

// New returns a Client. base is the model service root (e.g. http://sentinel-model:9000).
func New(base string) *Client {
	return &Client{
		http: &http.Client{Timeout: 30 * time.Second},
		base: base,
	}
}

// predictRequest is the JSON body posted to the model service.
type predictRequest struct {
	Title string `json:"title"`
	Diff  string `json:"diff"`
}

// Health is the model service /healthz response (predict-endpoint contract).
type Health struct {
	Status    string  `json:"status"`
	Threshold float64 `json:"threshold"`
	Artifact  string  `json:"artifact"`
}

// FetchHealth reads the model service /healthz. The gateway uses this at
// startup to source its default confidence threshold from the artifact
// (research R6). Best-effort: any error is the caller's cue to fall back.
func (c *Client) FetchHealth(ctx context.Context) (*Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model healthz: status %d: %s", resp.StatusCode, raw)
	}
	var h Health
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("model healthz: decode: %w", err)
	}
	return &h, nil
}

// Analyze returns the triage verdict for a PR.
func (c *Client) Analyze(ctx context.Context, title, diff string) (*Result, error) {
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes] + "\n...[diff truncated]..."
	}

	buf, err := json.Marshal(predictRequest{Title: title, Diff: diff})
	if err != nil {
		return nil, err
	}

	url := c.base + "/predict"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model: status %d: %s", resp.StatusCode, raw)
	}

	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("model: decode verdict: %w", err)
	}
	return &result, nil
}
