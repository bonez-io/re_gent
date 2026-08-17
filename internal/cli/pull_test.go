package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/remotetest"
	"github.com/regent-vcs/regent/internal/store"
)

// teammateFixture is a second machine on the same server: its own cache, its
// own spool, no record of anything the first machine pushed. That last part is
// the whole point — a machine that has pushed nothing cannot name a ref, so
// every fixture that shares a cache with the pusher would test the wrong thing.
func teammateFixture(t *testing.T, srv *remotetest.Server) *syncFixture {
	t.Helper()

	cfg := remote.Config{
		ServerURL: srv.URL(),
		RepoID:    "test-repo",
		CacheDir:  t.TempDir(),
		Timeout:   2 * time.Second,
	}
	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	cache, err := store.Open(cacheDir)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	spool, err := remote.OpenSpool(filepath.Join(cacheDir, "spool"))
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	return &syncFixture{cfg: cfg, srv: srv, cache: cache, spool: spool}
}

func runPullCapturingOutput(t *testing.T, cfg remote.Config, opts pullOptions) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := runPullCommand(&buf, cfg, opts)
	return buf.String(), err
}

// sessionsInCache is what `rgt log` and `rgt sessions` read: the SQLite index,
// not the object store. History that arrives without being indexed is history
// no command can show.
func sessionsInCache(t *testing.T, cfg remote.Config) []index.SessionInfo {
	t.Helper()
	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	cache, err := store.Open(cacheDir)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	idx, err := index.Open(cache)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()
	sessions, err := idx.ListAllSessions()
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	return sessions
}

// The ticket in one test: a machine that has pushed nothing asks the server
// what exists and ends up able to read it.
func TestPullDiscoversTheServersSessionsWithoutBeingNamed(t *testing.T) {
	pusher := newSyncFixture(t)
	pusher.addStep(t, "a.txt", "one", "first")
	pusher.addStep(t, "a.txt", "two", "second")
	if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	me := teammateFixture(t, pusher.srv)
	out, err := runPullCapturingOutput(t, me.cfg, pullOptions{})
	if err != nil {
		t.Fatalf("runPullCommand: %v", err)
	}
	if !strings.Contains(out, syncTestRef) {
		t.Errorf("pull never named the session it fetched:\n%s", out)
	}
	if !strings.Contains(out, "2 step(s)") {
		t.Errorf("pull does not say how much history arrived:\n%s", out)
	}

	sessions := sessionsInCache(t, me.cfg)
	if len(sessions) != 1 || sessions[0].ID != "claude_code--sess-1" {
		t.Fatalf("cache index holds %v; the pulled session is not readable by log or sessions", sessions)
	}
}

// Objects are not enough. Whatever `rgt log` reads has to be rebuilt from them,
// because the index is never transferred over the wire.
func TestPullRebuildsTheDerivedReadPathFromPulledObjects(t *testing.T) {
	pusher := newSyncFixture(t)
	tip := pusher.addStep(t, "a.txt", "one", "first")
	if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	me := teammateFixture(t, pusher.srv)
	if _, err := runPullCapturingOutput(t, me.cfg, pullOptions{}); err != nil {
		t.Fatalf("runPullCommand: %v", err)
	}

	cacheDir, err := remote.CacheDirFor(me.cfg)
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	pulled, err := store.Open(cacheDir)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	idx, err := index.Open(pulled)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	steps, err := idx.ListSteps("claude_code--sess-1", 10)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("index holds %d step(s) after pull, want 1", len(steps))
	}
	// Blame is derived at write time, so a pull that skipped it would leave
	// `rgt blame` broken on history that is otherwise complete.
	if _, err := pulled.ReadBlameForFile(tip, "a.txt"); err != nil {
		t.Errorf("blame was not rebuilt for pulled history: %v", err)
	}
}

