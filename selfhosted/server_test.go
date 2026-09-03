package selfhosted

import (
	"bytes"
	"encoding/json"
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

// onboardAdmin drives the full RFC 0005 first-start flow against a freshly
// created Server: login with the initial password, replace it and create the
// organization via POST /api/v1/onboarding/admin, and return the resulting
// session cookie/CSRF plus the organization slug. Every test in this file
// that needs an authenticated owner starts here instead of the removed
// bootstrap-token flow.
func onboardAdmin(t *testing.T, srv *Server, setup Setup, orgDisplayName string) (cookie, csrf, orgSlug string) {
	t.Helper()
	if !setup.Generated || setup.AdminPassword == "" {
		t.Fatalf("fresh selfhosted server unexpectedly has no generated initial password: %#v", setup)
	}
	login := serveRequest(srv, http.MethodPost, "/api/v1/auth/login", "", "",
		map[string]string{"username": setup.AdminUsername, "password": setup.AdminPassword})
	assertStatus(t, login, http.StatusCreated)
	var loginResp struct {
		CSRF                   string `json:"csrf"`
		PasswordChangeRequired bool   `json:"password_change_required"`
	}
	decodeResponse(t, login, &loginResp)
	if !loginResp.PasswordChangeRequired {
		t.Fatal("login with the initial password did not report password_change_required")
	}
	loginCookies := login.Result().Cookies()
	if len(loginCookies) != 1 {
		t.Fatalf("login response set %d cookies, want 1", len(loginCookies))
	}
	loginCookie := loginCookies[0].Name + "=" + loginCookies[0].Value

	onboard := serveRequestHeaders(srv, http.MethodPost, "/api/v1/onboarding/admin", "", loginCookie, map[string]any{
		"org": map[string]string{"display_name": orgDisplayName, "join_policy": "invite_only", "default_role": "reader"},
		"admin": map[string]string{
			"display_name": "Admin", "new_password": "correct-horse-battery-staple",
		},
	}, map[string]string{csrfHeaderName: loginResp.CSRF})
	assertStatus(t, onboard, http.StatusCreated)
	var onboardResp struct {
		CSRF string `json:"csrf"`
		Org  struct {
			Slug string `json:"slug"`
		} `json:"org"`
	}
	decodeResponse(t, onboard, &onboardResp)
	if onboardResp.CSRF == "" || onboardResp.Org.Slug == "" {
		t.Fatalf("onboarding response missing csrf or org slug: %s", onboard.Body.String())
	}
	cookies := onboard.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("onboarding response set %d cookies, want 1", len(cookies))
	}
	return cookies[0].Name + "=" + cookies[0].Value, onboardResp.CSRF, onboardResp.Org.Slug
}

// mintOwnerPAT exchanges an onboarded admin's session for a bearer PAT, the
// credential shape every other test in this package (and the conformance
// fixture) uses to act as "owner".
func mintOwnerPAT(t *testing.T, srv *Server, cookie, csrf string) string {
	t.Helper()
	resp := serveRequestHeaders(srv, http.MethodPost, "/api/v1/auth/tokens", "", cookie,
		map[string]any{"name": "test-owner-pat", "expires_in_days": 1}, map[string]string{csrfHeaderName: csrf})
	assertStatus(t, resp, http.StatusCreated)
	var created struct {
		Secret string `json:"secret"`
	}
	decodeResponse(t, resp, &created)
	if created.Secret == "" {
		t.Fatalf("token creation response missing secret: %s", resp.Body.String())
	}
	return "Bearer " + created.Secret
}

func TestSecureSelfHostedLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	srv, setup, err := New(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}

	assertStatus(t, serveRequest(srv, http.MethodGet, "/healthz", "", "", nil), http.StatusOK)
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/capabilities", "", "", nil), http.StatusOK)
	assertStatus(t, serveRequest(srv, http.MethodGet, "/repos", "", "", nil), http.StatusUnauthorized)
	assertStatus(t, serveRequest(srv, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"username": setup.AdminUsername, "password": "wrong"}), http.StatusUnauthorized)

	cookie, csrf, _ := onboardAdmin(t, srv, setup, "Test Org")
	ownerAuth := mintOwnerPAT(t, srv, cookie, csrf)

	me := serveRequest(srv, http.MethodGet, "/api/v1/auth/me", ownerAuth, "", nil)
	assertStatus(t, me, http.StatusOK)
	var meResp struct {
		User struct {
			InstanceOwner bool `json:"instance_owner"`
		} `json:"user"`
	}
	decodeResponse(t, me, &meResp)
	if !meResp.User.InstanceOwner {
		t.Fatalf("onboarded admin is not reported as instance owner: %s", me.Body.String())
	}

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
	// /api/feed (the first-run tutorial's long-poll, issue #107) is served by
	// the same public server core as /api/status and classified by the same
	// history:read action (internal/server permissionForRequest), so a
	// project reader can call it exactly like every other read route — no
	// selfhosted-specific wiring was needed to cover it.
	assertStatus(t, serveRequest(srv, http.MethodGet, "/alpha/api/feed", readerAuth, "", nil), http.StatusOK)
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

	// Browser sessions are accepted for reads but every cookie-authenticated
	// mutation must carry the matching CSRF token.
	assertStatus(t, serveRequest(srv, http.MethodPost, "/repos", "", cookie, map[string]string{"repo_id": "blocked-by-csrf"}), http.StatusForbidden)
	withCSRF := serveRequestHeaders(srv, http.MethodPost, "/repos", "", cookie, map[string]string{"repo_id": "browser-created"}, map[string]string{csrfHeaderName: csrf})
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
	restarted, restartedSetup, err := New(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if restartedSetup.Generated || restartedSetup.AdminPassword != "" || restartedSetup.PasswordChangeRequired {
		t.Fatalf("restart of an onboarded instance unexpectedly reported first-start state: %#v", restartedSetup)
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
	srv, _, err := New(t.TempDir(), "", "")
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
		{http.MethodGet, "/alpha/api/feed"},
		{http.MethodGet, "/api/v1/orgs/whatever"},
		{http.MethodPost, "/api/v1/admin/backup"},
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

func TestInitialAdminPasswordIsHashedAtRest(t *testing.T) {
	dataDir := t.TempDir()
	srv, setup, err := New(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	for _, path := range []string{filepath.Join(dataDir, "identity.db"), filepath.Join(dataDir, "identity.db-wal")} {
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if bytes.Contains(data, []byte(setup.AdminPassword)) {
			t.Fatalf("plaintext initial admin password appears in %s", filepath.Base(path))
		}
	}
}

func TestRestartKeepsSameInitialPassword(t *testing.T) {
	dataDir := t.TempDir()
	srv, setup, err := New(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, restartedSetup, err := New(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if restartedSetup.Generated || restartedSetup.AdminPassword != "" {
		t.Fatalf("restart before onboarding unexpectedly regenerated the initial password: %#v", restartedSetup)
	}
	if !restartedSetup.PasswordChangeRequired {
		t.Fatal("restart before onboarding should still report the initial password in force")
	}
	// The password from the first run must still work.
	login := serveRequest(restarted, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"username": setup.AdminUsername, "password": setup.AdminPassword})
	assertStatus(t, login, http.StatusCreated)
}

func TestExpiredPATIsRejected(t *testing.T) {
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	cookie, csrf, _ := onboardAdmin(t, srv, setup, "Test Org")
	ownerAuth := mintOwnerPAT(t, srv, cookie, csrf)
	token := strings.TrimPrefix(ownerAuth, "Bearer ")
	srv.identities.now = func() time.Time { return time.Now().UTC().Add(defaultPATLifetime + time.Hour) }
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/auth/me", "Bearer "+token, "", nil), http.StatusUnauthorized)
}

func TestLoginAttemptsAreRateLimited(t *testing.T) {
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	for attempt := 0; attempt < 10; attempt++ {
		assertStatus(t, serveRequest(srv, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"username": setup.AdminUsername, "password": "wrong"}), http.StatusUnauthorized)
	}
	limited := serveRequest(srv, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"username": setup.AdminUsername, "password": "wrong"})
	assertStatus(t, limited, http.StatusTooManyRequests)
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response omitted Retry-After")
	}
}

func TestOwnerRecoveryIssuesShortLivedAuditedPAT(t *testing.T) {
	dataDir := t.TempDir()
	srv, setup, err := New(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf, _ := onboardAdmin(t, srv, setup, "Test Org")
	_ = cookie
	_ = csrf
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
	restarted, _, err := New(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	assertStatus(t, serveRequest(restarted, http.MethodGet, "/api/v1/auth/me", "Bearer "+secret, "", nil), http.StatusOK)
}

func TestPasswordChangeRequiredGatesNonAuthRoutes(t *testing.T) {
	srv, setup, err := New(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	login := serveRequest(srv, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"username": setup.AdminUsername, "password": setup.AdminPassword})
	assertStatus(t, login, http.StatusCreated)
	cookies := login.Result().Cookies()
	cookie := cookies[0].Name + "=" + cookies[0].Value

	// A selfhosted-handled route reports the coded JSON reason.
	blocked := serveRequest(srv, http.MethodGet, "/api/v1/users", "", cookie, nil)
	assertStatus(t, blocked, http.StatusForbidden)
	var body struct {
		Code string `json:"code"`
	}
	decodeResponse(t, blocked, &body)
	if body.Code != "password_change_required" {
		t.Fatalf("blocked response code = %q, want password_change_required", body.Code)
	}

	// A core-delegated route is still denied (403), even though the core's
	// own error body does not carry selfhosted's "code" field.
	assertStatus(t, serveRequest(srv, http.MethodGet, "/repos", "", cookie, nil), http.StatusForbidden)

	// /api/v1/auth/me stays reachable so the UI can show the gated state.
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/auth/me", "", cookie, nil), http.StatusOK)
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

// TestUpgradeAdoptsExistingOwner covers a data directory from the
// bootstrap-token era: an instance owner exists, no instance_state row, no
// password. The server must adopt that owner as the admin instead of trying
// to create a second owner, print an initial password for them, and keep
// their existing tokens working so hooks never stop capturing mid-upgrade.
func TestUpgradeAdoptsExistingOwner(t *testing.T) {
	dir := t.TempDir()
	srv, setup, err := New(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf, _ := onboardAdmin(t, srv, setup, "Legacy Org")
	pat := mintOwnerPAT(t, srv, cookie, csrf)
	// Reshape the store into what a pre-RFC-0005 instance left behind.
	for _, stmt := range []string{
		"DELETE FROM instance_state",
		"DELETE FROM passwords",
		"DELETE FROM organizations",
		"UPDATE users SET password_change_required=0",
	} {
		if _, err := srv.identities.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}

	srv, setup, err = New(dir, "", "")
	if err != nil {
		t.Fatalf("upgrade start failed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if !setup.Generated || setup.AdminUsername != "admin" || setup.AdminPassword == "" {
		t.Fatalf("expected the existing owner adopted with a printed password, got %+v", setup)
	}
	// The old token still works on a data route.
	assertStatus(t, serveRequest(srv, http.MethodGet, "/api/v1/projects", pat, "", nil), http.StatusOK)
	// The printed password signs in and is flagged for replacement.
	login := serveRequest(srv, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"username": "admin", "password": setup.AdminPassword})
	assertStatus(t, login, http.StatusCreated)
	var body struct {
		PasswordChangeRequired bool `json:"password_change_required"`
	}
	decodeResponse(t, login, &body)
	if !body.PasswordChangeRequired {
		t.Fatalf("adopted owner should be required to replace the initial password: %s", login.Body.String())
	}
	// A second restart does not reprint or rotate.
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	srv2, setup2, err := New(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv2.Close() })
	if setup2.Generated || setup2.AdminPassword != "" || !setup2.PasswordChangeRequired {
		t.Fatalf("restart should keep the same initial password in force without reprinting: %+v", setup2)
	}
}
