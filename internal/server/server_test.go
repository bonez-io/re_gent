package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"lukechampine.com/blake3"

	"github.com/regent-vcs/regent/internal/store"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestServer returns a server backed by a temp dir, its data dir, and a
// running httptest.Server in front of it.
func newTestServer(t *testing.T, opts ...Option) (*Server, string, *httptest.Server) {
	t.Helper()
	dataDir := t.TempDir()
	srv, err := New(dataDir, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, dataDir, ts
}

// hashOf returns the BLAKE3 hash the store would assign to content.
func hashOf(content []byte) store.Hash {
	sum := blake3.Sum256(content)
	return store.Hash(hex.EncodeToString(sum[:]))
}

func createRepo(t *testing.T, ts *httptest.Server, repoID string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"repo_id": repoID})
	resp, err := http.Post(ts.URL+"/repos", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /repos: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestServerIsOpen verifies the server serves every endpoint without any auth —
// it is always open.
func TestServerIsOpen(t *testing.T) {
	_, _, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/repos")
	if err != nil {
		t.Fatalf("GET /repos: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("open server GET /repos = %d, want 200", resp.StatusCode)
	}
}

// TestHealthz verifies /healthz returns 200 "ok".
func TestHealthz(t *testing.T) {
	_, _, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Errorf("GET /healthz body = %q, want \"ok\"", body)
	}
}

// putObject uploads data to repo and returns (status, hash).
func putObject(t *testing.T, ts *httptest.Server, repo string, data []byte) (int, store.Hash) {
	t.Helper()
	h := hashOf(data)
	return putObjectAs(t, ts, repo, string(h), data), h
}

// putObjectAs uploads data under an arbitrary (possibly wrong) hash.
func putObjectAs(t *testing.T, ts *httptest.Server, repo, hash string, data []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/%s/objects/%s", ts.URL, repo, hash), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func headObject(t *testing.T, ts *httptest.Server, repo string, h store.Hash) int {
	t.Helper()
	resp, err := http.Head(fmt.Sprintf("%s/%s/objects/%s", ts.URL, repo, h))
	if err != nil {
		t.Fatalf("HEAD object: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func getObject(t *testing.T, ts *httptest.Server, repo string, h store.Hash) (int, []byte) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/%s/objects/%s", ts.URL, repo, h))
	if err != nil {
		t.Fatalf("GET object: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// postRef sends a CAS ref update and returns (status, server's current hash).
func postRef(t *testing.T, ts *httptest.Server, repo, ref, oldHash, newHash string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"old": oldHash, "new": newHash})
	resp, err := http.Post(fmt.Sprintf("%s/%s/refs/%s", ts.URL, repo, ref),
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST ref: %v", err)
	}
	defer resp.Body.Close()
	var rr struct {
		Hash string `json:"hash"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	return resp.StatusCode, rr.Hash
}

func getRef(t *testing.T, ts *httptest.Server, repo, ref string) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/%s/refs/%s", ts.URL, repo, ref))
	if err != nil {
		t.Fatalf("GET ref: %v", err)
	}
	defer resp.Body.Close()
	var rr struct {
		Hash string `json:"hash"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	return resp.StatusCode, rr.Hash
}

func listRefs(t *testing.T, ts *httptest.Server, repo, dir string) map[string]string {
	t.Helper()
	u := fmt.Sprintf("%s/%s/refs", ts.URL, repo)
	if dir != "" {
		u += "?dir=" + dir
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET refs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET refs: status %d", resp.StatusCode)
	}
	var lr struct {
		Refs map[string]string `json:"refs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatalf("decode refs: %v", err)
	}
	return lr.Refs
}

// objectPath is where a repo stores one object on disk.
func objectPath(dataDir, repo string, h store.Hash) string {
	return filepath.Join(dataDir, "repos", repo, "objects", string(h)[:2], string(h))
}

// ---------------------------------------------------------------------------
// repo registry & identity
// ---------------------------------------------------------------------------

func TestValidateRepoID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"simple", "alpha", false},
		{"with digits", "repo42", false},
		{"with punctuation", "my-repo_v2.1", false},
		{"single char", "a", false},
		{"empty", "", true},
		{"uppercase aliases on case-insensitive filesystems", "Alpha", true},
		{"leading dash", "-alpha", true},
		{"leading dot", ".alpha", true},
		{"dot", ".", true},
		{"dotdot", "..", true},
		{"slash", "a/b", true},
		{"backslash", `a\b`, true},
		{"space", "my repo", true},
		{"nul byte", "a\x00b", true},
		{"unicode", "répo", true},
		{"too long", strings.Repeat("a", 65), true},
		{"max length", strings.Repeat("a", 64), false},
		{"registry endpoint", "repos", true},
		{"windows device", "con", true},
		{"newline injection", "alpha\nbeta", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRepoID(tt.id)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateRepoID(%q) = nil, want error", tt.id)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateRepoID(%q) = %v, want nil", tt.id, err)
			}
		})
	}
}

func TestRepoRegistry(t *testing.T) {
	_, dataDir, ts := newTestServer(t)

	if got := createRepo(t, ts, "alpha"); got != http.StatusCreated {
		t.Fatalf("create alpha: status %d, want 201", got)
	}
	if got := createRepo(t, ts, "alpha"); got != http.StatusOK {
		t.Fatalf("re-create alpha: status %d, want 200 (idempotent)", got)
	}
	if got := createRepo(t, ts, "beta"); got != http.StatusCreated {
		t.Fatalf("create beta: status %d, want 201", got)
	}
	if got := createRepo(t, ts, "Alpha"); got != http.StatusBadRequest {
		t.Fatalf("create Alpha: status %d, want 400", got)
	}

	resp, err := http.Get(ts.URL + "/repos")
	if err != nil {
		t.Fatalf("GET /repos: %v", err)
	}
	defer resp.Body.Close()
	var lr struct {
		Repos []string `json:"repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	if len(lr.Repos) != 2 || lr.Repos[0] != "alpha" || lr.Repos[1] != "beta" {
		t.Fatalf("repos = %v, want [alpha beta]", lr.Repos)
	}

	// Each repo is its own store directory.
	for _, id := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(dataDir, "repos", id, "objects")); err != nil {
			t.Fatalf("repo %s has no object store: %v", id, err)
		}
	}
}

func TestReadOfUnknownRepoDoesNotCreateIt(t *testing.T) {
	_, dataDir, ts := newTestServer(t)

	h := hashOf([]byte("nothing here"))
	if got := headObject(t, ts, "ghost", h); got != http.StatusNotFound {
		t.Fatalf("HEAD in unknown repo: status %d, want 404", got)
	}
	if code, _ := getRef(t, ts, "ghost", "sessions/x"); code != http.StatusNotFound {
		t.Fatalf("GET ref in unknown repo: status %d, want 404", code)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "repos", "ghost")); !os.IsNotExist(err) {
		t.Fatalf("reading an unknown repo created it on disk (err=%v)", err)
	}
}

