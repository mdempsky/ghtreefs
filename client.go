package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/go-github/v70/github"
	"golang.org/x/oauth2"
)

type ghClient struct {
	gh       *github.Client
	cacheDir string

	mu    sync.Mutex
	trees map[string]*github.Tree // "sha" -> tree
}

func newGHClient(token, cacheDir string) *ghClient {
	var hc *http.Client
	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		hc = oauth2.NewClient(context.Background(), ts)
	}
	return &ghClient{
		gh:       github.NewClient(hc),
		cacheDir: cacheDir,
		trees:    make(map[string]*github.Tree),
	}
}

// GetTree fetches a git tree by SHA (non-recursive), with in-memory caching.
// Trees are content-addressed and immutable, so caching forever is safe.
func (c *ghClient) GetTree(ctx context.Context, owner, repo, sha string) (*github.Tree, error) {
	key := sha
	c.mu.Lock()
	if t, ok := c.trees[key]; ok {
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	tree, _, err := c.gh.Git.GetTree(ctx, owner, repo, sha, false)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.trees[key] = tree
	c.mu.Unlock()
	return tree, nil
}

// EnsureBlob downloads a blob into the disk cache if not already present,
// and returns the path to the cached file. Blobs are content-addressed, so
// the cache key is just the SHA — no owner/repo needed in the path.
func (c *ghClient) EnsureBlob(ctx context.Context, owner, repo, sha string) (string, error) {
	path := filepath.Join(c.cacheDir, sha)
	if _, err := os.Stat(path); err == nil {
		return path, nil // already cached
	}

	data, err := c.fetchBlobData(ctx, owner, repo, sha)
	if err != nil {
		return "", err
	}

	// Write atomically so no reader sees a partial file.
	tmp, err := os.CreateTemp(c.cacheDir, ".tmp.*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(tmpPath)
		if writeErr != nil {
			return "", writeErr
		}
		return "", closeErr
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return path, nil
}

// GetBlob returns the decoded content of a blob, using the disk cache.
// Used for symlink targets (which are small enough to read into memory).
func (c *ghClient) GetBlob(ctx context.Context, owner, repo, sha string) ([]byte, error) {
	path, err := c.EnsureBlob(ctx, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (c *ghClient) fetchBlobData(ctx context.Context, owner, repo, sha string) ([]byte, error) {
	blob, _, err := c.gh.Git.GetBlob(ctx, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	if blob.GetEncoding() == "base64" {
		// GitHub wraps base64 output at 60 chars with newlines.
		clean := strings.ReplaceAll(blob.GetContent(), "\n", "")
		return base64.StdEncoding.DecodeString(clean)
	}
	return []byte(blob.GetContent()), nil
}
