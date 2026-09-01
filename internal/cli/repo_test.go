package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bonez-io/re_gent/internal/store"
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

func TestOpenStoreFromCWDDoesNotSelectTheServerCache(t *testing.T) {
	workspace := t.TempDir()
	if _, err := store.Init(workspace); err != nil {
		t.Fatalf("init store: %v", err)
	}
	workspace = chdir(t, workspace)
	t.Setenv("REGENT_SERVER_URL", "https://regent.example.com")
	t.Setenv("REGENT_REPO_ID", "demo")

	s, err := openStoreFromCWD()
	if err != nil {
		t.Fatalf("openStoreFromCWD: %v", err)
	}
	if want := filepath.Join(workspace, ".regent"); s.Root != want {
		t.Fatalf("read from %s, want local store %s", s.Root, want)
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
