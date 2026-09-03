package cli

import (
	"bytes"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/remotetest"
)

// A server that advertises the "project_ids" feature (RFC 0004) makes
// connect compute a source fingerprint and enroll through the project API
// instead of the legacy repo_id flow, and the binding it writes carries
// project_id, never repo_id.
func TestConnectProject_EnrollsAndWritesProjectIDBinding(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	repo := gitRepoWithCommit(t, "widget-service")
	setGitRemote(t, repo, "https://github.com/acme/widget-service.git")

	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: repo, httpClient: http.DefaultClient}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}

	binding, err := config.LoadRemoteBinding(filepath.Join(repo, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if binding.ProjectID == "" {
		t.Fatal("binding has no project_id")
	}
	if binding.RepoID != "" {
		t.Errorf("a project-id binding must not also carry repo_id, got %q", binding.RepoID)
	}
	if binding.URL != srv.URL() {
		t.Errorf("URL = %q, want %q", binding.URL, srv.URL())
	}
	if srv.ProjectCount() != 1 {
		t.Errorf("server holds %d projects, want 1", srv.ProjectCount())
	}
}

// The headline guarantee, end to end through runConnect rather than the raw
// remote.EnrollProject client: connecting two clones of the same repository
// lands on one project, and the second run says so instead of creating a
// duplicate.
func TestConnectProject_TwoClonesOfSameRepoShareOneProject(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	original := gitRepoWithCommit(t, "shared-history")
	setGitRemote(t, original, "https://github.com/acme/shared-history.git")
	clone := copyDir(t, original)

	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: original, httpClient: http.DefaultClient}); err != nil {
		t.Fatalf("connect first clone: %v", err)
	}
	var out bytes.Buffer
	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: clone, httpClient: http.DefaultClient, out: &out}); err != nil {
		t.Fatalf("connect second clone: %v", err)
	}

	if srv.ProjectCount() != 1 {
		t.Errorf("server holds %d projects after connecting two clones of the same repo, want 1", srv.ProjectCount())
	}
	firstBinding, err := config.LoadRemoteBinding(filepath.Join(original, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding (original): %v", err)
	}
	secondBinding, err := config.LoadRemoteBinding(filepath.Join(clone, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding (clone): %v", err)
	}
	if firstBinding.ProjectID != secondBinding.ProjectID {
		t.Errorf("clones ended up with different projects: %q vs %q", firstBinding.ProjectID, secondBinding.ProjectID)
	}
	if !strings.Contains(strings.ToLower(out.String()), "already enrolled") {
		t.Errorf("second connect should say the project was already enrolled, got:\n%s", out.String())
	}
}

// Re-running connect in a project already bound by project_id must GET the
// project, confirm it, and be a no-op — not enroll a second time.
func TestConnectProject_ReconnectIsANoOp(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	repo := gitRepoWithCommit(t, "reconnect-me")
	setGitRemote(t, repo, "https://github.com/acme/reconnect-me.git")

	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: repo, httpClient: http.DefaultClient}); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: repo, httpClient: http.DefaultClient}); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if srv.ProjectCount() != 1 {
		t.Errorf("reconnecting created a second project: %d total", srv.ProjectCount())
	}
}

