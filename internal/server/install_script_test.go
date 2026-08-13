package server

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fetchInstallScript returns the rendered POSIX installer served by GET /install.
func fetchInstallScript(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/install")
	if err != nil {
		t.Fatalf("GET /install: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /install: status %d", resp.StatusCode)
	}
	return string(body)
}

// runInstaller executes the installer against a server whose /bin/rgt serves a
// stub, and returns the installer's output plus the stub's invocations.
//
// The stub is served as the binary rather than merely placed on PATH: the
// installer now always downloads, so anything on PATH would be overwritten by
// the real binary — which would launch the picker and block the test forever.
func runInstaller(t *testing.T, workDir string) (out string, calls string, serverURL string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh installer")
	}

	home := t.TempDir()
	callLog := filepath.Join(home, "calls.log")

	// A shell script stands in for the binary: it runs anywhere, records every
	// invocation, and returns immediately so nothing waits on input.
	binaries := t.TempDir()
	stub := "#!/bin/sh\necho \"$@\" >> " + callLog + "\nexit 0\n"
	name := "rgt_" + runtime.GOOS + "_" + runtime.GOARCH
	if err := os.WriteFile(filepath.Join(binaries, name), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	_, _, ts := newTestServer(t, WithBinariesDir(binaries))
	script := fetchInstallScript(t, ts.URL)

	scriptPath := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+os.Getenv("PATH"))
	// Never let a hang become a hung suite; the stub should return at once.
	done := make(chan struct{})
	var combined []byte
	var runErr error
	go func() {
		combined, runErr = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("installer hung: it should never block on input in a test")
	}
	if runErr != nil {
		t.Fatalf("run installer: %v\n%s", runErr, combined)
	}

	logged, readErr := os.ReadFile(callLog)
	if readErr != nil {
		return string(combined), "", ts.URL
	}
	return string(combined), string(logged), ts.URL
}

// TestInstallAlwaysReinstalls is the fix for a silent staleness bug: the
// installer used to skip the download when any rgt was already on PATH, so
// anyone who had installed once kept their old binary forever and re-running
// the installer appeared to succeed while changing nothing.
func TestInstallAlwaysReinstalls(t *testing.T) {
	out, _, _ := runInstaller(t, t.TempDir())

	if !strings.Contains(out, "Downloading rgt") {
		t.Errorf("installer must always download, got output:\n%s", out)
	}
	if strings.Contains(out, "already installed") {
		t.Errorf("installer must not skip on an existing binary, got:\n%s", out)
	}
}

// TestInstallHandsOverToSetup is the one-command onboarding promise: installing
// leads straight into wiring, so nobody has to run a separate command.
//
// This test previously branched on whether a terminal existed, asserting that
// without one the installer must NOT wire anything and should print
// instructions instead. That was a deliberate decision, and it was wrong: team
// onboarding happens in devcontainers, over SSH and in CI, so the no-terminal
// path is the main path, not a degraded one. The branch is gone and the
// promise is now unconditional.
func TestInstallHandsOverToSetup(t *testing.T) {
	_, calls, url := runInstaller(t, t.TempDir())

	if want := "setup " + url; !strings.Contains(calls, want) {
		t.Errorf("installer should hand over to %q; calls were:\n%s", want, calls)
	}
}

// TestInstallWiresWithoutATerminal is the Phase 2 specification: the paste has
// to work where team onboarding actually happens.
//
// A teammate runs this command inside a devcontainer, over SSH, from a
// provisioning script, or in CI. None of those have a controlling terminal. The
// installer previously treated "no terminal" as "wire nothing and print
// instructions" — which turns a one-command setup into a two-command setup
// exactly for the people least able to notice the second command was needed.
//
// No terminal must mean "wire it without asking", never "give up quietly".
func TestInstallWiresWithoutATerminal(t *testing.T) {
	out, calls, url := runInstaller(t, t.TempDir())

	if want := "setup " + url; !strings.Contains(calls, want) {
		t.Errorf("installer must wire the project with or without a terminal;\nwanted a call to %q\ncalls were:\n%s\ninstaller output was:\n%s", want, calls, out)
	}
}

// TestInstallScriptDoesNotDependOnATerminal inverts a test that used to require
// `< /dev/tty` in the script.
//
// That redirect existed so the interactive picker could read keystrokes under
// `curl | sh`, where stdin is the script itself. The mechanism was sound but
// the guard around it was not: `[ -r /dev/tty ]` reports success whenever the
// device node is readable, yet the redirect then fails with "Device not
// configured" if the process has no controlling terminal. Observed in this very
// suite — the script took the interactive branch, the redirect failed, the
// fallback swallowed the error, and the installer exited 0 having wired
// nothing.
//
// Depending on a terminal at all is the defect. Wiring is now unconditional and
// rgt setup decides, so any reappearance of /dev/tty here is a regression.
func TestInstallScriptDoesNotDependOnATerminal(t *testing.T) {
	_, _, ts := newTestServer(t)
	if script := fetchInstallScript(t, ts.URL); strings.Contains(script, "/dev/tty") {
		t.Error("installer must not depend on /dev/tty; wiring has to work in devcontainers, SSH and CI")
	}
}

// TestInstallVerifiesItsOwnWork closes the loop that Phase 1 and Phase 2 exist
// to close.
//
// Wiring can succeed mechanically and still capture nothing: hooks written but
// never fired, a binary absent from PATH inside the agent's environment, an
// agent host that needs restarting. Every other rgt command exits 0 in that
// state. On a team, the person who pasted the command is not the person who
// would notice — so the paste has to check itself and say so.
func TestInstallVerifiesItsOwnWork(t *testing.T) {
	_, calls, _ := runInstaller(t, t.TempDir())

	if !strings.Contains(calls, "doctor") {
		t.Errorf("installer must end by verifying the install with rgt doctor; calls were:\n%s", calls)
	}
}

// TestInstallFailsWhenVerificationFails is the half that makes the previous
// test worth anything. Calling doctor and then ignoring its verdict would be
// theatre: the teammate would still see the command finish cleanly.
func TestInstallFailsWhenVerificationFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh installer")
	}

	home := t.TempDir()

	// This stub succeeds at everything except doctor, reproducing the case
	// that matters: setup appeared to work, verification says it did not.
	binaries := t.TempDir()
	stub := "#!/bin/sh\ncase \"$1\" in doctor) exit 1 ;; esac\nexit 0\n"
	name := "rgt_" + runtime.GOOS + "_" + runtime.GOARCH
	if err := os.WriteFile(filepath.Join(binaries, name), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	_, _, ts := newTestServer(t, WithBinariesDir(binaries))
	scriptPath := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(scriptPath, []byte(fetchInstallScript(t, ts.URL)), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("installer exited 0 despite doctor failing; a broken setup would look successful.\noutput:\n%s", out)
	}
}
