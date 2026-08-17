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
	// workspaceClaudeSettings, when non-empty, puts a .claude/ directory one
	// level ABOVE the project.
	//
	// That is the layout of someone who keeps one agent open at
	// ~/Documents/GitHub and works on projects underneath it, and it is where
	// the one-line install ended in the wrong place twice (#27). Claude Code
	// loads settings from the directory it was opened in, and capture resolves
	// its store from the session's working directory, so an agent started up
	// there neither loads this project's hooks nor records into its .regent/.
	workspaceClaudeSettings string
}

// installRun is what one paste did: the transcript, the exit status, and the
// directories the assertions need to name. The paths are returned rather than
// reconstructed by each test, because the whole subject here is which directory
// something lands in.
type installRun struct {
	out        string
	exitedZero bool
	// project is the directory the paste ran in.
	project string
	// workspace is the ancestor holding .claude/, or "" when there is none.
	workspace string
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
func runInstallerVerification(t *testing.T, m machine) installRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh installer")
	}

	// The project always sits in a named subdirectory, so assertions can name it
	// by a word the user would recognise rather than by a temp-dir number, and
	// so the shadowed and unshadowed layouts differ in one fact only: whether
	// the directory above holds a .claude/.
	root := t.TempDir()
	workDir := filepath.Join(root, "tsenta-agent")
	workspace := ""
	if m.workspaceClaudeSettings != "" {
		workspace = root
		mustMkdirAll(t, filepath.Join(root, ".claude"))
		mustWriteFile(t, filepath.Join(root, ".claude", "settings.json"), m.workspaceClaudeSettings)
	}
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
		return installRun{out: string(r.out), exitedZero: r.err == nil, project: workDir, workspace: workspace}
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("installer hung on machine %q; it must never block on input", m.name)
		return installRun{}
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
	run := runInstallerVerification(t, machine{
		name:           "wired, no git identity",
		claudeSettings: hooksWired,
	})

	if !run.exitedZero {
		t.Errorf("install aborted on a machine whose only problem is an unset git identity; everything it had to do had already succeeded.\noutput:\n%s", run.out)
	}
}

// Exiting 0 is only defensible if the warning survives. Swallowing it would
// trade a wrong failure for a silent one: every step recorded anonymously, and
// nobody told while it still costs one command to fix.
func TestInstallStillWarnsAboutAnonymousRecording(t *testing.T) {
	run := runInstallerVerification(t, machine{
		name:           "wired, no git identity",
		claudeSettings: hooksWired,
	})

	if !strings.Contains(run.out, "anonymous") {
		t.Errorf("install said nothing about steps being recorded anonymously.\noutput:\n%s", run.out)
	}
	if !strings.Contains(run.out, "git config") {
		t.Errorf("install warned but did not say how to fix it.\noutput:\n%s", run.out)
	}
}

// The half that keeps the previous two honest. An install that wired no hooks
// captures nothing, every other rgt command exits 0 in that state, and the
// person who pasted the command is not the person who would notice the silence.
// That must still fail, and must still name the agent to wire.
func TestInstallStillFailsWhenNoHooksAreWired(t *testing.T) {
	run := runInstallerVerification(t, machine{
		name:           "nothing wired",
		claudeSettings: nothingWired,
	})

	if run.exitedZero {
		t.Errorf("install exited 0 having wired no hooks; nothing will ever be captured here.\noutput:\n%s", run.out)
	}
	if !strings.Contains(run.out, "claude") {
		t.Errorf("install failed without naming the agent that needs wiring.\noutput:\n%s", run.out)
	}
}

// #27, first failure, end to end. The paste runs inside a project whose agent
// is opened one directory up, and that directory has a .claude/ with no re_gent
// hook: a session started there loads those settings, finds nothing of ours,
// and captures nothing anywhere. The install must fail — and the remedy it
// prints must lead with opening the agent inside the project.
//
// The old remedy led with `cd <ancestor> && rgt init --agent claude`. A real
// user ran it. It works, in the sense that it wires something; it also makes
// every project under that directory record into one blended history there,
// which is how the same user reached the second failure below.
func TestInstallShadowedByAnUnwiredWorkspaceLeadsWithOpeningTheAgentHere(t *testing.T) {
	run := runInstallerVerification(t, machine{
		name:                    "project wired, agent opened one directory up, that directory unwired",
		claudeSettings:          hooksWired,
		workspaceClaudeSettings: nothingWired,
	})

	// The install must NOT unwind here. Whether the ancestor matters depends on
	// where the user opens their agent, which nothing in the install can see;
	// this project is wired and its binary resolves. Asserting failure was
	// asserting a fact we do not have — the repo owner, who opens his agent
	// inside his projects, got "Setup ran, but verification failed. Nothing
	// will be captured until those problems are fixed." over a project that
	// captures perfectly. What the install owes him is the warning and the one
	// action left, not a verdict.
	if !run.exitedZero {
		t.Errorf("install exited non-zero over a project that is wired correctly, because of where an agent might be opened.\noutput:\n%s", run.out)
	}
	openHere := strings.Index(run.out, "open the agent inside this project")
	if openHere < 0 {
		t.Fatalf("the install never tells the user to open the agent in the project.\noutput:\n%s", run.out)
	}
	if wireAncestor := strings.Index(run.out, "rgt init --agent claude"); wireAncestor >= 0 && wireAncestor < openHere {
		t.Errorf("the install still leads with wiring the directory above, the advice that produced the blended history in #27.\noutput:\n%s", run.out)
	}
}

