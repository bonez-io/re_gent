package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bonez-io/re_gent/serverauth"
)

// ---------------------------------------------------------------------------
// capabilities
// ---------------------------------------------------------------------------

func TestCapabilitiesIsPublicAndDefaultsToOpenDocument(t *testing.T) {
	// Even with a controller that denies everything, capabilities must be
	// served with no authentication at all, exactly like /healthz.
	_, _, ts := newTestServer(t, WithAccessController(denyAllController{}))

	resp, err := http.Get(ts.URL + "/api/v1/capabilities")
	if err != nil {
		t.Fatalf("GET capabilities: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc["deployment"] != "open" {
		t.Fatalf("deployment = %v, want open", doc["deployment"])
	}
	features, _ := doc["features"].([]any)
	if !containsAny(features, "project_ids") {
		t.Fatalf("features = %v, want to contain project_ids", features)
	}
}

func TestWithCapabilitiesOverridesDocument(t *testing.T) {
	_, _, ts := newTestServer(t, WithCapabilities(func(*http.Request) map[string]any {
		return map[string]any{"deployment": "managed", "api_version": "v1"}
	}))
	resp, err := http.Get(ts.URL + "/api/v1/capabilities")
	if err != nil {
		t.Fatalf("GET capabilities: %v", err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	if doc["deployment"] != "managed" {
		t.Fatalf("deployment = %v, want managed", doc["deployment"])
	}
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

type denyAllController struct{}

func (denyAllController) Authenticate(*http.Request) (serverauth.Principal, error) {
	return serverauth.Principal{}, serverauth.ErrNoCredentials
}
func (denyAllController) Authorize(context.Context, serverauth.Principal, serverauth.Permission) error {
	return serverauth.ErrUnauthenticated
}

// ---------------------------------------------------------------------------
// storage locator
// ---------------------------------------------------------------------------

type customLocator struct{ root string }

func (l customLocator) ProjectRoot(tenantID, projectID string) (string, error) {
	return filepath.Join(l.root, "tenant-"+orEmpty(tenantID), projectID), nil
}

func orEmpty(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func TestWithStorageLocatorRedirectsProjectStorage(t *testing.T) {
	dataDir := t.TempDir()
	altRoot := t.TempDir()
	srv, err := New(dataDir, WithStorageLocator(customLocator{root: altRoot}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.CreateRepo("alpha"); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	want := filepath.Join(altRoot, "tenant-none", "alpha")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected storage at %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "repos", "alpha")); err == nil {
		t.Fatalf("storage was created under the default layout despite a custom locator")
	}
}

// ---------------------------------------------------------------------------
// limiter
// ---------------------------------------------------------------------------

type fakeLimiter struct {
	err func(serverauth.LimitRequest) error
}

func (l fakeLimiter) Check(_ context.Context, _ serverauth.Principal, req serverauth.LimitRequest) error {
	if l.err == nil {
		return nil
	}
	return l.err(req)
}

func quotaDenyingEverything(serverauth.LimitRequest) error {
	return &serverauth.ErrQuotaExceeded{Reason: "test quota"}
}

func TestLimiterBlocksObjectWriteWith413AndCode(t *testing.T) {
	_, _, ts := newTestServer(t, WithLimiter(fakeLimiter{err: quotaDenyingEverything}))
	content := []byte("hello")
	hash := hashOf(content)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/alpha/objects/"+string(hash), bytes.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "quota_exceeded" {
		t.Fatalf("body = %#v, want code=quota_exceeded", body)
	}
}

func TestLimiterBlocksRefWriteWith413(t *testing.T) {
	// This limiter allows the object write (so the ref has a real target to
	// point at) and blocks only the ref move, isolating the ref-write check
	// from the object-write check.
	_, _, ts := newTestServer(t, WithLimiter(fakeLimiter{err: func(req serverauth.LimitRequest) error {
		if req.Kind != serverauth.LimitKindRef {
			return nil
		}
		return &serverauth.ErrQuotaExceeded{Reason: "test ref quota"}
	}}))
	content := []byte("step-object")
	hash := hashOf(content)
	putReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/alpha/objects/"+string(hash), bytes.NewReader(content))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	_, _ = io.Copy(io.Discard, putResp.Body)
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("object PUT status = %d, want 201", putResp.StatusCode)
	}

	body, _ := json.Marshal(map[string]string{"old": "", "new": string(hash)})
	refResp, err := http.Post(ts.URL+"/alpha/refs/sessions/s1", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST ref: %v", err)
	}
	defer refResp.Body.Close()
	if refResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("ref POST status = %d, want 413", refResp.StatusCode)
	}
}

func TestLimiterBlocksProjectCreateWith413(t *testing.T) {
	_, _, ts := newTestServer(t, WithLimiter(fakeLimiter{err: quotaDenyingEverything}))
	body, _ := json.Marshal(map[string]string{"repo_id": "alpha"})
	resp, err := http.Post(ts.URL+"/repos", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /repos: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}

	body, _ = json.Marshal(map[string]string{"display_name": "Alpha"})
	resp2, err := http.Post(ts.URL+"/api/v1/projects", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/projects: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// ingest filter
// ---------------------------------------------------------------------------

type fakeIngestFilter struct{ reject bool }

func (f fakeIngestFilter) Filter(context.Context, serverauth.Principal, string, string, []byte) (IngestAction, string, error) {
	if f.reject {
		return IngestReject, "looks like a secret", nil
	}
	return IngestAccept, "", nil
}

func TestIngestFilterRejectsObjectWith422AndCode(t *testing.T) {
	_, dataDir, ts := newTestServer(t, WithIngestFilter(fakeIngestFilter{reject: true}))
	content := []byte("super-secret-value")
	hash := hashOf(content)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/alpha/objects/"+string(hash), bytes.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "ingest_rejected" || body["error"] != "looks like a secret" {
		t.Fatalf("body = %#v", body)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "repos", "alpha", "objects", string(hash[:2]), string(hash))); err == nil {
		t.Fatal("rejected object was written to disk")
	}
}

func TestIngestFilterAcceptsByDefault(t *testing.T) {
	_, _, ts := newTestServer(t)
	content := []byte("plain content")
	hash := hashOf(content)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/alpha/objects/"+string(hash), bytes.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// versioned project API + legacy compatibility
// ---------------------------------------------------------------------------

func TestVersionedProjectAPICreateIsIdempotentByFingerprint(t *testing.T) {
	_, _, ts := newTestServer(t)
	fp := "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123ab"

	first := postJSON(t, ts.URL+"/api/v1/projects", map[string]string{"fingerprint": fp, "display_name": "Alpha", "remote": "example.com/alpha"})
	if first.status != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201: %s", first.status, first.body)
	}
	var firstOut struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(first.body, &firstOut); err != nil {
		t.Fatalf("decode: %v; body=%s", err, first.body)
	}
	if !firstOut.Created || firstOut.Project.ID == "" {
		t.Fatalf("unexpected first response: %#v", firstOut)
	}

	second := postJSON(t, ts.URL+"/api/v1/projects", map[string]string{"fingerprint": fp, "display_name": "Alpha Again"})
	if second.status != http.StatusOK {
		t.Fatalf("second create status = %d, want 200 (idempotent): %s", second.status, second.body)
	}
	var secondOut struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(second.body, &secondOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if secondOut.Created {
		t.Fatal("second create reported created=true, want the connect-once guarantee (created=false)")
	}
	if secondOut.Project.ID != firstOut.Project.ID {
		t.Fatalf("second create id = %s, want the same project %s", secondOut.Project.ID, firstOut.Project.ID)
	}

	get, err := http.Get(ts.URL + "/api/v1/projects/" + firstOut.Project.ID)
	if err != nil {
		t.Fatalf("GET project: %v", err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET project status = %d, want 200", get.StatusCode)
	}

	patchBody, _ := json.Marshal(map[string]string{"display_name": "Renamed Alpha"})
	patchReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/projects/"+firstOut.Project.ID, bytes.NewReader(patchBody))
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("PATCH project: %v", err)
	}
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", patchResp.StatusCode)
	}
	var patched struct {
		Project struct {
			DisplayName string `json:"display_name"`
		} `json:"project"`
	}
	_ = json.NewDecoder(patchResp.Body).Decode(&patched)
	if patched.Project.DisplayName != "Renamed Alpha" {
		t.Fatalf("display name = %q, want %q", patched.Project.DisplayName, "Renamed Alpha")
	}

	list, err := http.Get(ts.URL + "/api/v1/projects")
	if err != nil {
		t.Fatalf("GET /api/v1/projects: %v", err)
	}
	defer list.Body.Close()
	var listOut struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	_ = json.NewDecoder(list.Body).Decode(&listOut)
	if len(listOut.Projects) != 1 || listOut.Projects[0].ID != firstOut.Project.ID {
		t.Fatalf("list = %#v, want exactly the one project", listOut.Projects)
	}
}

func TestVersionedProjectAPIRequiresDisplayNameWithoutFingerprint(t *testing.T) {
	_, _, ts := newTestServer(t)
	resp := postJSON(t, ts.URL+"/api/v1/projects", map[string]string{})
	if resp.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.status, resp.body)
	}
	var body map[string]string
	_ = json.Unmarshal(resp.body, &body)
	if body["code"] != "invalid_request" {
		t.Fatalf("code = %q, want invalid_request", body["code"])
	}
}

