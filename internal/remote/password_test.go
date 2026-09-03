package remote

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/bonez-io/re_gent/internal/remotetest"
)

func newJarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func TestPasswordLoginThenCreateMachineCredential(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnablePasswordLogin("alice", "hunter22-password", false)

	client := newJarClient(t)
	result, err := PasswordLogin(context.Background(), client, srv.URL(), "alice", "hunter22-password")
	if err != nil {
		t.Fatalf("PasswordLogin: %v", err)
	}
	if result.User.Username != "alice" {
		t.Errorf("User.Username = %q, want alice", result.User.Username)
	}
	if result.CSRF == "" {
		t.Fatal("login response carried no CSRF token")
	}
	if result.PasswordChangeRequired {
		t.Error("PasswordChangeRequired = true, want false")
	}

	// The same client (same cookie jar) presents the session the login just
	// established; the CSRF token travels explicitly.
	secret, err := CreateMachineCredential(context.Background(), client, srv.URL(), result.CSRF, "test-machine (cli)")
	if err != nil {
		t.Fatalf("CreateMachineCredential: %v", err)
	}
	if secret == "" {
		t.Fatal("CreateMachineCredential returned an empty secret")
	}
	if secret == "hunter22-password" {
		t.Fatal("the password was returned as the machine credential")
	}
}

func TestPasswordLoginRejectsWrongPassword(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnablePasswordLogin("alice", "correct-password", false)

	client := newJarClient(t)
	_, err := PasswordLogin(context.Background(), client, srv.URL(), "alice", "wrong-password")
	if err == nil {
		t.Fatal("expected an error for a wrong password")
	}
}

func TestPasswordLoginReportsPasswordChangeRequired(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnablePasswordLogin("admin", "initial-random", true)

	client := newJarClient(t)
	result, err := PasswordLogin(context.Background(), client, srv.URL(), "admin", "initial-random")
	if err != nil {
		t.Fatalf("PasswordLogin: %v", err)
	}
	if !result.PasswordChangeRequired {
		t.Error("PasswordChangeRequired = false, want true for the initial admin password")
	}
}

// CreateMachineCredential must fail without a valid session, and a valid
// session without the matching CSRF header must be refused too — otherwise a
// page that merely gets a signed-in browser to load a URL could mint a
// credential on the user's behalf.
func TestCreateMachineCredentialRequiresSessionAndCSRF(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnablePasswordLogin("alice", "hunter22-password", false)

	// No session at all: a client with a jar that never logged in.
	anonClient := newJarClient(t)
	if _, err := CreateMachineCredential(context.Background(), anonClient, srv.URL(), "whatever", "m"); err == nil {
		t.Error("expected an error creating a credential with no session")
	}

	// A real session, but the wrong CSRF token.
	client := newJarClient(t)
	result, err := PasswordLogin(context.Background(), client, srv.URL(), "alice", "hunter22-password")
	if err != nil {
		t.Fatalf("PasswordLogin: %v", err)
	}
	if _, err := CreateMachineCredential(context.Background(), client, srv.URL(), result.CSRF+"-wrong", "m"); err == nil {
		t.Error("expected an error creating a credential with a mismatched CSRF token")
	}
}
