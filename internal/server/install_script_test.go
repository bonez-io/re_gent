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
// leads straight into the project picker, so nobody runs a separate connect.
func TestInstallHandsOverToSetup(t *testing.T) {
	out, calls, url := runInstaller(t, t.TempDir())

	if hasTTY() {
		if want := "setup " + url; !strings.Contains(calls, want) {
			t.Errorf("installer should hand over to %q; calls were:\n%s", want, calls)
		}
		return
	}
	if strings.Contains(calls, "setup ") {
		t.Errorf("with no terminal the picker cannot run; calls were:\n%s", calls)
	}
	if !strings.Contains(out, "rgt setup "+url) {
		t.Errorf("with no terminal the installer must print the command; output:\n%s", out)
	}
}

// hasTTY reports whether this environment has a controlling terminal; the
// installer legitimately behaves differently either way.
func hasTTY() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// TestInstallScriptReadsFromTTY pins the mechanism the picker depends on: under
// `curl | sh` stdin is the script itself, so the wizard must be fed /dev/tty or
// it receives EOF instead of keystrokes.
func TestInstallScriptReadsFromTTY(t *testing.T) {
	_, _, ts := newTestServer(t)
	if script := fetchInstallScript(t, ts.URL); !strings.Contains(script, "< /dev/tty") {
		t.Error("installer must run setup with stdin redirected from /dev/tty")
	}
}
