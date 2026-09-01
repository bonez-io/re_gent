package test

import (
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bonez-io/re_gent/internal/server"

	// See e2e_onboarding_test.go: this package builds rgt via `go build`, so
	// without a compile-time edge to the CLI the test cache serves stale passes.
	_ "github.com/bonez-io/re_gent/internal/cli"
)

// These are the acceptance tests for RFC 0002 (docs/rfcs/0002-git-push-integration.md),
// run at the only seam that can answer them honestly: a real `git push` from a
// real Git repository, through the hook the real binary installed, against the
// real server — and, for the offline cases, against a server that has gone away.
//
// A unit test can prove the script exits 0 in isolation. Only this proves that
// Git agrees: that the push completes, that a pre-existing hook keeps its veto,
// and that queued history actually arrives.

// pushableGitProject creates a Git repository with one commit and a bare remote it can
// push to, so `git push` has somewhere to go and something to send.
func pushableGitProject(t *testing.T) (project string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	project = filepath.Join(t.TempDir(), "proj")
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, "", "init", "-q", "--bare", remote)
	git(t, "", "init", "-q", project)
	git(t, project, "config", "user.email", "e2e@example.com")
	git(t, project, "config", "user.name", "e2e")
	git(t, project, "config", "--unset-all", "core.hooksPath")
	git(t, project, "remote", "add", "origin", remote)
	writeTestFile(t, project, "README", "hello\n")
	git(t, project, "add", "README")
	git(t, project, "commit", "-q", "-m", "init")
	return project
}