// #27, second failure, end to end, and the one that mattered most: the user had
// already followed the old advice, so BOTH directories are wired. Doctor
// reported four green ticks over a project whose .regent/ had been empty ever
// since, because the ancestor's session was recording into the ancestor's.
//
// The install must not report this project healthy, must say where the work is
// actually going, and must not abort — the steps are being recorded, and the
// only remaining move is one the installer cannot make on the user's behalf.
func TestInstallDoesNotReportHealthyWhenAWiredWorkspaceCapturesInstead(t *testing.T) {
	run := runInstallerVerification(t, machine{
		name:                    "project wired, agent opened one directory up, that directory wired too",
		claudeSettings:          hooksWired,
		workspaceClaudeSettings: hooksWired,
	})

	// The ancestor's .regent/, not the ancestor. The project path has the
	// ancestor path as a prefix, so a Contains check on the bare ancestor is
	// satisfied by any line that mentions the project — which doctor prints
	// several of. Found by mutation: the looser assertion survived a revert to
	// the original bug.
	recordedIn := filepath.Join(run.workspace, ".regent")
	if !strings.Contains(run.out, recordedIn) {
		t.Errorf("the install never names %s, the directory this project's work is actually recorded in.\noutput:\n%s", recordedIn, run.out)
	}
	if !strings.Contains(run.out, "open the agent inside this project") {
		t.Errorf("the install reports the situation without saying how to get out of it.\noutput:\n%s", run.out)
	}
	if !run.exitedZero {
		t.Errorf("install aborted on a machine that is capturing; the remaining move — opening the agent in the project — is not one the installer can make.\noutput:\n%s", run.out)
	}
	// The failure this whole epic exists to remove: a green tick over a project
	// recording nothing into itself. Doctor prints ✓ before a healthy check's
	// name, so the project's own settings path must not carry one.
	if strings.Contains(run.out, "✓ claude hooks") {
		t.Errorf("doctor ticked claude hooks green while every step lands in %s.\noutput:\n%s", run.workspace, run.out)
	}
}

// The last line of the paste has to leave the user with something to do, not a
// summary of what happened. Wiring the project is everything the installer can
// do; loading the hooks is the agent's job, and it only does it for a session
// started in this directory — which is the fact behind both halves of #27.
func TestInstallEndsBySayingTheOneThingLeftToDo(t *testing.T) {
	run := runInstallerVerification(t, machine{
		name:           "wired, nothing shadowing it",
		claudeSettings: hooksWired,
	})

	if !run.exitedZero {
		t.Fatalf("install failed on a healthy machine, so there is no closing advice to check.\noutput:\n%s", run.out)
	}
	// Anchored, and everything below is asserted against the tail rather than
	// the whole transcript. doctor already prints this project's path several
	// times on its way past, so an unanchored search for the directory name
	// would pass over an installer that says nothing at the end at all.
	const anchor = "One thing left"
	idx := strings.Index(run.out, anchor)
	if idx < 0 {
		t.Fatalf("the install ends with a report of what it did and no statement of what is left to do.\noutput:\n%s", run.out)
	}
	closing := run.out[idx:]

	// Named by its directory, so it is a command to run rather than a principle
	// to apply. The base name is enough: /var and /private/var are the same
	// directory on macOS and the script prints whichever pwd resolves.
	if !strings.Contains(closing, filepath.Base(run.project)) {
		t.Errorf("the closing advice never names the directory to open the agent in.\nclosing:\n%s", closing)
	}
	if !strings.Contains(closing, "restart") {
		t.Errorf("the closing advice never mentions restarting a session already open; agents read hooks at startup, so an open one captures nothing until it is.\nclosing:\n%s", closing)
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
