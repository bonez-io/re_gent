package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/remotetest"
)

// setupCodeEnv points the built binary at a private HOME (so credentials and
// server memory land somewhere this test controls and can inspect) and at
// srv, the same shape hermeticEnvForFake uses, but returning the HOME path
// too — every test in this file needs to read back the config file that
// landed there.
func setupCodeEnv(t *testing.T, srv *remotetest.Server) (env []string, home string) {
	t.Helper()
	home = t.TempDir()
	return []string{
		"HOME=" + home,
		"REGENT_SERVER_URL=" + srv.URL(),
	}, home
}

// storedCredential reads back the machine credential the binary stored for
// serverURL under home, the same file `rgt auth login` and `rgt connect
// --setup` both write.
func storedCredential(t *testing.T, home, serverURL string) string {
	t.Helper()
	cfg, err := config.LoadFrom(filepath.Join(home, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("load machine config: %v", err)
	}
	return config.TokenForServer(cfg, serverURL)
}

// TestE2EConnectSetupCodeEnrollsAndBindsProjectID is deliverable 6's
// headline e2e case: "connect --setup enrolls and binds project_id."
func TestE2EConnectSetupCodeEnrollsAndBindsProjectID(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableSetupCodes()

	project := gitProject(t, "setup-code-project", "https://github.com/acme/setup-code-project.git")
	code := srv.MintSetupCode("acme", srv.URL())
	env, home := setupCodeEnv(t, srv)

	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL(), "--setup", code)

	if id := projectIDOf(t, project); id == "" {
		t.Fatalf("connect --setup left the project with no project_id:\n%s", out)
	}
	if !hooksIntact(t, project) {
		t.Errorf("connect --setup did not wire hooks:\n%s", out)
	}

	credential := storedCredential(t, home, srv.URL())
	if credential == "" {
		t.Fatal("connect --setup did not store a machine credential")
	}
	if strings.Contains(out, credential) {
		t.Fatalf("the machine credential leaked into command output:\n%s", out)
	}
}

// TestE2EConnectSetupCodeReuseFails is deliverable 6's second e2e case:
// "second use of the same code fails with the expected message."
func TestE2EConnectSetupCodeReuseFails(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableSetupCodes()

	project := gitProject(t, "setup-code-reused", "https://github.com/acme/setup-code-reused.git")
	code := srv.MintSetupCode("acme", srv.URL())
	srv.ConsumeSetupCode(code)
	env, _ := setupCodeEnv(t, srv)

	out, err := e2eRunEnvRaw(t, rgt, project, env, "connect", srv.URL(), "--setup", code)
	if err == nil {
		t.Fatalf("connect --setup with an already-used code unexpectedly succeeded:\n%s", out)
	}
	if !strings.Contains(out, "generate a new one") {
		t.Errorf("error does not tell the user to generate a new code in the web UI:\n%s", out)
	}
	if id := projectIDOf(t, project); id != "" {
		t.Errorf("a failed setup-code exchange still bound the project (id=%q)", id)
	}
}

// TestE2EInitWithSetupCodeEqualsConnect is deliverable 6's third e2e case:
// "rgt init <url> --setup equals rgt connect."
func TestE2EInitWithSetupCodeEqualsConnect(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()
	srv.EnableSetupCodes()

	project := gitProject(t, "init-setup-code", "https://github.com/acme/init-setup-code.git")
	code := srv.MintSetupCode("acme", srv.URL())
	env, home := setupCodeEnv(t, srv)

	out := e2eRunEnv(t, rgt, project, env, nil, "init", srv.URL(), "--setup", code)

	if id := projectIDOf(t, project); id == "" {
		t.Fatalf("init <url> --setup left the project with no project_id:\n%s", out)
	}
	if !hooksIntact(t, project) {
		t.Errorf("init <url> --setup did not wire hooks:\n%s", out)
	}
	if storedCredential(t, home, srv.URL()) == "" {
		t.Error("init <url> --setup did not store a machine credential")
	}
}

// TestE2EAdminBackupWritesA0600File is deliverable 6's fourth e2e case:
// "admin backup writes a 0600 file."
func TestE2EAdminBackupWritesA0600File(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	const token = "admin-pat-for-e2e-backup"
	srv.RequireToken(token)
	content := []byte("fake tar bytes standing in for identity.db + projects.db")
	srv.EnableBackup(content)

	home := t.TempDir()
	env := []string{"HOME=" + home}
	// A credential seeded directly, the way `rgt auth login --token-stdin`
	// would leave it — the fake here gates every route on RequireToken, and
	// `/api/v1/auth/me` (which the PAT sign-in flow verifies against) is not
	// part of this fake's surface, so seeding the file is the faithful
	// equivalent of a completed `rgt auth login`.
	cfg := &config.UserConfig{}
	config.SetCredential(cfg, srv.URL(), token)
	if err := config.SaveTo(filepath.Join(home, ".regent", "config.toml"), cfg); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	workDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "backup.tar")
	out := e2eRunEnv(t, rgt, workDir, env, nil, "admin", "backup", srv.URL(), "--out", outPath)
	if !strings.Contains(out, outPath) {
		t.Errorf("backup output does not name the file it wrote:\n%s", out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("the credential leaked into command output:\n%s", out)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("backup file was not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup file mode = %o, want 0600", info.Mode().Perm())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("backup file content mismatch: got %q, want %q", got, content)
	}

	// A second run without --force must refuse to touch the file.
	if _, err := e2eRunEnvRaw(t, rgt, workDir, env, "admin", "backup", srv.URL(), "--out", outPath); err == nil {
		t.Error("a second backup without --force overwrote the existing file")
	}
	if got2, readErr := os.ReadFile(outPath); readErr != nil || string(got2) != string(content) {
		t.Error("the existing backup file was modified despite no --force")
	}
}