// git runs a git command; an unset-config failure is tolerated (exit 5 = no
// such key), everything else is fatal.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil && !(len(args) > 1 && args[1] == "--unset-all") {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitPush runs `git push` and returns output plus whether it succeeded. The
// hook is what runs in between, so this is the whole test in one call.
func gitPush(t *testing.T, project string, env []string, extra ...string) (string, bool) {
	t.Helper()
	args := append([]string{"push", "-q", "origin", "HEAD"}, extra...)
	cmd := exec.Command("git", args...)
	cmd.Dir = project
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// commitSomething makes a new commit so there is always something to push.
func commitSomething(t *testing.T, project, name string) {
	t.Helper()
	writeTestFile(t, project, name, name+"\n")
	git(t, project, "add", name)
	git(t, project, "commit", "-q", "-m", "add "+name)
}

// prePushHook returns the installed hook path for project.
func prePushHook(project string) string {
	return filepath.Join(project, ".git", "hooks", "pre-push")
}

// TestE2EConnectWiresTheGitPushHook: `rgt connect` writes the pre-push hook and
// reports it, and `rgt doctor` sees it.
func TestE2EConnectWiresTheGitPushHook(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	project := pushableGitProject(t)
	env := hermeticEnv(t, srv)

	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	assertContains(t, out, "Git pre-push hook configured", "connect output")

	hook, err := os.ReadFile(prePushHook(project))
	if err != nil {
		t.Fatalf("pre-push hook not written: %v", err)
	}
	if !strings.Contains(string(hook), "re_gent pre-push") {
		t.Errorf("hook file is not re_gent's:\n%s", hook)
	}

	doctor := e2eRunEnv(t, rgt, project, env, nil, "doctor")
	assertContains(t, doctor, "git push sync", "doctor lists the check")
}

// TestE2EGitPushDeliversQueuedSteps is the point of the whole feature: work
// captured while the server was unreachable reaches it on the next git push,
// with no `rgt sync` typed by anyone.
func TestE2EGitPushDeliversQueuedSteps(t *testing.T) {
	rgt := buildTestBinary(t)
	project := pushableGitProject(t)

	// A server that will be reachable, then not, then reachable again at the
	// same address. httptest cannot restart on a fixed port, so use two servers
	// sharing one data directory: close the first, start the second on the
	// address the binary knows. Between them the binary sees "connection
	// refused", which is exactly an outage.
	dataDir := t.TempDir()
	cacheDir := t.TempDir()
	first := newServerOn(t, dataDir, "")
	env := append(hermeticEnv(t, first), "REGENT_CACHE_DIR="+cacheDir)
	e2eRunEnv(t, rgt, project, env, nil, "connect", first.URL)
	addr := first.Listener.Addr().String()

	// Outage.
	first.Close()

	// Capture a turn during the outage. Delivery fails; the work spools.
	captureTurn(t, rgt, project, env, "e2e-push-session", "t1", "during_outage.go")
	status := e2eRunEnv(t, rgt, project, env, nil, "sync", "--status")
	if strings.Contains(status, "Up to date") {
		t.Fatalf("expected queued work after capturing during an outage:\n%s", status)
	}

	// Server comes back at the same address.
	second := newServerOn(t, dataDir, addr)
	defer second.Close()

	// Time passes. The failed agent turn started a 30s retry cooldown, and a
	// person's push does not land inside it — by the time they have noticed the
	// outage end and pushed, it has expired. A test cannot wait 30s, so it does
	// what the clock would: the cooldown marker is disposable derived state
	// (docs/server-mode.md), and removing it is exactly "the cooldown expired".
	// A push *inside* the cooldown is a separate contract, pinned by
	// TestE2EGitPushDuringOutageHonoursRetryCooldown.
	expireCooldown(t, cacheDir)

	// The developer pushes. No rgt command is typed.
	commitSomething(t, project, "after_outage.txt")
	out, ok := gitPush(t, project, env)
	if !ok {
		t.Fatalf("git push failed:\n%s", out)
	}
	assertContains(t, out, "Regent: delivered", "push output reports delivery")

	// The colleague's view: the server holds the session captured offline.
	sessions := serverSessions(t, second.URL, repoIDOf(t, project))
	if len(sessions) == 0 {
		t.Fatalf("server holds nothing after git push; queued history was not delivered\npush output:\n%s", out)
	}
	// And the queue is empty now.
	status = e2eRunEnv(t, rgt, project, env, nil, "sync", "--status")
	if !strings.Contains(status, "Up to date") {
		t.Errorf("queue not drained after push:\n%s", status)
	}
}

// TestE2EGitPushWithServerDownDoesNotBlockThePush: RFC 0002 D2. The server is
// gone; the push completes anyway; one line says work is queued and names the
// command that finishes it.
func TestE2EGitPushWithServerDownDoesNotBlockThePush(t *testing.T) {
	rgt := buildTestBinary(t)
	project := pushableGitProject(t)
	srv := startTestServer(t)
	env := append(hermeticEnv(t, srv), "REGENT_SERVER_TIMEOUT=2s")
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	srv.Close()

	captureTurn(t, rgt, project, env, "e2e-offline-session", "t1", "offline.go")
	commitSomething(t, project, "offline.txt")

	out, ok := gitPush(t, project, env)
	if !ok {
		t.Fatalf("git push was blocked by an unreachable Regent server — RFC 0002 D2 violated:\n%s", out)
	}
	if !strings.Contains(out, "queued") || !strings.Contains(out, "rgt sync") {
		t.Errorf("push output should say work is queued and name rgt sync:\n%s", out)
	}
	// Nothing was lost: it is still queued.
	status := e2eRunEnv(t, rgt, project, env, nil, "sync", "--status")
	if strings.Contains(status, "Up to date") {
		t.Errorf("queue emptied although the server was down:\n%s", status)
	}
}

// TestE2EGitPushDuringOutageHonoursRetryCooldown: a second push during the same
// outage does not pay the network timeout again; it reports the cooldown.
func TestE2EGitPushDuringOutageHonoursRetryCooldown(t *testing.T) {
	rgt := buildTestBinary(t)
	project := pushableGitProject(t)
	srv := startTestServer(t)
	env := append(hermeticEnv(t, srv), "REGENT_SERVER_TIMEOUT=2s")
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	srv.Close()

	captureTurn(t, rgt, project, env, "e2e-cooldown-session", "t1", "cd.go")
	commitSomething(t, project, "one.txt")
	first, ok := gitPush(t, project, env)
	if !ok {
		t.Fatalf("first push blocked:\n%s", first)
	}
	commitSomething(t, project, "two.txt")
	second, ok := gitPush(t, project, env)
	if !ok {
		t.Fatalf("second push blocked:\n%s", second)
	}
	assertContains(t, second, "cooling down", "second push during outage reports cooldown")
}

// TestE2EPrePushChainPreservesExistingHook: a hook that was there before rgt
// runs first and keeps its veto. Here it vetoes, so the push must fail with its
// exit code — and rgt must not have run.
func TestE2EPrePushChainPreservesExistingHook(t *testing.T) {
	rgt := buildTestBinary(t)
	project := pushableGitProject(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	marker := filepath.Join(t.TempDir(), "user-hook-ran")
	userHook := "#!/bin/sh\necho user-veto >&2\ntouch '" + marker + "'\nexit 7\n"
	writeTestFile(t, project, ".git/hooks/pre-push", userHook)
	if err := os.Chmod(prePushHook(project), 0o755); err != nil {
		t.Fatal(err)
	}

	out := e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	assertContains(t, out, "existing pre-push hook was kept", "connect reports chaining")

	commitSomething(t, project, "vetoed.txt")
	pushOut, ok := gitPush(t, project, env)
	if ok {
		t.Fatalf("push succeeded although the user's hook vetoes it; chaining lost the veto:\n%s", pushOut)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("user hook did not run:\n%s", pushOut)
	}
	assertContains(t, pushOut, "user-veto", "user hook output surfaces")
	if strings.Contains(pushOut, "Regent:") {
		t.Errorf("Regent ran although the push was vetoed before it:\n%s", pushOut)
	}
}

// TestE2EDisconnectRestoresPriorPrePushHook: disconnect removes only our file
// and puts the user's back byte for byte.
func TestE2EDisconnectRestoresPriorPrePushHook(t *testing.T) {
	rgt := buildTestBinary(t)
	project := pushableGitProject(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)

	userHook := "#!/bin/sh\n# the user's own hook\nexit 0\n"
	writeTestFile(t, project, ".git/hooks/pre-push", userHook)
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	e2eRunEnv(t, rgt, project, env, nil, "disconnect")

	got, err := os.ReadFile(prePushHook(project))
	if err != nil {
		t.Fatalf("user's hook not restored: %v", err)
	}
	if string(got) != userHook {
		t.Errorf("restored hook differs:\n got %q\nwant %q", got, userHook)
	}
	if _, err := os.Stat(prePushHook(project) + ".pre-regent"); err == nil {
		t.Error(".pre-regent copy left behind")
	}
}

// TestE2EGitHookOptOutScopes: --no-git-hook at connect writes nothing;
// REGENT_GIT_SYNC_ON_PUSH=0 at push time makes an installed hook do nothing.
func TestE2EGitHookOptOutScopes(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)

	t.Run("flag", func(t *testing.T) {
		project := pushableGitProject(t)
		env := hermeticEnv(t, srv)
		e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL, "--no-git-hook")
		if _, err := os.Stat(prePushHook(project)); err == nil {
			t.Error("pre-push written despite --no-git-hook")
		}
	})

	t.Run("env", func(t *testing.T) {
		project := pushableGitProject(t)
		env := hermeticEnv(t, srv)
		e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
		srv2 := startTestServer(t) // a different, empty server: if rgt runs it would queue
		_ = srv2
		commitSomething(t, project, "x.txt")
		out, ok := gitPush(t, project, append(env, "REGENT_GIT_SYNC_ON_PUSH=0"))
		if !ok {
			t.Fatalf("push failed:\n%s", out)
		}
		if strings.Contains(out, "Regent:") {
			t.Errorf("Regent ran despite REGENT_GIT_SYNC_ON_PUSH=0:\n%s", out)
		}
	})
}