// ---------------------------------------------------------------------------
// objects
// ---------------------------------------------------------------------------

func TestObjectRoundTripAndIdempotentPut(t *testing.T) {
	_, _, ts := newTestServer(t)
	data := []byte(`{"tree":"deadbeef","session_id":"s1"}`)

	status, h := putObject(t, ts, "alpha", data)
	if status != http.StatusCreated {
		t.Fatalf("first PUT: status %d, want 201", status)
	}
	if status, _ := putObject(t, ts, "alpha", data); status != http.StatusOK {
		t.Fatalf("second PUT: status %d, want 200 (already present)", status)
	}
	if got := headObject(t, ts, "alpha", h); got != http.StatusOK {
		t.Fatalf("HEAD: status %d, want 200", got)
	}
	code, body := getObject(t, ts, "alpha", h)
	if code != http.StatusOK {
		t.Fatalf("GET: status %d, want 200", code)
	}
	if !bytes.Equal(body, data) {
		t.Fatalf("GET returned %q, want %q", body, data)
	}
}

func TestPutRejectsContentThatDoesNotHashToTheURL(t *testing.T) {
	_, dataDir, ts := newTestServer(t)

	claimed := hashOf([]byte("the content I claim to send"))
	status := putObjectAs(t, ts, "alpha", string(claimed), []byte("something else entirely"))
	if status != http.StatusBadRequest {
		t.Fatalf("PUT with mismatched body: status %d, want 400", status)
	}
	if _, err := os.Stat(objectPath(dataDir, "alpha", claimed)); !os.IsNotExist(err) {
		t.Fatalf("poisoned object was stored anyway (err=%v)", err)
	}
	if got := headObject(t, ts, "alpha", claimed); got != http.StatusNotFound {
		t.Fatalf("HEAD after rejected PUT: status %d, want 404", got)
	}
}

