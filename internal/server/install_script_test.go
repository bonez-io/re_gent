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

// runInstaller executes the installer from workDir with a stub `rgt` on PATH and
// returns that stub's invocations, one per line. Because a working rgt is already
// on PATH the script skips the download entirely, so this exercises the wiring
// logic without touching the network.
// It returns the installer's combined output and the stub's invocations.
func runInstaller(t *testing.T, script, workDir string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh installer")
	}

	home := t.TempDir()
	binDir := t.TempDir()
	callLog := filepath.Join(home, "calls.log")

	// The stub records every invocation and succeeds, so `rgt version` makes the
	// script treat rgt as already installed.
	stub := "#!/bin/sh\necho \"$@\" >> " + callLog + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "rgt"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub rgt: %v", err)
	}

	scriptPath := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"HOME="+home,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run installer: %v\n%s", err, out)
	}

	logged, readErr := os.ReadFile(callLog)
	if readErr != nil {
		return string(out), "" // stub was never called
	}
	return string(out), string(logged)
}

// hasTTY reports whether this test environment has a controlling terminal. The
// installer's behaviour legitimately differs, so the assertion below follows it
// rather than assuming a developer machine.
func hasTTY() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// TestInstallHandsOverToSetup is the one-command onboarding promise: installing
// must lead straight into the project picker, so a teammate never runs a
// separate connect step. With no terminal it must instead say what to run —
// never silently wire nothing and never block on input nobody can give.
func TestInstallHandsOverToSetup(t *testing.T) {
	_, _, ts := newTestServer(t)
	script := fetchInstallScript(t, ts.URL)

	out, calls := runInstaller(t, script, t.TempDir())

	if hasTTY() {
		want := "setup " + ts.URL
		if !strings.Contains(calls, want) {
			t.Errorf("installer should hand over to %q; calls were:\n%s", want, calls)
		}
		return
	}
	if strings.Contains(calls, "setup ") {
		t.Errorf("with no terminal the picker cannot run; calls were:\n%s", calls)
	}
	if !strings.Contains(out, "rgt setup "+ts.URL) {
		t.Errorf("with no terminal the installer must print the command to run; output:\n%s", out)
	}
}

// TestInstallScriptReadsFromTTY pins the mechanism the picker depends on: under
// `curl | sh` stdin is the script itself, so the wizard must be fed /dev/tty or
// it receives EOF instead of keystrokes.
func TestInstallScriptReadsFromTTY(t *testing.T) {
	_, _, ts := newTestServer(t)
	script := fetchInstallScript(t, ts.URL)

	if !strings.Contains(script, "< /dev/tty") {
		t.Error("installer must run setup with stdin redirected from /dev/tty")
	}
}
