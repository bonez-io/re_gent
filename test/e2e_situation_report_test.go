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

// Claude Code reads .claude/settings.json from the directory it was opened in.
// init writes it at the project root, which is not necessarily that directory.
//
// Open the agent at a workspace root above the project and the settings init
// wrote are never loaded, so capture silently never starts — and doctor, which
// reads the same path init wrote, agrees everything is fine. Two commands
// confirming a file the agent never reads is worse than either of them being
// silent.
func TestE2EInitAndDoctorReportSettingsTheAgentAboveWillNotLoad(t *testing.T) {
	rgt := buildTestBinary(t)

	// The shape a monorepo or a multi-project checkout has: an agent is opened
	// at the workspace root, and the work happens in a project below it.
	workspace := t.TempDir()
	mustMkdirAll(t, filepath.Join(workspace, ".claude"))
	project := filepath.Join(workspace, "project")
	mustMkdirAll(t, project)
	workspacePath := resolvedPath(t, workspace)

	out := runRGTHermetic(t, rgt, project, "init", "--agent", "claude", "--skip-skills")
	// The workspace root is a prefix of every path init prints, so "mentions
	// the path" is satisfied by any of them. What has to be there is a line
	// about the workspace root *itself*.
	if !namesDirectoryAlone(out, workspacePath, project) {
		t.Errorf("init never named the workspace root whose agent will not load these settings:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "not load") {
		t.Errorf("init does not say the settings it wrote will not be loaded from up there:\n%s", out)
	}

	doctorOut, err := runRGTHermeticRaw(t, rgt, project, "doctor")
	if strings.Contains(doctorOut, "✓ claude hooks") {
		t.Errorf("doctor confirmed a hook file the agent above will never load:\n%s", doctorOut)
	}
	if !namesDirectoryAlone(doctorOut, workspacePath, project) {
		t.Errorf("doctor does not report the mismatch init warned about:\n%s", doctorOut)
	}
	if err == nil {
		t.Errorf("doctor exited 0 with capture that will never start:\n%s", doctorOut)
	}
}

// namesDirectoryAlone reports whether some line of out is about dir rather than
// about something underneath it.
func namesDirectoryAlone(out, dir, belowDir string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, dir) && !strings.Contains(line, belowDir) {
			return true
		}
	}
	return false
}

// The mirror: without a workspace root above it, there is no mismatch and
// neither command may invent one. Without this, "always warn" would satisfy the
// test above, and the warning would become noise on every ordinary project.
func TestE2EDoctorDoesNotInventASettingsMismatch(t *testing.T) {
	rgt := buildTestBinary(t)
	project := t.TempDir()

	out := runRGTHermetic(t, rgt, project, "init", "--agent", "claude", "--skip-skills")
	if strings.Contains(strings.ToLower(out), "not load") {
		t.Errorf("init warned about a workspace root that does not exist:\n%s", out)
	}

	doctorOut, _ := runRGTHermeticRaw(t, rgt, project, "doctor")
	if !strings.Contains(doctorOut, "✓ claude hooks") {
		t.Errorf("doctor withheld a hook file the agent here does load:\n%s", doctorOut)
	}
}

// resolvedPath is the path the running binary will print for dir. On macOS the
// temp directories tests are given live under a symlink (/var -> /private/var),
// and a process reports the resolved form — so an assertion against the
// unresolved one compares two spellings of the same directory and fails.
func resolvedPath(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
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
	// NO_COLOR because some assertions here are about which status symbol a line
	// carries, and an escape sequence sits between the symbol and the text.
	env := []string{"HOME=" + t.TempDir(), "NO_COLOR=1"}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "REGENT_") || strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "NO_COLOR=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}
