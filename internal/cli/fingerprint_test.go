package cli

import (
	"os/exec"
	"testing"
)

// The same repository must fingerprint identically however it was cloned:
// https, ssh, scp-style, with or without ".git", with or without a port. This
// is the property `rgt connect` relies on to make enrollment idempotent
// across a team's different clone styles.
func TestSourceFingerprintSameRepositoryHoweverCloned(t *testing.T) {
	spellings := []string{
		"https://github.com/bonez-io/re_gent.git",
		"https://github.com/bonez-io/re_gent",
		"git@github.com:bonez-io/re_gent.git",
		"ssh://git@github.com/bonez-io/re_gent.git",
		"https://GitHub.com/bonez-io/re_gent.git/",
	}

	// One repository, root commit held fixed: what varies across "clones" is
	// only how the remote is spelled, exactly what this test means to isolate.
	// (Two separate clones would also have two separate, unrelated root
	// commits — this is what a shared history looks like without needing an
	// actual second checkout.)
	repo := gitRepoWithCommit(t, "clone-style")

	var want string
	for i, remote := range spellings {
		setGitRemote(t, repo, remote)
		fp, ok := sourceFingerprint(repo)
		if !ok {
			t.Fatalf("sourceFingerprint(%q) reported not-a-git-repo", repo)
		}
		if i == 0 {
			want = fp.Hex
			continue
		}
		if fp.Hex != want {
			t.Errorf("remote %q fingerprinted to %q, want %q (same as %q)", remote, fp.Hex, want, spellings[0])
		}
	}
}

// Credentials embedded in a remote URL must never survive into the
// fingerprint: it travels to a server and (indirectly, via the enrolled
// project's Remote field) can be displayed back.
func TestSourceFingerprintStripsCredentials(t *testing.T) {
	repo := gitRepoWithCommit(t, "creds")

	setGitRemote(t, repo, "https://someone:ghp_averyrealtoken@github.com/acme/api.git")
	a, ok := sourceFingerprint(repo)
	if !ok {
		t.Fatal("not a git repo?")
	}
	setGitRemote(t, repo, "https://github.com/acme/api.git")
	b, ok := sourceFingerprint(repo)
	if !ok {
		t.Fatal("not a git repo?")
	}

	if a.Hex != b.Hex {
		t.Errorf("fingerprint differs with credentials present: %q vs %q", a.Hex, b.Hex)
	}
	if a.Remote != "github.com/acme/api" {
		t.Errorf("Remote = %q, want credentials stripped", a.Remote)
	}
}

// The port is not part of a repository's identity: the same server reached on
// a different port is one project.
func TestSourceFingerprintStripsPort(t *testing.T) {
	repo := gitRepoWithCommit(t, "port")
	setGitRemote(t, repo, "https://gitlab.example.com:8443/team/api.git")

	fp, ok := sourceFingerprint(repo)
	if !ok {
		t.Fatal("not a git repo?")
	}
	if fp.Remote != "gitlab.example.com/team/api" {
		t.Errorf("Remote = %q, want the port stripped", fp.Remote)
	}
}

// A repository with no remote still gets a fingerprint, from its root commit
// alone — RFC 0004: "A repository with no remote uses the root commit alone."
func TestSourceFingerprintNoRemoteUsesRootCommitAlone(t *testing.T) {
	repo := gitRepoWithCommit(t, "no-remote-project")
	fp, ok := sourceFingerprint(repo)
	if !ok {
		t.Fatal("a git repository with no remote should still have a fingerprint")
	}
	if fp.Remote != "" {
		t.Errorf("Remote = %q, want empty (no remote configured)", fp.Remote)
	}
	if fp.RootCommit == "" {
		t.Error("RootCommit is empty; a fingerprint with no remote and no commit is not stable")
	}
	if fp.Hex == "" {
		t.Error("Hex is empty")
	}

	// Two unrelated repositories with no remote must not collide just because
	// neither has a remote to distinguish them.
	other := gitRepoWithCommit(t, "no-remote-project")
	otherFP, ok := sourceFingerprint(other)
	if !ok {
		t.Fatal("second repo: not a git repo?")
	}
	if otherFP.Hex == fp.Hex {
		t.Error("two unrelated no-remote repositories fingerprinted identically")
	}
}

// A directory that is not a git repository at all has no fingerprint. This is
// the one case RFC 0004 says must fall back to --as and always be treated as
// a new project.
func TestSourceFingerprintNonGitDirectoryHasNone(t *testing.T) {
	dir := t.TempDir()
	if _, ok := sourceFingerprint(dir); ok {
		t.Errorf("a non-git directory reported having a fingerprint")
	}
}

// setGitRemote sets dir's origin remote to url, adding it if absent and
// replacing it otherwise, so a test can fingerprint the same repository under
// several remote spellings without those spellings ever coexisting.
func setGitRemote(t *testing.T, dir, url string) {
	t.Helper()
	args := []string{"-C", dir, "remote", "add", "origin", url}
	if gitRemoteURL(dir) != "" {
		args = []string{"-C", dir, "remote", "set-url", "origin", url}
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v\n%s", err, out)
	}
}