// A pull that quietly replaced local history with the server's would be a worse
// bug than the one this command fixes.
func TestPullRefusesToOverwriteDivergedLocalHistory(t *testing.T) {
	pusher := newSyncFixture(t)
	pusher.addStep(t, "a.txt", "theirs", "theirs")
	if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	me := teammateFixture(t, pusher.srv)
	mine := me.addStep(t, "a.txt", "mine", "mine")

	out, err := runPullCapturingOutput(t, me.cfg, pullOptions{})
	if err == nil {
		t.Fatalf("pull over diverged history succeeded:\n%s", out)
	}
	report := out + err.Error()
	for _, want := range []string{syncTestRef, "diverged", "rgt sync"} {
		if !strings.Contains(report, want) {
			t.Errorf("divergence report does not mention %q:\n%s", want, report)
		}
	}

	local, readErr := me.cache.ReadRef(syncTestRef)
	if readErr != nil || local != mine {
		t.Fatalf("local ref = %s, %v; want it untouched at %s", local, readErr, mine)
	}
}

// One unpullable ref must not stop the rest: a teammate with one diverged
// session still needs everyone else's history.
func TestPullDeliversTheOtherSessionsWhenOneHasDiverged(t *testing.T) {
	pusher := newSyncFixture(t)
	pusher.addStep(t, "a.txt", "theirs", "theirs")
	if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	// A second session, pushed by hand so it does not share the first's ref.
	otherStep, err := pusher.cache.ReadRef(syncTestRef)
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	const otherRef = "sessions/claude_code--sess-2"
	if err := pusher.cache.UpdateRef(otherRef, "", otherStep); err != nil {
		t.Fatalf("seed second ref: %v", err)
	}
	if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{ref: otherRef}); err != nil {
		t.Fatalf("push second ref: %v", err)
	}

	me := teammateFixture(t, pusher.srv)
	me.addStep(t, "a.txt", "mine", "mine") // diverges from sess-1 only

	out, err := runPullCapturingOutput(t, me.cfg, pullOptions{})
	if err == nil {
		t.Fatalf("pull with a diverged ref exited zero:\n%s", out)
	}
	if got, readErr := me.cache.ReadRef(otherRef); readErr != nil || got != otherStep {
		t.Errorf("the undiverged session was not pulled: ref = %s, %v; want %s", got, readErr, otherStep)
	}
}

// Local work the server has not seen is not a conflict, but silently rewinding
// it would be the same loss by another route.
func TestPullLeavesLocalWorkTheServerHasNotSeen(t *testing.T) {
	me := newSyncFixture(t)
	me.addStep(t, "a.txt", "delivered", "delivered")
	if _, err := runSyncCapturingOutput(t, me.cfg, syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	ahead := me.addStep(t, "a.txt", "not delivered", "pending")

	out, err := runPullCapturingOutput(t, me.cfg, pullOptions{})
	if err != nil {
		t.Fatalf("runPullCommand: %v", err)
	}
	if !strings.Contains(out, "rgt sync") {
		t.Errorf("pull does not point at the command that delivers the local steps:\n%s", out)
	}
	if local, readErr := me.cache.ReadRef(syncTestRef); readErr != nil || local != ahead {
		t.Fatalf("local ref = %s, %v; want it left at %s", local, readErr, ahead)
	}
}

// An empty server is a fact, not a failure.
func TestPullFromAServerWithNoHistorySaysSo(t *testing.T) {
	me := newSyncFixture(t)
	out, err := runPullCapturingOutput(t, me.cfg, pullOptions{})
	if err != nil {
		t.Fatalf("runPullCommand: %v", err)
	}
	if !strings.Contains(out, "no history") {
		t.Errorf("pull from an empty server = %q, want it to say the server holds no history", out)
	}
}

func TestPullRequiresServerModeConfiguration(t *testing.T) {
	_, err := runPullCapturingOutput(t, remote.Config{}, pullOptions{})
	if err == nil || !strings.Contains(err.Error(), "server mode is not configured") {
		t.Fatalf("error = %v, want a configuration hint", err)
	}
}

func TestPullCmdSurface(t *testing.T) {
	cmd := PullCmd()
	if cmd.Use != "pull [ref]" {
		t.Errorf("Use = %q, want %q", cmd.Use, "pull [ref]")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("two positional arguments should be rejected")
	}
}
