package server_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"lukechampine.com/blake3"

	"github.com/bonez-io/re_gent/server"
	"github.com/bonez-io/re_gent/serverauth"
)

type testAccessController struct{}

func (testAccessController) Authenticate(r *http.Request) (serverauth.Principal, error) {
	switch r.Header.Get("Authorization") {
	case "Bearer reader-alpha":
		return serverauth.Principal{Subject: "reader", TenantID: "tenant-a", Roles: []string{"reader"}, AuthMethod: "bearer"}, nil
	case "Bearer writer-alpha":
		return serverauth.Principal{Subject: "writer", TenantID: "tenant-a", Roles: []string{"writer"}, AuthMethod: "bearer"}, nil
	default:
		return serverauth.Principal{}, serverauth.ErrUnauthenticated
	}
}

func (testAccessController) Authorize(_ context.Context, principal serverauth.Principal, permission serverauth.Permission) error {
	if permission.Action == serverauth.ActionRepositoriesList || permission.Action == serverauth.ActionSkillList || permission.Action == serverauth.ActionSkillRead {
		return nil
	}
	if permission.Resource.RepositoryID != "alpha" {
		return serverauth.ErrNotFound
	}
	if permission.Action == serverauth.ActionRepositoryRead || permission.Action == serverauth.ActionObjectRead || permission.Action == serverauth.ActionRefRead || permission.Action == serverauth.ActionHistoryRead {
		return nil
	}
	for _, role := range principal.Roles {
		if role == "writer" {
			return nil
		}
	}
	return serverauth.ErrForbidden
}

