package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestRepoNodeReaddirShowsOnlyTreeDirectory(t *testing.T) {
	n := &RepoNode{}
	stream, errno := n.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir errno=%v, want 0", errno)
	}

	de, errno := stream.Next()
	if errno != 0 {
		t.Fatalf("first Next errno=%v, want 0", errno)
	}
	if de.Name != "tree" || de.Mode != syscall.S_IFDIR {
		t.Fatalf("first entry = {%q, %o}, want {tree, dir}", de.Name, de.Mode)
	}
}

func TestGitTreeNodeLoadRetriesAfterTransientError(t *testing.T) {
	var calls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/git/trees/tree1", func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha":       "tree1",
			"tree":      []any{},
			"truncated": false,
		})
	})

	n := &GitTreeNode{
		client:  newTestGHClient(t, mux),
		owner:   "o",
		repo:    "r",
		treeSHA: "tree1",
	}

	if errno := mustReaddirErrno(t, n); errno != syscall.EIO {
		t.Fatalf("first Readdir errno=%v, want EIO", errno)
	}
	if errno := mustReaddirErrno(t, n); errno != 0 {
		t.Fatalf("second Readdir errno=%v, want 0", errno)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls=%d, want 2", got)
	}
}

func mustReaddirErrno(t *testing.T, n *GitTreeNode) syscall.Errno {
	t.Helper()
	_, errno := n.Readdir(context.Background())
	return errno
}
