package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
)

// fakeDeviceServer is a minimal hand-rolled server implementing exactly the
// RFC 0004 routes `rgt auth login` needs for the device flow: capabilities
// (advertising "device"), device start/poll, and /api/v1/auth/me (used to
// confirm the identity a token was issued for, same as the PAT flow). It
// approves the device code on the Nth poll, so the polling loop in
// runAuthDeviceLogin is exercised for real rather than mocked away.
type fakeDeviceServer struct {
	*httptest.Server
	mu           sync.Mutex
	pollsUntilOK int
	polls        int
	accessToken  string
}

func newFakeDeviceServer(t *testing.T, pollsUntilOK int, accessToken string) *fakeDeviceServer {
	t.Helper()
	f := &fakeDeviceServer{pollsUntilOK: pollsUntilOK, accessToken: accessToken}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDeviceServer) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/capabilities":
		writeTestJSON(w, http.StatusOK, map[string]any{
			"deployment": "managed", "api_version": "v1",
			"auth_methods": []string{"device"}, "bootstrap_required": false,
			"features": []string{},
		})
	case "/api/v1/auth/device":
		writeTestJSON(w, http.StatusOK, map[string]any{
			"device_code": "dc-1", "user_code": "ABCD-1234",
			"verification_url": f.URL + "/device", "interval": 1, "expires_in": 600,
		})
	case "/api/v1/auth/device/token":
		f.mu.Lock()
		f.polls++
		ready := f.polls >= f.pollsUntilOK
		f.mu.Unlock()
		if !ready {
			writeTestJSON(w, http.StatusBadRequest, map[string]string{"code": "authorization_pending"})
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"access_token": f.accessToken, "refresh_token": "refresh-xyz", "expires_in": 3600,
		})
	case "/api/v1/auth/me":
		if r.Header.Get("Authorization") != "Bearer "+f.accessToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"viewer":      map[string]any{"id": "usr_1", "username": "shay", "display_name": "Shay", "instance_owner": false},
			"auth_method": "device",
		})
	default:
		http.NotFound(w, r)
	}
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// The headline case: a server advertising "device" makes `rgt auth login`
// run the device flow instead of prompting for a token, and the resulting
// access/refresh pair is stored keyed by server.
func TestAuthLoginUsesDeviceFlowWhenServerSupportsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newFakeDeviceServer(t, 2, "device-access-token")

	out := executeAuthCommand(t, nil, "login", srv.URL)
	if !strings.Contains(out, "Signed in") || !strings.Contains(out, "Shay") {
		t.Fatalf("login output = %q", out)
	}
	if strings.Contains(out, "device-access-token") {
		t.Fatalf("login output leaked the access token: %q", out)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.TokenForServer(cfg, srv.URL); got != "device-access-token" {
		t.Fatalf("stored access token = %q, want device-access-token", got)
	}
	if got := config.RefreshTokenForServer(cfg, srv.URL); got != "refresh-xyz" {
		t.Fatalf("stored refresh token = %q, want refresh-xyz", got)
	}
	if config.CredentialExpired(cfg, srv.URL) {
		t.Fatal("a freshly issued device token should not read as expired")
	}
}

// A server that only lists "pat" must keep using the token-prompt flow
// exactly as before RFC 0004 — this is the compatibility guarantee, not just
// a nice-to-have.
func TestAuthLoginKeepsPATFlowWhenServerHasNoDeviceMethod(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const token = "rgt_pat_test_secret_that_must_not_be_printed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/capabilities":
			writeTestJSON(w, http.StatusOK, map[string]any{
				"deployment": "self-hosted", "api_version": "v1",
				"auth_methods": []string{"pat"}, "bootstrap_required": false, "features": []string{},
			})
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]any{
				"viewer":      map[string]any{"id": "usr_1", "username": "shay", "display_name": "Shay", "instance_owner": true},
				"auth_method": "pat",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	out := executeAuthCommand(t, strings.NewReader(token+"\n"), "login", server.URL, "--token-stdin")
	if !strings.Contains(out, "Signed in") {
		t.Fatalf("login output = %q", out)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.TokenForServer(cfg, server.URL); got != token {
		t.Fatal("PAT flow did not store the token")
	}
	if got := config.RefreshTokenForServer(cfg, server.URL); got != "" {
		t.Fatalf("a PAT login must not fabricate a refresh token, got %q", got)
	}
}

// A server that is unreachable when capabilities is probed must still fall
// back to the PAT flow rather than failing the whole login — capabilities
// absence is legacy, not an error.
func TestAuthLoginFallsBackToPATWhenCapabilitiesUnreachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const token = "rgt_pat_test_secret_that_must_not_be_printed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" {
			http.NotFound(w, r) // capabilities and everything else 404, as a
			return              // server that predates RFC 0004 would.
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"viewer":      map[string]any{"id": "usr_1", "username": "shay", "display_name": "Shay", "instance_owner": true},
			"auth_method": "pat",
		})
	}))
	t.Cleanup(server.Close)

	out := executeAuthCommand(t, strings.NewReader(token+"\n"), "login", server.URL, "--token-stdin")
	if !strings.Contains(out, "Signed in") {
		t.Fatalf("login output = %q", out)
	}
}
