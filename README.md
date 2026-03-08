# ghtreefs

`ghtreefs` is a read-only FUSE filesystem that exposes GitHub repository trees as normal files and directories.

It mounts a virtual root (default `/gh`) and lets you browse a repository by Git tree SHA:

`/gh/<owner>/<repo>/tree/<tree-sha>/...`

The filesystem fetches tree/blob data from the GitHub API and caches blobs on disk by SHA.

## Status

This project is intentionally minimal:

- Read-only
- Tree-oriented (no PR/commit resolution in-path)
- Lazy network fetches
- Content-addressed caching

## Requirements

- Go 1.26+
- FUSE (via `go-fuse`)
- A GitHub token in `GITHUB_TOKEN` is strongly recommended to avoid low unauthenticated rate limits

## Build

```bash
go build -o ghtreefs .
```

## Run

```bash
export GITHUB_TOKEN=...   # recommended
mkdir -p /tmp/gh
./ghtreefs -mount /tmp/gh
```

Flags:

- `-mount` mount point (default: `/gh`)
- `-cache` blob cache directory (default: user cache dir + `/ghtreefs`)
- `-debug` enable FUSE debug logging

## Filesystem Layout

Mounted root:

- `/gh/<owner>/`
- `/gh/<owner>/<repo>/`
- `/gh/<owner>/<repo>/tree/<tree-sha>/...`

At `<tree-sha>`, entries map to git tree entries:

- directories (`040000`) -> directories
- regular files (`100644`, `100755`) -> regular files
- symlinks (`120000`) -> symlinks
- submodules (`160000`) -> read-only file containing pinned commit SHA + newline

## Resolving a Tree SHA

`ghtreefs` expects a tree SHA. Resolve refs outside the filesystem, for example with the GitHub CLI:

```bash
# Example: resolve a branch to its commit SHA
gh api repos/<owner>/<repo>/git/ref/heads/<branch> --jq '.object.sha'

# Then resolve commit SHA to tree SHA
gh api repos/<owner>/<repo>/git/commits/<commit-sha> --jq '.tree.sha'
```

Then browse:

```bash
ls /tmp/gh/<owner>/<repo>/tree/<tree-sha>
```

## Caching

- Blob cache is on disk and keyed by blob SHA.
- Tree cache is in-memory and keyed by tree SHA.
- Because trees are content-addressed, identical trees can be reused across repos (for example, forks).

## Test

On macOS, tests run inside a Linux container via Apple `container`:

```bash
make test
```

## Docker Image

A `Dockerfile` is included. The container image installs `fuse3` and runs the binary with default mount `/gh`.

## Limitations

- No write operations
- No in-filesystem PR/commit/ref resolution
- API/network failures surface as read errors (`EIO`) at access time
- Large traversals can still hit GitHub API limits

## License

Public domain.
