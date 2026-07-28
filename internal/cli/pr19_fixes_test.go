package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// I1: deriveRepoID must not emit a server-reserved id for a folder that
// literally sanitizes to one (e.g. a directory named "repos"/"aux"/"nul").
func TestDeriveRepoID_AvoidsReservedNames(t *testing.T) {
	for _, dir := range []string{"/home/me/repos", "/x/aux", "/y/nul", "/z/com1"} {
		if got := deriveRepoID(dir); reservedRepoIDs[got] {
			t.Errorf("deriveRepoID(%q) = %q, which is a server-reserved id", dir, got)
		}
	}
	// Normal names must be untouched.
	if got := deriveRepoID("/home/me/girlfriend-assistant"); got != "girlfriend-assistant" {
		t.Errorf("deriveRepoID = %q, want girlfriend-assistant", got)
	}
}

// C1: merge in server mode would write to the disposable local cache without
// reaching the server, silently losing the result. It must refuse instead.
func TestRunMerge_RefusesInServerMode(t *testing.T) {
	t.Setenv("REGENT_SERVER_URL", "http://127.0.0.1:7654")
	t.Setenv("REGENT_REPO_ID", "test-repo")

	err := runMerge(t.TempDir(), "sessions/a", "sessions/b")
	if !errors.Is(err, errServerModeMergeUnsupported) {
		t.Fatalf("runMerge in server mode = %v, want errServerModeMergeUnsupported", err)
	}
}

// I2: a configured-but-broken server mode (malformed repo-local config) must
// surface an error, not be swallowed as "server mode not configured".
func TestOpenServerModeCache_SurfacesBrokenConfig(t *testing.T) {
	repo := t.TempDir()
	regentDir := filepath.Join(repo, ".regent")
	if err := os.MkdirAll(regentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regentDir, "config.toml"), []byte("this is not = valid toml ["), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, ok, err := openServerModeCache(repo)
	if ok {
		t.Fatal("ok must be false for a broken config")
	}
	if err == nil {
		t.Fatal("a malformed config must surface an error, not be swallowed as \"not configured\"")
	}
}