func TestAccessControllerConformance(t *testing.T) {
	srv, err := server.New(t.TempDir(), server.WithAccessController(testAccessController{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, repoID := range []string{"alpha", "beta"} {
		if _, err := srv.CreateRepo(repoID); err != nil {
			t.Fatalf("CreateRepo(%q): %v", repoID, err)
		}
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	t.Run("health remains public", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/healthz", "", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("anonymous data access is denied", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/repos", "", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got == "" {
			t.Fatal("missing WWW-Authenticate header")
		}
	})

	t.Run("repository listing is tenant filtered", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/repos", "reader-alpha", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var body struct {
			Repos []string `json:"repos"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Repos) != 1 || body.Repos[0] != "alpha" {
			t.Fatalf("repos = %v, want [alpha]", body.Repos)
		}
	})

	t.Run("cross tenant direct access is denied", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/beta/api/sessions", "reader-alpha", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("reader cannot write", func(t *testing.T) {
		content := []byte("object")
		sum := blake3.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		resp := doRequest(t, http.MethodPut, ts.URL+"/alpha/objects/"+hash, "reader-alpha", bytes.NewReader(content))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("writer can write", func(t *testing.T) {
		content := []byte("object")
		sum := blake3.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		resp := doRequest(t, http.MethodPut, ts.URL+"/alpha/objects/"+hash, "writer-alpha", bytes.NewReader(content))
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}
	})
}

func TestAccessControllerInternalErrorsAreNotExposed(t *testing.T) {
	controller := errorAccessController{err: errors.New("database contains secret detail")}
	srv, err := server.New(t.TempDir(), server.WithAccessController(controller))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp := doRequest(t, http.MethodGet, ts.URL+"/repos", "anything", nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("secret detail")) {
		t.Fatalf("response leaked controller error: %s", body)
	}
}

type errorAccessController struct{ err error }

func (c errorAccessController) Authenticate(*http.Request) (serverauth.Principal, error) {
	return serverauth.Principal{Subject: "test"}, nil
}

func (c errorAccessController) Authorize(context.Context, serverauth.Principal, serverauth.Permission) error {
	return c.err
}

// anonymousPrincipalController distinguishes "no credentials at all"
// (serverauth.ErrNoCredentials) from "credentials present but wrong"
// (serverauth.ErrUnauthenticated), and grants a public read on repo "public"
// to the explicit anonymous principal the core builds for the former case —
// exactly the RFC 0004 public-project policy shape. It also counts Authorize
// calls so a test can prove the core never calls Authorize at all for the bad-
// credentials case (that request fails on Authenticate alone, same as before
// this seam existed).
type anonymousPrincipalController struct {
	authorizeCalls *int
}

func (c anonymousPrincipalController) Authenticate(r *http.Request) (serverauth.Principal, error) {
	switch r.Header.Get("Authorization") {
	case "":
		return serverauth.Principal{}, serverauth.ErrNoCredentials
	case "Bearer bad-token":
		return serverauth.Principal{}, serverauth.ErrUnauthenticated
	case "Bearer reader-alpha":
		return serverauth.Principal{Subject: "reader", Roles: []string{"reader"}, AuthMethod: "bearer"}, nil
	default:
		return serverauth.Principal{}, serverauth.ErrUnauthenticated
	}
}

func (c anonymousPrincipalController) Authorize(_ context.Context, principal serverauth.Principal, permission serverauth.Permission) error {
	*c.authorizeCalls++
	if principal.AuthMethod == "anonymous" {
		if principal.Subject != "" {
			return errors.New("Anonymous() principal must have an empty Subject")
		}
		isRead := permission.Action == serverauth.ActionHistoryRead || permission.Action == serverauth.ActionObjectRead || permission.Action == serverauth.ActionRefRead
		if permission.Resource.RepositoryID == "public" && isRead {
			return nil // the one documented exception: a public project's reads
		}
		return serverauth.ErrUnauthenticated
	}
	return nil // any authenticated principal may do anything else in this test
}

func TestAnonymousPrincipalReachesAuthorizeAndCanBeGrantedPublicRead(t *testing.T) {
	var authorizeCalls int
	controller := anonymousPrincipalController{authorizeCalls: &authorizeCalls}
	srv, err := server.New(t.TempDir(), server.WithAccessController(controller))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, repoID := range []string{"public", "private"} {
		if _, err := srv.CreateRepo(repoID); err != nil {
			t.Fatalf("CreateRepo(%q): %v", repoID, err)
		}
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	t.Run("anonymous read of a public project is granted", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/public/api/status", "", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
		}
	})

	t.Run("anonymous read of a private project stays denied", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/private/api/status", "", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got == "" {
			t.Fatal("missing WWW-Authenticate header")
		}
	})

	authorizeCalls = 0
	t.Run("bad credentials fail at Authenticate and never reach Authorize", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/public/api/status", "bad-token", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if authorizeCalls != 0 {
			t.Fatalf("Authorize was called %d times for a request with bad (not absent) credentials, want 0", authorizeCalls)
		}
	})

	authorizeCalls = 0
	t.Run("no credentials at all does reach Authorize", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/private/api/status", "", nil)
		defer resp.Body.Close()
		if authorizeCalls == 0 {
			t.Fatal("Authorize was never called for a request with no credentials at all; the anonymous principal must still reach policy")
		}
	})

	t.Run("authenticated principal is unaffected by the anonymous seam", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/private/api/status", "reader-alpha", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// projectAPIController is a coarse role-gated controller (no per-resource
// scoping, unlike testAccessController) used to prove every new versioned
// project-API route is wired into the route-policy switch: anonymous is
// denied, a reader can list/read but not write, and a writer can create but
// not rename.
type projectAPIController struct{}

func (projectAPIController) Authenticate(r *http.Request) (serverauth.Principal, error) {
	switch r.Header.Get("Authorization") {
	case "":
		return serverauth.Principal{}, serverauth.ErrNoCredentials
	case "Bearer reader":
		return serverauth.Principal{Subject: "reader", Roles: []string{"reader"}, AuthMethod: "bearer"}, nil
	case "Bearer writer":
		return serverauth.Principal{Subject: "writer", Roles: []string{"writer"}, AuthMethod: "bearer"}, nil
	case "Bearer owner":
		return serverauth.Principal{Subject: "owner", Roles: []string{"owner"}, AuthMethod: "bearer"}, nil
	default:
		return serverauth.Principal{}, serverauth.ErrUnauthenticated
	}
}

func (projectAPIController) Authorize(_ context.Context, principal serverauth.Principal, permission serverauth.Permission) error {
	if principal.AuthMethod == "anonymous" {
		return serverauth.ErrUnauthenticated
	}
	role := ""
	if len(principal.Roles) > 0 {
		role = principal.Roles[0]
	}
	switch permission.Action {
	case serverauth.ActionRepositoriesList, serverauth.ActionRepositoryRead:
		return nil // every authenticated role in this test may list/read
	case serverauth.ActionRepositoryCreate:
		if role == "writer" || role == "owner" {
			return nil
		}
		return serverauth.ErrForbidden
	case serverauth.ActionRepositoryWrite:
		if role == "owner" {
			return nil
		}
		return serverauth.ErrForbidden
	default:
		return serverauth.ErrForbidden // fail closed, including the generic ActionRequest
	}
}

func TestVersionedProjectAPIAuthorization(t *testing.T) {
	srv, err := server.New(t.TempDir(), server.WithAccessController(projectAPIController{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	postJSON := func(t *testing.T, path, token string, payload map[string]string) *http.Response {
		t.Helper()
		data, _ := json.Marshal(payload)
		return doRequest(t, http.MethodPost, ts.URL+path, token, bytes.NewReader(data))
	}

	t.Run("anonymous is denied on every project route", func(t *testing.T) {
		for _, req := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/projects"},
			{http.MethodPost, "/api/v1/projects"},
			{http.MethodGet, "/api/v1/projects/prj_doesnotmatter"},
			{http.MethodPatch, "/api/v1/projects/prj_doesnotmatter"},
			{http.MethodGet, "/api/v1/orgs/acme/projects"},
			{http.MethodPost, "/api/v1/orgs/acme/projects"},
		} {
			resp := doRequest(t, req.method, ts.URL+req.path, "", nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s: status = %d, want 401", req.method, req.path, resp.StatusCode)
			}
		}
	})

	t.Run("reader can list and read but not create or rename", func(t *testing.T) {
		listResp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/projects", "reader", nil)
		defer listResp.Body.Close()
		if listResp.StatusCode != http.StatusOK {
			t.Fatalf("list status = %d, want 200", listResp.StatusCode)
		}
		createResp := postJSON(t, "/api/v1/projects", "reader", map[string]string{"display_name": "Reader Attempt"})
		defer createResp.Body.Close()
		if createResp.StatusCode != http.StatusForbidden {
			t.Fatalf("create status = %d, want 403", createResp.StatusCode)
		}
	})

	var writerCreatedID string
	t.Run("writer can create but not rename", func(t *testing.T) {
		createResp := postJSON(t, "/api/v1/projects", "writer", map[string]string{"display_name": "Writer Project"})
		defer createResp.Body.Close()
		if createResp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(createResp.Body)
			t.Fatalf("create status = %d, want 201: %s", createResp.StatusCode, body)
		}
		var out struct {
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		}
		if err := json.NewDecoder(createResp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		writerCreatedID = out.Project.ID

		patchBody, _ := json.Marshal(map[string]string{"display_name": "Renamed"})
		patchResp := doRequest(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+writerCreatedID, "writer", bytes.NewReader(patchBody))
		defer patchResp.Body.Close()
		if patchResp.StatusCode != http.StatusForbidden {
			t.Fatalf("rename by writer status = %d, want 403", patchResp.StatusCode)
		}
	})

	t.Run("owner can rename", func(t *testing.T) {
		if writerCreatedID == "" {
			t.Skip("previous subtest did not produce a project id")
		}
		patchBody, _ := json.Marshal(map[string]string{"display_name": "Renamed By Owner"})
		patchResp := doRequest(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+writerCreatedID, "owner", bytes.NewReader(patchBody))
		defer patchResp.Body.Close()
		if patchResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(patchResp.Body)
			t.Fatalf("rename by owner status = %d, want 200: %s", patchResp.StatusCode, body)
		}
	})

	t.Run("org-scoped route works the same way", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/orgs/acme/projects", "owner", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("unknown api/v1 route stays fail-closed", func(t *testing.T) {
		resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/something-unclassified", "owner", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (ActionRequest fail-closed)", resp.StatusCode)
		}
	})
}

func doRequest(t *testing.T, method, url, token string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}
