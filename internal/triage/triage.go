// Package triage is SYSTEM_FLOW stage 4: send a PR's title and diff to
// Gemini and get back a strict-JSON verdict. Uses the Gemini REST endpoint
// over stdlib net/http with responseSchema, so the whole service stays
// dependency-free and testable offline.
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

const defaultModel = "gemini-2.5-flash"

const systemPrompt = `You are a strict maintainer triaging GitHub pull requests for "AI slop":
low-effort, AI-generated changes that are superficial, boilerplate, hallucinated,
or add no real value. Judge the PR from its title and unified diff.
Set is_slop true only when you are confident the change is slop.
confidence is your certainty in that verdict, from 0.0 to 1.0.
reason is one concise sentence a maintainer can read.`

// Result is the model's verdict, matching the enforced response schema.
type Result struct {
	IsSlop     bool    `json:"is_slop"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// Client calls the Gemini generateContent endpoint.
type Client struct {
	http   *http.Client
	apiKey string
	base   string
	model  string
}

// New returns a Client. base is the Gemini API root.
func New(apiKey, base string) *Client {
	return &Client{
		http:   &http.Client{Timeout: 30 * time.Second},
		apiKey: apiKey,
		base:   base,
		model:  defaultModel,
	}
}

// Analyze returns the triage verdict for a PR.
func (c *Client) Analyze(ctx context.Context, title, diff string) (*Result, error) {
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes] + "\n...[diff truncated]..."
	}

	reqBody := geminiRequest{
		Contents: []content{{Parts: []part{{Text: fmt.Sprintf(
			"%s\n\nPR title: %s\n\nUnified diff:\n%s", systemPrompt, title, diff)}}}},
		GenerationConfig: genConfig{
			ResponseMIMEType: "application/json",
			ResponseSchema:   resultSchema,
		},
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.base, c.model, c.apiKey)
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
		return nil, fmt.Errorf("gemini: status %d: %s", resp.StatusCode, raw)
	}

	var gr geminiResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("gemini: decode envelope: %w", err)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini: empty response")
	}

	var result Result
	if err := json.Unmarshal([]byte(gr.Candidates[0].Content.Parts[0].Text), &result); err != nil {
		return nil, fmt.Errorf("gemini: decode verdict: %w", err)
	}
	return &result, nil
}

// --- Gemini REST wire types ---

type geminiRequest struct {
	Contents         []content `json:"contents"`
	GenerationConfig genConfig `json:"generationConfig"`
}

type content struct {
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type genConfig struct {
	ResponseMIMEType string `json:"responseMimeType"`
	ResponseSchema   schema `json:"responseSchema"`
}

type schema struct {
	Type       string            `json:"type"`
	Properties map[string]schema `json:"properties,omitempty"`
	Required   []string          `json:"required,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// resultSchema forces the model to return exactly the Result shape.
var resultSchema = schema{
	Type: "object",
	Properties: map[string]schema{
		"is_slop":    {Type: "boolean"},
		"confidence": {Type: "number"},
		"reason":     {Type: "string"},
	},
	Required: []string{"is_slop", "confidence", "reason"},
}
