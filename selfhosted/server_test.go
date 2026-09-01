package selfhosted

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type bootstrapResponse struct {
	Viewer    User   `json:"viewer"`
	Token     string `json:"token"`
	CSRFToken string `json:"csrf_token"`
}

func TestSecureSelfHostedLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	srv, setup, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !setup.BootstrapRequired || !strings.HasPrefix(setup.BootstrapToken, bootstrapPrefix) {
		t.Fatalf("fresh setup = %#v, want one-time bootstrap credential", setup)
	}
	bootstrapPath := filepath.Join(dataDir, "bootstrap-token")
	bootstrapFile, err := os.ReadFile(bootstrapPath)
	if err != nil || strings.TrimSpace(string(bootstrapFile)) != setup.BootstrapToken {
		t.Fatalf("bootstrap delivery file: content=%q err=%v", bootstrapFile, err)
	}
	if runtime.GOOS != "windows" {
		bootstrapInfo, err := os.Stat(bootstrapPath)
		if err != nil || bootstrapInfo.Mode().Perm() != 0o600 {
			t.Fatalf("bootstrap delivery file mode: info=%v err=%v", bootstrapInfo, err)
		}
	}

	assertStatus(t, serveRequest(srv, http.MethodGet, "/healthz", "", "", nil), http.StatusOK)
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/capabilities", "", "", nil), http.StatusOK)
	assertStatus(t, serveRequest(srv, http.MethodGet, "/repos", "", "", nil), http.StatusUnauthorized)
	assertStatus(t, serveRequest(srv, http.MethodPost, "/api/v1/auth/bootstrap", "Bootstrap wrong", "", map[string]string{"username": "owner", "display_name": "Owner"}), http.StatusUnauthorized)

	bootstrap := serveRequest(srv, http.MethodPost, "/api/v1/auth/bootstrap", "Bootstrap "+setup.BootstrapToken, "", map[string]string{"username": "owner", "display_name": "Owner"})
	assertStatus(t, bootstrap, http.StatusCreated)
	var owner bootstrapResponse
	decodeResponse(t, bootstrap, &owner)
	if !owner.Viewer.InstanceOwner || !strings.HasPrefix(owner.Token, personalTokenPrefix) || owner.CSRFToken == "" {
		t.Fatalf("bootstrap response omitted owner credentials or identity: %#v", owner.Viewer)
	}
	if _, err := os.Stat(bootstrapPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap delivery file remains after setup: %v", err)
	}
	cookies := bootstrap.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie is not hardened: %#v", cookies)
	}
	assertStatus(t, serveRequest(srv, http.MethodPost, "/api/v1/auth/bootstrap", "Bootstrap "+setup.BootstrapToken, "", map[string]string{"username": "again", "display_name": "Again"}), http.StatusUnauthorized)

	ownerAuth := "Bearer " + owner.Token
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/auth/me", ownerAuth, "", nil), http.StatusOK)
	assertStatus(t, serveRequest(srv, http.MethodPost, "/repos", ownerAuth, "", map[string]string{"repo_id": "alpha"}), http.StatusCreated)

	createdUser := serveRequest(srv, http.MethodPost, "/api/v1/users", ownerAuth, "", map[string]any{"username": "reader", "display_name": "Read Only", "repo_id": "alpha", "role": RoleReader})
	assertStatus(t, createdUser, http.StatusCreated)
	var created struct {
		User         User   `json:"user"`
		InitialToken string `json:"initial_token"`
	}
	decodeResponse(t, createdUser, &created)
	if !strings.HasPrefix(created.InitialToken, personalTokenPrefix) {
		t.Fatal("new user's one-time token was not returned")
	}

	projectOwner := createTestUser(t, srv, ownerAuth, "project-owner", "Project Owner")
	projectAdmin := createTestUser(t, srv, ownerAuth, "project-admin", "Project Admin")
	assertStatus(t, serveRequest(srv, http.MethodPut, "/alpha/api/v1/access/members", ownerAuth, "", map[string]any{"user_id": projectOwner.User.ID, "role": RoleOwner}), http.StatusNoContent)
	assertStatus(t, serveRequest(srv, http.MethodPut, "/alpha/api/v1/access/members", ownerAuth, "", map[string]any{"user_id": projectAdmin.User.ID, "role": RoleAdmin}), http.StatusNoContent)
	assertStatus(t, serveRequest(srv, http.MethodPut, "/alpha/api/v1/access/members", "Bearer "+projectAdmin.InitialToken, "", map[string]any{"user_id": projectOwner.User.ID, "role": RoleReader}), http.StatusForbidden)

	readerAuth := "Bearer " + created.InitialToken
	assertStatus(t, serveRequest(srv, http.MethodGet, "/alpha/api/status", readerAuth, "", nil), http.StatusOK)
	assertStatus(t, serveRequest(srv, http.MethodPut, "/alpha/objects/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", readerAuth, "", []byte("content")), http.StatusForbidden)
	assertStatus(t, serveRequest(srv, http.MethodGet, "/missing/api/status", readerAuth, "", nil), http.StatusNotFound)

	members := serveRequest(srv, http.MethodGet, "/alpha/api/v1/access/members", readerAuth, "", nil)
	assertStatus(t, members, http.StatusOK)
	var memberList struct {
		Members []Member `json:"members"`
	}
	decodeResponse(t, members, &memberList)
	if len(memberList.Members) != 4 {
		t.Fatalf("members = %#v, want instance owner plus three project members", memberList.Members)
	}
	var tokenCreateAudits int
	if err := srv.identities.db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action='token.create'").Scan(&tokenCreateAudits); err != nil {
		t.Fatal(err)
	}
	if tokenCreateAudits != 3 {
		t.Fatalf("token.create audit count = %d, want 3 created-user credentials", tokenCreateAudits)
	}

	// Browser sessions are accepted for reads but every cookie-authenticated
	// mutation must carry the matching CSRF token.
	cookie := cookies[0].Name + "=" + cookies[0].Value
	assertStatus(t, serveRequest(srv, http.MethodPost, "/repos", "", cookie, map[string]string{"repo_id": "blocked-by-csrf"}), http.StatusForbidden)
	withCSRF := serveRequestHeaders(srv, http.MethodPost, "/repos", "", cookie, map[string]string{"repo_id": "browser-created"}, map[string]string{csrfHeaderName: owner.CSRFToken})
	assertStatus(t, withCSRF, http.StatusCreated)

	// A user's PAT can be revoked immediately and never becomes valid again.
	tokens := serveRequest(srv, http.MethodGet, "/api/v1/auth/tokens", readerAuth, "", nil)
	assertStatus(t, tokens, http.StatusOK)
	var tokenList struct {
		Tokens []Token `json:"tokens"`
	}
	decodeResponse(t, tokens, &tokenList)
	if len(tokenList.Tokens) != 1 {
		t.Fatalf("reader tokens = %#v", tokenList.Tokens)
	}
	assertStatus(t, serveRequest(srv, http.MethodDelete, "/api/v1/auth/tokens/"+tokenList.Tokens[0].ID, readerAuth, "", nil), http.StatusNoContent)
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/auth/me", readerAuth, "", nil), http.StatusUnauthorized)

	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, restartedSetup, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if restartedSetup.BootstrapRequired || restartedSetup.BootstrapToken != "" {
		t.Fatalf("restart reopened bootstrap: %#v", restartedSetup)
	}
	assertStatus(t, serveRequest(restarted, http.MethodGet, "/api/v1/auth/me", ownerAuth, "", nil), http.StatusOK)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dataDir, "identity.db"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("identity.db mode = %o, want 600", info.Mode().Perm())
		}
		dataInfo, err := os.Stat(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if dataInfo.Mode().Perm() != 0o700 {
			t.Fatalf("data dir mode = %o, want 700", dataInfo.Mode().Perm())
		}
	}
}

