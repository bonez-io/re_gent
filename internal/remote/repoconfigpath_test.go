package remote

import (
	"os"
	"path/filepath"
	"testing"
)

// I5: RepoConfigPath must not treat a permission/IO stat error as "no config
// here". A .regent/config.toml that exists but cannot be stat-ed (e.g. its
// directory lacks search permission) must return the path so the caller's read
// surfaces the real error, instead of silently reporting the repo as
// disconnected from its server.
func TestRepoConfigPath_SurfacesNonNotExistStatError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based permission test is ineffective as root")
	}
	repo := t.TempDir()
	regentDir := filepath.Join(repo, ".regent")
	if err := os.MkdirAll(regentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regentDir, "config.toml"), []byte("[remote]\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Remove search permission on .regent so stat of config.toml fails with a
	// permission error (EACCES), which is NOT os.IsNotExist.
	if err := os.Chmod(regentDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(regentDir, 0o755) })

	if got := RepoConfigPath(repo); got == "" {
		t.Fatal(`RepoConfigPath returned "" on a permission error; a non-not-exist stat error must surface, not be swallowed as "no config"`)
	}
}
