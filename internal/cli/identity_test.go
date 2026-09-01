package cli

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bonez-io/re_gent/internal/remote"
)

// A project is identified by the repository it belongs to. Today it is
// identified by the folder it happens to sit in, and both halves of that are
// wrong in ways people hit immediately:
//
//   - two people whose checkout is called "api" register the same id and write
//     into one pile of history on the server;
//   - renaming your folder derives a different id, so your history is still on
//     the server but nothing points at it any more.
//
// The repository's remote is the identity everyone already agrees on. These
// tests pin what deriving from it means, starting with the part that has no
// filesystem in it: turning a remote URL into a server-legal id.

// The same repository must produce the same identity however it was cloned.
// This is the property the whole ticket rests on — get it wrong and two
// teammates on https and ssh become two projects.
func TestIdentityIsTheSameHoweverTheRepositoryWasCloned(t *testing.T) {
	same := []string{
		"https://github.com/bonez-io/re_gent.git",
		"https://github.com/bonez-io/re_gent",
		"git@github.com:bonez-io/re_gent.git",
		"ssh://git@github.com/bonez-io/re_gent.git",
		"https://GitHub.com/bonez-io/re_gent.git/",
	}

	want := identityFromRemote(same[0])
	for _, r := range same[1:] {
		if got := identityFromRemote(r); got != want {
			t.Errorf("identityFromRemote(%q) = %q, want %q — the same repository cloned two ways became two projects",
				r, got, want)
		}
	}
}

func TestIdentityFromRemote(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		want   string
		why    string
	}{
		{
			name:   "host owner and repo",
			remote: "https://github.com/bonez-io/re_gent.git",
			want:   "github.com-bonez-io-re_gent",
			why:    "the host is part of the identity: github.com/acme/api and gitlab.com/acme/api are different projects",
		},
		{
			name:   "credentials are never part of an identity",
			remote: "https://someone:ghp_averyrealtoken@github.com/acme/api.git",
			want:   "github.com-acme-api",
			why:    "this id is sent to a server and stored in a committed config; a token must not travel in it",
		},
		{
			name:   "port is not part of the identity",
			remote: "https://gitlab.example.com:8443/team/api.git",
			want:   "gitlab.example.com-team-api",
			why:    "the same repository reached on a different port is one project, not two",
		},
		{
			name:   "nested groups are kept",
			remote: "https://gitlab.com/group/subgroup/api.git",
			want:   "gitlab.com-group-subgroup-api",
			why:    "dropping the group would collide with every other subgroup's api",
		},
		{
			name:   "uppercase is folded",
			remote: "https://github.com/Acme/API.git",
			want:   "github.com-acme-api",
			why:    "the server's id charset is lowercase; folding must not depend on how someone typed the clone url",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityFromRemote(tc.remote); got != tc.want {
				t.Errorf("identityFromRemote(%q) = %q, want %q\n%s", tc.remote, got, tc.want, tc.why)
			}
		})
	}
}

// Whatever is derived has to be something the server will accept, or connect
// fails with a 400 that names none of this.
func TestDerivedIdentityIsAlwaysServerLegal(t *testing.T) {
	remotes := []string{
		"https://github.com/bonez-io/re_gent.git",
		"https://gitlab.example.com/a-very-long-group-name/another-long-subgroup/and-a-repository-name-that-runs-well-past-the-limit.git",
		"git@github.com:_leading/underscore.git",
		"https://github.com/acme/repos.git",
		"https://example.com/x/ünïcodé.git",
	}

	for _, r := range remotes {
		id := identityFromRemote(r)
		if err := remote.ValidateRepoID(id); err != nil {
			t.Errorf("identityFromRemote(%q) = %q, which the server rejects: %v", r, id, err)
		}
	}
}

// Truncation is where uniqueness usually dies: two long remotes sharing a
// prefix collapse to one id, and the two projects silently merge on the server.
func TestLongRemotesStayDistinctAfterTruncation(t *testing.T) {
	base := "https://gitlab.example.com/a-very-long-group-name/another-long-subgroup/and-a-repository-name-that-runs-well-past-the-limit"
	a := identityFromRemote(base + "-one.git")
	b := identityFromRemote(base + "-two.git")

	if a == b {
		t.Errorf("two different repositories derived the same identity %q; their histories would merge on the server", a)
	}
}

// A project with no remote still needs a stable identity, and the folder name
// alone is what this ticket exists to stop relying on. The first commit is the
// one thing about a repository that never changes.
func TestProjectWithNoRemoteIsIdentifiedByItsFirstCommit(t *testing.T) {
	first := gitRepoWithCommit(t, "shared-name")
	second := gitRepoWithCommit(t, "shared-name")

	a, b := deriveRepoID(first), deriveRepoID(second)

	if err := remote.ValidateRepoID(a); err != nil {
		t.Fatalf("deriveRepoID(%s) = %q, which the server rejects: %v", first, a, err)
	}
	if a == b {
		t.Errorf("two unrelated projects both called %q derived the same identity %q; "+
			"connecting the second writes into the first's history", "shared-name", a)
	}
}

// Renaming a folder is not starting a new project. This is the complaint the
// ticket opens with, and it is the one a user notices: history that was there
// yesterday is gone today.
func TestRenamingTheFolderDoesNotChangeIdentity(t *testing.T) {
	repo := gitRepoWithCommit(t, "before")
	before := deriveRepoID(repo)

	renamed := filepath.Join(filepath.Dir(repo), "after")
	if err := exec.Command("mv", repo, renamed).Run(); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if after := deriveRepoID(renamed); after != before {
		t.Errorf("identity changed from %q to %q because the folder was renamed; "+
			"the project's history is still on the server and nothing points at it", before, after)
	}
}

// gitRepoWithCommit creates a real repository with one commit and no remote.
// Real git rather than a hand-written .git, because the first-commit lookup is
// a git question and a fixture would only prove the fixture.
func gitRepoWithCommit(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	mustMkdir(t, dir)
	// The content has to differ between repositories, or two of them created in
	// the same second with the same message and the same tree produce the same
	// commit hash — and a test about telling projects apart would pass or fail
	// on the clock. Two unrelated projects that happen to share a folder name
	// have different contents in reality; this reproduces that, not a fixture.
	mustWrite(t, filepath.Join(dir, "README.md"), "# "+name+"\n"+dir+"\n")

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "-q", "-m", "first commit in " + dir},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}
