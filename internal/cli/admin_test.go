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

// seedAdminCredential stores a credential for serverURL exactly as `rgt auth
// login --token-stdin` would, without needing a running auth flow: admin
// backup only reads config.TokenForServer, so this is enough to exercise it
// in isolation.
func seedAdminCredential(t *testing.T, serverURL, token string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	config.SetCredential(cfg, serverURL, token)
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func executeAdminCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := AdminCmd()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestAdminBackupWritesA0600File(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const token = "admin-pat-for-backup-test"
	content := []byte("fake tar bytes standing in for identity.db + projects.db")

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableBackup(content)

	seedAdminCredential(t, srv.URL(), token)

	outPath := filepath.Join(t.TempDir(), "backup.tar")
	out, err := executeAdminCommand(t, "backup", srv.URL(), "--out", outPath)
	if err != nil {
		t.Fatalf("rgt admin backup: %v\n%s", err, out)
	}
	if !strings.Contains(out, outPath) {
		t.Errorf("output does not name the file it wrote: %q", out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("credential leaked into command output: %q", out)
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
		t.Errorf("backup file content = %q, want %q", got, content)
	}
}

func TestAdminBackupRefusesToOverwriteWithoutForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const token = "admin-pat-for-overwrite-test"

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableBackup([]byte("first"))
	seedAdminCredential(t, srv.URL(), token)

	outPath := filepath.Join(t.TempDir(), "backup.tar")
	if err := os.WriteFile(outPath, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := executeAdminCommand(t, "backup", srv.URL(), "--out", outPath)
	if err == nil {
		t.Fatalf("expected a refusal to overwrite without --force, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error does not mention --force: %v", err)
	}
	got, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "already here" {
		t.Error("the existing file was overwritten despite no --force")
	}

	// --force lets the same command through and replaces the content.
	out, err = executeAdminCommand(t, "backup", srv.URL(), "--out", outPath, "--force")
	if err != nil {
		t.Fatalf("rgt admin backup --force: %v\n%s", err, out)
	}
	got, readErr = os.ReadFile(outPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "first" {
		t.Errorf("backup file content after --force = %q, want %q", got, "first")
	}
}

func TestAdminBackupRequiresSignIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableBackup([]byte("irrelevant"))

	outPath := filepath.Join(t.TempDir(), "backup.tar")
	_, err := executeAdminCommand(t, "backup", srv.URL(), "--out", outPath)
	if err == nil {
		t.Fatal("expected an error with no stored credential")
	}
	if _, statErr := os.Stat(outPath); statErr == nil {
		t.Error("a backup file was written despite no stored credential")
	}
}
