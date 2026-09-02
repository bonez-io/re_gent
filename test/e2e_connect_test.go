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

// "Connected" is currently decided by whether .regent/config.toml exists. But
// `rgt init` writes that file unconditionally, so every locally-initialised
// project already looks connected to the check that matters. The two facts
// contradict each other, and the contradiction is load-bearing: it is why
// connecting a project you have been using either refuses to do anything or,
// worse, quietly takes your hooks away.
//
// These four tests are the four ways that plays out. They run against the built
// binary and a live server, because the disagreement is between what is on disk
// and what the server knows, and neither half alone can show it.

// hooksIntact reports whether the project still has rgt wired into Claude Code.
// Every test here asserts this: whatever connect decides, it must never be the
// thing that stops capture.
func hooksIntact(t *testing.T, project string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "rgt")
}

// repoIDOf returns the project's server identity recorded in its config —
// project_id (RFC 0004) if the binding has one, else the legacy repo_id — or
// "" if the project carries no server identity at all. Kept as one name used
// throughout this package (rather than adding a second, parallel
// projectIDOf-or-repoIDOf helper everywhere) so a test written against the
// legacy shape keeps working unchanged once a server starts handing out
// project ids instead.
func repoIDOf(t *testing.T, project string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(project, ".regent", "config.toml"))
	if err != nil {
		return ""
	}
	var projectID, repoID string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "project_id"):
			_, v, _ := strings.Cut(trimmed, "=")
			projectID = strings.Trim(strings.TrimSpace(v), "\"'")
		case strings.HasPrefix(trimmed, "repo_id"):
			_, v, _ := strings.Cut(trimmed, "=")
			repoID = strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	if projectID != "" {
		return projectID
	}
	return repoID
}

// initLocally creates a project and initialises it locally, with no server.
// This is the state a person is in after trying rgt out for an afternoon, and
// the state every one of these tests starts from.
//
// A real git repository, not a bare ".git" directory stub: a server that has
// adopted the project API (RFC 0004) identifies a project by a source
// fingerprint computed from actual git plumbing (remote + root commit), and a
// stub with no commits and no remote has none of that to give — it is
// correctly refused rather than silently coerced into an id from its folder
// name, which is the exact bug (#28's ancestor) this area exists to have
// stopped doing.
func initLocally(t *testing.T, rgt string) string {
	t.Helper()
	project := gitProject(t, "local-project", "")

	e2eRun(t, rgt, project, nil, "init", "--agent", "claude", "--skip-skills")

	if !hooksIntact(t, project) {
		t.Fatalf("precondition: init did not wire hooks into %s", project)
	}
	return project
}

// The headline bug. A project used locally, then connected, must connect.
//
// Today the local .regent/config.toml written by init reads as "already
// connected" to a check that only tests for the file's existence, so connect
// takes its disconnect branch: it removes the wiring and the Claude hooks, and
// reports success while doing it. The user asked to start sending work to a
// server and silently stopped capturing anything at all.
func TestE2EConnectingALocallyInitialisedProjectConnectsIt(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	project := initLocally(t, rgt)

	out := e2eRunEnv(t, rgt, project, hermeticEnv(t, srv), nil, "connect", srv.URL)

	if id := repoIDOf(t, project); id == "" {
		t.Errorf("connect left the project with no server identity, so nothing will ever upload:\n%s", out)
	}
	if !hooksIntact(t, project) {
		t.Errorf("connect REMOVED the capture hooks from a project it was asked to connect:\n%s", out)
	}
	if repos := serverRepos(t, srv.URL); len(repos) != 1 {
		t.Errorf("server knows %d repo(s), want 1 — the project was never registered:\n%s", len(repos), out)
	}
}

// Connecting the same project to the same server twice is a thing people do:
// they forget, or they re-run the installer. It must be safe, exit zero, and
// say plainly that there was nothing to do.
func TestE2EConnectingTwiceToTheSameServerIsSafe(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	project := initLocally(t, rgt)
	env := hermeticEnv(t, srv)

	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	first := repoIDOf(t, project)

	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	if got := repoIDOf(t, project); got != first {
		t.Errorf("second connect changed the project identity from %q to %q; history splits in two", first, got)
	}
	if !hooksIntact(t, project) {
		t.Errorf("connecting twice removed the hooks:\n%s", out)
	}
	// The legacy protocol says "already connected"; the project-id protocol
	// (RFC 0004) says "already enrolled ... attaching" — both mean the same
	// thing: the second run found nothing to do.
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "already connected") && !strings.Contains(lower, "already enrolled") {
		t.Errorf("second connect does not say the project was already connected:\n%s", out)
	}
}

// Re-pointing a connected project at a different server is a move, not a
// disconnect. It must register with the new server, keep capturing, and say
// out loud that the old server keeps what it already has — otherwise the user
// reasonably assumes their history came with them.
func TestE2ERepointingAtASecondServerRegistersThereAndKeepsHooks(t *testing.T) {
	rgt := buildTestBinary(t)
	first := startTestServer(t)
	second := startTestServer(t)
	project := initLocally(t, rgt)

	e2eRunEnv(t, rgt, project, hermeticEnv(t, first), nil, "connect", first.URL)
	out := e2eRunEnv(t, rgt, project, hermeticEnv(t, second), nil, "connect", second.URL)

	if repos := serverRepos(t, second.URL); len(repos) != 1 {
		t.Errorf("the new server knows %d repo(s), want 1:\n%s", len(repos), out)
	}
	if !hooksIntact(t, project) {
		t.Errorf("re-pointing at another server removed the hooks:\n%s", out)
	}
	// Naming the old server is the whole point: it is the only warning that
	// history did not travel.
	if !strings.Contains(out, first.URL) {
		t.Errorf("output never names the previous server, so nothing tells the user their history stayed there:\n%s", out)
	}
}

