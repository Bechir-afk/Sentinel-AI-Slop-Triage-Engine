package triage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGemini returns a canned generateContent response wrapping verdictJSON.
func fakeGemini(t *testing.T, verdictJSON string, check func(reqBody string)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if check != nil {
			check(string(body))
		}
		resp := geminiResponse{}
		resp.Candidates = append(resp.Candidates, struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		}{})
		resp.Candidates[0].Content.Parts = append(resp.Candidates[0].Content.Parts, struct {
			Text string `json:"text"`
		}{Text: verdictJSON})
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestAnalyze(t *testing.T) {
	var sawSchema bool
	srv := fakeGemini(t, `{"is_slop":true,"confidence":0.95,"reason":"boilerplate"}`, func(reqBody string) {
		if strings.Contains(reqBody, `"responseMimeType":"application/json"`) {
			sawSchema = true
		}
	})
	defer srv.Close()

	c := New("key", srv.URL)
	res, err := c.Analyze(context.Background(), "Add stuff", "diff --git ...")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.IsSlop || res.Confidence != 0.95 || res.Reason != "boilerplate" {
		t.Errorf("verdict = %+v", res)
	}
	if !sawSchema {
		t.Error("request did not set responseMimeType to application/json")
	}
}

func TestAnalyzeTruncatesLargeDiff(t *testing.T) {
	var reqLen int
	srv := fakeGemini(t, `{"is_slop":false,"confidence":0.1,"reason":"ok"}`, func(reqBody string) {
		reqLen = len(reqBody)
	})
	defer srv.Close()

	huge := strings.Repeat("x", maxDiffBytes*2)
	c := New("key", srv.URL)
	if _, err := c.Analyze(context.Background(), "big", huge); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if reqLen >= maxDiffBytes*2 {
		t.Errorf("diff not truncated: request body %d bytes", reqLen)
	}
}

func TestAnalyzeAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New("key", srv.URL)
	if _, err := c.Analyze(context.Background(), "t", "d"); err == nil {
		t.Fatal("expected error on 429, got nil")
	}
}

func TestAnalyzeEmptyCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"candidates":[]}`)
	}))
	defer srv.Close()

	c := New("key", srv.URL)
	if _, err := c.Analyze(context.Background(), "t", "d"); err == nil {
		t.Fatal("expected error on empty candidates, got nil")
	}
}
