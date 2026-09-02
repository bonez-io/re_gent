package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/selfhosted"
)

// TestSelfHostedDevLoop exercises the whole "Stream F" local dev loop end to
// end, the same sequence docs/ui-development.md and scripts/dev-smoke.sh
// walk a person through by hand:
//
//	regent-server (self-hosted auth) -> bootstrap the first owner ->
//	rgt auth login -> git init a project -> rgt connect -> one captured
//	Claude turn -> rgt sync -> the server holds the session, readable with a
//	bearer token and refused anonymously.
//
// Unlike test/e2e_server_test.go this drives the *secure* self-hosted
// composition (selfhosted.Server, run in-process via httptest so no port or
// Docker is needed) rather than the open core server, because that is what
// `make serve` and the production profile actually run. Every rgt invocation
// gets its own HOME so this test never reads or writes the developer's real
// ~/.regent/config.toml, and REGENT_SERVER_URL is cleared (see TestMain) so
// it never inherits ambient server-mode configuration either.
func TestSelfHostedDevLoop(t *testing.T) {
	rgt := buildTestBinary(t)

	dataDir := t.TempDir()
	srv, setup, err := selfhosted.New(dataDir)
	if err != nil {
		t.Fatalf("start self-hosted server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	if !setup.BootstrapRequired {
		t.Fatalf("fresh self-hosted server did not require bootstrap: %#v", setup)
	}

	// capabilities is public and unauthenticated, exactly like a browser or
	// dev-bootstrap.sh checking whether setup is still needed.
	capBody := httpGetJSON(t, ts.URL+"/api/v1/capabilities", "")
	if required, _ := capBody["bootstrap_required"].(bool); !required {
		t.Fatalf("capabilities did not report bootstrap_required=true: %#v", capBody)
	}

	// Bootstrap the first owner the way scripts/dev-bootstrap.sh does: the
	// bootstrap credential authorizes exactly one POST, which both claims it
	// and creates the owner.
	pat, csrfToken := bootstrapFirstOwner(t, ts.URL, setup.BootstrapToken, "smoke", "Smoke Test")
	if pat == "" {
		t.Fatal("bootstrap did not return a personal access token")
	}
	if csrfToken == "" {
		t.Fatal("bootstrap did not return a CSRF token")
	}

	// A second bootstrap attempt must fail: the credential is one-time, the
	// same guarantee docs/self-hosted.md and dev-bootstrap.sh's idempotency
	// both depend on.
	if status := bootstrapAttemptStatus(t, ts.URL, setup.BootstrapToken); status != http.StatusUnauthorized {
		t.Fatalf("second bootstrap attempt with the same credential = %d, want %d (bootstrap must be one-time)", status, http.StatusUnauthorized)
	}

	// Each "machine" in this test gets its own HOME so config.toml lands in a
	// directory this test owns, never the real developer's.
	home := t.TempDir()
	env := []string{"HOME=" + home, "REGENT_SERVER_URL="}

	e2eRunEnv(t, rgt, home, env, []byte(pat+"\n"), "auth", "login", ts.URL, "--token-stdin")

	project := filepath.Join(t.TempDir(), "smoke-project")
	mustMkdirAll(t, project)
	gitInit(t, project)

	// --as only supplies a display name (RFC 0004): the server assigns its
	// own opaque storage key ("prj_<hex>") to the enrolled project. So the
	// URL key used below is read back from the binding connect writes, not
	// assumed to equal displayName.
	const displayName = "smoke-project"
	out := e2eRunEnv(t, rgt, project, env, nil, "connect", ts.URL, "--as", displayName, "--agent", "claude")
	assertContains(t, out, "Registered", "connect output")

	binding, err := config.LoadRemoteBinding(filepath.Join(project, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("read .regent/config.toml after connect: %v", err)
	}
	projectKey := binding.Key()
	if projectKey == "" {
		t.Fatalf("connect wrote no project_id or repo_id into .regent/config.toml: %#v", binding)
	}

	const sessionID = "dev-loop-session"
	captureTurn(t, rgt, project, env, sessionID, "t1", "hello.go")

	syncOut := e2eRunEnv(t, rgt, project, env, nil, "sync")
	t.Logf("rgt sync:\n%s", syncOut)

	// The project registry's view: the enrolled project is listed, under the
	// display name --as requested, at the key connect bound to.
	projectsBody := httpGetJSON(t, ts.URL+"/api/v1/projects", "Bearer "+pat)
	projects, _ := projectsBody["projects"].([]any)
	listed := false
	for _, p := range projects {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		name, _ := m["display_name"].(string)
		if id == projectKey && name == displayName {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("GET /api/v1/projects did not list %q with display_name %q: %#v", projectKey, displayName, projectsBody)
	}

	// The colleague's view: read the session back over the wire with a
	// bearer token, the same request docs/self-hosted.md and
	// scripts/dev-smoke.sh both make.
	sessionsBody := httpGetJSON(t, ts.URL+"/"+projectKey+"/api/sessions", "Bearer "+pat)
	sessions, _ := sessionsBody["sessions"].([]any)
	found := false
	for _, s := range sessions {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["session_id"].(string); strings.Contains(id, sessionID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("server holds no session matching %q after connect+capture+sync: %#v", sessionID, sessionsBody)
	}

	// Anonymous reads of the same endpoint must be refused: this is the
	// property that makes self-hosted mode worth running over the open core
	// server for local dev in the first place.
	anonStatus := httpGetStatus(t, ts.URL+"/"+projectKey+"/api/sessions", "")
	if anonStatus != http.StatusUnauthorized {
		t.Errorf("anonymous GET %s/api/sessions = %d, want %d", projectKey, anonStatus, http.StatusUnauthorized)
	}
}

// bootstrapFirstOwner POSTs the bootstrap credential the way
// scripts/dev-bootstrap.sh does and returns the new owner's personal access
// token and CSRF token.
func bootstrapFirstOwner(t *testing.T, baseURL, bootstrapToken, username, displayName string) (pat, csrfToken string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "display_name": displayName})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/bootstrap", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build bootstrap request: %v", err)
	}
	req.Header.Set("Authorization", "Bootstrap "+bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/bootstrap: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/auth/bootstrap: status %d", resp.StatusCode)
	}
	var out struct {
		Token     string `json:"token"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	return out.Token, out.CSRFToken
}

// bootstrapAttemptStatus POSTs a bootstrap attempt and returns just the
// status code, for asserting the credential cannot be reused.
func bootstrapAttemptStatus(t *testing.T, baseURL, bootstrapToken string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "again", "display_name": "Again"})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/bootstrap", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build bootstrap request: %v", err)
	}
	req.Header.Set("Authorization", "Bootstrap "+bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/bootstrap: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// httpGetJSON performs an authenticated (or, with an empty bearer, anonymous)
// GET and decodes the JSON body, failing the test on transport errors or a
// non-2xx status.
func httpGetJSON(t *testing.T, url, bearer string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET %s: %v", url, err)
	}
	return body
}

// httpGetStatus performs a GET and returns just the status code, for
// asserting a request is refused rather than decoding its (error) body.
func httpGetStatus(t *testing.T, url, bearer string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// gitInit makes dir a minimal git repository so isProjectDir accepts it as a
// connect target, without giving it a remote (connect is invoked with
// --as, so no identity is derived from one).
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "smoke@example.com"},
		{"config", "user.name", "Smoke Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
