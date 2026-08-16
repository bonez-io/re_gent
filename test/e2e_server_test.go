package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
