package main

import (
	"testing"

	"github.com/regent-vcs/regent/internal/remote"
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
