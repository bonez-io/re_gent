package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/remote"
)

// Sharing the wiring through version control is two commands making one promise:
// `rgt init` writes the hook config, `rgt connect` tells the user to commit it so
// teammates are wired by cloning. These tests are the promise checked from the
// teammate's side — a second directory standing in for a second machine, where
// the installing machine's filesystem does not exist.

// The bug (#23): `rgt init` embeds the absolute path of the running binary into
// .claude/settings.json, because hooks run inside the agent host's environment
// where PATH may not contain rgt. That is right for the machine that ran the
// install and wrong for everybody who clones the file: the path exists on
// exactly one machine, the hook never runs, and Claude Code says nothing about a
// hook that failed to start. The silent failure the absolute path was introduced
// to prevent, moved one person to the left.
//
// Two directories, because one cannot show it. On the installing machine the
// embedded path resolves, so every single-directory test passes either way.
func TestACommittedHookRunsOnATeammatesMachineToo(t *testing.T) {
	installer := t.TempDir()
	installerBinary := fakeRgt(t, filepath.Join(installer, "tools", "rgt"))

	if _, err := installClaudeHookWith(installer, installerBinary); err != nil {
		t.Fatalf("installClaudeHookWith: %v", err)
	}

	// The machine that ran the install must still not depend on PATH: that is
	// what the absolute path is for, and it is not up for trade.
	withPath(t, t.TempDir())
	for _, command := range claudeHookCommands(t, installer) {
		ran, err := runHookCommand(command)
		if err != nil {
			t.Fatalf("the installing machine's hook %q did not run: %v", command, err)
		}
		if ran != installerBinary {
			t.Errorf("hook %q ran %q, want the binary that installed it (%q)", command, ran, installerBinary)
		}
	}

	// Now the teammate. They clone the settings file and nothing else — the
	// installer's binary lives on the installer's machine — and they have rgt
	// installed somewhere else entirely, on PATH.
	teammate := t.TempDir()
	mustMkdir(t, filepath.Join(teammate, ".claude"))
	copyTestFile(t, filepath.Join(installer, ".claude", "settings.json"),
		filepath.Join(teammate, ".claude", "settings.json"))
	teammateBinary := fakeRgt(t, filepath.Join(teammate, "opt", "bin", "rgt"))
	withPath(t, filepath.Dir(teammateBinary))
	if err := os.RemoveAll(filepath.Dir(installerBinary)); err != nil {
		t.Fatalf("remove the installer's binary: %v", err)
	}

	commands := claudeHookCommands(t, teammate)
	if len(commands) == 0 {
		t.Fatal("the cloned settings file has no re_gent hook in it at all")
	}
	for _, command := range commands {
		ran, err := runHookCommand(command)
		if err != nil {
			t.Fatalf("the teammate's cloned hook %q did not run, so nothing is captured for them: %v", command, err)
		}
		if ran != teammateBinary {
			t.Errorf("cloned hook %q ran %q, want the teammate's own rgt (%q)", command, ran, teammateBinary)
		}
	}
}

// Re-running init must not stack a second copy of the hook, and dedupe is done
// by recognising our own command. The shared form is a shell expression rather
// than a bare invocation, so this is the guard that recognising it did not
// quietly stop working — the failure would be duplicate hooks, meaning every
// turn captured twice.
func TestTheSharedHookStaysRecognisableAsOurs(t *testing.T) {
	for _, binary := range []string{"/usr/local/bin/rgt", "/Users/someone/.local/bin/rgt-dev", "/opt/regent/bin/regent"} {
		for _, args := range []string{claudeUserHookArgs, claudeAssistantHookArgs, claudeToolBatchHookArgs} {
			command := sharedHookCommand(binary, args)
			if !isRegentHookCommand(command) {
				t.Errorf("%q is not recognised as re_gent's own hook; re-init would install a second copy", command)
			}
		}
	}

	root := t.TempDir()
	binary := fakeRgt(t, filepath.Join(root, "tools", "rgt"))
	for i := 0; i < 2; i++ {
		if _, err := installClaudeHookWith(root, binary); err != nil {
			t.Fatalf("installClaudeHookWith (pass %d): %v", i+1, err)
		}
	}
	if commands := claudeHookCommands(t, root); len(commands) != 3 {
		t.Errorf("wiring twice left %d re_gent hooks, want 3 (one per event): %v", len(commands), commands)
	}
}

