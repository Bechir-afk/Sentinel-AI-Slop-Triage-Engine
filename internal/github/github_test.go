package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchDiff(t *testing.T) {
	const wantDiff = "diff --git a/x b/x\n+hello\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/repo/pulls/7" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github.v3.diff" {
			t.Errorf("Accept = %s", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %s", got)
		}
		io.WriteString(w, wantDiff)
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	got, err := c.FetchDiff(context.Background(), "octo", "repo", 7)
	if err != nil {
		t.Fatalf("FetchDiff: %v", err)
	}
	if got != wantDiff {
		t.Errorf("diff = %q, want %q", got, wantDiff)
	}
}

func TestFetchDiffError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	if _, err := c.FetchDiff(context.Background(), "octo", "repo", 7); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func TestAddLabel(t *testing.T) {
	var gotBody map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/repo/issues/7/labels" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	if err := c.AddLabel(context.Background(), "octo", "repo", 7, "needs-human-review"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if len(gotBody["labels"]) != 1 || gotBody["labels"][0] != "needs-human-review" {
		t.Errorf("labels payload = %v", gotBody)
	}
}

func TestPostComment(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/repo/issues/7/comments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := New("tok", srv.URL)
	if err := c.PostComment(context.Background(), "octo", "repo", 7, "hi"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotBody["body"] != "hi" {
		t.Errorf("comment body = %q", gotBody["body"])
	}
}
