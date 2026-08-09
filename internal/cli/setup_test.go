package cli

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// TestTTYPairOutputIsWritable reproduces the bug that made the picker look
// frozen: the installer runs `rgt setup <url> < /dev/tty`, and a shell redirect
// opens the terminal READ-ONLY. Handing that same handle back as the output
// made every draw fail silently. Whatever ttyPair returns for output must
// accept writes.
func TestTTYPairOutputIsWritable(t *testing.T) {
	// Stand in for the installer's read-only stdin.
	ro, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer ro.Close()

	realStdin := os.Stdin
	os.Stdin = ro
	defer func() { os.Stdin = realStdin }()

	in, out, cleanup, err := ttyPair()
	if err != nil {
		t.Skip("no controlling terminal in this environment")
	}
	defer cleanup()

	if out == ro {
		t.Fatal("output must never be the read-only stdin handed to us")
	}
	if _, err := out.Write(nil); err != nil {
		t.Errorf("picker output handle must be writable, got: %v", err)
	}
	if in == nil {
		t.Error("input handle must not be nil")
	}
}

// TestTextPickerSelectsAndConnects drives the picker the way a person types:
// tick a project by number, then "c".
func TestTextPickerSelectsAndConnects(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")
	mkProject(t, root, "beta")

	var out bytes.Buffer
	// Entries: 1) .. 2) alpha 3) beta -> "2" ticks alpha, "c" confirms.
	got := runTextPicker(root, bufio.NewReader(strings.NewReader("2\nc\n")), &out)

	want := filepath.Join(root, "alpha")
	if len(got) != 1 || got[0] != want {
		t.Errorf("picked = %v, want [%s]", got, want)
	}
	if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "beta") {
		t.Errorf("both projects should be listed; got:\n%s", out.String())
	}
}

// TestTextPickerQuitSelectsNothing: q must wire nothing even after ticking.
func TestTextPickerQuitSelectsNothing(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")

	var out bytes.Buffer
	if got := runTextPicker(root, bufio.NewReader(strings.NewReader("2\nq\n")), &out); len(got) != 0 {
		t.Errorf("quitting must select nothing, got %v", got)
	}
}

// TestTextPickerBrowsesIntoFolders: choosing a plain folder navigates into it,
// so projects nested under an org directory are reachable.
func TestTextPickerBrowsesIntoFolders(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "acme/web")

	var out bytes.Buffer
	// 1) .. 2) acme/ -> "2" enters acme, then 1) .. 2) web -> "2" ticks it.
	got := runTextPicker(root, bufio.NewReader(strings.NewReader("2\n2\nc\n")), &out)

	want := filepath.Join(root, "acme", "web")
	if len(got) != 1 || got[0] != want {
		t.Errorf("picked = %v, want [%s]", got, want)
	}
}