// The hook a teammate clones names a path from somebody else's machine, and an
// agent host that cannot launch a hook says nothing about it. Doctor is the only
// thing that can, and it used to be satisfied by the settings file merely being
// present and mentioning re_gent — the same "reports something other than what
// is true" failure this area keeps producing.
func TestDoctorSaysSoWhenTheHookNamesABinaryThisMachineDoesNotHave(t *testing.T) {
	installer := t.TempDir()
	installerBinary := fakeRgt(t, filepath.Join(installer, "tools", "rgt"))
	if _, err := installClaudeHookWith(installer, installerBinary); err != nil {
		t.Fatalf("installClaudeHookWith: %v", err)
	}

	teammate := t.TempDir()
	mustMkdir(t, filepath.Join(teammate, ".regent"))
	copyTestFile(t, filepath.Join(installer, ".claude", "settings.json"),
		filepath.Join(teammate, ".claude", "settings.json"))
	if err := os.RemoveAll(filepath.Dir(installerBinary)); err != nil {
		t.Fatalf("remove the installer's binary: %v", err)
	}

	// A teammate with no rgt anywhere: capture cannot happen and doctor has to
	// be the one to say why.
	withPath(t, t.TempDir())
	finding := findFinding(t, diagnose(teammate), "hook binary")
	if finding.OK {
		t.Fatalf("doctor calls this machine healthy, but its hook runs a binary that is not here: %s", finding.Detail)
	}
	if finding.Severity != severityFailure {
		t.Error("a hook that cannot launch is not a degraded install, it is no capture at all; it must fail, not warn")
	}
	if !strings.Contains(finding.Detail, installerBinary) {
		t.Errorf("detail does not name the path that is missing, so the reader cannot act on it: %s", finding.Detail)
	}

	// The same teammate once rgt is installed: the fallback covers them and
	// doctor must not invent a problem.
	withPath(t, filepath.Dir(fakeRgt(t, filepath.Join(teammate, "opt", "bin", "rgt"))))
	if finding := findFinding(t, diagnose(teammate), "hook binary"); !finding.OK {
		t.Errorf("doctor reports a problem for a teammate whose rgt is on PATH and whose hook falls back to it: %s", finding.Detail)
	}
}

// The bug (#12): the file recording which server a project belongs to sits
// inside .regent/, and .regent/ is the directory every project excludes from
// version control. The nested .regent/.gitignore that re-includes config.toml
// cannot rescue it — git does not descend into an excluded directory to read
// it — so a clone had no idea the project was ever connected and every history
// command reported nothing.
//
// Two repositories and a shared origin, because the claim is about what a
// teammate gets, and no single checkout can show that.
func TestACloneOfAConnectedProjectKnowsItsServer(t *testing.T) {
	const serverURL = "https://regent.example.test"
	// A remote both checkouts agree on, which is where the project's identity
	// comes from — see deriveRepoID.
	const originURL = "https://github.com/acme/widgets.git"

	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare")
	// Name the branch a clone will check out, rather than inheriting whatever
	// init.defaultBranch this machine is configured with.
	git(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	alice := t.TempDir()
	newGitRepo(t, alice)
	git(t, alice, "remote", "add", "origin", origin)

	// The wiring, exactly as connect leaves it: the binding, the hook config,
	// the nested ignore file — and a project whose root .gitignore excludes
	// .regent/, which is every project that ever ran `rgt init`.
	writeFile(t, alice, ".gitignore", "node_modules/\n.regent/\n")
	mustMkdir(t, filepath.Join(alice, ".regent"))
	if err := createRegentGitignore(alice); err != nil {
		t.Fatalf("createRegentGitignore: %v", err)
	}
	repoID := deriveRepoIDFor(t, alice, originURL)
	// Named literally, not recomputed. The property that makes a clone safe is
	// that identity comes from the remote both checkouts share — a fallback that
	// happens to agree (the root commit, the folder name) would satisfy an
	// equality between the two and prove nothing about which fact was used.
	if want := "github.com-acme-widgets"; repoID != want {
		t.Fatalf("identity derived as %q, want %q from the shared remote", repoID, want)
	}
	writeFile(t, alice, ".regent/config.toml",
		"[remote]\nurl = '"+serverURL+"'\nrepo_id = '"+repoID+"'\n")
	if _, err := installClaudeHookWith(alice, fakeRgt(t, filepath.Join(alice, "tools", "rgt"))); err != nil {
		t.Fatalf("installClaudeHookWith: %v", err)
	}

	if err := commitWiring(alice); err != nil {
		t.Fatalf("commitWiring: %v", err)
	}
	git(t, alice, "add", "--", ".gitignore")
	if out, _ := exec.Command("git", "-C", alice, "diff", "--cached", "--name-only").Output(); len(strings.TrimSpace(string(out))) > 0 {
		git(t, alice, "commit", "-qm", "rest")
	}
	git(t, alice, "push", "-q", "origin", "HEAD:refs/heads/main")

	// The teammate. Nothing is told to them: they clone and that is all.
	bob := filepath.Join(t.TempDir(), "widgets")
	git(t, t.TempDir(), "clone", "-q", origin, bob)

	if _, err := os.Stat(filepath.Join(bob, ".regent", "config.toml")); err != nil {
		t.Fatalf("the clone has no binding: %v", err)
	}
	// The exclusion itself, asked about independently of the commit that got
	// past it: the binding must be a path git will look at, and everything else
	// under .regent/ must stay ignored.
	if gitIgnores(t, bob, ".regent/config.toml") {
		t.Error(".regent/config.toml is still excluded in the clone; sharing the binding depends on remembering -f every time")
	}
	if !gitIgnores(t, bob, ".regent/index.db") {
		t.Error("narrowing the exclusion also stopped ignoring machine-local state; the index would be committed")
	}

	// The clone resolves the same server through the same code the hooks use.
	t.Setenv("HOME", t.TempDir()) // no per-user config to borrow an answer from
	cfg, err := remote.LoadConfigForCWD(func(string) (string, bool) { return "", false }, bob)
	if err != nil {
		t.Fatalf("LoadConfigForCWD in the clone: %v", err)
	}
	if cfg.ServerURL != serverURL || cfg.RepoID != repoID {
		t.Errorf("clone resolved %q/%q, want %q/%q — it does not know where its history lives",
			cfg.ServerURL, cfg.RepoID, serverURL, repoID)
	}

	// And the identity a clone would derive for itself agrees with the binding
	// it inherited, so the two can never name different projects on the server.
	if got := deriveRepoIDFor(t, bob, originURL); got != repoID {
		t.Errorf("the clone derives identity %q but inherited %q; the teammate would write into a different project's history", got, repoID)
	}
}

// deriveRepoIDFor derives a project's identity as if it were checked out from
// remoteURL, which is what a teammate's checkout actually has. The tests clone
// over a filesystem path because there is no server to clone from.
func deriveRepoIDFor(t *testing.T, dir, remoteURL string) string {
	t.Helper()
	previous := gitRemoteURL(dir)
	git(t, dir, "config", "remote.origin.url", remoteURL)
	id := deriveRepoID(dir)
	if previous != "" {
		git(t, dir, "config", "remote.origin.url", previous)
	}
	return id
}

// Narrowing rewrites somebody else's file, so what it refuses to touch matters
// as much as what it changes. A project that excluded something more specific
// than the whole directory said something more specific, and quietly replacing
// that would be a worse failure than the binding staying unshared.
func TestNarrowingLeavesMoreSpecificExclusionsAlone(t *testing.T) {
	for _, line := range []string{".regent/objects/", ".regent/index.db", "**/.regent/log/", "myapp.regent"} {
		root := t.TempDir()
		writeFile(t, root, ".gitignore", "node_modules/\n"+line+"\n")

		narrowed, err := narrowRegentExclusion(root)
		if err != nil {
			t.Fatalf("narrowRegentExclusion: %v", err)
		}
		if narrowed {
			t.Errorf("rewrote a .gitignore whose only re_gent rule was %q, which nobody asked to change", line)
		}
		data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if !strings.Contains(string(data), line) {
			t.Errorf("%q no longer in .gitignore after narrowing:\n%s", line, data)
		}
	}
}

// gitIgnores asks git — not a reimplementation of git — whether a path is
// excluded here.
//
// Two details of check-ignore make the obvious call wrong, and both were found
// by writing the obvious call first. Its exit status means "some rule matched",
// and a re-inclusion (`!.regent/config.toml`) is a rule, so the pattern has to be
// read rather than the status. And without --no-index it reports nothing for a
// tracked path, which would have made this assertion vacuous the moment the
// commit it is checking succeeded.
func gitIgnores(t *testing.T, dir, path string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "check-ignore", "--no-index", "-v", path).Output()
	if err != nil {
		return false // no rule matched at all
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) < 3 {
		t.Fatalf("cannot read check-ignore output %q", out)
	}
	pattern := strings.TrimSpace(strings.Split(fields[2], "\t")[0])
	return !strings.HasPrefix(pattern, "!")
}