// TestE2EGitPushInLocalModeIsSilent: a project that was only ever `rgt init`ed
// has the hook (wired early, per RFC 0002 D3) and the hook does nothing.
func TestE2EGitPushInLocalModeIsSilent(t *testing.T) {
	rgt := buildTestBinary(t)
	project := pushableGitProject(t)
	env := []string{"HOME=" + t.TempDir(), "REGENT_SERVER_URL="}

	e2eRunEnv(t, rgt, project, env, nil, "init", "--agent", "claude")
	if _, err := os.Stat(prePushHook(project)); err != nil {
		t.Fatalf("init did not wire the pre-push hook: %v", err)
	}
	captureTurn(t, rgt, project, env, "e2e-local", "t1", "local.go")
	commitSomething(t, project, "y.txt")

	out, ok := gitPush(t, project, env)
	if !ok {
		t.Fatalf("push failed in local mode:\n%s", out)
	}
	if strings.Contains(out, "Regent") {
		t.Errorf("hook spoke in local mode:\n%s", out)
	}
}

// TestE2EGitPushNoVerifySkipsTheHook pins Git's own escape hatch: --no-verify
// bypasses every pre-push hook including ours, and RFC 0002 documents rather
// than fights that.
func TestE2EGitPushNoVerifySkipsTheHook(t *testing.T) {
	rgt := buildTestBinary(t)
	project := pushableGitProject(t)
	srv := startTestServer(t)
	env := hermeticEnv(t, srv)
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)
	srv.Close()
	captureTurn(t, rgt, project, env, "e2e-nv", "t1", "nv.go")
	commitSomething(t, project, "nv.txt")

	out, ok := gitPush(t, project, env, "--no-verify")
	if !ok {
		t.Fatalf("push failed:\n%s", out)
	}
	if strings.Contains(out, "Regent") {
		t.Errorf("hook ran despite --no-verify:\n%s", out)
	}
}

