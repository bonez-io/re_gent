package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/remotetest"
	"github.com/spf13/cobra"
)

// newTestPasswordLoginCmd builds a bare *cobra.Command wired the way
// runAuthPasswordLogin expects (stdin, stdout/stderr, a context), without
// going through AuthCmd()'s RunE — that dispatch gates the password path on
// isTerminal(os.Stdin), which is never true for a test binary's stdin, so
// exercising it through cmd.Execute() alone could never reach this code.
// readHiddenInput is the seam that stands in for the terminal-only password
// prompt: production wires it to term.ReadPassword; a test substitutes a
// fixed answer.
func newTestPasswordLoginCmd(t *testing.T, stdin string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "login"}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(stdin))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return cmd
}

func TestRunAuthPasswordLoginStoresCredentialAndNeverPrints(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	const (
		username = "alice"
		password = "s3cr3t-onboarding-password"
	)
	srv.EnablePasswordLogin(username, password, false)

	prevReadHiddenInput := readHiddenInput
	readHiddenInput = func(uintptr) ([]byte, error) { return []byte(password), nil }
	t.Cleanup(func() { readHiddenInput = prevReadHiddenInput })

	cmd := newTestPasswordLoginCmd(t, username+"\n")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	if err := runAuthPasswordLogin(cmd, cfg, srv.URL()); err != nil {
		t.Fatalf("runAuthPasswordLogin: %v", err)
	}

	out := cmd.OutOrStdout().(*bytes.Buffer).String()
	if !strings.Contains(out, "Signed in as "+username) {
		t.Errorf("output does not confirm sign-in: %q", out)
	}
	if strings.Contains(out, password) {
		t.Fatalf("password leaked into command output: %q", out)
	}

	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	credential := config.TokenForServer(saved, srv.URL())
	if credential == "" {
		t.Fatal("no credential was stored for the server")
	}
	if strings.Contains(out, credential) {
		t.Fatalf("minted machine credential leaked into command output: %q", out)
	}

	// The credential minted must be the machine-credential secret the fake's
	// PAT-creation route returned, not the password or the session cookie.
	if credential == password {
		t.Fatal("the password itself was stored as the credential")
	}

	// Exactly one setup-code-free machine credential was created, named after
	// this machine, authenticated by the session + CSRF the login handed
	// back — proving the CSRF header and cookie both made it onto the
	// request rather than the fake rejecting it for some other reason and
	// this test passing by accident.
	created := srv.RecordedRequests("POST /api/v1/auth/tokens")
	if len(created) != 1 {
		t.Fatalf("machine credential creation requests = %d, want 1", len(created))
	}
	name, _ := created[0]["name"].(string)
	if !strings.Contains(name, "(cli)") {
		t.Errorf("machine credential name = %q, want it to carry \"(cli)\"", name)
	}
}

func TestRunAuthPasswordLoginRefusesWhenPasswordChangeRequired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	const (
		username = "admin"
		password = "initial-random-password"
	)
	srv.EnablePasswordLogin(username, password, true)

	prevReadHiddenInput := readHiddenInput
	readHiddenInput = func(uintptr) ([]byte, error) { return []byte(password), nil }
	t.Cleanup(func() { readHiddenInput = prevReadHiddenInput })

	cmd := newTestPasswordLoginCmd(t, username+"\n")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	err = runAuthPasswordLogin(cmd, cfg, srv.URL())
	if err == nil {
		t.Fatal("expected an error while the initial admin password is still in force")
	}
	if !strings.Contains(err.Error(), "web UI") {
		t.Errorf("error does not point at finishing setup in the web UI: %v", err)
	}

	saved, loadErr := config.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if config.TokenForServer(saved, srv.URL()) != "" {
		t.Error("a machine credential was stored despite password_change_required")
	}
	if got := srv.RecordedRequests("POST /api/v1/auth/tokens"); len(got) != 0 {
		t.Errorf("a machine credential was minted despite password_change_required: %d requests", len(got))
	}
}

func TestRunAuthPasswordLoginRejectsWrongPassword(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnablePasswordLogin("alice", "correct-password", false)

	prevReadHiddenInput := readHiddenInput
	readHiddenInput = func(uintptr) ([]byte, error) { return []byte("wrong-password"), nil }
	t.Cleanup(func() { readHiddenInput = prevReadHiddenInput })

	cmd := newTestPasswordLoginCmd(t, "alice\n")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	if err := runAuthPasswordLogin(cmd, cfg, srv.URL()); err == nil {
		t.Fatal("expected an error for a wrong password")
	}
	if config.TokenForServer(cfg, srv.URL()) != "" {
		t.Error("a credential was stored for a rejected password")
	}
}
