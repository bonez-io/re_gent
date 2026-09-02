package publicgate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initTestRepo creates a git repo in a temp dir with the given tracked
// (staged) files and returns its root. Staging is enough for `git
// ls-files` — a commit isn't required.
func initTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")

	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		runGit(t, root, "add", rel)
	}
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestNewPathAllowlist_TracksGitFiles(t *testing.T) {
	root := initTestRepo(t, map[string]string{
		"main.go":              "package main",
		"internal/pkg/file.go": "package pkg",
		"README.md":            "# hi",
	})

	al, err := NewPathAllowlist(root)
	if err != nil {
		t.Fatalf("NewPathAllowlist: %v", err)
	}

	for _, tracked := range []string{"main.go", "internal/pkg/file.go", "README.md"} {
		if !al.Allowed(tracked) {
			t.Errorf("expected %q to be allowed (tracked)", tracked)
		}
	}

	for _, untracked := range []string{"not_tracked.go", "internal/other/nope.go"} {
		if al.Allowed(untracked) {
			t.Errorf("expected %q to be rejected (not tracked)", untracked)
		}
	}
}

func TestNewPathAllowlist_NonGitDir(t *testing.T) {
	root := t.TempDir()

	al, err := NewPathAllowlist(root)
	if err == nil {
		t.Fatal("expected an error for a non-git directory")
	}
	if !errors.Is(err, ErrNotGitRepo) {
		t.Errorf("expected error to wrap ErrNotGitRepo, got %v", err)
	}
	if al == nil {
		t.Fatal("expected a non-nil allowlist even on error")
	}
	if al.Allowed("anything.go") {
		t.Error("expected the fallback allowlist to permit nothing")
	}
}

func TestPathAllowlist_DeniesBlockedPatternsEvenIfTracked(t *testing.T) {
	root := initTestRepo(t, map[string]string{
		".env":                   "SECRET=1",
		".env.production":        "SECRET=2",
		"id_rsa":                 "not really a key",
		"id_rsa.pub":             "not really a key either",
		"certs/server.pem":       "cert",
		"certs/server.key":       "key",
		".regent/config.toml":    "binding",
		"normal/allowed_file.go": "package normal",
	})

	al, err := NewPathAllowlist(root)
	if err != nil {
		t.Fatalf("NewPathAllowlist: %v", err)
	}

	blocked := []string{
		".env",
		".env.production",
		"id_rsa",
		"id_rsa.pub",
		"certs/server.pem",
		"certs/server.key",
		".regent/config.toml",
	}
	for _, p := range blocked {
		if al.Allowed(p) {
			t.Errorf("expected %q to be denied even though tracked", p)
		}
	}

	if !al.Allowed("normal/allowed_file.go") {
		t.Error("expected the normal tracked file to be allowed")
	}
}

func TestPathAllowlist_DeniesGitDir(t *testing.T) {
	root := initTestRepo(t, map[string]string{"main.go": "package main"})
	al, err := NewPathAllowlist(root)
	if err != nil {
		t.Fatalf("NewPathAllowlist: %v", err)
	}
	if al.Allowed(".git/config") {
		t.Error("expected .git/config to be denied")
	}
}

func TestPathAllowlist_NormalizationRejectsEscapesAndAbsolutes(t *testing.T) {
	root := initTestRepo(t, map[string]string{"main.go": "package main"})
	al, err := NewPathAllowlist(root)
	if err != nil {
		t.Fatalf("NewPathAllowlist: %v", err)
	}

	rejected := []string{
		"../outside.go",
		"../../etc/passwd",
		"/etc/passwd",
		"/main.go",
		`C:\Users\shay\main.go`,
		"",
		".",
	}
	for _, p := range rejected {
		if al.Allowed(p) {
			t.Errorf("expected %q to be rejected", p)
		}
	}

	// A path that merely resolves (via ./) to a tracked file should still
	// be allowed — normalization, not blanket rejection.
	if !al.Allowed("./main.go") {
		t.Error("expected ./main.go to normalize to the tracked main.go")
	}
	if !al.Allowed(`main.go`) {
		t.Error("expected the plain tracked path to be allowed")
	}
}

func TestPathAllowlist_NilAllowsNothing(t *testing.T) {
	var al *PathAllowlist
	if al.Allowed("main.go") {
		t.Error("expected a nil *PathAllowlist to allow nothing")
	}
}