// A fork's root commit matches a public upstream project. Without --as-fork,
// connect must stop, explain the two choices, and write nothing.
func TestConnectProject_ForkWithoutAsForkStopsAndWritesNothing(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	upstreamRepo := gitRepoWithCommit(t, "upstream-project")
	setGitRemote(t, upstreamRepo, "https://github.com/upstream/project.git")
	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: upstreamRepo, httpClient: http.DefaultClient, org: "upstream-org"}); err != nil {
		t.Fatalf("connect upstream: %v", err)
	}
	upstreamBinding, err := config.LoadRemoteBinding(filepath.Join(upstreamRepo, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	srv.MarkProjectPublic(upstreamBinding.ProjectID)

	fork := copyDir(t, upstreamRepo)
	setGitRemote(t, fork, "https://github.com/contributor/project.git")
	// The fork must not inherit the upstream's binding: this is a fresh
	// checkout that has never been connected.
	mustRemoveAll(t, filepath.Join(fork, ".regent"))

	err = runConnect(connectParams{serverURL: srv.URL(), projectRoot: fork, httpClient: http.DefaultClient, org: "fork-org"})
	if err == nil {
		t.Fatal("expected connect to stop on a detected fork without --as-fork")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "fork") {
		t.Errorf("error should explain the fork, got: %v", err)
	}
	forkBinding, bindErr := config.LoadRemoteBinding(filepath.Join(fork, ".regent", "config.toml"))
	if bindErr == nil && forkBinding.Connected() {
		t.Error("a stopped fork enrollment must not write a binding")
	}
}

// --as-fork accepts the fork on its own terms: it gets its own project.
func TestConnectProject_ForkWithAsForkEnrollsItsOwnProject(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	upstreamRepo := gitRepoWithCommit(t, "upstream-project-2")
	setGitRemote(t, upstreamRepo, "https://github.com/upstream/project2.git")
	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: upstreamRepo, httpClient: http.DefaultClient, org: "upstream-org-2"}); err != nil {
		t.Fatalf("connect upstream: %v", err)
	}
	upstreamBinding, err := config.LoadRemoteBinding(filepath.Join(upstreamRepo, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	srv.MarkProjectPublic(upstreamBinding.ProjectID)

	fork := copyDir(t, upstreamRepo)
	setGitRemote(t, fork, "https://github.com/contributor/project2.git")
	mustRemoveAll(t, filepath.Join(fork, ".regent"))

	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: fork, httpClient: http.DefaultClient, org: "fork-org-2", asFork: true}); err != nil {
		t.Fatalf("connect fork with --as-fork: %v", err)
	}
	forkBinding, err := config.LoadRemoteBinding(filepath.Join(fork, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding (fork): %v", err)
	}
	if forkBinding.ProjectID == "" || forkBinding.ProjectID == upstreamBinding.ProjectID {
		t.Errorf("fork should have enrolled its own project, got %q (upstream is %q)", forkBinding.ProjectID, upstreamBinding.ProjectID)
	}
}

// A directory that is not a git repository has no fingerprint (RFC 0004), so
// it is always a new project, named from the folder by default and from
// --as when given — connect still succeeds, it just cannot promise that a
// second checkout will find its way back to the same project.
func TestConnectProject_NonGitDirectoryFallsBackToFolderName(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	dir := t.TempDir()
	var out bytes.Buffer
	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: dir, httpClient: http.DefaultClient, out: &out}); err != nil {
		t.Fatalf("connect should succeed for a non-git directory without --as, got: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "no source fingerprint") {
		t.Errorf("connect should note that this project has no source fingerprint, got:\n%s", out.String())
	}
	binding, err := config.LoadRemoteBinding(filepath.Join(dir, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if binding.ProjectID == "" {
		t.Fatal("connect did not write a project_id binding")
	}

	// --as overrides the folder-name default and does not create a second
	// project on a rerun in the same directory.
	dir2 := t.TempDir()
	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: dir2, httpClient: http.DefaultClient, repoID: "Hand-Named Project"}); err != nil {
		t.Fatalf("connect with --as should succeed for a non-git directory: %v", err)
	}
	if srv.ProjectCount() != 2 {
		t.Fatalf("expected 2 distinct projects (one per directory), got %d", srv.ProjectCount())
	}
	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: dir2, httpClient: http.DefaultClient, repoID: "Hand-Named Project"}); err != nil {
		t.Fatalf("reconnecting should be a no-op: %v", err)
	}
	if srv.ProjectCount() != 2 {
		t.Errorf("reconnecting a non-git directory created a second project: %d total", srv.ProjectCount())
	}
}

// A legacy server (no project_ids feature) must be completely unaffected:
// this is the same assertion as the existing connect tests, restated here to
// pin the branch point explicitly.
func TestConnectProject_LegacyServerUsesRepoIDNotProjectID(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	// EnableProjectIDs deliberately not called.

	repo := gitRepoWithCommit(t, "legacy-project")
	setGitRemote(t, repo, "https://github.com/acme/legacy-project.git")

	if err := runConnect(connectParams{serverURL: srv.URL(), projectRoot: repo, httpClient: http.DefaultClient}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	binding, err := config.LoadRemoteBinding(filepath.Join(repo, ".regent", "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if binding.ProjectID != "" {
		t.Errorf("a legacy server must never produce a project_id binding, got %q", binding.ProjectID)
	}
	if binding.RepoID == "" {
		t.Error("a legacy server binding must still carry repo_id")
	}
}

// --- test helpers -----------------------------------------------------------

// copyDir makes an independent, on-disk copy of src (including .git), so a
// test can simulate "a second clone of the same repository" — same root
// commit, same history — without shelling out to a real git remote.
func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src)+"-clone")
	if out, err := exec.Command("cp", "-r", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("cp -r %s %s: %v\n%s", src, dst, err, out)
	}
	return dst
}

func mustRemoveAll(t *testing.T, path string) {
	t.Helper()
	if out, err := exec.Command("rm", "-rf", path).CombinedOutput(); err != nil {
		t.Fatalf("rm -rf %s: %v\n%s", path, err, out)
	}
}