func TestLegacyRepoAppearsInVersionedProjectListing(t *testing.T) {
	_, _, ts := newTestServer(t)
	if status := createRepo(t, ts, "legacy-one"); status != http.StatusCreated {
		t.Fatalf("create legacy repo: status %d", status)
	}
	list, err := http.Get(ts.URL + "/api/v1/projects")
	if err != nil {
		t.Fatalf("GET /api/v1/projects: %v", err)
	}
	defer list.Body.Close()
	var listOut struct {
		Projects []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"projects"`
	}
	_ = json.NewDecoder(list.Body).Decode(&listOut)
	found := false
	for _, p := range listOut.Projects {
		if p.ID == "legacy-one" {
			found = true
		}
	}
	if !found {
		t.Fatalf("legacy repo missing from versioned listing: %#v", listOut.Projects)
	}

	get, err := http.Get(ts.URL + "/api/v1/projects/legacy-one")
	if err != nil {
		t.Fatalf("GET legacy project: %v", err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET legacy project status = %d, want 200", get.StatusCode)
	}
}

func TestPreexistingRepoDirectoryIsListedWithoutARegistryRow(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "repos", "predates-registry"), 0o755); err != nil {
		t.Fatalf("seed legacy dir: %v", err)
	}
	srv, err := New(dataDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	list, err := http.Get(ts.URL + "/api/v1/projects")
	if err != nil {
		t.Fatalf("GET /api/v1/projects: %v", err)
	}
	defer list.Body.Close()
	var listOut struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	_ = json.NewDecoder(list.Body).Decode(&listOut)
	found := false
	for _, p := range listOut.Projects {
		if p.ID == "predates-registry" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pre-registry directory missing from listing: %#v", listOut.Projects)
	}
}

func TestLegacyReposResponseCarriesDeprecationHeaders(t *testing.T) {
	_, _, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/repos")
	if err != nil {
		t.Fatalf("GET /repos: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Deprecation") != "true" {
		t.Fatalf("Deprecation header = %q, want true", resp.Header.Get("Deprecation"))
	}
	if link := resp.Header.Get("Link"); link == "" || link != `</api/v1/projects>; rel="successor-version"` {
		t.Fatalf("Link header = %q", link)
	}
}

// ---------------------------------------------------------------------------
// commit linkage
// ---------------------------------------------------------------------------

func TestCommitStepsRouteIsWiredAndHonest(t *testing.T) {
	_, _, ts := newTestServer(t)
	if status := createRepo(t, ts, "alpha"); status != http.StatusCreated {
		t.Fatalf("create repo: status %d", status)
	}
	resp, err := http.Get(ts.URL + "/alpha/api/commits/deadbeef/steps")
	if err != nil {
		t.Fatalf("GET commit steps: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Steps []any `json:"steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Honest, not fabricated: no step anywhere records a git_commit effect
	// today (see internal/store.Effect), so the truthful answer is empty —
	// this asserts the route is wired and returns a well-formed envelope, not
	// that it invents a match.
	if body.Steps == nil || len(body.Steps) != 0 {
		t.Fatalf("steps = %#v, want an empty (not nil, not fabricated) list", body.Steps)
	}
}

func TestCommitStepsRouteUnknownProjectIs404(t *testing.T) {
	_, _, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/missing/api/commits/deadbeef/steps")
	if err != nil {
		t.Fatalf("GET commit steps: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// jsonResult is a decoded HTTP JSON response.
type jsonResult struct {
	status int
	body   []byte
}

func postJSON(t *testing.T, url string, payload any) jsonResult {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return jsonResult{status: resp.StatusCode, body: body}
}
