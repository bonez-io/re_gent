package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	// See e2e_onboarding_test.go: this package builds rgt via `go build`, so
	// without a compile-time edge to the CLI the test cache serves stale passes.
	_ "github.com/bonez-io/re_gent/internal/cli"
	"github.com/bonez-io/re_gent/internal/remotetest"
)

// Identity is the thing a user never sets and always depends on. These are the
// ways the old folder-name identity failed, written as the situations people
// are actually in rather than as properties of a function.

// gitProject creates a real repository under a chosen folder name, optionally
// with an origin remote. Real git because identity is derived by asking git,
// and a hand-written .git would only prove the fixture.
func gitProject(t *testing.T, folder, origin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), folder)
	mustMkdirAll(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+dir+"\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	steps := [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "-q", "-m", "first commit in " + dir},
	}
	if origin != "" {
		steps = append(steps, []string{"remote", "add", "origin", origin})
	}
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// Two people, two unrelated projects, both checked out into a folder called
// "api". Under folder-name identity they registered the same id and wrote into
// one history: each saw the other's sessions, and neither had a way to notice.
func TestE2ETwoProjectsNamedTheSameKeepSeparateHistories(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	ours := gitProject(t, "api", "https://github.com/acme/api.git")
	theirs := gitProject(t, "api", "https://github.com/globex/api.git")

	e2eRunEnv(t, rgt, ours, hermeticEnv(t, srv), nil, "connect", srv.URL)
	e2eRunEnv(t, rgt, theirs, hermeticEnv(t, srv), nil, "connect", srv.URL)

	a, b := repoIDOf(t, ours), repoIDOf(t, theirs)
	if a == b {
		t.Errorf("both projects registered as %q; each is writing into the other's history", a)
	}
	if repos := serverRepos(t, srv.URL); len(repos) != 2 {
		t.Errorf("server knows %d repo(s), want 2 — the second registration landed on the first: %v", len(repos), repos)
	}
}

// Renaming the folder you work in must not register a second project.
//
// Note what this does and does not prove. It passes even with identity derived
// from the folder name, because the binding lives inside the folder and travels
// with it: connect sees a project already bound to this server and does not
// re-derive anything. That is worth pinning — it is the observable outcome, and
// it is what would break if connect ever started re-deriving — but the claim
// that a *rename* cannot change a derived identity is carried by
// TestRenamingTheFolderDoesNotChangeIdentity in internal/cli, which exercises
// derivation directly and fails loudly when it regresses.
//
// Written down because the first version of this test was named for the
// stronger claim and was checked against a mutation that made it pass anyway.
func TestE2ERenamingTheProjectFolderDoesNotRegisterASecondProject(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	project := gitProject(t, "before", "https://github.com/acme/service.git")
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	before := repoIDOf(t, project)

	renamed := filepath.Join(filepath.Dir(project), "after")
	if err := os.Rename(project, renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}

	e2eRunEnv(t, rgt, renamed, env, nil, "connect", srv.URL)

	if after := repoIDOf(t, renamed); after != before {
		t.Errorf("identity changed from %q to %q on rename; the history is still on the server with nothing pointing at it", before, after)
	}
	if repos := serverRepos(t, srv.URL); len(repos) != 1 {
		t.Errorf("server knows %d repo(s), want 1 — the rename registered a second, empty project: %v", len(repos), repos)
	}
}

// Once a project has an identity, nothing may change it underneath the project.
//
// Derivation is a guess made once, from whatever git said at the time. Remotes
// get renamed, moved between hosts, and repointed at forks. If identity were
// re-derived on every run, any of those would silently orphan the history
// recorded so far — the same failure as the rename, arriving later and with
// less warning.
func TestE2EIdentityIsFrozenOnceTheProjectIsConnected(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	project := gitProject(t, "service", "https://github.com/acme/service.git")
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	original := repoIDOf(t, project)

	// The repository moves: new owner, new host.
	cmd := exec.Command("git", "-C", project, "remote", "set-url", "origin", "https://gitlab.com/globex/service.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set-url: %v\n%s", err, out)
	}

	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	if got := repoIDOf(t, project); got != original {
		t.Errorf("identity changed from %q to %q because the git remote moved; every step recorded so far is orphaned", original, got)
	}
}

// Derivation is a guess, and it will be wrong for someone. A repository whose
// remote is a fork, a monorepo whose subdirectories are separate projects, a
// checkout with no remote whose derived id is a hash nobody can read — each
// needs a way to say what the project is called, once, and have that stick.
//
// The override is recorded in the binding like any other identity, which is
// what makes it survive: it is not a flag that must be repeated on every
// command, it is the answer to the question, written down.
func TestE2EAnExplicitIdentityIsUsedAndRecorded(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	project := gitProject(t, "checkout", "https://github.com/acme/monorepo.git")

	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL, "--as", "billing-service")

	// Against a project-id server (RFC 0004) --as supplies the display name,
	// not the storage key: the server assigns its own opaque id. The
	// recorded answer is that id, read back from the binding, paired with
	// the display name on the server's own listing.
	key := repoIDOf(t, project)
	if key == "" {
		t.Fatalf("connect wrote no project identity for %s", project)
	}
	if !projectListed(t, srv.URL, key, "billing-service") {
		t.Errorf("GET /api/v1/projects does not list id=%q display_name=%q", key, "billing-service")
	}

	// And it sticks: a later connect with no flag must not quietly go back to
	// a different project, which would split the history in two.
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	if got := repoIDOf(t, project); got != key {
		t.Errorf("identity changed from %q to %q on a later connect; the recorded answer was ignored", key, got)
	}
}

// An override the server will reject has to fail here, with the reason, rather
// than 400 mid-connect with a message about a regular expression.
//
// The charset restriction this test exercises is specifically the legacy
// repo_id rule (internal/remote.ValidateRepoID): against a project-id server
// (RFC 0004) --as is a free-text display name, so "Not/A Valid Id" is a
// perfectly acceptable project name there and would not be refused at all.
// startTestServer's real server always advertises "project_ids" (it is the
// default for both self-hosted and this suite's in-process server), so it
// cannot exercise this path any more. remotetest.Server can: left without
// EnableProjectIDs it speaks the legacy protocol, which is exactly the
// server this test is about.
func TestE2EAnUnusableExplicitIdentityIsRefusedUpFront(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	project := gitProject(t, "checkout", "https://github.com/acme/api.git")

	out := e2eRunExpectingFailure(t, rgt, project, "connect", srv.URL(), "--as", "Not/A Valid Id")

	if repos := serverRepos(t, srv.URL()); len(repos) != 0 {
		t.Errorf("a rejected identity still registered %v on the server", repos)
	}
	if !strings.Contains(out, "Not/A Valid Id") {
		t.Errorf("the failure never quotes the identity that was refused:\n%s", out)
	}
}
