package triage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeModel returns a canned /predict response wrapping verdictJSON.
func fakeModel(t *testing.T, verdictJSON string, check func(reqBody string)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if check != nil {
			check(string(body))
		}
		io.WriteString(w, verdictJSON)
	}))
}

func TestAnalyze(t *testing.T) {
	var sawTitle bool
	srv := fakeModel(t, `{"is_slop":true,"confidence":0.95,"reason":"boilerplate"}`, func(reqBody string) {
		if strings.Contains(reqBody, `"title":"Add stuff"`) {
			sawTitle = true
		}
	})
	defer srv.Close()

	c := New(srv.URL)
	res, err := c.Analyze(context.Background(), "Add stuff", "diff --git ...")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.IsSlop || res.Confidence != 0.95 || res.Reason != "boilerplate" {
		t.Errorf("verdict = %+v", res)
	}
	if !sawTitle {
		t.Error("request did not carry the PR title")
	}
}

func TestAnalyzeTruncatesLargeDiff(t *testing.T) {
	var reqLen int
	srv := fakeModel(t, `{"is_slop":false,"confidence":0.1,"reason":"ok"}`, func(reqBody string) {
		reqLen = len(reqBody)
	})
	defer srv.Close()

	huge := strings.Repeat("x", maxDiffBytes*2)
	c := New(srv.URL)
	if _, err := c.Analyze(context.Background(), "big", huge); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if reqLen >= maxDiffBytes*2 {
		t.Errorf("diff not truncated: request body %d bytes", reqLen)
	}
}

func TestAnalyzeAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL)
	if _, err := c.Analyze(context.Background(), "t", "d"); err == nil {
		t.Fatal("expected error on 503, got nil")
	}
}

func TestAnalyzeBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `not json`)
	}))
	defer srv.Close()

	c := New(srv.URL)
	if _, err := c.Analyze(context.Background(), "t", "d"); err == nil {
		t.Fatal("expected error on malformed verdict, got nil")
	}
}
