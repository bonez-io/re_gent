package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/skills"
)

func getPath(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func newSkillServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	srv, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

func TestSkillsCatalogListsEveryBuiltInSkill(t *testing.T) {
	rec := getPath(t, newSkillServer(t), "/api/skills")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var out skillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != len(skills.Names()) {
		t.Errorf("total = %d, want %d", out.Total, len(skills.Names()))
	}

	byName := map[string]skillJSON{}
	for _, skill := range out.Skills {
		byName[skill.Name] = skill
	}
	for _, name := range skills.Names() {
		entry, ok := byName[name]
		if !ok {
			t.Errorf("%s missing from the catalog", name)
			continue
		}
		if entry.Description == "" {
			t.Errorf("%s has no description", name)
		}
		// The grant is what a client shows before anyone installs. Omitting it
		// would make the catalog unable to answer "what will this run?".
		if entry.AllowedTools == "" {
			t.Errorf("%s has no allowed_tools", name)
		}
		if entry.Source != "builtin" {
			t.Errorf("%s source = %q, want builtin", name, entry.Source)
		}
	}
}

// A withheld skill is listed with its reason rather than hidden: a client that
// cannot see it cannot explain why `rgt skill install rewind` warns.
func TestWithheldSkillIsListedWithItsReason(t *testing.T) {
	rec := getPath(t, newSkillServer(t), "/api/skills")
	var out skillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, skill := range out.Skills {
		if skill.Name == "rewind" {
			if skill.Withheld == "" {
				t.Error("rewind is withheld by default but the catalog does not say so")
			}
			return
		}
	}
	t.Skip("no withheld skill in this build")
}

func TestSkillContentIsServedAsMarkdown(t *testing.T) {
	rec := getPath(t, newSkillServer(t), "/api/skills/blame")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	expected, err := skills.Get("blame")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != expected.Content {
		t.Error("served content differs from the embedded skill")
	}
}

func TestUnknownSkillIs404(t *testing.T) {
	if rec := getPath(t, newSkillServer(t), "/api/skills/no-such-skill"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// The name is a caller-supplied path segment. Traversal must be refused before
// it reaches the filesystem, not sanitised afterwards.
func TestSkillNameCannotEscapeTheRegistry(t *testing.T) {
	for _, path := range []string{
		"/api/skills/..",
		"/api/skills/%2e%2e",
		"/api/skills/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/api/skills/blame%2f..%2f..%2fsecret",
	} {
		rec := getPath(t, newSkillServer(t), path)
		if rec.Code == http.StatusOK {
			t.Errorf("%s was served; it must be refused", path)
		}
	}
}

// The registry is read-only over HTTP. A skill is executable instruction, so
// publishing must not be reachable by anyone who can reach the port.
func TestRegistryRefusesWrites(t *testing.T) {
	srv := newSkillServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, "/api/skills", strings.NewReader("{}")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/skills = %d, want 405", method, rec.Code)
		}
	}
}

func TestPublishedSkillIsServedAndOverridesTheBuiltIn(t *testing.T) {
	dir := t.TempDir()
	published := "---\ndescription: A skill this operator published.\nallowed-tools: Bash(rgt log *)\n---\n\nlocal body\n"
	writeSkillFile(t, dir, "team-convention", published)

	override := "---\ndescription: Our own blame.\nallowed-tools: Bash(rgt blame *)\n---\n\noverridden\n"
	writeSkillFile(t, dir, "blame", override)

	srv := newSkillServer(t, WithSkillsDir(dir))

	var out skillsResponse
	if err := json.Unmarshal(getPath(t, srv, "/api/skills").Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := map[string]skillJSON{}
	for _, skill := range out.Skills {
		found[skill.Name] = skill
	}

	if entry, ok := found["team-convention"]; !ok {
		t.Error("published skill missing from the catalog")
	} else if entry.Source != "local" || entry.Description != "A skill this operator published." {
		t.Errorf("published skill = %+v", entry)
	}

	// A local skill of the same name wins: the file the operator put on this
	// server is the more specific answer than the one compiled in.
	if entry := found["blame"]; entry.Source != "local" {
		t.Errorf("blame source = %q, want local (the published copy must win)", entry.Source)
	}
	if body := getPath(t, srv, "/api/skills/blame").Body.String(); body != override {
		t.Error("served blame is the built-in, not the published override")
	}
	if body := getPath(t, srv, "/api/skills/team-convention").Body.String(); body != published {
		t.Error("published skill content was not served")
	}
}

// The registry is global. Skills describe how to interrogate re_gent, not one
// project's history, so they must not be reachable only under a repo id.
func TestRegistryIsNotRepoScoped(t *testing.T) {
	srv := newSkillServer(t)
	if rec := getPath(t, srv, "/api/skills"); rec.Code != http.StatusOK {
		t.Errorf("global /api/skills = %d, want 200", rec.Code)
	}
	if rec := getPath(t, srv, "/some-repo/api/skills"); rec.Code == http.StatusOK {
		t.Error("/<repo>/api/skills was served; the registry is global")
	}
}

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
