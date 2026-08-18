package main

import (
	"path/filepath"
	"testing"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
)

func TestOpenHookRecorderMakesServerChoiceAtCommandEdge(t *testing.T) {
	workspace := t.TempDir()
	cacheRoot := t.TempDir()
	t.Setenv("REGENT_SERVER_URL", "https://regent.example.test")
	t.Setenv("REGENT_REPO_ID", "demo")
	t.Setenv("REGENT_CACHE_DIR", cacheRoot)

	cacheDir, err := remote.CacheDirFor(remote.Config{ServerURL: "https://regent.example.test", RepoID: "demo", CacheDir: cacheRoot})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := openHookRecorder(workspace)
	if err != nil || !ok {
		t.Fatalf("openHookRecorder = %v, %v", ok, err)
	}
	defer rec.Close()
	if rec.Store.Root != cacheDir {
		t.Fatalf("hook store = %s, want server cache %s", rec.Store.Root, cacheDir)
	}
}

func TestCommandStorePreservesLocalReadsWhenServerConfigIsInvalid(t *testing.T) {
	workspace := t.TempDir()
	if _, err := store.Init(workspace); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REGENT_SERVER_URL", "ftp://invalid.example.test")
	t.Setenv("REGENT_REPO_ID", "demo")

	s, err := commandStore(workspace)
	if err != nil {
		t.Fatalf("commandStore: %v", err)
	}
	if want := filepath.Join(workspace, ".regent"); s.Root != want {
		t.Fatalf("store root = %s, want local store %s", s.Root, want)
	}
}
