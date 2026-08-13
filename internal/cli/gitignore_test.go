package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitignore "github.com/sabhiram/go-gitignore"
)

// TestRegentGitignoreSharesOnlyConfig asserts the .regent/.gitignore that
// connect/init writes ignores all machine-local state (object store, index,
// refs, logs, backups) but keeps config.toml committable — so `git add .regent`
// shares only the server wiring teammates inherit, never per-machine state.
func TestRegentGitignoreSharesOnlyConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".regent"), 0o755); err != nil {
		t.Fatalf("mkdir .regent: %v", err)
	}
	if err := createRegentGitignore(root); err != nil {
		t.Fatalf("createRegentGitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".regent", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	// Evaluate with the same gitignore engine git uses (paths are relative to
	// the .regent/ dir the file lives in).
	gi := gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)

	for _, p := range []string{"index.db", "objects/aa/bbcc", "refs/sessions/x", "log/hook-error.log", "snapshot.backup"} {
		if !gi.MatchesPath(p) {
			t.Errorf("%q should be git-ignored (machine-local state) but is not", p)
		}
	}
	for _, p := range []string{"config.toml", ".gitignore"} {
		if gi.MatchesPath(p) {
			t.Errorf("%q must NOT be git-ignored (teammates inherit it) but is", p)
		}
	}
}
