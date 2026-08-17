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
