package cli

import (
	"bytes"
	"encoding/json"
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

func (f *syncFixture) addSessionStep(t *testing.T, refName, sessionID string, timestamp int64, entries []storedConversationEntry) store.Hash {
	t.Helper()

	treeHash, err := f.cache.WriteTree(&store.Tree{})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	args, err := f.cache.WriteBlob([]byte(`{"file_path":"demo.txt"}`))
	if err != nil {
		t.Fatalf("write args: %v", err)
	}
	result, err := f.cache.WriteBlob([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("write result: %v", err)
	}
	conversationData, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	conversationHash, err := f.cache.WriteBlob(conversationData)
	if err != nil {
		t.Fatalf("write conversation: %v", err)
	}
	stepHash, err := f.cache.WriteStep(&store.Step{
		Tree:           treeHash,
		Conversation:   conversationHash,
		Causes:         []store.Cause{{ToolUseID: "tool-1", ToolName: "Write", ArgsBlob: args, ResultBlob: result}},
		SessionID:      sessionID,
		Origin:         "claude_code",
		TurnID:         "turn-1",
		TimestampNanos: timestamp,
	})
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := f.cache.UpdateRef(refName, "", stepHash); err != nil {
		t.Fatalf("update ref: %v", err)
	}
	return stepHash
}

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

func TestPullRestoresNormalizedConversationIntoFreshIndex(t *testing.T) {
	pusher := newSyncFixture(t)
	entries := []storedConversationEntry{
		{Type: "user", Text: "create demo.txt", TS: 100},
		{Type: "reasoning", Text: "I will write the requested file", TS: 110},
		{Type: "assistant", Text: "Created demo.txt", TS: 130},
	}
	tip := pusher.addSessionStep(t, syncTestRef, "claude_code--sess-1", 140, entries)
	if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	me := teammateFixture(t, pusher.srv)
	if _, err := runPullCapturingOutput(t, me.cfg, pullOptions{}); err != nil {
		t.Fatalf("pull: %v", err)
	}

	idx, err := index.Open(me.cache)
	if err != nil {
		t.Fatalf("open pulled index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	messages, err := idx.GetMessagesForStep(tip)
	if err != nil {
		t.Fatalf("read pulled conversation: %v", err)
	}
	if len(messages) != 5 {
		t.Fatalf("pulled conversation has %d messages, want user/reasoning/assistant/tool call/tool result: %#v", len(messages), messages)
	}
	wantText := map[string]string{
		"user":      "create demo.txt",
		"reasoning": "I will write the requested file",
		"assistant": "Created demo.txt",
	}
	for _, message := range messages {
		if want, ok := wantText[message.MessageType]; ok && message.ContentText != want {
			t.Errorf("%s text = %q, want %q", message.MessageType, message.ContentText, want)
		}
	}

	step, err := me.cache.ReadStep(tip)
	if err != nil {
		t.Fatalf("read pulled step: %v", err)
	}
	if _, err := rebuildDerived(me.cache, idx, tip); err != nil {
		t.Fatalf("repeat rebuild: %v", err)
	}
	again, err := idx.GetMessagesForStep(tip)
	if err != nil {
		t.Fatalf("read rebuilt conversation: %v", err)
	}
	if len(again) != len(messages) {
		t.Errorf("repeat rebuild duplicated conversation: %d messages became %d", len(messages), len(again))
	}
	if step.Conversation == "" {
		t.Fatal("precondition: pulled step lost its canonical conversation hash")
	}
}

func TestPullOrdersSessionsByRecordedActivityNotRefIteration(t *testing.T) {
	pusher := newSyncFixture(t)
	const (
		newRef     = "sessions/claude_code--a-new"
		newSession = "claude_code--a-new"
		oldRef     = "sessions/claude_code--z-old"
		oldSession = "claude_code--z-old"
	)
	pusher.addSessionStep(t, oldRef, oldSession, time.Unix(100, 0).UnixNano(), nil)
	pusher.addSessionStep(t, newRef, newSession, time.Unix(200, 0).UnixNano(), nil)
	for _, refName := range []string{oldRef, newRef} {
		if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{ref: refName}); err != nil {
			t.Fatalf("push %s: %v", refName, err)
		}
	}

	me := teammateFixture(t, pusher.srv)
	if _, err := runPullCapturingOutput(t, me.cfg, pullOptions{}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	sessions := sessionsInCache(t, me.cfg)
	if len(sessions) != 2 {
		t.Fatalf("pulled sessions = %d, want 2: %#v", len(sessions), sessions)
	}
	// Server refs are sorted, so a-new is rebuilt before z-old. This assertion
	// fails if index time (and therefore loop order) is allowed to define recency.
	if sessions[0].ID != newSession {
		t.Errorf("most recent pulled session = %q, want %q; order = %#v", sessions[0].ID, newSession, sessions)
	}
	if got := sessions[0].LastSeenAt; !got.Equal(time.Unix(200, 0)) {
		t.Errorf("new session last seen = %s, want recorded step time %s", got, time.Unix(200, 0))
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
