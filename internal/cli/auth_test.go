package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
)

func TestAuthLoginStatusLogoutLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const token = "rgt_pat_test_secret_that_must_not_be_printed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"viewer":{"id":"usr_1","username":"shay","display_name":"Shay","instance_owner":true},"auth_method":"pat"}`))
	}))
	t.Cleanup(server.Close)

	loginOut := executeAuthCommand(t, strings.NewReader(token+"\n"), "login", server.URL, "--token-stdin")
	if !strings.Contains(loginOut, "Signed in") || strings.Contains(loginOut, token) {
		t.Fatalf("login output is missing confirmation or leaked the token: %q", loginOut)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.TokenForServer(cfg, server.URL); got != token {
		t.Fatalf("stored token mismatch")
	}
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}

	statusOut := executeAuthCommand(t, nil, "status", server.URL)
	if !strings.Contains(statusOut, "Shay") || !strings.Contains(statusOut, "instance owner") || strings.Contains(statusOut, token) {
		t.Fatalf("unsafe or incomplete status output: %q", statusOut)
	}

	logoutOut := executeAuthCommand(t, nil, "logout", server.URL)
	if !strings.Contains(logoutOut, "Signed out") {
		t.Fatalf("logout output = %q", logoutOut)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.TokenForServer(cfg, server.URL); got != "" {
		t.Fatal("logout left the credential in machine config")
	}
}

func TestAuthLoginRejectsInvalidTokenWithoutSaving(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const rejected = "rgt_pat_rejected_secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	cmd := AuthCmd()
	cmd.SetArgs([]string{"login", server.URL, "--token-stdin"})
	cmd.SetIn(strings.NewReader(rejected))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || strings.Contains(err.Error()+out.String(), rejected) {
		t.Fatalf("invalid login error was missing or leaked token: err=%v out=%q", err, out.String())
	}
	cfg, loadErr := config.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if config.TokenForServer(cfg, server.URL) != "" {
		t.Fatal("rejected token was persisted")
	}
}

func TestAuthLoginHasNoTokenFlag(t *testing.T) {
	login, _, err := AuthCmd().Find([]string{"login"})
	if err != nil {
		t.Fatal(err)
	}
	if login.Flags().Lookup("token") != nil {
		t.Fatal("auth login exposes a process-visible --token flag")
	}
}

func TestAuthRequestRefusesPlaintextRemoteServer(t *testing.T) {
	_, err := verifyAuthToken(http.DefaultClient, "http://regent.example.com", "rgt_pat_test")
	if err == nil || !strings.Contains(err.Error(), "plaintext HTTP") {
		t.Fatalf("error = %v, want plaintext credential refusal", err)
	}
	if _, err := normalizeAuthServerURL("http://127.0.0.1:7654"); err != nil {
		t.Fatalf("loopback development URL was rejected: %v", err)
	}
}

func executeAuthCommand(t *testing.T, in *strings.Reader, args ...string) string {
	t.Helper()
	cmd := AuthCmd()
	cmd.SetArgs(args)
	if in != nil {
		cmd.SetIn(in)
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rgt auth %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}