func TestObjectHashValidation(t *testing.T) {
	_, _, ts := newTestServer(t)
	data := []byte("payload")
	full := string(hashOf(data))

	tests := []struct {
		name string
		hash string
	}{
		{"too short", full[:10]},
		{"too long", full + "ab"},
		{"uppercase", strings.ToUpper(full)},
		{"non hex", strings.Repeat("z", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := putObjectAs(t, ts, "alpha", tt.hash, data); got != http.StatusBadRequest {
				t.Fatalf("PUT %s: status %d, want 400", tt.name, got)
			}
			if got := headObject(t, ts, "alpha", store.Hash(tt.hash)); got != http.StatusBadRequest {
				t.Fatalf("HEAD %s: status %d, want 400", tt.name, got)
			}
		})
	}

	// An empty final segment is a routing miss, not an object request.
	resp, err := http.Head(ts.URL + "/alpha/objects/")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("empty hash: status %d, want 404", resp.StatusCode)
	}
}

func TestPutRejectsOversizedObject(t *testing.T) {
	_, _, ts := newTestServer(t, WithMaxObjectBytes(64))

	big := bytes.Repeat([]byte("x"), 65)
	if got := putObjectAs(t, ts, "alpha", string(hashOf(big)), big); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT: status %d, want 413", got)
	}
	small := bytes.Repeat([]byte("x"), 64)
	if got := putObjectAs(t, ts, "alpha", string(hashOf(small)), small); got != http.StatusCreated {
		t.Fatalf("at-limit PUT: status %d, want 201", got)
	}
}

func TestEmptyObjectIsStorable(t *testing.T) {
	_, _, ts := newTestServer(t)
	status, h := putObject(t, ts, "alpha", []byte{})
	if status != http.StatusCreated {
		t.Fatalf("PUT empty object: status %d, want 201", status)
	}
	code, body := getObject(t, ts, "alpha", h)
	if code != http.StatusOK || len(body) != 0 {
		t.Fatalf("GET empty object: status %d, len %d", code, len(body))
	}
}

// ---------------------------------------------------------------------------
// AC-3: dedupe is per repo — identical content is shared only within a repo
// ---------------------------------------------------------------------------

func TestDedupeIsPerRepo(t *testing.T) {
	_, dataDir, ts := newTestServer(t)
	shared := []byte("README: identical in both repos\n")

	// Within one repo, identical content is stored once.
	status, h := putObject(t, ts, "alpha", shared)
	if status != http.StatusCreated {
		t.Fatalf("alpha first PUT: status %d, want 201", status)
	}
	if status, _ := putObject(t, ts, "alpha", shared); status != http.StatusOK {
		t.Fatalf("alpha second PUT: status %d, want 200 (deduped)", status)
	}

	// Across repos it is NOT shared: beta must not see alpha's object …
	if got := headObject(t, ts, "beta", h); got != http.StatusNotFound {
		t.Fatalf("beta HEAD of alpha's object: status %d, want 404 (no cross-repo leak)", got)
	}
	if code, _ := getObject(t, ts, "beta", h); code != http.StatusNotFound {
		t.Fatalf("beta GET of alpha's object: status %d, want 404", code)
	}

	// … and uploading it to beta really stores a second copy.
	if status, _ := putObject(t, ts, "beta", shared); status != http.StatusCreated {
		t.Fatalf("beta PUT: status %d, want 201 (own copy)", status)
	}
	for _, repo := range []string{"alpha", "beta"} {
		if _, err := os.Stat(objectPath(dataDir, repo, h)); err != nil {
			t.Fatalf("repo %s does not hold its own copy: %v", repo, err)
		}
	}

	// Content addressing still holds: the same bytes hash to the same address
	// in both repos, so isolation is a storage boundary, not a hash change.
	if code, body := getObject(t, ts, "beta", h); code != http.StatusOK || !bytes.Equal(body, shared) {
		t.Fatalf("beta GET after upload: status %d, body %q", code, body)
	}
}

