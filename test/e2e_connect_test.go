package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	// See e2e_onboarding_test.go: this package builds rgt via `go build`, so
	// without a compile-time edge to the CLI the test cache serves stale passes.
	_ "github.com/regent-vcs/regent/internal/cli"
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

// repoIDOf returns the repo_id recorded in the project's config, or "" if the
// project carries no server identity.
func repoIDOf(t *testing.T, project string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(project, ".regent", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "repo_id") {
			_, v, _ := strings.Cut(line, "=")
			return strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	return ""
}

// initLocally creates a project and initialises it locally, with no server.
// This is the state a person is in after trying rgt out for an afternoon, and
// the state every one of these tests starts from.
func initLocally(t *testing.T, rgt string) string {
	t.Helper()
	project := filepath.Join(t.TempDir(), "local-project")
	mustMkdirAll(t, project)
	mustMkdirAll(t, filepath.Join(project, ".git"))

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
	if !strings.Contains(strings.ToLower(out), "already connected") {
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
// at the same address — connect must re-register.
//
// Trusting the local file here is the quiet version of the same failure: every
// future upload is rejected for an unknown repo, and nothing surfaces it.
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

	if got := repoIDOf(t, project); got == "repo-the-server-never-issued" {
		t.Errorf("connect trusted a repo id the server does not know; every upload from here fails silently:\n%s", out)
	}
	if !hooksIntact(t, project) {
		t.Errorf("re-registering removed the hooks:\n%s", out)
	}
}
