package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/skills"
)

const publishedSkill = "---\ndescription: A skill only the server has.\nallowed-tools: Bash(rgt log *)\n---\n\nbody\n"

// registryStub serves one published skill and 404s everything else.
func registryStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/skills/team-convention" {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(publishedSkill))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The gap this closes: the UI can offer a skill published to the server, and
// the command it hands the user has to be able to install it.
func TestResolvePrefersTheRegistryForASkillOnlyItHas(t *testing.T) {
	srv := registryStub(t)
	content, origin, err := resolveSkill(context.Background(), srv.URL, "team-convention")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if origin != originRegistry {
		t.Errorf("origin = %q, want %q", origin, originRegistry)
	}
	if content != publishedSkill {
		t.Error("content is not what the registry served")
	}
}

// Offline install must keep working: the embedded set is why a fresh machine
// with no server is useful at all.
func TestResolveFallsBackToTheEmbeddedCopyWhenTheServerIsUnreachable(t *testing.T) {
	content, origin, err := resolveSkill(context.Background(), "http://127.0.0.1:1", "blame")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if origin != originBuiltin {
		t.Errorf("origin = %q, want %q", origin, originBuiltin)
	}
	expected, _ := skills.Get("blame")
	if content != expected.Content {
		t.Error("fallback did not return the embedded skill")
	}
}

// When neither source has it, the user should hear about both attempts. Being
// told only "connection refused" hides that the name was also wrong.
func TestResolveReportsBothAttemptsWhenNeitherHasTheSkill(t *testing.T) {
	_, _, err := resolveSkill(context.Background(), "http://127.0.0.1:1", "no-such-skill")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not built into this rgt") {
		t.Errorf("error does not mention the local attempt: %v", err)
	}
}

// A wrong URL — a static file server, a login page — must not put HTML on disk
// where an agent will load it as instructions.
func TestFetchRefusesAResponseThatIsNotASkill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>login</body></html>"))
	}))
	defer srv.Close()

	if _, err := fetchSkill(context.Background(), srv.URL, "blame"); err == nil {
		t.Fatal("an HTML page was accepted as a skill")
	}

	// And the install path degrades to the embedded copy rather than failing.
	content, origin, err := resolveSkill(context.Background(), srv.URL, "blame")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if origin != originBuiltin || strings.Contains(content, "<html") {
		t.Error("a bad response reached the caller instead of the embedded skill")
	}
}

// A skill is written to disk and loaded by an agent, so an unbounded body is a
// disk-filling vector as well as a nonsense one.
func TestFetchRefusesAnOversizedSkill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("---\n" + strings.Repeat("x", maxSkillBytes+16)))
	}))
	defer srv.Close()

	if _, err := fetchSkill(context.Background(), srv.URL, "blame"); err == nil {
		t.Fatal("an oversized body was accepted")
	}
}

func TestFetchReportsAnUnknownSkillDistinctly(t *testing.T) {
	srv := registryStub(t)
	_, err := fetchSkill(context.Background(), srv.URL, "nope")
	if err == nil || !strings.Contains(err.Error(), "does not have") {
		t.Errorf("404 not reported clearly: %v", err)
	}
}

// The registry is global, so a server URL alone is enough — a project need not
// be registered with a repo id to install that server's skills.
func TestRegistryURLUsesAnExplicitOverride(t *testing.T) {
	if got := registryURL(nil, t.TempDir(), "http://example.test/"); got != "http://example.test" {
		t.Errorf("registryURL = %q, want the trimmed override", got)
	}
}
