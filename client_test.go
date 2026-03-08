package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v70/github"
)

func newTestGHClient(t *testing.T, handler http.Handler) *ghClient {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	gh := github.NewClient(srv.Client())
	baseURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse base URL: %v", err)
	}
	gh.BaseURL = baseURL
	gh.UploadURL = baseURL

	return &ghClient{
		gh:       gh,
		cacheDir: t.TempDir(),
		trees:    make(map[string]*github.Tree),
	}
}

func TestGetTreeCachesBySHAAcrossRepos(t *testing.T) {
	var calls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o1/r1/git/trees/tree1", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha":       "tree1",
			"tree":      []any{},
			"truncated": false,
		})
	})
	mux.HandleFunc("/repos/o2/r2/git/trees/tree1", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected API call for second repo; tree should have been shared from cache")
	})

	c := newTestGHClient(t, mux)

	if _, err := c.GetTree(context.Background(), "o1", "r1", "tree1"); err != nil {
		t.Fatalf("GetTree first repo: %v", err)
	}
	if _, err := c.GetTree(context.Background(), "o2", "r2", "tree1"); err != nil {
		t.Fatalf("GetTree second repo: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls=%d, want 1", got)
	}
}
