package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	// See e2e_onboarding_test.go: this package builds rgt via `go build`, so
	// without a compile-time edge to the CLI the test cache serves stale passes.
	_ "github.com/bonez-io/re_gent/internal/cli"
)

// re_gent decides what counts as itself by filename: a binary is rgt only if it
// is literally called "rgt" or "regent". Nothing about the tool requires that,
// and the moment it is untrue — a second build kept side by side, a versioned
// name, a rename to avoid clobbering a released copy — two separate things go
// wrong at once, both of them silent:
//
//   - the installer refuses to write the running binary's path into the hook,
//     falling back to a bare "rgt" that resolves through PATH to some other
//     build, or to nothing at all;
//   - doctor does not recognise the hook it just wrote, and reports that
//     nothing will be captured on a project that is capturing fine.
//
// The second is the worse of the two. A tool whose job is to answer "is capture
// working" and answers it wrongly is worse than no check, because it teaches
// people to ignore it. Both are asserted here, against the built binary, since
// the filename is the input and only a real build has one.

// buildTestBinaryNamed builds rgt under a chosen filename. Everything else in
// this package builds it as "rgt", which is precisely the assumption under test.
func buildTestBinaryNamed(t *testing.T, name string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, "./cmd/rgt")
	cmd.Dir = filepath.Dir(cwd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}
	return out
}

// runAllowingFailure returns what the user would see whatever the exit code.
// Doctor's other checks (a git identity, for one) depend on the machine, so
// asserting the exit code here would couple this test to facts it is not about.
func runAllowingFailure(t *testing.T, bin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestE2EInitAndDoctorAgreeWhenTheBinaryIsNotNamedRgt(t *testing.T) {
	rgt := buildTestBinaryNamed(t, "rgt-dev")
	project := t.TempDir()

	e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")

	// Half one: the hook must invoke the binary that wrote it. A bare "rgt"
	// here is the silent failure — it runs whatever else is on PATH, or
	// nothing, and capture stops with no error anywhere.
	settings, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(settings), rgt) {
		t.Errorf("init wired hooks to something other than the running binary (%s);\n"+
			"whatever those hooks invoke, it is not the rgt that installed them:\n%s", rgt, settings)
	}

	// Half two: doctor must recognise the hook init just wrote. These two
	// commands ship in one binary and disagreeing with each other is not a
	// judgement call.
	out := runAllowingFailure(t, rgt, project, "doctor")
	if strings.Contains(out, "no re_gent hook") {
		t.Errorf("doctor reports no re_gent hook in a project rgt init wired seconds earlier:\n%s", out)
	}
}
