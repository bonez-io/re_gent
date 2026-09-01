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
