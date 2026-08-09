package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// press feeds one key to the model, the way a keyboard would.
func press(m tea.Model, k tea.KeyMsg) tea.Model {
	next, _ := m.Update(k)
	return next
}

func key(r string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)} }
func down() tea.KeyMsg        { return tea.KeyMsg{Type: tea.KeyDown} }
func enter() tea.KeyMsg       { return tea.KeyMsg{Type: tea.KeyEnter} }
func right() tea.KeyMsg       { return tea.KeyMsg{Type: tea.KeyRight} }

// TestPickerSelectsWithArrowsAndSpace is the interaction the picker exists for:
// move with arrows, tick with space, confirm with enter.
func TestPickerSelectsWithArrowsAndSpace(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")
	mkProject(t, root, "beta")

	var m tea.Model = newPickerModel(root)
	m = press(m, down())   // onto alpha (entry 0 is "..")
	m = press(m, key(" ")) // tick it
	m = press(m, enter())  // connect

	got := m.(pickerModel).picked()
	want := filepath.Join(root, "alpha")
	if len(got) != 1 || got[0] != want {
		t.Errorf("picked = %v, want [%s]", got, want)
	}
}

// TestPickerQuitSelectsNothing: q must wire nothing even after ticking.
func TestPickerQuitSelectsNothing(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")

	var m tea.Model = newPickerModel(root)
	m = press(m, down())
	m = press(m, key(" "))
	m = press(m, key("q"))

	if got := m.(pickerModel).picked(); len(got) != 0 {
		t.Errorf("quitting must select nothing, got %v", got)
	}
}

// TestPickerBrowsesIntoFolders: → opens a plain folder, so projects nested under
// an org directory are reachable without leaving the picker.
func TestPickerBrowsesIntoFolders(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "acme/web")

	var m tea.Model = newPickerModel(root)
	m = press(m, down())   // onto "acme/"
	m = press(m, right())  // open it
	m = press(m, down())   // onto "web"
	m = press(m, key(" ")) // tick
	m = press(m, enter())

	got := m.(pickerModel).picked()
	want := filepath.Join(root, "acme", "web")
	if len(got) != 1 || got[0] != want {
		t.Errorf("picked = %v, want [%s]", got, want)
	}
}

// TestPickerMarksAlreadyConnected: a project that already points at a server is
// labelled, so nobody wires it a second time wondering why nothing changed.
func TestPickerMarksAlreadyConnected(t *testing.T) {
	root := t.TempDir()
	wired := mkProject(t, root, "alpha")
	writeFile(t, wired, ".regent/config.toml", "[remote]\nurl = 'http://example.test'\n")
	mkProject(t, root, "beta")

	m := newPickerModel(root)
	for _, e := range m.entries {
		if e.label == "alpha" && !e.wired {
			t.Error("alpha is connected and should be marked as such")
		}
		if e.label == "beta" && e.wired {
			t.Error("beta is not connected and must not be marked")
		}
	}
	if !strings.Contains(m.View(), "already connected") {
		t.Errorf("view should say a project is already connected:\n%s", m.View())
	}
}

// TestPickerRefusesEnterWithNothingSelected: confirming an empty selection
// would connect nothing and look exactly like a dead key, so enter must decline
// and say what to press instead.
func TestPickerRefusesEnterWithNothingSelected(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")

	var m tea.Model = newPickerModel(root)
	m = press(m, down())  // onto alpha, nothing ticked
	m = press(m, enter()) // must not confirm

	pm := m.(pickerModel)
	if pm.done {
		t.Error("enter must not confirm an empty selection")
	}
	if len(pm.picked()) != 0 {
		t.Errorf("nothing should be picked, got %v", pm.picked())
	}
	if pm.hint == "" || !strings.Contains(pm.View(), "space") {
		t.Errorf("the picker should say how to select; view:\n%s", pm.View())
	}

	// Ticking then confirming still works.
	m = press(m, key(" "))
	m = press(m, enter())
	if got := m.(pickerModel).picked(); len(got) != 1 {
		t.Errorf("after selecting, enter should confirm; picked %v", got)
	}
}

// TestPickerNothingOption: leaving without connecting is an explicit choice in
// the list, not a key you have to already know.
func TestPickerNothingOption(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha")

	m := newPickerModel(root)
	last := m.entries[len(m.entries)-1]
	if !last.isNone {
		t.Fatalf("last entry should be the opt-out, got %+v", last)
	}
	if !strings.Contains(m.View(), "Nothing") {
		t.Errorf("the opt-out should be visible; view:\n%s", m.View())
	}

	// Select a project first, then choose Nothing: it must still wire nothing.
	var tm tea.Model = m
	tm = press(tm, down())
	tm = press(tm, key(" "))
	for i := 0; i < len(m.entries); i++ {
		tm = press(tm, down()) // walk to the last row
	}
	tm = press(tm, enter())

	pm := tm.(pickerModel)
	if !pm.aborted {
		t.Error("choosing Nothing should leave without connecting")
	}
	if got := pm.picked(); len(got) != 0 {
		t.Errorf("Nothing must override earlier ticks, got %v", got)
	}
}

// TestReadAnswerAcceptsCarriageReturn is the fix for a prompt that appeared to
// ignore input: a full-screen UI can return the terminal with ICRNL cleared, so
// Enter arrives as \r. Waiting only for \n blocked forever while the keystroke
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

// TestOfferShareDeclinesOnCarriageReturn drives the actual prompt the way the
// terminal delivers it after the picker exits.
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
