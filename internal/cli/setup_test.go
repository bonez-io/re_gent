package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitRepo makes a real repository with one commit, so HEAD exists.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "tester@example.com")
	run("config", "user.name", "Tester")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestCommitWiringLeavesOtherStagedWorkAlone is the safety property behind
// committing on someone's behalf: a plain `git add && git commit` would sweep up
// whatever they had already staged into a commit labelled "Wire re_gent".
func TestCommitWiringLeavesOtherStagedWorkAlone(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, ".regent/config.toml", "[remote]\nurl = 'http://example.test'\n")
	writeFile(t, dir, ".claude/settings.json", "{\"hooks\":{}}\n")

	// Unrelated work the user staged before running setup.
	writeFile(t, dir, "unrelated.txt", "my in-progress work\n")
	if out, err := exec.Command("git", "-C", dir, "add", "unrelated.txt").CombinedOutput(); err != nil {
		t.Fatalf("stage unrelated: %v\n%s", err, out)
	}

	if err := commitWiring(dir); err != nil {
		t.Fatalf("commitWiring: %v", err)
	}

	committed, err := exec.Command("git", "-C", dir, "show", "--name-only", "--format=", "HEAD").Output()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	files := string(committed)
	for _, want := range sharedFiles {
		if !strings.Contains(files, want) {
			t.Errorf("commit should contain %s; got:\n%s", want, files)
		}
	}
	if strings.Contains(files, "unrelated.txt") {
		t.Errorf("commit must NOT contain the user's unrelated staged work; got:\n%s", files)
	}

	// And it must still be staged, exactly as they left it.
	staged, err := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if !strings.Contains(string(staged), "unrelated.txt") {
		t.Errorf("unrelated work should remain staged; staged files:\n%s", staged)
	}
}

// TestCommitWiringReportsNothingToCommit covers the re-run case: setup is
// idempotent, so a second share attempt must report rather than fail oddly.
func TestCommitWiringReportsNothingToCommit(t *testing.T) {
	dir := gitRepo(t)
	if err := commitWiring(dir); err == nil {
		t.Error("want an error when there is no wiring to commit")
	}
}

// TestIsConnectedRequiresBothAServerAndAnIdentity is what "connected" means,
// and it is the assertion that survived the picker.
//
// It was one clause of a test about how the picker labelled its rows, but the
// fact it pins has nothing to do with a UI: `rgt init` writes
// .regent/config.toml unconditionally, so treating that file's existence as a
// binding made every locally-used project look connected. Connecting one then
// took the disconnect branch and removed its hooks while reporting success. A
// binding is a server address AND a project identity; half of one is a
// half-written file.
func TestIsConnectedRequiresBothAServerAndAnIdentity(t *testing.T) {
	root := t.TempDir()

	whole := mkProject(t, root, "alpha")
	writeFile(t, whole, ".regent/config.toml",
		"[remote]\nurl = 'http://example.test'\nrepo_id = 'alpha'\n")
	if !isConnected(whole) {
		t.Error("a config with both a server URL and a repo_id is a connection")
	}

	// The shape `rgt init` leaves behind: a config file, no binding.
	half := mkProject(t, root, "gamma")
	writeFile(t, half, ".regent/config.toml", "[remote]\nurl = 'http://example.test'\n")
	if isConnected(half) {
		t.Error("a config with a server URL but no repo_id was read as connected")
	}

	if isConnected(mkProject(t, root, "beta")) {
		t.Error("a project with no .regent/config.toml at all is not connected")
	}
}

// TestReadAnswerAcceptsCarriageReturn is the fix for a prompt that appeared to
// ignore input: Enter can arrive as \r rather than \n — from a Windows
// terminal, or from one an agent host or full-screen program handed back with
// ICRNL cleared. Waiting only for \n blocked forever while the keystroke
// echoed, which looked exactly like a dead prompt.
func TestReadAnswerAcceptsCarriageReturn(t *testing.T) {
	cases := map[string]string{
		"n\r":     "n",
		"n\n":     "n",
		"yes\r\n": "yes",
		"\r":      "",
	}
	for in, want := range cases {
		got, err := readAnswer(strings.NewReader(in))
		if err != nil {
			t.Errorf("readAnswer(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("readAnswer(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOfferShareDeclinesOnCarriageReturn drives the actual prompt with the line
// ending a terminal can deliver instead of \n.
func TestOfferShareDeclinesOnCarriageReturn(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, ".regent/config.toml", "[remote]\nurl = 'http://example.test'\n")

	done := make(chan struct{})
	go func() {
		offerShare([]string{dir}, strings.NewReader("n\r"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("offerShare blocked on a carriage-return answer")
	}

	out, err := exec.Command("git", "-C", dir, "log", "--oneline", "-1").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.Contains(string(out), "Wire re_gent") {
		t.Error("declining must not commit")
	}
}

// TestServerIsRemembered is what makes bare `rgt` work: the URL is stored after
// setup, so later runs need no argument.
func TestServerIsRemembered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := resolveServerURL(""); err == nil {
		t.Error("with no server known yet, resolving must fail with guidance")
	}

	rememberServer("http://team.example:7654")

	got, err := resolveServerURL("")
	if err != nil {
		t.Fatalf("after remembering, resolve should succeed: %v", err)
	}
	if got != "http://team.example:7654" {
		t.Errorf("resolved %q, want the remembered server", got)
	}

	// An explicit argument still wins, and a trailing slash is normalised.
	if got, _ := resolveServerURL("http://other.example:7654/"); got != "http://other.example:7654" {
		t.Errorf("explicit arg should win and lose its trailing slash, got %q", got)
	}
}

// The two setupTargets tests that stood here moved to discover_test.go when
// setup was folded into connect. "Wires only the current project" became
// TestConnectInsideAProjectWiresOnlyThatProject; "fails outside a project" was
// already pinned by TestConnectWithoutTerminalReportsAndFails.
