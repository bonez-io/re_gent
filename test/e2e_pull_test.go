package test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	// See e2e_onboarding_test.go: this package builds rgt via `go build`, so
	// without a compile-time edge to the CLI the test cache serves stale passes.
	_ "github.com/regent-vcs/regent/internal/cli"
)

// The read half of the team story, at the #6 seam: one developer records and
// pushes, a second clone of the same project reads it back.
//
// Every assertion here is written against a machine that has pushed nothing.
// That is the whole difficulty: `rgt sync --pull` can only offer refs this
// machine previously sent, read from its own spool, so on a fresh clone it has
// nothing to offer and no way to ask the server what exists.

const teamOrigin = "https://github.com/acme/team-project.git"

// machineEnv is one person's machine: the shared server, a private home, and a
// cache directory of its own.
//
// hermeticEnv already gives each caller a separate HOME, and on this platform
// the cache hangs off it — but "two machines" is the load-bearing fact of every
// test in this file, so it is stated outright rather than inherited from how
// os.UserCacheDir happens to resolve. Two people sharing one cache would pass
// these tests without a single byte crossing the network.
func machineEnv(t *testing.T, srvURL string) []string {
	t.Helper()
	return []string{
		"HOME=" + t.TempDir(),
		"REGENT_SERVER_URL=" + srvURL,
		"REGENT_CACHE_DIR=" + t.TempDir(),
	}
}

// e2eRunEnvRaw is e2eRunEnv without the exit-code assertion, for commands whose
// exit code is itself under test.
func e2eRunEnvRaw(t *testing.T, rgtPath, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(rgtPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A teammate clones a connected project and runs one command. Before this
// existed they saw "no sessions" and exit zero while the server held everything.
func TestE2ESecondCloneCanPullAndReadTheTeamsHistory(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	author := gitProject(t, "team-project", teamOrigin)
	authorEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, author, authorEnv, nil, "connect", srv.URL)

	const session = "shared-session"
	captureTurn(t, rgt, author, authorEnv, session, "t1", "hello.go")
	captureTurn(t, rgt, author, authorEnv, session, "t2", "goodbye.go")

	// The second machine: the same project by identity — same git origin, so
	// the same repo id — and nothing else in common.
	clone := gitProject(t, "team-project", teamOrigin)
	cloneEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, clone, cloneEnv, nil, "connect", srv.URL)

	// No ref is named, because a fresh clone has no way to know one.
	out := e2eRunEnv(t, rgt, clone, cloneEnv, nil, "pull")
	if !strings.Contains(out, session) {
		t.Errorf("pull never named the session it was supposed to discover:\n%s", out)
	}

	canonical := "claude_code--" + session
	pulled := parseLogJSON(t, e2eRunEnv(t, rgt, clone, cloneEnv, nil, "log", "--session", canonical, "--json"))
	if len(pulled.Steps) != 2 {
		t.Fatalf("log on the second clone shows %d step(s) after pulling, want 2:\n%s",
			len(pulled.Steps), out)
	}

	// The same history, not merely the same count.
	original := parseLogJSON(t, e2eRunEnv(t, rgt, author, authorEnv, nil, "log", "--session", canonical, "--json"))
	recorded := map[string]bool{}
	for _, s := range original.Steps {
		recorded[s.Hash] = true
	}
	for _, s := range pulled.Steps {
		if !recorded[s.Hash] {
			t.Errorf("pulled step %s is not one the author recorded", s.Hash[:8])
		}
	}

	// And the sessions listing — the command a teammate reaches for first —
	// has to show it too.
	if listing := e2eRunEnv(t, rgt, clone, cloneEnv, nil, "sessions"); !strings.Contains(listing, canonical) {
		t.Errorf("sessions on the second clone does not list the pulled session:\n%s", listing)
	}
}

// Pulling is worth doing only if it leaves something behind. Reads have to work
// with the server gone, or the cache is a proxy rather than a copy.
func TestE2EPulledHistoryReadsWithTheServerGone(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	author := gitProject(t, "team-project", teamOrigin)
	authorEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, author, authorEnv, nil, "connect", srv.URL)
	captureTurn(t, rgt, author, authorEnv, "offline-session", "t1", "hello.go")

	clone := gitProject(t, "team-project", teamOrigin)
	cloneEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, clone, cloneEnv, nil, "connect", srv.URL)
	e2eRunEnv(t, rgt, clone, cloneEnv, nil, "pull")

	srv.Close() // the server is now unreachable; the pulled history is local

	canonical := "claude_code--offline-session"
	offline := parseLogJSON(t, e2eRunEnv(t, rgt, clone, cloneEnv, nil, "log", "--session", canonical, "--json"))
	if len(offline.Steps) != 1 {
		t.Fatalf("log with the server gone shows %d step(s), want 1", len(offline.Steps))
	}
}

// The empty state has to stop lying. A connected project whose cache is empty
// has history — it is just somewhere else — and "no sessions" sends the user to
// look for a wiring problem that does not exist.
func TestE2EConnectedProjectWithAnEmptyCacheSaysItHasNotPulled(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	author := gitProject(t, "team-project", teamOrigin)
	authorEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, author, authorEnv, nil, "connect", srv.URL)
	captureTurn(t, rgt, author, authorEnv, "recorded-elsewhere", "t1", "hello.go")

	clone := gitProject(t, "team-project", teamOrigin)
	cloneEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, clone, cloneEnv, nil, "connect", srv.URL)

	// Deliberately no pull: this is the state a teammate is in the moment they
	// finish cloning.
	for _, command := range []string{"sessions", "log", "status"} {
		out, _ := e2eRunEnvRaw(t, rgt, clone, cloneEnv, command)
		lower := strings.ToLower(out)

		if strings.Contains(lower, "no sessions") {
			t.Errorf("`rgt %s` claims there are no sessions while the server holds one:\n%s", command, out)
		}
		if !strings.Contains(lower, "not yet pulled") {
			t.Errorf("`rgt %s` does not say this project is connected but not yet pulled:\n%s", command, out)
		}
		if !strings.Contains(out, "rgt pull") {
			t.Errorf("`rgt %s` does not name the command that would fetch the history:\n%s", command, out)
		}
	}
}
