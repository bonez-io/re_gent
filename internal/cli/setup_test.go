package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

// TestPickerSelectsProjects drives the model the way a keyboard would: move to a
// project, select it, confirm.
func TestPickerSelectsProjects(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")
	mkProject(t, root, "beta")
	if err := os.MkdirAll(filepath.Join(root, "just-a-folder"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m := newPickerModel(root)

	// Entries: ".." then the two projects, then the plain folder.
	if len(m.entries) != 4 {
		t.Fatalf("want 4 entries (up, 2 projects, 1 folder), got %d: %+v", len(m.entries), m.entries)
	}
	if !m.entries[1].isProject || m.entries[1].label != "alpha" {
		t.Errorf("projects should be listed first; entry[1] = %+v", m.entries[1])
	}
	if m.entries[3].isProject {
		t.Errorf("a plain folder must not be selectable; entry[3] = %+v", m.entries[3])
	}

	press := func(model tea.Model, key string) tea.Model {
		next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		return next
	}
	// Down to "alpha", select it, then continue.
	down, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	sel := press(down, " ")
	final := press(sel, "c")

	got := final.(pickerModel).picked()
	want := filepath.Join(root, "alpha")
	if len(got) != 1 || got[0] != want {
		t.Errorf("picked = %v, want [%s]", got, want)
	}
}

// TestPickerQuitSelectsNothing: aborting must wire nothing, even if boxes were
// ticked first.
func TestPickerQuitSelectsNothing(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")

	m := newPickerModel(root)
	down, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	sel, _ := down.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	quit, _ := sel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})

	if got := quit.(pickerModel).picked(); len(got) != 0 {
		t.Errorf("quitting must select nothing, got %v", got)
	}
}