// newServerOn starts the production server on dataDir. With addr "" it picks a
// port; with an addr it binds exactly there, which is how a test simulates a
// server coming back after an outage at the address the binary already knows.
func newServerOn(t *testing.T, dataDir, addr string) *httptest.Server {
	t.Helper()
	h, err := server.New(dataDir)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if addr == "" {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		return ts
	}
	ts := httptest.NewUnstartedServer(h)
	ln, err := netListen(addr)
	if err != nil {
		t.Fatalf("relisten on %s: %v", addr, err)
	}
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// netListen binds a TCP listener on addr. Kept tiny and separate so the
// import stays local to the one helper that needs it.
func netListen(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// expireCooldown removes every retry-after marker under cacheDir, standing in
// for the 30 seconds a test cannot afford to sleep.
func expireCooldown(t *testing.T, cacheDir string) {
	t.Helper()
	removed := 0
	_ = filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "retry-after" {
			if rmErr := os.Remove(path); rmErr == nil {
				removed++
			}
		}
		return nil
	})
	if removed == 0 {
		t.Fatalf("no retry-after marker under %s: the outage capture did not start a cooldown, so this test is not exercising what it claims", cacheDir)
	}
}

// TestE2EConcurrentPushHookAndAgentTurnIsSafe pins RFC 0002 D4's claim that
// concurrency needs no lock: spool writes are atomic, objects are content
// addressed so a duplicate upload is a no-op, and the high-water mark only
// advances on server confirmation. The worst case is redundant work.
//
// It is an end-to-end test because the claim is about separate processes. An
// in-process test would share a mutex the real thing does not have.
func TestE2EConcurrentPushHookAndAgentTurnIsSafe(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	project := pushableGitProject(t)
	env := hermeticEnv(t, srv)
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	// Seed several sessions so there is real work in the outbox.
	for i := 0; i < 4; i++ {
		id := "race-" + string(rune('a'+i))
		captureTurn(t, rgt, project, env, id, "t1", id+".go")
	}

	// Eight hook invocations at once, all draining the same spool, while more
	// agent turns land underneath them.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(rgt, "git-hook", "pre-push", "origin", "url")
			cmd.Dir = project
			cmd.Env = append(os.Environ(), env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("hook exited non-zero under contention: %v\n%s", err, out)
			}
		}()
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "race-concurrent-" + string(rune('x'+n))
			captureTurn(t, rgt, project, env, id, "t1", id+".go")
		}(i)
	}
	wg.Wait()

	// Nothing corrupted: the queue still reads, and a final sync converges.
	e2eRunEnv(t, rgt, project, env, nil, "sync", "--status")
	e2eRunEnv(t, rgt, project, env, nil, "sync")

	status := e2eRunEnv(t, rgt, project, env, nil, "sync", "--status")
	if !strings.Contains(status, "Up to date") {
		t.Errorf("outbox did not converge after contention:\n%s", status)
	}
	if len(serverSessions(t, srv.URL, repoIDOf(t, project))) == 0 {
		t.Error("server holds nothing after concurrent delivery")
	}
}
