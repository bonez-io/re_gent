package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests pin RFC 0002's contract for the pre-push hook: it is written
// only where Git will read it, it preserves whatever hook was there, it can be
// removed leaving the repository exactly as found, and — above all — its
// Regent block can never fail a push.

// gitInit makes root a Git repository. Tests that need a real .git use it; the
// pure script tests do not.
func gitInit(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// Some CI images set a global core.hooksPath. Pin it unset for this repo so
	// the test exercises the ordinary layout regardless.
	exec.Command("git", "-C", root, "config", "--unset", "core.hooksPath").Run() //nolint:errcheck
}

func TestGitHookIsNotWrittenOutsideAGitRepository(t *testing.T) {
	root := t.TempDir()

	outcome, err := wireGitHook(root)
	if err != nil {
		t.Fatalf("wireGitHook: %v", err)
	}
	if outcome.Path != "" {
		t.Fatalf("hook written at %s in a directory with no .git", outcome.Path)
	}
	if !strings.Contains(outcome.Skipped, "not a Git repository") {
		t.Errorf("Skipped = %q, want a reason naming the missing .git", outcome.Skipped)
	}
}

func TestGitHookIsWrittenIntoDotGitHooks(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)

	outcome, err := wireGitHook(root)
	if err != nil {
		t.Fatalf("wireGitHook: %v", err)
	}
	want := filepath.Join(root, ".git", "hooks", "pre-push")
	if outcome.Path != want {
		t.Fatalf("Path = %s, want %s", outcome.Path, want)
	}
	if outcome.Chained {
		t.Error("Chained = true with no previous hook")
	}

	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("hook not on disk: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("hook is not executable: %v", info.Mode())
	}
	content, _ := os.ReadFile(want)
	if !isRegentGitHook(content) {
		t.Error("written hook does not carry the re_gent marker")
	}
	if !strings.Contains(string(content), gitHookVerb+" "+gitHookName) {
		t.Errorf("hook does not invoke `rgt %s %s`:\n%s", gitHookVerb, gitHookName, content)
	}
}

// A hook that was already there keeps every power it had: it runs first, with
// the same arguments, and its non-zero exit still aborts the push. Ours runs
// only when the push is going to proceed.
func TestGitHookPreservesAndChainsAnExistingHook(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	hookPath := filepath.Join(root, ".git", "hooks", "pre-push")
	userHook := "#!/bin/sh\necho user-hook-ran \"$@\"\nexit 3\n"
	mustWrite(t, hookPath, userHook)
	if err := os.Chmod(hookPath, 0o755); err != nil {
		t.Fatal(err)
	}

	outcome, err := wireGitHook(root)
	if err != nil {
		t.Fatalf("wireGitHook: %v", err)
	}
	if !outcome.Chained {
		t.Fatal("Chained = false, but a previous hook existed")
	}

	prev, err := os.ReadFile(hookPath + gitHookPrevSuffix)
	if err != nil {
		t.Fatalf("previous hook not preserved: %v", err)
	}
	if string(prev) != userHook {
		t.Errorf("previous hook altered:\n got %q\nwant %q", prev, userHook)
	}

	// Run the installed hook the way Git would. The user's hook exits 3, so
	// the wrapper must exit 3 too — and must have run it with our arguments.
	out, code := runHook(t, hookPath, "origin", "https://example.invalid/repo.git")
	if code != 3 {
		t.Errorf("exit = %d, want 3 propagated from the user's hook\n%s", code, out)
	}
	if !strings.Contains(out, "user-hook-ran origin https://example.invalid/repo.git") {
		t.Errorf("user hook did not run first with the push arguments:\n%s", out)
	}
}

// The property everything else depends on. Whatever rgt does — here it is a
// binary that does not exist, and a PATH with no rgt on it — the Regent block
// exits 0 and the push proceeds.
func TestGitHookRegentBlockAlwaysExitsZero(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	hooksDir := filepath.Join(root, ".git", "hooks")

	outcome, err := installGitHook(hooksDir, filepath.Join(root, "no-such-rgt"))
	if err != nil {
		t.Fatalf("installGitHook: %v", err)
	}
	t.Setenv("PATH", t.TempDir()) // nothing on PATH either

	out, code := runHook(t, outcome.Path, "origin", "url")
	if code != 0 {
		t.Errorf("exit = %d with rgt missing entirely; the Regent block must exit 0\n%s", code, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("hook printed with nothing to say:\n%s", out)
	}
}

// Even a Regent binary that fails hard cannot fail the push: the script
// discards its exit code. Modelled with a fake rgt that exits 1.
func TestGitHookDiscardsRegentExitCode(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	fake := filepath.Join(root, "rgt")
	mustWrite(t, fake, "#!/bin/sh\necho fake-rgt \"$@\"\nexit 1\n")
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}

	outcome, err := installGitHook(filepath.Join(root, ".git", "hooks"), fake)
	if err != nil {
		t.Fatalf("installGitHook: %v", err)
	}
	out, code := runHook(t, outcome.Path, "origin", "url")
	if code != 0 {
		t.Errorf("exit = %d, want 0 even though rgt exited 1\n%s", code, out)
	}
	if !strings.Contains(out, "fake-rgt "+gitHookVerb+" "+gitHookName+" origin url") {
		t.Errorf("rgt was not invoked as `%s %s` with Git's arguments:\n%s", gitHookVerb, gitHookName, out)
	}
}

