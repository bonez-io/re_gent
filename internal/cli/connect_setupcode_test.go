package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/remotetest"
)

// chdirTo changes into dir for the duration of the test, restoring the
// previous working directory on cleanup — the same pattern connect_test.go
// and discover_test.go use, since runConnectRunE resolves its project from
// os.Getwd() rather than an injected path.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
}

// TestConnectSetupCodeEnrollsBindsProjectIDAndStoresCredential is deliverable
// 6's headline case: "connect --setup enrolls and binds project_id."
func TestConnectSetupCodeEnrollsBindsProjectIDAndStoresCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableSetupCodes()
	code := srv.MintSetupCode("acme", srv.URL())

	repo := gitRepoWithCommit(t, "setup-code-project")
	setGitRemote(t, repo, "https://github.com/acme/setup-code-project.git")
	chdirTo(t, repo)

	cmd := ConnectCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{srv.URL(), "--setup", code})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rgt connect --setup: %v\n%s", err, out.String())
	}

	binding, err := config.LoadRemoteBinding(filepath.Join(repo, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if binding.ProjectID == "" {
		t.Fatal("connect --setup left the project with no project_id")
	}
	if binding.URL != srv.URL() {
		t.Errorf("bound URL = %q, want %q", binding.URL, srv.URL())
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	credential := config.TokenForServer(cfg, srv.URL())
	if credential == "" {
		t.Fatal("the setup-code credential was not stored")
	}
	if strings.Contains(out.String(), credential) {
		t.Fatalf("the machine credential leaked into command output:\n%s", out.String())
	}

	// Step 1 of the documented exchange happened, and the code carried this
	// machine's hostname (RFC 0005 Appendix A: "POST /api/v1/auth/setup-code
	// with the code and the machine's hostname").
	exchanges := srv.RecordedRequests("POST /api/v1/auth/setup-code")
	if len(exchanges) != 1 {
		t.Fatalf("setup-code exchange requests = %d, want 1", len(exchanges))
	}
	if got, _ := exchanges[0]["machine_name"].(string); got != hostname() {
		t.Errorf("machine_name = %q, want %q", got, hostname())
	}

	if _, statErr := os.Stat(filepath.Join(repo, ".claude", "settings.json")); statErr != nil {
		t.Errorf("connect --setup did not wire hooks: %v", statErr)
	}
}

// TestConnectSetupCodeReuseFailsWithClearMessage is deliverable 6's second
// case: "second use of the same code fails with the expected message."
func TestConnectSetupCodeReuseFailsWithClearMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableSetupCodes()
	code := srv.MintSetupCode("acme", srv.URL())
	srv.ConsumeSetupCode(code) // already used, exactly like a second `rgt connect --setup` run

	repo := gitRepoWithCommit(t, "setup-code-reused")
	setGitRemote(t, repo, "https://github.com/acme/setup-code-reused.git")
	chdirTo(t, repo)

	cmd := ConnectCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{srv.URL(), "--setup", code})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("connecting with an already-used setup code unexpectedly succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "generate a new one") {
		t.Errorf("error does not tell the user to generate a new code: %v", err)
	}
	if strings.Contains(err.Error(), code) {
		t.Errorf("error echoes the spent code back, inviting reuse: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(repo, ".regent", "config.toml")); statErr == nil {
		t.Error("a failed setup-code exchange still left a server binding behind")
	}
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		t.Fatal(cfgErr)
	}
	if config.TokenForServer(cfg, srv.URL()) != "" {
		t.Error("a failed setup-code exchange still stored a credential")
	}
}

// TestConnectSetupCodeExpiredFailsWithClearMessage exercises the other
// documented failure code, "setup_code_expired" (RFC 0005 Appendix A).
func TestConnectSetupCodeExpiredFailsWithClearMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableSetupCodes()
	code := srv.MintSetupCode("acme", srv.URL())
	srv.ExpireSetupCode(code)

	repo := gitRepoWithCommit(t, "setup-code-expired")
	setGitRemote(t, repo, "https://github.com/acme/setup-code-expired.git")
	chdirTo(t, repo)

	cmd := ConnectCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{srv.URL(), "--setup", code})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("connecting with an expired setup code unexpectedly succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "generate a new one") {
		t.Errorf("error does not clearly say the code expired and how to fix it: %v", err)
	}
}

// TestInitSetupCodeAliasBehavesLikeConnect is deliverable 6's third case:
// "rgt init <url> --setup equals rgt connect."
func TestInitSetupCodeAliasBehavesLikeConnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableSetupCodes()
	code := srv.MintSetupCode("acme", srv.URL())

	repo := gitRepoWithCommit(t, "init-setup-code")
	setGitRemote(t, repo, "https://github.com/acme/init-setup-code.git")
	chdirTo(t, repo)

	cmd := InitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{srv.URL(), "--setup", code})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rgt init <url> --setup: %v\n%s", err, out.String())
	}

	binding, err := config.LoadRemoteBinding(filepath.Join(repo, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if binding.ProjectID == "" {
		t.Fatal("init <url> --setup left the project with no project_id")
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".claude", "settings.json")); statErr != nil {
		t.Errorf("init <url> --setup did not wire hooks: %v", statErr)
	}

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		t.Fatal(cfgErr)
	}
	if config.TokenForServer(cfg, srv.URL()) == "" {
		t.Error("init <url> --setup did not store the setup-code credential")
	}
}

// TestInitWithNoURLKeepsLocalBehavior guards the other half of the alias:
// "rgt init with no URL keeps its current local behavior."
func TestInitWithNoURLKeepsLocalBehavior(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	chdirTo(t, repo)

	cmd := InitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--agent", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rgt init: %v\n%s", err, out.String())
	}

	if _, statErr := os.Stat(filepath.Join(repo, ".regent")); statErr != nil {
		t.Errorf("bare init did not initialize .regent/: %v", statErr)
	}
	if binding, _ := config.LoadRemoteBinding(filepath.Join(repo, ".regent", "config.toml")); binding.Connected() {
		t.Error("bare init connected the project to a server; it should stay purely local")
	}
}
