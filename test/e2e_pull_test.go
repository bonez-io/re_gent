package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	// See e2e_onboarding_test.go: this package builds rgt via `go build`, so
	// without a compile-time edge to the CLI the test cache serves stale passes.
	_ "github.com/regent-vcs/regent/internal/cli"
)

func conversationFromLog(t *testing.T, output string) []any {
	t.Helper()
	var log struct {
		Steps []struct {
			Messages []json.RawMessage `json:"messages"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(output), &log); err != nil {
		t.Fatalf("decode log conversation: %v\n%s", err, output)
	}
	if len(log.Steps) != 1 {
		t.Fatalf("log steps = %d, want 1\n%s", len(log.Steps), output)
	}
	messages := make([]any, 0, len(log.Steps[0].Messages))
	for _, raw := range log.Steps[0].Messages {
		var message any
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode log message: %v\n%s", err, raw)
		}
		messages = append(messages, message)
	}
	return messages
}

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

func TestE2EFreshMachinePullRestoresCompleteConversation(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	author := gitProject(t, "conversation-project", "https://github.com/acme/conversation-project.git")
	authorEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, author, authorEnv, nil, "connect", srv.URL)
	captureTurn(t, rgt, author, authorEnv, "conversation-session", "t1", "conversation.go")

	const canonical = "claude_code--conversation-session"
	originalLog := e2eRunEnv(t, rgt, author, authorEnv, nil, "log", "--session", canonical, "--json")
	originalConversation := conversationFromLog(t, originalLog)
	if len(originalConversation) == 0 {
		t.Fatalf("recording machine has no conversation precondition:\n%s", originalLog)
	}

	clone := gitProject(t, "conversation-project", "https://github.com/acme/conversation-project.git")
	cloneEnv := machineEnv(t, srv.URL)
	e2eRunEnv(t, rgt, clone, cloneEnv, nil, "connect", srv.URL)
	e2eRunEnv(t, rgt, clone, cloneEnv, nil, "pull")

	pulledLog := e2eRunEnv(t, rgt, clone, cloneEnv, nil, "log", "--session", canonical, "--json")
	pulledConversation := conversationFromLog(t, pulledLog)
	if !reflect.DeepEqual(pulledConversation, originalConversation) {
		t.Errorf("fresh-machine conversation differs from recording machine\noriginal: %#v\npulled:   %#v", originalConversation, pulledConversation)
	}

	step := parseLogJSON(t, originalLog).Steps[0]
	originalShow := e2eRunEnv(t, rgt, author, authorEnv, nil, "show", step.Hash)
	pulledShow := e2eRunEnv(t, rgt, clone, cloneEnv, nil, "show", step.Hash)
	if pulledShow != originalShow {
		t.Errorf("fresh-machine show output differs from recording machine\n--- original ---\n%s\n--- pulled ---\n%s", originalShow, pulledShow)
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

// An empty cache says nothing by itself about the server. Exercise the real
// binary against the production HTTP server for every state the empty-cache
// reporter has to distinguish. A hand-written Client would only prove the
// wording branch, not that the request identifies the state on the wire.
func TestE2EEmptyCacheReportsWhatTheLiveServerActuallyKnows(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	t.Run("known project with no history", func(t *testing.T) {
		project := gitProject(t, "known-empty", "https://github.com/acme/known-empty.git")
		env := machineEnv(t, srv.URL)
		e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

		out, err := e2eRunEnvRaw(t, rgt, project, env, "sessions")
		if err != nil {
			t.Fatalf("sessions failed: %v\n%s", err, out)
		}
		if !strings.Contains(strings.ToLower(out), "holds no history yet") {
			t.Errorf("empty registered project did not report its empty server history:\n%s", out)
		}
		if strings.Contains(strings.ToLower(out), "not yet pulled") {
			t.Errorf("empty registered project was reported as awaiting a pull:\n%s", out)
		}
	})

	t.Run("unknown project", func(t *testing.T) {
		project := gitProject(t, "unknown-project", "https://github.com/acme/unknown-project.git")
		env := machineEnv(t, srv.URL)
		mustMkdirAll(t, project+"/.regent")
		writeTestFile(t, project, ".regent/config.toml", "[remote]\nurl = \""+srv.URL+"\"\nrepo_id = \"unknown-project\"\n")

		out, err := e2eRunEnvRaw(t, rgt, project, env, "sessions")
		if err != nil {
			t.Fatalf("sessions failed: %v\n%s", err, out)
		}
		if !strings.Contains(strings.ToLower(out), "does not know this project") {
			t.Errorf("unknown project was not identified as unknown to the server:\n%s", out)
		}
		if !strings.Contains(out, "rgt connect") {
			t.Errorf("unknown project report does not name re-registration:\n%s", out)
		}
		if strings.Contains(strings.ToLower(out), "history is recorded") {
			t.Errorf("unknown project claimed its history was safe on the server:\n%s", out)
		}
	})

	t.Run("unreachable server", func(t *testing.T) {
		project := gitProject(t, "unreachable-project", "https://github.com/acme/unreachable-project.git")
		env := machineEnv(t, srv.URL)
		e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
		srv.Close()

		out, err := e2eRunEnvRaw(t, rgt, project, env, "sessions")
		if err != nil {
			t.Fatalf("sessions failed: %v\n%s", err, out)
		}
		lower := strings.ToLower(out)
		if !strings.Contains(lower, "cannot reach") {
			t.Errorf("unreachable server was not reported as unreachable:\n%s", out)
		}
		for _, unsafeClaim := range []string{"not yet pulled", "history is recorded"} {
			if strings.Contains(lower, unsafeClaim) {
				t.Errorf("unreachable server report made unsafe claim %q:\n%s", unsafeClaim, out)
			}
		}
	})
}