func TestGitHookOptOutEnvSkipsRegentAtRunTime(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	fake := filepath.Join(root, "rgt")
	mustWrite(t, fake, "#!/bin/sh\necho SHOULD-NOT-RUN\nexit 0\n")
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	outcome, err := installGitHook(filepath.Join(root, ".git", "hooks"), fake)
	if err != nil {
		t.Fatalf("installGitHook: %v", err)
	}

	t.Setenv(gitHookOptOutEnv, "0")
	out, code := runHook(t, outcome.Path, "origin", "url")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.Contains(out, "SHOULD-NOT-RUN") {
		t.Errorf("rgt ran despite %s=0:\n%s", gitHookOptOutEnv, out)
	}
}

func TestGitHookOptOutEnvSkipsWiring(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	t.Setenv(gitHookOptOutEnv, "0")

	outcome, err := wireGitHook(root)
	if err != nil {
		t.Fatalf("wireGitHook: %v", err)
	}
	if outcome.Path != "" {
		t.Errorf("hook written at %s despite %s=0", outcome.Path, gitHookOptOutEnv)
	}
	if pathExists(filepath.Join(root, ".git", "hooks", "pre-push")) {
		t.Error("pre-push exists on disk despite opt-out")
	}
}

// Only the literal "0" opts out. "false", "no", "" and typos keep the default,
// which is on: a mistake fails towards syncing.
func TestGitHookOptOutRequiresLiteralZero(t *testing.T) {
	for _, v := range []string{"", "false", "no", "off", "1", " 0 "} {
		lookup := func(string) (string, bool) { return v, true }
		want := strings.TrimSpace(v) == "0"
		if got := optedOutOfGitHook(lookup); got != want {
			t.Errorf("value %q: optedOut = %v, want %v", v, got, want)
		}
	}
	if optedOutOfGitHook(func(string) (string, bool) { return "", false }) {
		t.Error("unset variable opted out")
	}
}

func TestGitHookRewireIsIdempotent(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)

	first, err := wireGitHook(root)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(first.Path)

	second, err := wireGitHook(root)
	if err != nil {
		t.Fatalf("second wire: %v", err)
	}
	after, _ := os.ReadFile(second.Path)
	if !bytes.Equal(before, after) {
		t.Error("re-wiring changed the hook content")
	}
	if pathExists(first.Path + gitHookPrevSuffix) {
		t.Error("re-wiring our own hook created a .pre-regent copy of it")
	}
}

// Two hooks and neither is ours means a state we did not create. Choosing
// which to keep would destroy the other, so refuse and say so.
func TestGitHookRefusesWhenBothPrevAndCurrentAreForeign(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	hooksDir := filepath.Join(root, ".git", "hooks")
	mustWrite(t, filepath.Join(hooksDir, "pre-push"), "#!/bin/sh\necho a\n")
	mustWrite(t, filepath.Join(hooksDir, "pre-push"+gitHookPrevSuffix), "#!/bin/sh\necho b\n")

	_, err := installGitHook(hooksDir, "rgt")
	if err == nil {
		t.Fatal("installed over two foreign hooks; should have refused")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error should say it refused: %v", err)
	}
	// And touched nothing.
	a, _ := os.ReadFile(filepath.Join(hooksDir, "pre-push"))
	if string(a) != "#!/bin/sh\necho a\n" {
		t.Error("existing pre-push was modified")
	}
}

func TestRemoveGitHookRestoresThePreviousHookExactly(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	hookPath := filepath.Join(root, ".git", "hooks", "pre-push")
	userHook := "#!/bin/sh\n# mine\nexit 0\n"
	mustWrite(t, hookPath, userHook)

	if _, err := wireGitHook(root); err != nil {
		t.Fatal(err)
	}
	removed, err := removeGitHook(root)
	if err != nil {
		t.Fatalf("removeGitHook: %v", err)
	}
	if !removed {
		t.Fatal("removed = false after wiring")
	}
	got, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("previous hook not restored: %v", err)
	}
	if string(got) != userHook {
		t.Errorf("restored hook differs:\n got %q\nwant %q", got, userHook)
	}
	if pathExists(hookPath + gitHookPrevSuffix) {
		t.Error(".pre-regent copy left behind after restore")
	}
}