// ---------------------------------------------------------------------------
// AC-2: refs are namespaced per repo
// ---------------------------------------------------------------------------

func TestRefsAreNamespacedPerRepo(t *testing.T) {
	_, dataDir, ts := newTestServer(t)
	const ref = "sessions/claude_code--shared-session-id"

	_, alphaStep := putObject(t, ts, "alpha", []byte(`{"tree":"a","session_id":"s"}`))
	_, betaStep := putObject(t, ts, "beta", []byte(`{"tree":"b","session_id":"s"}`))

	if code, _ := postRef(t, ts, "alpha", ref, "", string(alphaStep)); code != http.StatusOK {
		t.Fatalf("alpha ref update: status %d, want 200", code)
	}
	// The identical ref NAME in beta is a different ref: creating it must not
	// conflict with alpha's, and must not read back alpha's value.
	if code, _ := postRef(t, ts, "beta", ref, "", string(betaStep)); code != http.StatusOK {
		t.Fatalf("beta ref update: status %d, want 200 (no collision with alpha)", code)
	}

	if code, got := getRef(t, ts, "alpha", ref); code != http.StatusOK || got != string(alphaStep) {
		t.Fatalf("alpha ref = (%d, %s), want (200, %s)", code, got, alphaStep)
	}
	if code, got := getRef(t, ts, "beta", ref); code != http.StatusOK || got != string(betaStep) {
		t.Fatalf("beta ref = (%d, %s), want (200, %s)", code, got, betaStep)
	}

	// Listing is scoped too.
	for repo, want := range map[string]store.Hash{"alpha": alphaStep, "beta": betaStep} {
		refs := listRefs(t, ts, repo, "sessions")
		if len(refs) != 1 {
			t.Fatalf("%s: listed %d refs, want 1: %v", repo, len(refs), refs)
		}
		if got := refs["claude_code--shared-session-id"]; got != string(want) {
			t.Fatalf("%s: listed ref = %s, want %s", repo, got, want)
		}
	}

	// On disk they are two different files.
	alphaPath := filepath.Join(dataDir, "repos", "alpha", "refs", filepath.FromSlash(ref))
	betaPath := filepath.Join(dataDir, "repos", "beta", "refs", filepath.FromSlash(ref))
	a, err := os.ReadFile(alphaPath)
	if err != nil {
		t.Fatalf("read alpha ref file: %v", err)
	}
	b, err := os.ReadFile(betaPath)
	if err != nil {
		t.Fatalf("read beta ref file: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("both repos wrote the same ref value %q", a)
	}
}

func TestRefCannotPointAtAnotherReposObject(t *testing.T) {
	_, _, ts := newTestServer(t)

	_, alphaOnly := putObject(t, ts, "alpha", []byte(`{"tree":"secret","session_id":"alpha"}`))
	createRepo(t, ts, "beta")

	code, _ := postRef(t, ts, "beta", "sessions/x", "", string(alphaOnly))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-repo ref update: status %d, want 422", code)
	}
	if code, _ := getRef(t, ts, "beta", "sessions/x"); code != http.StatusNotFound {
		t.Fatalf("beta ref exists after rejected update: status %d, want 404", code)
	}
}