// newGitRepo turns an existing directory into a repository with one commit, so
// HEAD exists and a push has something to send. gitRepo makes its own temp
// directory; these tests need the repository to be somewhere they chose.
func newGitRepo(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "tester@example.com")
	git(t, dir, "config", "user.name", "Tester")
	writeFile(t, dir, "README.md", "hi\n")
	git(t, dir, "add", "--", "README.md")
	git(t, dir, "commit", "-qm", "init")
}

// git runs a git command and fails the test if it does not succeed.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// fakeRgt writes an executable that reports which copy of itself ran, so a test
// can tell "the hook fired" apart from "the hook fired the wrong binary" —
// the whole distinction #23 is about.
func fakeRgt(t *testing.T, path string) string {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho \"$0\"\n"), 0o755); err != nil {
		t.Fatalf("write fake rgt at %s: %v", path, err)
	}
	return path
}

// runHookCommand executes a hook command the way an agent host does: as a shell
// command line. It returns the path of the binary that actually ran.
//
// The shell is named absolutely because these tests control PATH to decide
// whether rgt is reachable, and an agent host finds its own shell without
// consulting the PATH the hook then sees.
func runHookCommand(command string) (string, error) {
	out, err := exec.Command("/bin/sh", "-c", command).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// withPath replaces PATH for the duration of a test, so "is rgt reachable here"
// is a fact the test sets rather than one it inherits from the machine running
// it.
func withPath(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

func copyTestFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	mustMkdir(t, filepath.Dir(dst))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// claudeHookCommands returns the re_gent hook command lines configured in a
// project's .claude/settings.json.
func claudeHookCommands(t *testing.T, projectRoot string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectRoot, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse Claude settings: %v", err)
	}

	return claudeHookCommandsIn(settings)
}