func TestRemoveGitHookLeavesAForeignHookAlone(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	hookPath := filepath.Join(root, ".git", "hooks", "pre-push")
	mustWrite(t, hookPath, "#!/bin/sh\necho not-ours\n")

	removed, err := removeGitHook(root)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("removed a hook that is not ours")
	}
	if !pathExists(hookPath) {
		t.Error("foreign hook deleted")
	}
}

func TestRemoveGitHookWithNothingInstalledIsANoOp(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	removed, err := removeGitHook(root)
	if err != nil || removed {
		t.Errorf("removed=%v err=%v on a repo with no hook", removed, err)
	}
}

// A hooks manager owning core.hooksPath means .git/hooks is dead code. Do not
// write there; say where the line belongs.
func TestGitHookDetectsCoreHooksPath(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	custom := filepath.Join(root, ".husky")
	mustMkdir(t, custom)
	if out, err := exec.Command("git", "-C", root, "config", "core.hooksPath", custom).CombinedOutput(); err != nil {
		t.Fatalf("set core.hooksPath: %v\n%s", err, out)
	}

	outcome, err := wireGitHook(root)
	if err != nil {
		t.Fatalf("wireGitHook: %v", err)
	}
	if outcome.Path != "" {
		t.Errorf("wrote %s while core.hooksPath is set", outcome.Path)
	}
	if !strings.Contains(outcome.Skipped, "core.hooksPath") || !strings.Contains(outcome.Skipped, gitHookVerb) {
		t.Errorf("Skipped should name core.hooksPath and the rgt command to add: %q", outcome.Skipped)
	}
	if pathExists(filepath.Join(root, ".git", "hooks", "pre-push")) {
		t.Error("pre-push written into .git/hooks despite core.hooksPath")
	}
}

// Worktrees: .git is a file pointing at a gitdir whose commondir names the
// shared repository. Hooks live in the shared one; writing into the worktree's
// private gitdir would install a hook Git never runs.
func TestGitHookResolvesWorktreeCommonDir(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	// Need a commit before a worktree can be added.
	for _, args := range [][]string{
		{"-C", root, "config", "user.email", "t@example.com"},
		{"-C", root, "config", "user.name", "t"},
		{"-C", root, "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}

	outcome, err := wireGitHook(wt)
	if err != nil {
		t.Fatalf("wireGitHook in worktree: %v", err)
	}
	want := filepath.Join(root, ".git", "hooks", "pre-push")
	got, _ := filepath.EvalSymlinks(outcome.Path)
	wantResolved, _ := filepath.EvalSymlinks(filepath.Dir(want))
	if filepath.Dir(got) != wantResolved {
		t.Errorf("hook written to %s, want the shared %s", outcome.Path, want)
	}
}

// The script embeds an absolute binary path when it has one and falls back to
// PATH, exactly as sharedHookCommand does for the committed Claude settings.
func TestGitHookScriptEmbedsAbsoluteBinaryWithPathFallback(t *testing.T) {
	s := gitHookScript("/opt/x y'z/rgt")
	if !strings.Contains(s, `'/opt/x y'\''z/rgt'`) {
		t.Errorf("absolute path not shell-quoted:\n%s", s)
	}
	if !strings.Contains(s, "command -v rgt") {
		t.Errorf("no PATH fallback:\n%s", s)
	}
	if !strings.HasSuffix(strings.TrimSpace(s), "# <<< re_gent pre-push <<<") {
		t.Errorf("script must end with the closing marker:\n%s", s)
	}
	if !strings.Contains(s, "\nexit 0\n") {
		t.Errorf("script must exit 0 unconditionally:\n%s", s)
	}
	if !strings.Contains(s, "</dev/null") {
		t.Errorf("rgt must not read Git's refs from stdin (RFC 0002 prohibition 4):\n%s", s)
	}
}

func TestGitHookScriptWithBareBinaryUsesPathOnly(t *testing.T) {
	s := gitHookScript("rgt")
	if strings.Contains(s, "[ -x '") {
		t.Errorf("bare binary should not produce an -x path test:\n%s", s)
	}
	if !strings.Contains(s, "command -v rgt") {
		t.Errorf("bare binary must use PATH:\n%s", s)
	}
}

// --- runtime half: rgt git-hook pre-push ---

// In local mode there is nothing to deliver to. The command says nothing and
// touches nothing; the whole point of wiring the hook early is that this is
// safe.
func TestGitPrePushHookIsInertInLocalMode(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)
	// No server config anywhere: env explicitly empty, no repo config.
	env := func(k string) (string, bool) {
		if k == "REGENT_SERVER_URL" {
			return "", true
		}
		return "", false
	}
	var out bytes.Buffer
	runGitPrePushHook(&out, env, time.Now)
	if out.Len() != 0 {
		t.Errorf("printed in local mode:\n%s", out.String())
	}
}

func TestGitPrePushHookHonoursOptOutEnv(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)
	env := func(k string) (string, bool) {
		switch k {
		case gitHookOptOutEnv:
			return "0", true
		case "REGENT_SERVER_URL":
			return "http://127.0.0.1:1", true // would fail if reached
		case "REGENT_REPO_ID":
			return "x", true
		}
		return "", false
	}
	var out bytes.Buffer
	runGitPrePushHook(&out, env, time.Now)
	if out.Len() != 0 {
		t.Errorf("printed despite opt-out:\n%s", out.String())
	}
}

