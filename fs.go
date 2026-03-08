package main

import (
	"context"
	"log"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// bytesFileHandle is a read-only in-memory FileHandle for tiny synthetic files
// (e.g. submodule entries) that have no corresponding git blob to fetch.
type bytesFileHandle struct {
	content []byte
}

var _ fs.FileReader = (*bytesFileHandle)(nil)

func (h *bytesFileHandle) Read(_ context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(h.content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(h.content)) {
		end = int64(len(h.content))
	}
	return fuse.ReadResultData(h.content[off:end]), 0
}

// RootNode is the root of the FUSE mount (e.g. /gh).
// Its children are owner names (GitHub users/orgs).
type RootNode struct {
	fs.Inode
	client *ghClient
}

var _ fs.NodeGetattrer = (*RootNode)(nil)
var _ fs.NodeLookuper = (*RootNode)(nil)

func (n *RootNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0755
	return 0
}

func (n *RootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	out.Mode = syscall.S_IFDIR | 0755
	child := &OwnerNode{client: n.client, owner: name}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// OwnerNode represents /gh/{owner}.
// Its children are repository names.
type OwnerNode struct {
	fs.Inode
	client *ghClient
	owner  string
}

var _ fs.NodeGetattrer = (*OwnerNode)(nil)
var _ fs.NodeLookuper = (*OwnerNode)(nil)

func (n *OwnerNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0755
	return 0
}

func (n *OwnerNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	out.Mode = syscall.S_IFDIR | 0755
	child := &RepoNode{client: n.client, owner: n.owner, repo: name}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// RepoNode represents /gh/{owner}/{repo}.
// It has one child directory: "tree".
type RepoNode struct {
	fs.Inode
	client *ghClient
	owner  string
	repo   string
}

var _ fs.NodeGetattrer = (*RepoNode)(nil)
var _ fs.NodeLookuper = (*RepoNode)(nil)
var _ fs.NodeReaddirer = (*RepoNode)(nil)

func (n *RepoNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0755
	return 0
}

func (n *RepoNode) Readdir(_ context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream([]fuse.DirEntry{
		{Name: "tree", Mode: syscall.S_IFDIR},
	}), 0
}

func (n *RepoNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	out.Mode = syscall.S_IFDIR | 0755
	switch name {
	case "tree":
		child := &TreesDirNode{client: n.client, owner: n.owner, repo: n.repo}
		return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}
	return nil, syscall.ENOENT
}

// TreesDirNode represents /gh/{owner}/{repo}/tree/.
// Listing it returns nothing; children are fetched by tree SHA on Lookup.
type TreesDirNode struct {
	fs.Inode
	client *ghClient
	owner  string
	repo   string
}

var _ fs.NodeGetattrer = (*TreesDirNode)(nil)
var _ fs.NodeLookuper = (*TreesDirNode)(nil)

func (n *TreesDirNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0755
	return 0
}

func (n *TreesDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if _, err := n.client.GetTree(ctx, n.owner, n.repo, name); err != nil {
		log.Printf("GetTree %s/%s %s: %v", n.owner, n.repo, name, err)
		return nil, syscall.ENOENT
	}
	child := &GitTreeNode{
		client:  n.client,
		owner:   n.owner,
		repo:    n.repo,
		treeSHA: name,
	}
	out.Mode = syscall.S_IFDIR | 0755
	return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// gitEntry represents one entry from a git tree.
type gitEntry struct {
	name    string
	gitMode string // "100644", "100755", "040000", "120000", "160000"
	sha     string
	size    uint64
}

func (e gitEntry) isDir() bool       { return e.gitMode == "040000" }
func (e gitEntry) isSymlink() bool   { return e.gitMode == "120000" }
func (e gitEntry) isSubmodule() bool { return e.gitMode == "160000" }

func (e gitEntry) fuseType() uint32 {
	switch {
	case e.isDir():
		return syscall.S_IFDIR
	case e.isSymlink():
		return syscall.S_IFLNK
	default:
		return syscall.S_IFREG
	}
}

func (e gitEntry) perm() uint32 {
	switch e.gitMode {
	case "100755":
		return 0755
	case "040000":
		return 0755
	case "120000":
		return 0777
	default:
		return 0644
	}
}

// GitTreeNode represents a git tree object (directory).
// Tree entries are fetched lazily from the GitHub API on first access.
type GitTreeNode struct {
	fs.Inode
	client  *ghClient
	owner   string
	repo    string
	treeSHA string

	mu      sync.Mutex
	entries []gitEntry
	loaded  bool
	loadErr error
}

var _ fs.NodeGetattrer = (*GitTreeNode)(nil)
var _ fs.NodeLookuper = (*GitTreeNode)(nil)
var _ fs.NodeReaddirer = (*GitTreeNode)(nil)

func (n *GitTreeNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0755
	return 0
}

func (n *GitTreeNode) load(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.loaded {
		return n.loadErr
	}
	tree, err := n.client.GetTree(ctx, n.owner, n.repo, n.treeSHA)
	if err != nil {
		n.loadErr = err
		return err
	}
	n.entries = make([]gitEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		n.entries = append(n.entries, gitEntry{
			name:    e.GetPath(),
			gitMode: e.GetMode(),
			sha:     e.GetSHA(),
			size:    uint64(e.GetSize()),
		})
	}
	n.loaded = true
	n.loadErr = nil
	return nil
}

func (n *GitTreeNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if err := n.load(ctx); err != nil {
		return nil, syscall.EIO
	}
	n.mu.Lock()
	entries := make([]fuse.DirEntry, len(n.entries))
	for i, e := range n.entries {
		entries[i] = fuse.DirEntry{Name: e.name, Mode: e.fuseType()}
	}
	n.mu.Unlock()
	return fs.NewListDirStream(entries), 0
}

func (n *GitTreeNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if err := n.load(ctx); err != nil {
		return nil, syscall.EIO
	}

	n.mu.Lock()
	var found *gitEntry
	for i := range n.entries {
		if n.entries[i].name == name {
			e := n.entries[i]
			found = &e
			break
		}
	}
	n.mu.Unlock()

	if found == nil {
		return nil, syscall.ENOENT
	}

	switch {
	case found.isDir():
		child := &GitTreeNode{
			client:  n.client,
			owner:   n.owner,
			repo:    n.repo,
			treeSHA: found.sha,
		}
		out.Mode = syscall.S_IFDIR | 0755
		return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case found.isSymlink():
		child := &GitSymlinkNode{
			client: n.client,
			owner:  n.owner,
			repo:   n.repo,
			sha:    found.sha,
		}
		out.Mode = syscall.S_IFLNK | 0777
		return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFLNK}), 0

	case found.isSubmodule():
		// Represent submodules as a text file containing the pinned commit SHA.
		content := []byte(found.sha + "\n")
		child := &GitBlobNode{staticContent: content, filePerm: 0444}
		out.Mode = syscall.S_IFREG | 0444
		out.Size = uint64(len(content))
		return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG}), 0

	default: // regular file (100644 or 100755)
		child := &GitBlobNode{
			client:   n.client,
			owner:    n.owner,
			repo:     n.repo,
			sha:      found.sha,
			blobSize: found.size,
			filePerm: found.perm(),
		}
		out.Mode = syscall.S_IFREG | found.perm()
		out.Size = found.size
		return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}
}