func TestRefCAS(t *testing.T) {
	_, _, ts := newTestServer(t)
	_, one := putObject(t, ts, "alpha", []byte("step one"))
	_, two := putObject(t, ts, "alpha", []byte("step two"))
	_, three := putObject(t, ts, "alpha", []byte("step three"))
	const ref = "sessions/codex_cli--s1"

	if code, _ := postRef(t, ts, "alpha", ref, "", string(one)); code != http.StatusOK {
		t.Fatalf("create ref: status %d, want 200", code)
	}
	// Creating an existing ref with old="" is a conflict, not an overwrite.
	code, current := postRef(t, ts, "alpha", ref, "", string(two))
	if code != http.StatusConflict {
		t.Fatalf("recreate ref: status %d, want 409", code)
	}
	if current != string(one) {
		t.Fatalf("conflict reported current %s, want %s", current, one)
	}
	// Stale expected-old is rejected …
	if code, _ := postRef(t, ts, "alpha", ref, string(three), string(two)); code != http.StatusConflict {
		t.Fatalf("stale CAS: status %d, want 409", code)
	}
	// … and the correct one succeeds.
	if code, _ := postRef(t, ts, "alpha", ref, string(one), string(two)); code != http.StatusOK {
		t.Fatalf("valid CAS: status %d, want 200", code)
	}
	if _, got := getRef(t, ts, "alpha", ref); got != string(two) {
		t.Fatalf("ref = %s, want %s", got, two)
	}
}

func TestRefRequestValidation(t *testing.T) {
	_, _, ts := newTestServer(t)
	_, valid := putObject(t, ts, "alpha", []byte("target"))

	tests := []struct {
		name     string
		ref      string
		old, new string
		want     int
	}{
		{"unknown target object", "sessions/a", "", strings.Repeat("ab", 32), http.StatusUnprocessableEntity},
		{"invalid new hash", "sessions/a", "", "not-a-hash", http.StatusBadRequest},
		{"invalid old hash", "sessions/a", "zz", string(valid), http.StatusBadRequest},
		{"lock suffix", "sessions/a.lock", "", string(valid), http.StatusBadRequest},
		{"empty segment", "sessions//a", "", string(valid), http.StatusBadRequest},
		{"oversized name", "sessions/" + strings.Repeat("n", 600), "", string(valid), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := postRef(t, ts, "alpha", tt.ref, tt.old, tt.new)
			if code != tt.want {
				t.Fatalf("status %d, want %d", code, tt.want)
			}
		})
	}
}

func TestRefNameWithEncodedSeparatorStaysOneSegment(t *testing.T) {
	_, dataDir, ts := newTestServer(t)
	_, target := putObject(t, ts, "alpha", []byte("encoded ref target"))

	// %2F inside the last segment must stay part of the ref NAME, not become a
	// path separator that reshapes the route.
	body, _ := json.Marshal(map[string]string{"old": "", "new": string(target)})
	resp, err := http.Post(ts.URL+"/alpha/refs/sessions/claude_code--a%2Fb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST ref: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (a separator inside a segment is rejected)", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "repos", "alpha", "refs", "sessions", "claude_code--a")); !os.IsNotExist(err) {
		t.Fatalf("encoded separator created a nested ref path (err=%v)", err)
	}
}

// ---------------------------------------------------------------------------
// hostile input
// ---------------------------------------------------------------------------

func TestPathTraversalIsRejected(t *testing.T) {
	_, dataDir, ts := newTestServer(t)
	_, victim := putObject(t, ts, "alpha", []byte("alpha's private object"))

	paths := []string{
		"/alpha/../beta/objects/" + string(victim),
		"/alpha/objects/../../beta/objects/" + string(victim),
		"/%2e%2e/alpha/objects/" + string(victim),
		"/alpha/refs/sessions/%2e%2e/%2e%2e/escape",
		"/./alpha/refs/sessions/x",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			// An http.Client would normalise the path, so speak HTTP directly.
			resp, err := rawRequest(t, ts.URL, "GET "+p+" HTTP/1.1")
			if err != nil {
				t.Fatalf("raw request: %v", err)
			}
			if !strings.Contains(resp, " 400 ") && !strings.Contains(resp, " 404 ") {
				t.Fatalf("traversal path was not rejected: %s", firstLine(resp))
			}
		})
	}

	// Nothing escaped the data dir.
	if _, err := os.Stat(filepath.Join(dataDir, "..", "escape")); err == nil {
		t.Fatal("a file was created outside the data dir")
	}
}

