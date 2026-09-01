package remote

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfigAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// noEnv is an Env that reports no REGENT_* variables set, so resolution depends
// purely on the config files under test.
func noEnv(string) (string, bool) { return "", false }

// TestLoadConfigForCWD_WiresConnectAndLogin is the regression test for the bug
// where `rgt connect` (which writes a repo-local [remote] binding) did not
// enable server mode, because resolution only read the global [server] table.
// The fix layers the repo-local [remote] (url+repo_id) with the per-user [auth]
// token, so connect + login together produce a working server-mode config.
func TestLoadConfigForCWD_WiresConnectAndLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Legacy ~/.regent/config.toml as written by the old top-level login command.
	writeConfigAt(t, filepath.Join(home, ".regent", "config.toml"), `
[auth]
server_url = "http://127.0.0.1:7654"
token = "logintoken0123456789abcd"
`)

	// <repo>/.regent/config.toml as written by `rgt connect`.
	repo := t.TempDir()
	writeConfigAt(t, filepath.Join(repo, ".regent", "config.toml"), `
[remote]
url = "http://127.0.0.1:7654"
repo_id = "girlfriend-assistant"
`)

	cfg, err := LoadConfigForCWD(noEnv, repo)
	if err != nil {
		t.Fatalf("LoadConfigForCWD: %v", err)
	}
	if !cfg.Enabled() {
		t.Fatalf("expected server mode enabled after connect+login, got %+v", cfg)
	}
	if cfg.ServerURL != "http://127.0.0.1:7654" {
		t.Errorf("ServerURL = %q, want http://127.0.0.1:7654", cfg.ServerURL)
	}
	if cfg.RepoID != "girlfriend-assistant" {
		t.Errorf("RepoID = %q, want girlfriend-assistant", cfg.RepoID)
	}
	if cfg.Token != "logintoken0123456789abcd" {
		t.Errorf("Token = %q, want the [auth] token", cfg.Token)
	}
}

func TestLoadConfigForCWD_SelectsCredentialForBoundServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfigAt(t, filepath.Join(home, ".regent", "config.toml"), `
[[credentials]]
server_url = "https://one.example"
token = "token-for-one-0123456789"

[[credentials]]
server_url = "https://two.example"
token = "token-for-two-0123456789"
`)
	repo := t.TempDir()
	writeConfigAt(t, filepath.Join(repo, ".regent", "config.toml"), `
[remote]
url = "https://two.example"
repo_id = "project"
`)

	cfg, err := LoadConfigForCWD(noEnv, repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "token-for-two-0123456789" {
		t.Fatalf("selected token = %q, want only the bound server credential", cfg.Token)
	}
}

// TestLoadConfigForCWD_RepoBindingWinsOverGlobal asserts the per-repo binding
// takes precedence, which is what keeps multi-repo coherent: two repos on one
// machine must each resolve their own repo_id.
func TestLoadConfigForCWD_RepoBindingWinsOverGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfigAt(t, filepath.Join(home, ".regent", "config.toml"), `
[server]
url = "http://example.com"
repo_id = "global-repo"
token = "globaltoken0123456789"
`)
	repo := t.TempDir()
	writeConfigAt(t, filepath.Join(repo, ".regent", "config.toml"), `
[remote]
url = "http://127.0.0.1:7654"
repo_id = "repo-local-id"
`)

	cfg, err := LoadConfigForCWD(noEnv, repo)
	if err != nil {
		t.Fatalf("LoadConfigForCWD: %v", err)
	}
	if cfg.RepoID != "repo-local-id" {
		t.Errorf("RepoID = %q, want repo-local-id (repo binding must win)", cfg.RepoID)
	}
	if cfg.ServerURL != "http://127.0.0.1:7654" {
		t.Errorf("ServerURL = %q, want the repo-local url", cfg.ServerURL)
	}
	// A token associated with a different global URL must never be sent to the
	// repo-local server.
	if cfg.Token != "" {
		t.Errorf("Token = %q, want no cross-server credential", cfg.Token)
	}
}

func TestRepoConfigPath_FindsNearestUpward(t *testing.T) {
	repo := t.TempDir()
	writeConfigAt(t, filepath.Join(repo, ".regent", "config.toml"), "")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := RepoConfigPath(sub)
	want := filepath.Join(repo, ".regent", "config.toml")
	if got != want {
		t.Errorf("RepoConfigPath(%q) = %q, want %q", sub, got, want)
	}

	// A directory with no .regent above it resolves to "".
	if got := RepoConfigPath(t.TempDir()); got != "" {
		t.Errorf("RepoConfigPath with no repo = %q, want \"\"", got)
	}
}
