package test

import (
	"strings"
	"testing"

	// See e2e_onboarding_test.go: this package builds rgt via `go build`, so
	// without a compile-time edge to the CLI the test cache serves stale passes.
	_ "github.com/bonez-io/re_gent/internal/cli"
)

// Connecting a project switches reads to a machine-local cache keyed to the
// server. Everything recorded before that moment sits in the project's own
// .regent/ and is reachable by nothing: no command reads it, no command uploads
// it, and no command mentions it.
//
// It is silent data loss, and it lands on the worst possible person — someone
// trying the tool out locally for an afternoon and then wiring it to a server,
// who watches their first day's history disappear at the exact moment they are
// deciding whether this thing is trustworthy.
//
// Written at the seam ticket #6 established: the built binary, a live server,
// and history produced by the real hooks rather than by writing objects by hand.

// serverRefs asks the server which refs it holds for a repo, the way any client
// would. A ref is the tip of a session, so this is the server's answer to "do
// you have this project's history".
func serverRefs(t *testing.T, baseURL, repoID string) map[string]string {
	t.Helper()
	var body struct {
		Refs map[string]string `json:"refs"`
	}
	getJSON(t, baseURL+"/"+repoID+"/refs", &body)
	return body.Refs
}

func TestE2EConnectingKeepsHistoryRecordedBeforeIt(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	project := gitProject(t, "tried-it-locally", "https://github.com/acme/tried-it-locally.git")
	e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")

	const session = "afternoon-of-trying-it"
	captureTurn(t, rgt, project, nil, session, "turn-1", "hello.go")
	captureTurn(t, rgt, project, nil, session, "turn-2", "goodbye.go")

	canonical := "claude_code--" + session
	before := parseLogJSON(t, e2eRun(t, rgt, project, nil, "log", "--session", canonical, "--json"))
	if len(before.Steps) != 2 {
		t.Fatalf("precondition: expected 2 steps recorded locally, got %d", len(before.Steps))
	}

	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	// The history has to still be there. This is the whole ticket: a user who
	// connects has not asked to discard anything.
	after := parseLogJSON(t, e2eRunEnv(t, rgt, project, env, nil, "log", "--session", canonical, "--json"))
	if len(after.Steps) != len(before.Steps) {
		t.Errorf("connect left %d of %d steps readable; the rest is on disk and reachable by nothing",
			len(after.Steps), len(before.Steps))
	}

	// And it has to be the same history, not merely the same count.
	seen := map[string]bool{}
	for _, s := range after.Steps {
		seen[s.Hash] = true
	}
	for _, s := range before.Steps {
		if !seen[s.Hash] {
			t.Errorf("step %s was recorded before connecting and is not readable after", s.Hash[:8])
		}
	}
}

// Readable is not the same as uploaded. If connect leaves the history only in
// the local cache, it survives on this machine and exists nowhere else — so a
// teammate reading the server, or this user after clearing a cache the design
// calls disposable, still sees nothing.
func TestE2EHistoryRecordedBeforeConnectingReachesTheServer(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	project := gitProject(t, "tried-it-locally", "https://github.com/acme/tried-it-locally.git")
	e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")

	const session = "afternoon-of-trying-it"
	captureTurn(t, rgt, project, nil, session, "turn-1", "hello.go")

	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	repoID := repoIDOf(t, project)
	if refs := serverRefs(t, srv.URL, repoID); len(refs) == 0 {
		t.Errorf("the server holds no refs for %s after connecting a project with history:\n%s", repoID, out)
	}
}

// Whatever happens to the history must be said out loud. Dropping it silently
// is the failure this ticket exists to remove, and carrying over part of it
// while reporting success is the same failure in better clothes.
func TestE2EConnectSaysWhatItCarriedOver(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	project := gitProject(t, "tried-it-locally", "https://github.com/acme/tried-it-locally.git")
	e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")
	// Two turns, so the step count is something a message has to have counted
	// rather than something it could get right by printing "1".
	captureTurn(t, rgt, project, nil, "afternoon-of-trying-it", "turn-1", "hello.go")
	captureTurn(t, rgt, project, nil, "afternoon-of-trying-it", "turn-2", "goodbye.go")

	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	// The counts are the assertion, not the vocabulary. This used to look for
	// any of "histor"/"session"/"step" anywhere in the output, which passed on
	// unrelated wording while connect was still discarding everything — a green
	// test for the exact bug this file was written against. What a user needs
	// is how much came across and that it left the machine.
	for _, want := range []string{"Carried over 1 session (2 steps)", "uploaded 1 session"} {
		if !strings.Contains(out, want) {
			t.Errorf("connect output does not say %q, so a user has no way to know whether their history came across:\n%s", want, out)
		}
	}
}