func TestAnonymousIdentityAndDataRoutesAreDenied(t *testing.T) {
	srv, _, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/repos"},
		{http.MethodGet, "/api/skills"},
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/auth/tokens"},
		{http.MethodPost, "/api/v1/auth/session"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/alpha/api/v1/access/members"},
		{http.MethodGet, "/alpha/api/status"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := serveRequest(srv, test.method, test.path, "", "", nil)
			assertStatus(t, response, http.StatusUnauthorized)
			if response.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 response omitted WWW-Authenticate")
			}
		})
	}
}

func createTestUser(t *testing.T, srv http.Handler, ownerAuth, username, displayName string) struct {
	User         User   `json:"user"`
	InitialToken string `json:"initial_token"`
} {
	t.Helper()
	response := serveRequest(srv, http.MethodPost, "/api/v1/users", ownerAuth, "", map[string]string{"username": username, "display_name": displayName})
	assertStatus(t, response, http.StatusCreated)
	var created struct {
		User         User   `json:"user"`
		InitialToken string `json:"initial_token"`
	}
	decodeResponse(t, response, &created)
	return created
}

func TestBootstrapSecretsAreHashedAtRest(t *testing.T) {
	dataDir := t.TempDir()
	srv, setup, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	for _, path := range []string{filepath.Join(dataDir, "identity.db"), filepath.Join(dataDir, "identity.db-wal")} {
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if bytes.Contains(data, []byte(setup.BootstrapToken)) {
			t.Fatalf("plaintext bootstrap credential appears in %s", filepath.Base(path))
		}
	}
}