func TestMethodsAreRestricted(t *testing.T) {
	_, _, ts := newTestServer(t)
	_, h := putObject(t, ts, "alpha", []byte("obj"))

	tests := []struct {
		name, method, path string
	}{
		{"delete object", http.MethodDelete, "/alpha/objects/" + string(h)},
		{"put ref", http.MethodPut, "/alpha/refs/sessions/a"},
		{"delete repos", http.MethodDelete, "/repos"},
		{"post ref list", http.MethodPost, "/alpha/refs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, ts.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status %d, want 405", resp.StatusCode)
			}
		})
	}
}

func TestUnknownRoutes(t *testing.T) {
	_, _, ts := newTestServer(t)
	for _, p := range []string{"/", "/alpha", "/alpha/unknown/thing", "/alpha/objects/a/b"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET %s: status %d, want 404 or 400", p, resp.StatusCode)
		}
	}
}

func TestMalformedJSONBodies(t *testing.T) {
	_, _, ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/repos", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("POST /repos: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed repo body: status %d, want 400", resp.StatusCode)
	}

	resp2, err := http.Post(ts.URL+"/alpha/refs/sessions/a", "application/json", strings.NewReader("["))
	if err != nil {
		t.Fatalf("POST ref: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed ref body: status %d, want 400", resp2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// AC-1 (protocol level) + concurrency
// ---------------------------------------------------------------------------

// TestInterleavedWritesKeepReposIndependent drives two repos through one server
// with interleaved writes and checks that neither repo can observe the other.
func TestInterleavedWritesKeepReposIndependent(t *testing.T) {
	_, _, ts := newTestServer(t)
	const ref = "sessions/claude_code--s1"

	type repoState struct {
		id   string
		tips []store.Hash
	}
	alpha := &repoState{id: "alpha"}
	beta := &repoState{id: "beta"}

	for i := 0; i < 4; i++ {
		for _, rs := range []*repoState{alpha, beta} {
			payload := []byte(fmt.Sprintf(`{"repo":%q,"step":%d}`, rs.id, i))
			status, h := putObject(t, ts, rs.id, payload)
			if status != http.StatusCreated {
				t.Fatalf("%s step %d PUT: status %d, want 201", rs.id, i, status)
			}
			var old string
			if len(rs.tips) > 0 {
				old = string(rs.tips[len(rs.tips)-1])
			}
			if code, _ := postRef(t, ts, rs.id, ref, old, string(h)); code != http.StatusOK {
				t.Fatalf("%s step %d ref update: status %d, want 200", rs.id, i, code)
			}
			rs.tips = append(rs.tips, h)
		}
	}

	for _, rs := range []*repoState{alpha, beta} {
		other := beta
		if rs == beta {
			other = alpha
		}
		_, got := getRef(t, ts, rs.id, ref)
		if want := string(rs.tips[len(rs.tips)-1]); got != want {
			t.Fatalf("%s tip = %s, want %s", rs.id, got, want)
		}
		for _, h := range rs.tips {
			if code := headObject(t, ts, rs.id, h); code != http.StatusOK {
				t.Fatalf("%s lost its own object %s (status %d)", rs.id, h, code)
			}
			if code := headObject(t, ts, other.id, h); code != http.StatusNotFound {
				t.Fatalf("%s can see %s's object %s (status %d)", other.id, rs.id, h, code)
			}
		}
	}
}

func TestConcurrentWritesAcrossRepos(t *testing.T) {
	_, _, ts := newTestServer(t)
	const goroutines = 8
	const perGoroutine = 5

	repoFor := func(g int) string { return fmt.Sprintf("repo%d", g%4) }
	payload := func(g, i int) []byte {
		return []byte(fmt.Sprintf("g%d-i%d-%s", g, i, repoFor(g)))
	}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perGoroutine)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			repo := repoFor(g)
			for i := 0; i < perGoroutine; i++ {
				data := payload(g, i)
				h := hashOf(data)
				req, _ := http.NewRequest(http.MethodPut,
					fmt.Sprintf("%s/%s/objects/%s", ts.URL, repo, h), bytes.NewReader(data))
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					errs <- err
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("PUT %s: status %d", repo, resp.StatusCode)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}

	// Every object landed in exactly the repo it was written to.
	for g := 0; g < goroutines; g++ {
		repo := repoFor(g)
		for i := 0; i < perGoroutine; i++ {
			h := hashOf(payload(g, i))
			if code := headObject(t, ts, repo, h); code != http.StatusOK {
				t.Fatalf("%s missing object g%d-i%d (status %d)", repo, g, i, code)
			}
			for other := 0; other < 4; other++ {
				otherRepo := fmt.Sprintf("repo%d", other)
				if otherRepo == repo {
					continue
				}
				if code := headObject(t, ts, otherRepo, h); code != http.StatusNotFound {
					t.Fatalf("object leaked from %s into %s (status %d)", repo, otherRepo, code)
				}
			}
		}
	}
}

// TestConcurrentCASOnOneRefHasOneWinner proves the server never silently
// clobbers a competing update: only one racer may advance the ref from a given
// value, and the losers are told the real current value.
func TestConcurrentCASOnOneRefHasOneWinner(t *testing.T) {
	_, _, ts := newTestServer(t)
	const ref = "sessions/claude_code--race"
	const racers = 6

	_, base := putObject(t, ts, "alpha", []byte("base step"))
	if code, _ := postRef(t, ts, "alpha", ref, "", string(base)); code != http.StatusOK {
		t.Fatalf("seed ref: status %d", code)
	}

	candidates := make([]store.Hash, racers)
	for i := range candidates {
		_, h := putObject(t, ts, "alpha", []byte(fmt.Sprintf("candidate %d", i)))
		candidates[i] = h
	}

	var wg sync.WaitGroup
	results := make(chan int, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{"old": string(base), "new": string(candidates[i])})
			resp, err := http.Post(fmt.Sprintf("%s/alpha/refs/%s", ts.URL, ref),
				"application/json", bytes.NewReader(body))
			if err != nil {
				results <- 0
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			results <- resp.StatusCode
		}(i)
	}
	wg.Wait()
	close(results)

	winners, conflicts := 0, 0
	for code := range results {
		switch code {
		case http.StatusOK:
			winners++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1 (CAS must not allow clobbering)", winners)
	}
	if conflicts != racers-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, racers-1)
	}

	_, final := getRef(t, ts, "alpha", ref)
	found := false
	for _, c := range candidates {
		if final == string(c) {
			found = true
		}
	}
	if !found {
		t.Fatalf("final ref %s is not one of the candidates", final)
	}
}