// GitBlobNode represents a git blob (file).
// On Open, the blob is downloaded into the disk cache and a real fd is returned,
// so the kernel's page cache does all the heavy lifting.
type GitBlobNode struct {
	fs.Inode
	client   *ghClient
	owner    string
	repo     string
	sha      string
	blobSize uint64
	filePerm uint32

	// staticContent is set for synthetic files (e.g. submodule SHAs).
	staticContent []byte
}

var _ fs.NodeGetattrer = (*GitBlobNode)(nil)
var _ fs.NodeOpener = (*GitBlobNode)(nil)

func (n *GitBlobNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFREG | n.filePerm
	if n.staticContent != nil {
		out.Size = uint64(len(n.staticContent))
	} else {
		out.Size = n.blobSize
	}
	return 0
}

func (n *GitBlobNode) Open(ctx context.Context, _ uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.staticContent != nil {
		return &bytesFileHandle{n.staticContent}, fuse.FOPEN_KEEP_CACHE, 0
	}
	path, err := n.client.EnsureBlob(ctx, n.owner, n.repo, n.sha)
	if err != nil {
		log.Printf("EnsureBlob %s/%s %s: %v", n.owner, n.repo, n.sha, err)
		return nil, 0, syscall.EIO
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	return fs.NewLoopbackFile(fd), fuse.FOPEN_KEEP_CACHE, 0
}

// GitSymlinkNode represents a git symlink (mode 120000).
// The link target is stored as the blob content.
type GitSymlinkNode struct {
	fs.Inode
	client *ghClient
	owner  string
	repo   string
	sha    string

	mu      sync.Mutex
	target  []byte
	fetched bool
}

var _ fs.NodeGetattrer = (*GitSymlinkNode)(nil)
var _ fs.NodeReadlinker = (*GitSymlinkNode)(nil)

func (n *GitSymlinkNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFLNK | 0777
	n.mu.Lock()
	if n.fetched {
		out.Size = uint64(len(n.target))
	}
	n.mu.Unlock()
	return 0
}

func (n *GitSymlinkNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	n.mu.Lock()
	if n.fetched {
		t := n.target
		n.mu.Unlock()
		return t, 0
	}
	n.mu.Unlock()

	data, err := n.client.GetBlob(ctx, n.owner, n.repo, n.sha)
	if err != nil {
		return nil, syscall.EIO
	}

	n.mu.Lock()
	n.target = data
	n.fetched = true
	n.mu.Unlock()
	return data, 0
}
