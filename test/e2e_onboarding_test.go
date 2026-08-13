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

	if strings.Contains(out, "Initialization complete") {
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

	if !strings.Contains(out, "Initialization complete") {
		t.Errorf("init wired Claude but did not report completion:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "settings.json")); err != nil {
		t.Errorf("init reported success but wrote no Claude settings: %v", err)
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
