package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The installer's last act is `rgt doctor`, and a non-zero exit unwinds the
// whole paste. That makes doctor's verdict the installer's verdict, so what
// doctor is willing to fail on decides what an install is willing to fail on.
//
// These tests run the generated script for real against two machines, with the
// real rgt answering `doctor`. Asserting on the script's text would prove only
// that we wrote what we meant to write; the question is what the script does
// when doctor disagrees with it.

// machine is one state of the box the paste lands on: what is wired, and
// therefore what doctor will find.
type machine struct {
	name string
	// claudeSettings is written to .claude/settings.json. A file with no
	// re_gent hook in it is the shape left behind when wiring silently did
	// nothing.
	claudeSettings string
}

var (
	// hooksWired: a .claude/settings.json that actually invokes re_gent.
	hooksWired = `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"rgt tool-batch-hook"}]}]}}`
	// nothingWired: settings present, re_gent absent. Nothing will be captured.
	nothingWired = `{"hooks":{}}`
)

// runInstallerVerification runs the served install script end to end against a
// machine with NO git identity configured, and reports whether it exited 0.
//
// The served "binary" is a shim: it answers everything with success except
// `doctor`, which it hands to a freshly built, real rgt. The download,
// PATH handling, hand-off to setup and the verification branch are all the
// script's own; only doctor's verdict has to be genuine, and it is.
func runInstallerVerification(t *testing.T, m machine) (out string, exitedZero bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh installer")
	}

	workDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(workDir, ".regent"))
	mustMkdirAll(t, filepath.Join(workDir, ".claude"))
	mustWriteFile(t, filepath.Join(workDir, ".claude", "settings.json"), m.claudeSettings)

	real := buildRGT(t)
	binaries := t.TempDir()
	shim := "#!/bin/sh\nif [ \"$1\" = doctor ]; then\n  exec " + real + " \"$@\"\nfi\nexit 0\n"
	name := "rgt_" + runtime.GOOS + "_" + runtime.GOARCH
	mustWriteExecutable(t, filepath.Join(binaries, name), shim)

	_, _, ts := newTestServer(t, WithBinariesDir(binaries))
	scriptPath := filepath.Join(t.TempDir(), "install.sh")
	mustWriteExecutable(t, scriptPath, fetchInstallScript(t, ts.URL))

	cmd := exec.Command("sh", scriptPath)
	cmd.Dir = workDir
	// A deliberately bare environment: this is the fresh VPS, the container
	// image, the CI runner. HOME is a temp dir so the install lands in
	// $HOME/.local/bin, the git config files are /dev/null so no identity can
	// leak in from the developer running the suite, and PATH is the system
	// minimum so a `claude` or `codex` binary on the real machine cannot change
	// which agents doctor decides to check.
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"NO_COLOR=1",
	}

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := cmd.CombinedOutput()
		done <- result{b, err}
	}()
	select {
	case r := <-done:
		return string(r.out), r.err == nil
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("installer hung on machine %q; it must never block on input", m.name)
		return "", false
	}
}

// buildRGT compiles the real CLI so the verification step is checked against
// the doctor that actually ships, not a stand-in that agrees with us by
// construction.
func buildRGT(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "rgt")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/rgt")
	cmd.Dir = filepath.Join("..", "..")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build rgt: %v\n%s", err, b)
	}
	return out
}

// A curl | sh install is the single place git identity is least likely to be
// configured: a fresh VPS, a container image, a CI runner. Treating that as an
// installation failure aborts at the last step, after the download, the wiring
// and the hand-off have all already succeeded, and hands back a message about
// git config for something the install did correctly.
func TestInstallCompletesWhenGitIdentityIsUnset(t *testing.T) {
	out, exitedZero := runInstallerVerification(t, machine{
		name:           "wired, no git identity",
		claudeSettings: hooksWired,
	})

	if !exitedZero {
		t.Errorf("install aborted on a machine whose only problem is an unset git identity; everything it had to do had already succeeded.\noutput:\n%s", out)
	}
}

// Exiting 0 is only defensible if the warning survives. Swallowing it would
// trade a wrong failure for a silent one: every step recorded anonymously, and
// nobody told while it still costs one command to fix.
func TestInstallStillWarnsAboutAnonymousRecording(t *testing.T) {
	out, _ := runInstallerVerification(t, machine{
		name:           "wired, no git identity",
		claudeSettings: hooksWired,
	})

	if !strings.Contains(out, "anonymous") {
		t.Errorf("install said nothing about steps being recorded anonymously.\noutput:\n%s", out)
	}
	if !strings.Contains(out, "git config") {
		t.Errorf("install warned but did not say how to fix it.\noutput:\n%s", out)
	}
}

// The half that keeps the previous two honest. An install that wired no hooks
// captures nothing, every other rgt command exits 0 in that state, and the
// person who pasted the command is not the person who would notice the silence.
// That must still fail, and must still name the agent to wire.
func TestInstallStillFailsWhenNoHooksAreWired(t *testing.T) {
	out, exitedZero := runInstallerVerification(t, machine{
		name:           "nothing wired",
		claudeSettings: nothingWired,
	})

	if exitedZero {
		t.Errorf("install exited 0 having wired no hooks; nothing will ever be captured here.\noutput:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Errorf("install failed without naming the agent that needs wiring.\noutput:\n%s", out)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustWriteExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
