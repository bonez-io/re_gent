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
func runInstaller(t *testing.T, script, workDir string) string {
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
		return "" // stub was never called
	}
	return string(logged)
}

// TestInstallAutoConnectsInsideAProject is the one-command onboarding promise:
// running the installer from inside a project must ALSO wire that project to the
// server, so a teammate never runs a separate `rgt connect`.
func TestInstallAutoConnectsInsideAProject(t *testing.T) {
	_, _, ts := newTestServer(t)
	script := fetchInstallScript(t, ts.URL)

	// A project is identified by its .git directory.
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	calls := runInstaller(t, script, project)
	want := "connect " + ts.URL
	if !strings.Contains(calls, want) {
		t.Errorf("installer run inside a project should call %q; calls were:\n%s", want, calls)
	}
}

// TestInstallDoesNotConnectOutsideAProject guards the other half: `curl | sh`
// runs in whatever directory the teammate happens to be in. Connecting blindly
// would scatter .regent/ into home directories, so a non-project directory must
// install rgt and stop there.
func TestInstallDoesNotConnectOutsideAProject(t *testing.T) {
	_, _, ts := newTestServer(t)
	script := fetchInstallScript(t, ts.URL)

	calls := runInstaller(t, script, t.TempDir()) // no .git

	if strings.Contains(calls, "connect ") {
		t.Errorf("installer must not connect outside a project; calls were:\n%s", calls)
	}
}
