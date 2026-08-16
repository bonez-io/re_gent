package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/server"
)

// This file is the seam that ticket #6 exists to build.
//
// Two harnesses already existed and never met. The end-to-end suite builds the
// real rgt binary and runs command sequences in a temporary project — but
// TestMain forces local mode for the whole process, so no test has ever seen a
// server. The remote test server speaks the real wire protocol — but is only
// ever driven in-process, never through the binary.
//
// Every bug reported against connect, sharing, and team visibility lives in
// that gap, which is why the suite is entirely green while all of them
// reproduce by hand. The tests here close it: a temporary project, the real
// binary, a live server, a sequence of commands, and assertions on what the
// user actually sees.

// TestE2EConnectRegistersWithALiveServer is the smallest complete pass through
// the new seam. If this cannot be written and run, nothing in Spec #2 is
// testable at the level its bugs occur.
func TestE2EConnectRegistersWithALiveServer(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	// connect derives the repo id from the folder name, so the project is
	// named deliberately rather than taking whatever t.TempDir() produces.
	project := filepath.Join(t.TempDir(), "acceptance-project")
	mustMkdirAll(t, project)
	// isProjectDir looks for .git or .regent; without one, connect falls
	// through to the project picker, prints suggestions and exits 0 having
	// done nothing. A bare temp dir would pass a naive assertion.
	mustMkdirAll(t, filepath.Join(project, ".git"))

	out := e2eRunEnv(t, rgt, project, hermeticEnv(t, srv), nil, "connect", srv.URL)

	assertContains(t, out, "Registered", "connect output")

	if repos := serverRepos(t, srv.URL); !slices.Contains(repos, "acceptance-project") {
		t.Errorf("server does not know the project after connect.\n/repos returned: %v\nconnect said:\n%s", repos, out)
	}
}

// TestE2ECapturedWorkReachesTheServer is the assertion that matters for a team:
// work recorded by an agent hook has to arrive somewhere a colleague can reach,
// without anyone running an explicit upload command.
//
// It also exercises the half of the seam the connect test does not — passing
// environment through to the binary, so a test can put the process into server
// mode the way a real machine is.
func TestE2ECapturedWorkReachesTheServer(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	project := filepath.Join(t.TempDir(), "capture-project")
	mustMkdirAll(t, project)
	mustMkdirAll(t, filepath.Join(project, ".git"))

	env := hermeticEnv(t, srv)
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	// A complete Claude Code turn: prompt, one tool call, assistant reply.
	const sid = "e2e-server-session"

	e2eRunEnv(t, rgt, project, env,
		jsonObj("session_id", sid, "turn_id", "t1", "cwd", project, "prompt", "add a file"),
		"message-hook", "user")

	writeTestFile(t, project, "main.go", "package main\n")

	e2eRunEnv(t, rgt, project, env,
		jsonObj("session_id", sid, "turn_id", "t1", "cwd", project,
			"tool_calls", []any{map[string]any{
				"tool_name":     "Write",
				"tool_use_id":   "tu1",
				"tool_input":    map[string]string{"file_path": "main.go"},
				"tool_response": "written",
			}}),
		"tool-batch-hook")

	e2eRunEnv(t, rgt, project, env,
		jsonObj("session_id", sid, "turn_id", "t1", "cwd", project,
			"last_assistant_message", "done"),
		"message-hook", "assistant")

	// The colleague's view: what does the server actually hold?
	sessions := serverSessions(t, srv.URL, "capture-project")
	if len(sessions) == 0 {
		t.Fatalf("server holds no sessions after a complete captured turn; a teammate would see nothing")
	}
}

// TestE2ETwoIdentitiesAreBothAttributed is the acceptance case for team
// attribution, driven the way it actually happens: two people, two machines
// with two git identities, work arriving in one shared project.
//
// It runs at the #6 seam because that is the only place the question can be
// asked honestly. Attribution is produced by the binary from git identity,
// carried on the step, and read back by whatever a colleague looks at — an
// in-process test can fake any link in that chain and still pass.
//
// The failure it is written against is not "no author shown" but "one author
// shown": a listing that names one person for everyone's work is worse than one
// that names nobody, because it reads as an answer.
func TestE2ETwoIdentitiesAreBothAttributed(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	project := filepath.Join(t.TempDir(), "attribution-project")
	mustMkdirAll(t, project)
	mustMkdirAll(t, filepath.Join(project, ".git"))

	base := hermeticEnv(t, srv)
	e2eRunEnv(t, rgt, project, base, nil, "connect", srv.URL)

	people := []struct{ session, name, email string }{
		{"e2e-ada-session", "Ada Lovelace", "ada@team.dev"},
		{"e2e-grace-session", "Grace Hopper", "grace@team.dev"},
	}
	for i, p := range people {
		captureTurn(t, rgt, project, identityEnv(base, p.name, p.email), p.session, "t1", fmt.Sprintf("file%d.go", i))
	}

	// Grace then takes a turn inside the session Ada started — the ordinary
	// case of one person picking up another's work. Ada's session must stay
	// Ada's: attribution that moves to whoever touched a session last changes
	// under a reader as the session grows, and points at the wrong person.
	captureTurn(t, rgt, project, identityEnv(base, people[1].name, people[1].email), people[0].session, "t2", "handover.go")

	sessions := serverSessions(t, srv.URL, "attribution-project")
	if len(sessions) != len(people) {
		t.Fatalf("server holds %d sessions, want %d: %#v", len(sessions), len(people), sessions)
	}

	// session id -> "Name <email>", keyed the way a colleague reads the list.
	seen := map[string]string{}
	for _, sess := range sessions {
		id, _ := sess["session_id"].(string)
		author, ok := sess["author"].(map[string]any)
		if !ok {
			t.Fatalf("session %q carries no author; a shared listing cannot say who ran it", id)
		}
		name, _ := author["name"].(string)
		email, _ := author["email"].(string)
		seen[id] = fmt.Sprintf("%s <%s>", name, email)
	}

	for _, p := range people {
		want := fmt.Sprintf("%s <%s>", p.name, p.email)
		found := false
		for id, got := range seen {
			if !strings.Contains(id, p.session) {
				continue
			}
			found = true
			if got != want {
				t.Errorf("session %q attributed to %s, want the person who started it, %s", id, got, want)
			}
		}
		if !found {
			t.Errorf("no session matching %q in the listing: %v", p.session, seen)
		}
	}
}

