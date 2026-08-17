package server

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The generated install script invoked `rgt setup`, a command that had been
// removed. Every `curl … | sh` install therefore died at the wiring step with
// "Setup did not finish", after downloading and installing the binary
// correctly — and the suite stayed green throughout, because the script tests
// serve a stub binary that exits 0 whatever it is asked to do. The tests
// asserted the stub had been *called* with `setup <url>`, which pinned the
// broken call rather than catching it.
//
// This checks the script against the real command tree instead: every rgt verb
// the script runs must be one the shipped binary still answers to. It is the
// general form of the bug, so removing any other command out from under the
// installer fails here rather than in someone's terminal.
func TestInstallScriptOnlyCallsCommandsTheBinaryStillHas(t *testing.T) {
	_, _, ts := newTestServer(t)
	script := fetchInstallScript(t, ts.URL)

	// Lines that invoke rgt directly or behind a shell failure guard, e.g.
	// `if ! rgt connect "$URL"; then`.
	verbLine := regexp.MustCompile(`(?m)^\s*(if\s+!\s+)?rgt\s+([a-z][a-z-]*)`)
	matches := verbLine.FindAllStringSubmatch(script, -1)
	if len(matches) == 0 {
		t.Fatal("no rgt invocations found in the install script; this test is checking nothing")
	}

	rgt := buildRealBinary(t)
	for _, m := range matches {
		verb := m[2]
		// Invoked for real, not with --help: cobra answers --help before it
		// reaches a command's RunE, so a removed command still prints a
		// friendly help page. The first version of this test did that and
		// passed against the live bug. Run in a scratch directory because
		// some of these verbs write to the one they are run in.
		cmd := exec.Command(rgt, verb)
		cmd.Dir = t.TempDir()
		out, _ := cmd.CombinedOutput()
		text := string(out)
		if strings.Contains(text, "has been removed") || strings.Contains(text, "unknown command") {
			t.Errorf("the install script runs `rgt %s`, which the binary no longer has:\n%s",
				verb, strings.TrimSpace(text))
		}
	}
}

// buildRealBinary builds the actual rgt, not the stub the other script tests
// use. The stub is what let this bug live: it answers every command with
// success, so a script calling a command that does not exist looks fine.
func buildRealBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "rgt")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/rgt")
	cmd.Dir = "../.."
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build rgt: %v\n%s", err, b)
	}
	return out
}
