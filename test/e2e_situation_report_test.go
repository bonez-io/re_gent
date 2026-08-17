package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	// Load-bearing for the same reason as in e2e_onboarding_test.go: these tests
	// shell out to a freshly built rgt, so without an import of the package under
	// test Go's cache would replay a stale pass against a binary that no longer
	// exists.
	_ "github.com/regent-vcs/regent/internal/cli"
)

// These tests pin one promise: a command must describe the situation the user is
// in, not the files the command happens to touch. Each failure below was
// reported as "the output told me the wrong thing and I looked in the wrong
// place", so each assertion is written against what the built binary prints.

// A project bound to a server has its history somewhere else. Reporting a local
// initialisation there is not merely incomplete, it points the user at a
// directory that holds none of their work.
func TestE2EInitOnServerBoundProjectReportsTheBinding(t *testing.T) {
	rgt := buildTestBinary(t)
	project := t.TempDir()

	// The binding `rgt connect` writes: a [remote] table in the project's own
	// .regent/config.toml. Written by hand so the test does not need a live
	// server to reach the state a connected project is in.
	const serverURL = "https://regent.example.test"
	const repoID = "bound-project"
	mustMkdirAll(t, filepath.Join(project, ".regent"))
	writeTestFile(t, project, ".regent/config.toml",
		"[remote]\nurl = \""+serverURL+"\"\nrepo_id = \""+repoID+"\"\n")

	out := runRGTHermetic(t, rgt, project, "init", "--agent", "claude", "--skip-skills")

	if !strings.Contains(out, serverURL) {
		t.Errorf("init never named the server this project is bound to:\n%s", out)
	}
	if !strings.Contains(out, repoID) {
		t.Errorf("init never named the repo id this project is registered as:\n%s", out)
	}

	// The local-initialisation claims. Each one sends a user to .regent/ for
	// history that is not there.
	for _, claim := range []string{
		"Created .regent/ directory",
		"Initialized object store",
		"Created SQLite index",
		"Repository:",
	} {
		if strings.Contains(out, claim) {
			t.Errorf("init described a server-bound project as a local initialisation (%q):\n%s", claim, out)
		}
	}
}

// "No sessions found" and "this session recorded no steps" are different facts
// with different causes, and only the first is worth running doctor over.
//
// log answered both with the first, because it decided by counting *headed*
// sessions: a session that was captured but never completed a turn has no head
// step, so it counted as no session at all. The user is then told capture never
// ran — and sent to doctor, which correctly reports that everything is wired,
// leaving them with two commands that disagree and no explanation.
func TestE2ELogDistinguishesNoSessionsFromNoSteps(t *testing.T) {
	rgt := buildTestBinary(t)
	project := t.TempDir()

	e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")

	// Nothing has ever been captured here. This is the case doctor explains.
	fresh := e2eRun(t, rgt, project, nil, "log")
	if !strings.Contains(fresh, "doctor") {
		t.Errorf("log in a repository that captured nothing does not point at doctor:\n%s", fresh)
	}

	// Now capture runs: a prompt arrives and is recorded against a session. No
	// turn completes, so there is no step. The hook wiring is provably fine —
	// this output just ran through it.
	const sid = "e2e-session-without-steps"
	e2eRunStdin(t, rgt, project,
		jsonObj("session_id", sid, "turn_id", "t1", "cwd", project, "prompt", "think about it"),
		"message-hook", "user")

	out := e2eRun(t, rgt, project, nil, "log")

	if strings.Contains(strings.ToLower(out), "no sessions") {
		t.Errorf("log reported no sessions after one was captured:\n%s", out)
	}
	if !strings.Contains(out, sid) {
		t.Errorf("log never names the session that recorded nothing:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "no steps") {
		t.Errorf("log does not say the session recorded no steps:\n%s", out)
	}
	// Sending this user to doctor is the reported bug. Capture ran; doctor will
	// tell them everything is fine and they will be no wiser.
	if strings.Contains(out, "doctor") {
		t.Errorf("log sent a user whose capture demonstrably ran to doctor:\n%s", out)
	}
}

// runRGTHermetic runs rgt with every REGENT_* variable stripped and HOME pointed
// at a scratch directory.
//
// The suite's TestMain sets REGENT_SERVER_URL to the empty string, which is
// *set* as far as os.LookupEnv is concerned, so it overrides a [remote] binding
// read from a config file. A test about config-file bindings therefore cannot
// use the shared runners: it would be asserting against a process that could not
// see the binding it just wrote.
func runRGTHermetic(t *testing.T, rgtPath, dir string, args ...string) string {
	t.Helper()
	out, err := runRGTHermeticRaw(t, rgtPath, dir, args...)
	if err != nil {
		t.Fatalf("rgt %v failed: %v\nOutput:\n%s", args, err, out)
	}
	return out
}

// runRGTHermeticRaw is runRGTHermetic without the exit-code assertion, for the
// commands whose non-zero exit is itself under test.
func runRGTHermeticRaw(t *testing.T, rgtPath, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(rgtPath, args...)
	cmd.Dir = dir
	env := []string{"HOME=" + t.TempDir()}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "REGENT_") || strings.HasPrefix(entry, "HOME=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}