// A never-panics contract, pinned: a nil-dereferencing clock is about as
// broken as an input gets, and the hook must still return normally.
func TestGitPrePushHookRecoversFromPanic(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)
	env := func(k string) (string, bool) {
		switch k {
		case "REGENT_SERVER_URL":
			return "http://127.0.0.1:1", true
		case "REGENT_REPO_ID":
			return "x", true
		case "REGENT_CACHE_DIR":
			return filepath.Join(root, "cache"), true
		}
		return "", false
	}
	var out bytes.Buffer
	var nilClock func() time.Time
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped runGitPrePushHook: %v", r)
		}
	}()
	runGitPrePushHook(&out, env, nilClock)
	// Either it returned before touching the clock (empty cache → nothing
	// owed) or it recovered; both are acceptable, and neither may panic.
}

// runHook executes an installed hook the way Git would: as a program, with the
// remote name and URL as arguments, stdin closed. Returns combined output and
// the exit code.
func runHook(t *testing.T, path string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("run %s: %v\n%s", path, err, out)
	return "", -1
}

// --- doctor ---

func TestDoctorReportsGitHookWiring(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)

	// Absent: advisory, names the fix.
	f, present := gitHookFinding(root)
	if !present {
		t.Fatal("finding omitted inside a Git repository")
	}
	if f.OK {
		t.Fatal("OK=true with no hook installed")
	}
	if f.Severity != severityWarning {
		t.Errorf("Severity = %v, want warning: sync-on-push is a convenience over capture", f.Severity)
	}
	if !strings.Contains(f.Detail, "rgt connect") {
		t.Errorf("Detail should name the wiring command: %q", f.Detail)
	}

	// Wired: OK, names the file.
	outcome, err := wireGitHook(root)
	if err != nil {
		t.Fatal(err)
	}
	f, _ = gitHookFinding(root)
	if !f.OK {
		t.Errorf("OK=false after wiring: %s", f.Detail)
	}
	if f.Detail != outcome.Path {
		t.Errorf("Detail = %q, want the hook path %q", f.Detail, outcome.Path)
	}
}

// Outside a Git repository there is no push to sync on, so there is nothing to
// say. Same rule as agents: doctor does not report on what is not here. This is
// also what keeps the installer's own doctor run green in a plain directory.
func TestDoctorGitHookFindingIsOmittedOutsideAGitRepo(t *testing.T) {
	if f, present := gitHookFinding(t.TempDir()); present {
		t.Fatalf("finding emitted outside a Git repository: %+v", f)
	}
}

func TestDoctorGitHookFindingReportsAForeignHook(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	mustWrite(t, filepath.Join(root, ".git", "hooks", "pre-push"), "#!/bin/sh\nexit 0\n")

	f, _ := gitHookFinding(root)
	if f.OK {
		t.Fatal("OK=true over a hook that is not ours")
	}
	if !strings.Contains(f.Detail, "not re_gent's") {
		t.Errorf("Detail should say the hook is foreign: %q", f.Detail)
	}
}

func TestDoctorGitHookFindingReportsChainedHook(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	mustWrite(t, filepath.Join(root, ".git", "hooks", "pre-push"), "#!/bin/sh\nexit 0\n")
	if _, err := wireGitHook(root); err != nil {
		t.Fatal(err)
	}
	f, _ := gitHookFinding(root)
	if !f.OK || !strings.Contains(f.Detail, "chains") {
		t.Errorf("finding should be OK and mention the chained hook: OK=%v %q", f.OK, f.Detail)
	}
}

func TestDoctorGitHookFindingHonoursOptOut(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	t.Setenv(gitHookOptOutEnv, "0")
	f, _ := gitHookFinding(root)
	if !f.OK || !strings.Contains(f.Detail, gitHookOptOutEnv) {
		t.Errorf("opt-out should be OK and say why: OK=%v %q", f.OK, f.Detail)
	}
}
