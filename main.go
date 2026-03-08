package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func main() {
	mountpoint := flag.String("mount", "/gh", "filesystem mount point")
	debug := flag.Bool("debug", false, "enable FUSE debug logging")

	defaultCache, err := os.UserCacheDir()
	if err != nil {
		defaultCache = os.TempDir()
	}
	defaultCache = filepath.Join(defaultCache, "ghtreefs")
	cacheDir := flag.String("cache", defaultCache, "blob cache directory")

	flag.Parse()

	if err := os.MkdirAll(*cacheDir, 0755); err != nil {
		log.Fatalf("create cache dir %s: %v", *cacheDir, err)
	}

	token := os.Getenv("GITHUB_TOKEN")
	client := newGHClient(token, *cacheDir)

	root := &RootNode{client: client}

	// Cache attrs and entries for a long time; git objects are immutable.
	timeout := time.Hour
	opts := &fs.Options{
		AttrTimeout:  &timeout,
		EntryTimeout: &timeout,
		MountOptions: fuse.MountOptions{
			Debug:  *debug,
			Name:   "ghtreefs",
			FsName: "ghtreefs",
		},
	}

	server, err := fs.Mount(*mountpoint, root, opts)
	if err != nil {
		log.Fatalf("mount %s: %v", *mountpoint, err)
	}
	log.Printf("mounted ghtreefs at %s (cache: %s)", *mountpoint, *cacheDir)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	log.Printf("unmounting...")
	if err := server.Unmount(); err != nil {
		log.Fatalf("unmount: %v", err)
	}
}