// identityEnv is one person's machine: the shared server settings plus their
// own git identity.
func identityEnv(base []string, name, email string) []string {
	return append(append([]string{}, base...),
		"REGENT_AUTHOR_NAME="+name,
		"REGENT_AUTHOR_EMAIL="+email,
	)
}

// captureTurn drives one complete Claude Code turn — prompt, tool call,
// assistant reply — through the real binary, which is what makes a step exist.
func captureTurn(t *testing.T, rgt, project string, env []string, sessionID, turnID, file string) {
	t.Helper()

	e2eRunEnv(t, rgt, project, env,
		jsonObj("session_id", sessionID, "turn_id", turnID, "cwd", project, "prompt", "add "+file),
		"message-hook", "user")

	writeTestFile(t, project, file, "package main\n")

	e2eRunEnv(t, rgt, project, env,
		jsonObj("session_id", sessionID, "turn_id", turnID, "cwd", project,
			"tool_calls", []any{map[string]any{
				"tool_name":     "Write",
				"tool_use_id":   "tu-" + sessionID + "-" + turnID,
				"tool_input":    map[string]string{"file_path": file},
				"tool_response": "written",
			}}),
		"tool-batch-hook")

	e2eRunEnv(t, rgt, project, env,
		jsonObj("session_id", sessionID, "turn_id", turnID, "cwd", project,
			"last_assistant_message", "done"),
		"message-hook", "assistant")
}

// TestE2ELocalSessionsListingNamesTheAuthor is the same question asked of the
// local command-line read rather than the server: after a captured turn, does
// `rgt sessions` tell the user who ran it? The index is a separate store from
// the objects the server reads, so a pass on one says nothing about the other.
func TestE2ELocalSessionsListingNamesTheAuthor(t *testing.T) {
	rgt := buildTestBinary(t)

	project := t.TempDir()
	env := []string{"HOME=" + t.TempDir(), "REGENT_SERVER_URL=", "REGENT_AUTHOR_NAME=Ada Lovelace", "REGENT_AUTHOR_EMAIL=ada@team.dev"}

	e2eRunEnv(t, rgt, project, env, nil, "init", "--agent", "claude")
	captureTurn(t, rgt, project, env, "e2e-local-session", "t1", "local.go")

	out := e2eRunEnv(t, rgt, project, env, nil, "sessions", "--format=json")

	var got struct {
		Sessions []struct {
			Author *struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("sessions output is not valid JSON: %v\n%s", err, out)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1\n%s", len(got.Sessions), out)
	}
	if got.Sessions[0].Author == nil {
		t.Fatalf("local session listing carries no author\n%s", out)
	}
	if got.Sessions[0].Author.Name != "Ada Lovelace" || got.Sessions[0].Author.Email != "ada@team.dev" {
		t.Errorf("author = %+v, want Ada Lovelace <ada@team.dev>\n%s", got.Sessions[0].Author, out)
	}

	text := e2eRunEnv(t, rgt, project, env, nil, "sessions")
	if !strings.Contains(text, "Ada Lovelace") {
		t.Errorf("human-readable session listing does not name the author:\n%s", text)
	}
}

// ─── seam helpers ───────────────────────────────────────────────────────────

// startTestServer runs the real production server on a temporary data dir.
//
// Deliberately not the in-process remote test double: this seam exists to catch
// disagreements between what the binary sends and what the server accepts, and
// a hand-written double can only ever agree with whoever wrote it.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, err := server.New(t.TempDir())
	if err != nil {
		t.Fatalf("start test server: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// hermeticEnv points the binary at the test server and at a private home
// directory, so a test neither reads nor writes the developer's real state.
//
// The home redirect is not hygiene alone — it is load-bearing. In server mode
// the binary keeps a machine-local cache under the user's cache directory,
// keyed by project id with the server address absent from the path. Without
// this, two tests using the same project name share one cache and one set of
// push watermarks: the second run's spool reports the session as already
// delivered and uploads nothing to a server that has never seen it.
//
// That is a real product defect, not a test artefact — it is why two same-named
// projects pointed at different servers collide on one machine, and it is
// tracked separately. Here it is simply kept out of the way, so a failure in
// these tests means what it says.
func hermeticEnv(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	return []string{
		"HOME=" + t.TempDir(),
		"REGENT_SERVER_URL=" + srv.URL,
	}
}

// buildTestBinary builds rgt once per test from the repo root.
func buildTestBinary(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return buildRGTBinary(t, filepath.Dir(cwd))
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// serverRepos asks the server which repos it knows, the way any client would.
func serverRepos(t *testing.T, baseURL string) []string {
	t.Helper()
	var body struct {
		Repos []string `json:"repos"`
	}
	getJSON(t, baseURL+"/repos", &body)
	return body.Repos
}

// serverSessions reads the server's session listing — the same data the viewer
// renders, which makes it the right thing to assert a teammate can see.
func serverSessions(t *testing.T, baseURL, repoID string) []map[string]any {
	t.Helper()
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	getJSON(t, baseURL+"/"+repoID+"/api/sessions", &body)
	return body.Sessions
}

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}
