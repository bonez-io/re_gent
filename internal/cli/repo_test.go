package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
)

// TestMain neutralises ambient server-mode configuration for this package.
//
// The read commands resolve their store through openStoreFromCWD, which now
// consults the environment and ~/.regent/config.toml. Without this guard a
// machine configured for server mode would send every local-mode test in this
// package at a shared machine-local cache instead of its own fixture. Tests
// that want server mode set the variables themselves with t.Setenv.
func TestMain(m *testing.M) {
	os.Setenv("REGENT_SERVER_URL", "")
	os.Exit(m.Run())
}

// chdir moves into dir for the duration of one test and returns the path as
// the process sees it. On macOS the temp root is a symlink (/var ->
// /private/var), so the resolved form is what store lookups from the working
// directory produce.
func chdir(t *testing.T, dir string) string {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	resolved, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after chdir: %v", err)
	}
	return resolved
}

func TestOpenStoreFromCWDUsesTheServerCacheWhenConfigured(t *testing.T) {
	workspace := t.TempDir()
	cacheRoot := t.TempDir()

	// A workspace with no .regent/ at all: in server mode there is nothing to
	// initialise locally, which is the whole point of the cutover.
	_ = chdir(t, workspace)
	t.Setenv("REGENT_SERVER_URL", "https://regent.example.com")
	t.Setenv("REGENT_REPO_ID", "demo")
	t.Setenv("REGENT_CACHE_DIR", cacheRoot)

	// Ask for the cache path rather than rebuilding it here. The layout gained
	// a per-server segment, and a test that spells the path out by hand is a
	// second definition of it that drifts from the first.
	cacheDir, err := remote.CacheDirFor(remote.Config{
		ServerURL: "https://regent.example.com",
		RepoID:    "demo",
		CacheDir:  cacheRoot,
	})
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if _, err := store.Open(cacheDir); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	s, err := openStoreFromCWD()
	if err != nil {
		t.Fatalf("openStoreFromCWD: %v", err)
	}
	if s.Root != cacheDir {
		t.Fatalf("read from %s, want the server cache %s", s.Root, cacheDir)
	}
}

func TestOpenStoreFromCWDExplainsAMissingCache(t *testing.T) {
	_ = chdir(t, t.TempDir())
	t.Setenv("REGENT_SERVER_URL", "https://regent.example.com")
	t.Setenv("REGENT_REPO_ID", "demo")
	t.Setenv("REGENT_CACHE_DIR", t.TempDir()) // configured, but never populated

	_, err := openStoreFromCWD()
	if err == nil {
		t.Fatal("expected an error when the server-mode cache is absent")
	}
	// 'rgt init' would be the wrong advice in server mode; the cache is
	// rebuilt from the server, which is the source of truth.
	//
	// The advice used to be 'rgt sync --pull <ref>', which asked for a ref name
	// this machine has no way of knowing — it has pushed nothing, so its spool
	// lists nothing, and there was no call that asked the server what exists.
	// 'rgt pull' discovers that itself, so it is the command that can actually
	// be run from here.
	if !strings.Contains(err.Error(), "rgt pull") {
		t.Fatalf("error should point at 'rgt pull', got: %v", err)
	}
	if strings.Contains(err.Error(), "rgt init") {
		t.Fatalf("error must not suggest 'rgt init' in server mode, got: %v", err)
	}
}

func TestOpenStoreFromCWDFallsBackToTheLocalRepository(t *testing.T) {
	workspace := t.TempDir()
	if _, err := store.Init(workspace); err != nil {
		t.Fatalf("init store: %v", err)
	}
	workspace = chdir(t, workspace)

	// Half a configuration is not a configuration: a stray variable must never
	// make an ordinary local repository unreadable.
	t.Setenv("REGENT_SERVER_URL", "https://regent.example.com")
	t.Setenv("REGENT_REPO_ID", "")

	s, err := openStoreFromCWD()
	if err != nil {
		t.Fatalf("openStoreFromCWD: %v", err)
	}
	if want := filepath.Join(workspace, ".regent"); s.Root != want {
		t.Fatalf("read from %s, want the local store %s", s.Root, want)
	}
}

func TestOpenStoreFromCWDIgnoresAnInvalidServerConfig(t *testing.T) {
	workspace := t.TempDir()
	if _, err := store.Init(workspace); err != nil {
		t.Fatalf("init store: %v", err)
	}
	workspace = chdir(t, workspace)

	// A malformed URL degrades to local rather than making reads fail.
	t.Setenv("REGENT_SERVER_URL", "ftp://nope")
	t.Setenv("REGENT_REPO_ID", "demo")

	s, err := openStoreFromCWD()
	if err != nil {
		t.Fatalf("openStoreFromCWD: %v", err)
	}
	if want := filepath.Join(workspace, ".regent"); s.Root != want {
		t.Fatalf("read from %s, want the local store %s", s.Root, want)
	}
}