// ---------------------------------------------------------------------------
// low-level helper for hostile-path tests
// ---------------------------------------------------------------------------

// rawRequest sends a request line verbatim so the client cannot normalise the
// path, and returns the raw response.
func rawRequest(t *testing.T, serverURL, requestLine string) (string, error) {
	t.Helper()
	host := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, requestLine+"\r\nHost: "+host+"\r\nConnection: close\r\n\r\n"); err != nil {
		return "", err
	}
	out, err := io.ReadAll(io.LimitReader(conn, 8<<10))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func firstLine(s string) string {
	if i := strings.Index(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// --- rgt remote protocol handler tests (from RE-7) ---

func makeStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return s
}

func TestHandlerObjectHeadMissing(t *testing.T) {
	srv := httptest.NewServer(Handler(makeStore(t)))
	defer srv.Close()

	resp, err := http.Head(srv.URL + "/objects/aabbcc0000000000000000000000000000000000000000000000000000001234ab")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandlerObjectPutAndHead(t *testing.T) {
	remStore := makeStore(t)
	remSrv := httptest.NewServer(Handler(remStore))
	defer remSrv.Close()

	// Write to a local store to get the canonical hash.
	local := makeStore(t)
	data := []byte("test object content")
	h, _ := local.WriteBlob(data)

	req, _ := http.NewRequest(http.MethodPut, remSrv.URL+"/objects/"+string(h), bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Errorf("PUT: expected 201 or 200, got %d", resp.StatusCode)
	}

	// HEAD should now return 200.
	resp2, err := http.Head(remSrv.URL + "/objects/" + string(h))
	if err != nil {
		t.Fatalf("HEAD after PUT: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("HEAD after PUT: expected 200, got %d", resp2.StatusCode)
	}
}

func TestHandlerObjectPutIdempotent(t *testing.T) {
	local := makeStore(t)
	remStore := makeStore(t)
	remSrv := httptest.NewServer(Handler(remStore))
	defer remSrv.Close()

	data := []byte("idempotent content")
	blobHash, _ := local.WriteBlob(data)

	put := func() int {
		req, _ := http.NewRequest(http.MethodPut, remSrv.URL+"/objects/"+string(blobHash), bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if sc := put(); sc != http.StatusCreated && sc != http.StatusOK {
		t.Errorf("first PUT: unexpected status %d", sc)
	}
	if sc := put(); sc != http.StatusOK {
		t.Errorf("second PUT (idempotent): expected 200, got %d", sc)
	}
}

func TestHandlerRefGetSetCAS(t *testing.T) {
	s := makeStore(t)
	srv := httptest.NewServer(Handler(s))
	defer srv.Close()

	// GET non-existent ref → 404.
	resp, err := http.Get(srv.URL + "/refs/sessions/test-sess")
	if err != nil {
		t.Fatalf("GET ref: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}

	h, _ := s.WriteBlob([]byte("step-data"))

	postRef := func(url, old, newHash string) int {
		body, _ := json.Marshal(map[string]string{"old": old, "new": newHash})
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST ref: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	refURL := srv.URL + "/refs/sessions/test-sess"

	if sc := postRef(refURL, "", string(h)); sc != http.StatusOK {
		t.Errorf("create ref: expected 200, got %d", sc)
	}

	// GET the ref → should return the hash.
	resp2, err := http.Get(refURL)
	if err != nil {
		t.Fatalf("GET ref: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	var rr struct{ Hash string }
	_ = json.Unmarshal(body, &rr)
	if store.Hash(rr.Hash) != h {
		t.Errorf("GET ref: expected %s, got %s", h, rr.Hash)
	}

	// POST with wrong old value → 409.
	if sc := postRef(refURL, "wrongoldvalue", string(h)); sc != http.StatusConflict {
		t.Errorf("CAS conflict: expected 409, got %d", sc)
	}
}

func TestHandlerListRefs(t *testing.T) {
	s := makeStore(t)
	srv := httptest.NewServer(Handler(s))
	defer srv.Close()

	h, _ := s.WriteBlob([]byte("blob"))
	_ = s.UpdateRef("sessions/s1", "", h)

	resp, err := http.Get(srv.URL + "/refs?dir=sessions")
	if err != nil {
		t.Fatalf("GET /refs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var lr struct {
		Refs map[string]string `json:"refs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if lr.Refs["s1"] != string(h) {
		t.Errorf("expected ref s1=%s, got %s", h, lr.Refs["s1"])
	}
}

// TestInstallEndpoint verifies GET /install serves the personalized shell
// installer: it always targets this server's host and references /bin/rgt. The
// server is always open, so the script never mentions a token.
func TestInstallEndpoint(t *testing.T) {
	_, _, ts := newTestServer(t)

	for _, path := range []string{"/install", "/install.sh"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		script := string(body)
		// httptest.Server listens on 127.0.0.1:<port>; the script must
		// target THIS server via its Host header.
		host := strings.TrimPrefix(ts.URL, "http://")
		if !strings.Contains(script, host) {
			t.Errorf("GET %s body does not mention server host %q", path, host)
		}
		if strings.Contains(script, "REGENT_TOKEN") {
			t.Errorf("GET %s: open-server script must not mention REGENT_TOKEN", path)
		}
		if !strings.Contains(script, "/bin/rgt") {
			t.Errorf("GET %s body does not reference the /bin/rgt download route", path)
		}
	}
}

// TestBinaryEndpoint verifies GET /bin/rgt serves a non-empty body. In tests
// os.Executable() is the test binary, which is fine: we only assert 200 +
// non-empty.
func TestBinaryEndpoint(t *testing.T) {
	_, _, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/bin/rgt")
	if err != nil {
		t.Fatalf("GET /bin/rgt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /bin/rgt (no auth) = %d, want 200", resp.StatusCode)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("read /bin/rgt body: %v", err)
	}
	if n == 0 {
		t.Errorf("GET /bin/rgt returned an empty body")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("GET /bin/rgt Content-Type = %q, want application/octet-stream", ct)
	}
}