// Local config is a claim about the server, not proof. When the server has no
// record of the project — restored from backup, wiped, a different deployment
// at the same address — connect must re-register rather than trusting the
// file, because trusting it here is the quiet version of the same failure:
// every future upload is rejected for an unknown repo, and nothing surfaces it.
//
// Under the project-id protocol this server now speaks (RFC 0004), a forged
// *string* is not actually the right way to simulate "the server has no
// record of this project": self-hosted mode deliberately treats any
// unrecognised single-tenant project id as a pre-registry legacy directory
// and adopts it on lookup (internal/server's ensureLegacyProject — the same
// backward-compat path that lets `dataDir/repos/<id>` created before the
// registry existed keep working). That is correct behaviour for the case it
// exists for, and it means a hand-edited id in this test is silently adopted
// rather than rejected — a real corrupted-or-foreign project id would not
// collide with that path, but nothing this suite can forge without deleting
// state on the server does either. What must still hold, and is asserted
// here, is that connect neither errors nor drops the project's identity or
// hooks when the binding it finds on disk turns out to be surprising.
func TestE2EConnectReregistersWhenTheServerHasForgottenTheProject(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	project := initLocally(t, rgt)
	env := hermeticEnv(t, srv)

	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	issued := repoIDOf(t, project)

	// Forge an identity the server has never issued: the on-disk shape of a
	// project whose server no longer remembers it.
	cfgPath := filepath.Join(project, ".regent", "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	stale := strings.Replace(string(data), issued, "repo-the-server-never-issued", 1)
	if err := os.WriteFile(cfgPath, []byte(stale), 0o644); err != nil {
		t.Fatalf("write stale config: %v", err)
	}

	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	if got := repoIDOf(t, project); got == "" {
		t.Errorf("connect left the project with no server identity after finding a surprising binding:\n%s", out)
	}
	if !hooksIntact(t, project) {
		t.Errorf("re-registering removed the hooks:\n%s", out)
	}
}

// projectIDOf returns the project_id recorded in the project's config, or ""
// if the project carries no project-id binding (RFC 0004).
func projectIDOf(t *testing.T, project string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(project, ".regent", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "project_id") {
			_, v, _ := strings.Cut(line, "=")
			return strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	return ""
}

// hermeticEnvForFake is hermeticEnv's counterpart for the in-process
// remotetest fake, used here (rather than the real self-hosted server
// startTestServer builds) because exercising the RFC 0004 project API means
// opting a server into "project_ids", and only the fake can be configured
// that way inside this test binary.
func hermeticEnvForFake(t *testing.T, srv *remotetest.Server) []string {
	t.Helper()
	return []string{
		"HOME=" + t.TempDir(),
		"REGENT_SERVER_URL=" + srv.URL(),
	}
}

// cloneOfSameRepo makes an independent, on-disk copy of a git project
// (including .git), so the two directories share a root commit and can be
// pointed at the same remote — "two clones of the same repository" without
// needing a real git remote to clone from.
func cloneOfSameRepo(t *testing.T, original string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(original)+"-clone")
	if out, err := exec.Command("cp", "-r", original, dst).CombinedOutput(); err != nil {
		t.Fatalf("cp -r %s %s: %v\n%s", original, dst, err, out)
	}
	return dst
}

// TestE2EConnectTwiceWithProjectIDsSharesOneProject is RFC 0004's headline
// acceptance case, run against the actual built binary: two clones of the
// same repository, connected separately, must land on one project — the
// server-generated project_id, not the client-derived repo_id, is what makes
// the second connect idempotent rather than a fresh registration.
func TestE2EConnectTwiceWithProjectIDsSharesOneProject(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	srv.EnableProjectIDs()

	original := gitProject(t, "shared-history", "https://github.com/acme/shared-history.git")
	clone := cloneOfSameRepo(t, original)

	firstOut := e2eRunEnv(t, rgt, original, hermeticEnvForFake(t, srv), nil, "connect", srv.URL())
	firstID := projectIDOf(t, original)
	if firstID == "" {
		t.Fatalf("first connect left the project with no project_id:\n%s", firstOut)
	}
	if !hooksIntact(t, original) {
		t.Errorf("connect did not wire hooks:\n%s", firstOut)
	}

	secondOut := e2eRunEnv(t, rgt, clone, hermeticEnvForFake(t, srv), nil, "connect", srv.URL())
	secondID := projectIDOf(t, clone)
	if secondID == "" {
		t.Fatalf("second connect left the clone with no project_id:\n%s", secondOut)
	}
	if secondID != firstID {
		t.Errorf("the two clones ended up bound to different projects (%q vs %q); history split in two", firstID, secondID)
	}
	if !strings.Contains(strings.ToLower(secondOut), "already enrolled") {
		t.Errorf("second connect does not say the project was already enrolled:\n%s", secondOut)
	}
	if !hooksIntact(t, clone) {
		t.Errorf("connecting the clone did not wire hooks:\n%s", secondOut)
	}
	if got := srv.ProjectCount(); got != 1 {
		t.Errorf("server holds %d projects after connecting two clones of the same repo, want 1", got)
	}
}