func TestExpiredPATIsRejected(t *testing.T) {
	srv, setup, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	bootstrap := serveRequest(srv, http.MethodPost, "/api/v1/auth/bootstrap", "Bootstrap "+setup.BootstrapToken, "", map[string]string{"username": "owner", "display_name": "Owner"})
	var owner bootstrapResponse
	decodeResponse(t, bootstrap, &owner)
	srv.identities.now = func() time.Time { return time.Now().UTC().Add(defaultPATLifetime + time.Hour) }
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/auth/me", "Bearer "+owner.Token, "", nil), http.StatusUnauthorized)
}

func TestBootstrapAttemptsAreRateLimited(t *testing.T) {
	srv, _, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	for attempt := 0; attempt < 10; attempt++ {
		assertStatus(t, serveRequest(srv, http.MethodPost, "/api/v1/auth/bootstrap", "Bootstrap invalid", "", map[string]string{"username": "owner", "display_name": "Owner"}), http.StatusUnauthorized)
	}
	limited := serveRequest(srv, http.MethodPost, "/api/v1/auth/bootstrap", "Bootstrap invalid", "", map[string]string{"username": "owner", "display_name": "Owner"})
	assertStatus(t, limited, http.StatusTooManyRequests)
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response omitted Retry-After")
	}
}

func TestOwnerRecoveryIssuesShortLivedAuditedPAT(t *testing.T) {
	dataDir := t.TempDir()
	srv, setup, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := serveRequest(srv, http.MethodPost, "/api/v1/auth/bootstrap", "Bootstrap "+setup.BootstrapToken, "", map[string]string{"username": "owner", "display_name": "Owner"})
	assertStatus(t, bootstrap, http.StatusCreated)
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	token, secret, err := RecoverOwnerToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, personalTokenPrefix) || token.Name != "operator recovery" || time.Until(token.ExpiresAt) > 25*time.Hour {
		t.Fatalf("unexpected recovery token metadata: %#v", token)
	}
	restarted, _, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	assertStatus(t, serveRequest(restarted, http.MethodGet, "/api/v1/auth/me", "Bearer "+secret, "", nil), http.StatusOK)
}

func serveRequest(handler http.Handler, method, path, authorization, cookie string, body any) *httptest.ResponseRecorder {
	return serveRequestHeaders(handler, method, path, authorization, cookie, body, nil)
}

func serveRequestHeaders(handler http.Handler, method, path, authorization, cookie string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		if raw, ok := body.([]byte); ok {
			reader = bytes.NewReader(raw)
		} else {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		}
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, want, recorder.Body.String())
	}
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
}
