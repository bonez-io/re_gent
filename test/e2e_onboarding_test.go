package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	// Not used by any code here, and load-bearing anyway.
	//
	// This package builds rgt by shelling out to `go build`, so nothing it
	// compiles against depends on the CLI it tests. Go's test cache therefore
	// cannot see a change to internal/cli, and `go test ./test/` replays the
	// previous result against a binary that no longer exists. Found by mutating
	// both of init's exit guards and watching the suite report a cached pass —
	// three times.
	//
	// A suite that answers for code it did not run is worse than no suite. This
	// import makes the test binary depend on the package under test, so any
	// change to it invalidates the cache the ordinary way.
	_ "github.com/regent-vcs/regent/internal/cli"
)

// The onboarding path is about to lose a lot of code: an interactive wizard, a
// second project picker, a second terminal probe. Deleting them is only safe if
// the promises they sat behind are pinned from outside the package, at the
// level a user actually experiences them.
//
// These are those promises. They are deliberately written against the built
// binary and its exit code, because that is the whole of what a script, a
// devcontainer, or a `curl | sh` install can observe.

// A run that wires nothing must fail loudly.
//
// This is the shipped bug in its final form: the summary said "Initialization
// complete" and the process exited 0 after installing no hooks at all, so
// nothing was captured and nothing said so. A person might notice the empty
// `rgt log` a week later. A script never would.
func TestE2EInitFailsWhenNoHooksCouldBeWired(t *testing.T) {
	rgt := buildTestBinary(t)
	project := t.TempDir()

	// Block the only requested agent: a regular file where the installer needs
	// a directory, which is what a stale or hand-edited `.claude` looks like.
	if err := os.WriteFile(filepath.Join(project, ".claude"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	out := e2eRunExpectingFailure(t, rgt, project, "init", "--agent", "claude", "--skip-skills")

	if strings.Contains(out, "Ready to capture") {
		t.Errorf("init claimed completion after wiring nothing:\n%s", out)
	}
	// The user has to be told which agent failed, not merely that something did.
	if !strings.Contains(strings.ToLower(out), "claude") {
		t.Errorf("failure output never names the agent that could not be wired:\n%s", out)
	}
}

// The mirror of the above, and the reason the exit code is worth asserting at
// all: a successful run must exit 0 and say so. Without this, "always fail"
// would satisfy the test above.
func TestE2EInitSucceedsWhenAnAgentIsWired(t *testing.T) {
	rgt := buildTestBinary(t)
	project := t.TempDir()

	out := e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")

	if !strings.Contains(out, "Ready to capture") {
		t.Errorf("init wired Claude but did not report completion:\n%s", out)
	}
	if strings.Contains(out, "Agent skills:") {
		t.Errorf("init reported optional skills even though they were skipped:\n%s", out)
	}
	if strings.Contains(out, "Step 1/3") {
		t.Errorf("init regressed to the verbose three-step installer output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "settings.json")); err != nil {
		t.Errorf("init reported success but wrote no Claude settings: %v", err)
	}
}

// `rgt init` run inside a project whose agent is opened one directory up.
//
// The remedy itself is pinned in internal/cli, but that leaves the question of
// whether init actually prints it — and the printing is the half that was
// wrong. The old text ended on "wire that directory too: cd <ancestor> && rgt
// init --agent claude", a user ran exactly that, and every project under that
// directory now records into one blended history there (#27).
//
// Asserted against the built binary because a user reads this from a terminal,
// and because reportClaudeSettingsScope is reached only through the real wiring
// path — nothing in-process covers the call site.
func TestE2EInitUnderAShadowingWorkspaceLeadsWithOpeningTheAgentHere(t *testing.T) {
	rgt := buildTestBinary(t)

	// The workspace above: a .claude/ with no re_gent hook, which is what an
	// agent opened there would load instead of the project's.
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755); err != nil {
		t.Fatalf("seed workspace .claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("seed workspace settings: %v", err)
	}
	project := filepath.Join(workspace, "tsenta-agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	out := e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")

	openHere := strings.Index(out, "open the agent inside this project")
	if openHere < 0 {
		t.Fatalf("init wrote hooks an agent opened at %s will never load, and never said to open it here instead:\n%s", workspace, out)
	}
	if wireAncestor := strings.Index(out, "rgt init --agent claude"); wireAncestor >= 0 && wireAncestor < openHere {
		t.Errorf("init still leads with wiring the directory above, the advice that produced the blended history in #27:\n%s", out)
	}
	// Offering the ancestor-wide option is fine; offering it without saying the
	// project's own history goes elsewhere is what made it look like the fix.
	if strings.Contains(out, "rgt init --agent claude") && !strings.Contains(out, filepath.Join(workspace, ".regent")) {
		t.Errorf("init offers to wire %s without naming where the history would then land:\n%s", workspace, out)
	}
}

// e2eRunExpectingFailure is e2eRun inverted: the non-zero exit is the assertion
// rather than an abort. Kept separate from e2eRun so that a test which merely
// forgets to check an error can never quietly pass through it.
func e2eRunExpectingFailure(t *testing.T, rgtPath, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(rgtPath, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("rgt %v exited 0, but it configured nothing.\nOutput:\n%s", args, out)
	}
	return string(out)
}

// A failure must be reported once.
//
// Every error `rgt` produced was printed twice: cobra printed it, then main
// printed the same error it had just returned. It was easy to miss on a
// one-line "unknown flag", and impossible to miss once the removed commands
// started answering with a five-line explanation of what to run instead —
// which arrived, in full, twice.
//
// Asserted against the built binary because that is where the duplication
// lives: neither printer is wrong on its own, only the pair of them.
func TestErrorsArePrintedOnce(t *testing.T) {
	rgt := buildTestBinary(t)

	// Both a removed command and an ordinary one, because silencing is
	// inheritable and it is entirely possible to fix this on one command and
	// believe it fixed everywhere. The first version of this test asserted only
	// the removed command and passed against exactly that mistake.
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"a removed command", []string{"setup"}, "has been removed"},
		{"an ordinary command", []string{"cat"}, "accepts 1 arg(s)"},
	}

	for _, tc := range cases {
		out := e2eRunExpectingFailure(t, rgt, t.TempDir(), tc.args...)
		if n := strings.Count(out, tc.message); n != 1 {
			t.Errorf("%s: the failure was printed %d times, want 1:\n%s", tc.name, n, out)
		}
	}
}
