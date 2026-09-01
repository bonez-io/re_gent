package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/remotetest"
	"github.com/bonez-io/re_gent/internal/store"
)

const syncTestRef = "sessions/claude_code--sess-1"

type syncFixture struct {
	cfg   remote.Config
	srv   *remotetest.Server
	cache *store.Store
	spool *remote.Spool
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()

	srv := remotetest.New()
	t.Cleanup(srv.Close)

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

func (f *syncFixture) addStep(t *testing.T, path, content, tool string) store.Hash {
	t.Helper()

	parent, err := f.cache.ReadRef(syncTestRef)
	if err != nil {
		parent = ""
	}
	blob, err := f.cache.WriteBlob([]byte(content))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	treeHash, err := f.cache.WriteTree(&store.Tree{Entries: []store.TreeEntry{{Path: path, Blob: blob}}})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	args, err := f.cache.WriteBlob([]byte(`{"tool":"` + tool + `"}`))
	if err != nil {
		t.Fatalf("write args: %v", err)
	}
	stepHash, err := f.cache.WriteStep(&store.Step{
		Parent:         parent,
		Tree:           treeHash,
		Causes:         []store.Cause{{ToolUseID: tool, ToolName: "Write", ArgsBlob: args}},
		SessionID:      "claude_code--sess-1",
		Origin:         "claude_code",
		TimestampNanos: time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := f.cache.UpdateRef(syncTestRef, parent, stepHash); err != nil {
		t.Fatalf("update ref: %v", err)
	}
	return stepHash
}

func runSyncCapturingOutput(t *testing.T, cfg remote.Config, opts syncOptions) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := runSync(&buf, cfg, opts)
	return buf.String(), err
}

func TestSyncRequiresServerModeConfiguration(t *testing.T) {
	_, err := runSyncCapturingOutput(t, remote.Config{}, syncOptions{status: true})
	if err == nil || !strings.Contains(err.Error(), "server mode is not configured") {
		t.Fatalf("error = %v, want a configuration hint", err)
	}
}

func TestSyncStatusReportsQueuedWorkWithoutNetwork(t *testing.T) {
	f := newSyncFixture(t)
	f.addStep(t, "a.txt", "one", "first")
	f.addStep(t, "a.txt", "two", "second")

	f.srv.SetOffline(true) // --status must not need the server at all
	out, err := runSyncCapturingOutput(t, f.cfg, syncOptions{status: true})
	if err != nil {
		t.Fatalf("runSync --status: %v", err)
	}

	for _, want := range []string{"Queued for delivery: 1 ref(s), 2 step(s)", syncTestRef, "rgt sync"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	if total := f.srv.Requests("GET") + f.srv.Requests("POST"); total != 0 {
		t.Errorf("--status made %d network request(s); it must be offline-safe", total)
	}
}

func TestSyncDeliversAndThenReportsClean(t *testing.T) {
	f := newSyncFixture(t)
	tip := f.addStep(t, "a.txt", "one", "first")

	out, err := runSyncCapturingOutput(t, f.cfg, syncOptions{})
	if err != nil {
		t.Fatalf("runSync: %v", err)
	}
	if !strings.Contains(out, "delivered") {
		t.Errorf("output = %q, want a delivery line", out)
	}
	if f.srv.Ref(syncTestRef) != tip {
		t.Fatalf("server ref = %s, want %s", f.srv.Ref(syncTestRef), tip)
	}

	out, err = runSyncCapturingOutput(t, f.cfg, syncOptions{status: true})
	if err != nil {
		t.Fatalf("runSync --status: %v", err)
	}
	if !strings.Contains(out, "Up to date") {
		t.Errorf("status after delivery = %q, want 'Up to date'", out)
	}

	out, err = runSyncCapturingOutput(t, f.cfg, syncOptions{})
	if err != nil {
		t.Fatalf("second runSync: %v", err)
	}
	const cleanMessage = "Up to date — all captured steps are already synced.\n"
	if out != cleanMessage {
		t.Errorf("clean sync output = %q, want %q", out, cleanMessage)
	}
}

func TestSyncReportsFailureWithoutLosingWork(t *testing.T) {
	f := newSyncFixture(t)
	f.addStep(t, "a.txt", "one", "first")

	f.srv.SetOffline(true)
	_, err := runSyncCapturingOutput(t, f.cfg, syncOptions{})
	if err == nil {
		t.Fatal("expected an error while the server is unreachable")
	}
	if !strings.Contains(err.Error(), "still queued locally") {
		t.Errorf("error must say the work is safe, got: %v", err)
	}

	status, statusErr := f.spool.Status(f.cache)
	if statusErr != nil {
		t.Fatalf("status: %v", statusErr)
	}
	if status.Clean() {
		t.Fatal("a failed sync must leave the work queued")
	}
}

func TestSyncPullRebuildsCacheAndIndexFromServer(t *testing.T) {
	f := newSyncFixture(t)
	f.addStep(t, "a.txt", "one", "first")
	tip := f.addStep(t, "a.txt", "two", "second")
	if _, err := runSyncCapturingOutput(t, f.cfg, syncOptions{}); err != nil {
		t.Fatalf("initial push: %v", err)
	}

	// Lose the entire cache: the server is now the only copy.
	cacheDir, err := remote.CacheDirFor(f.cfg)
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatalf("wipe cache: %v", err)
	}

	out, err := runSyncCapturingOutput(t, f.cfg, syncOptions{pull: true, ref: syncTestRef})
	if err != nil {
		t.Fatalf("runSync --pull: %v", err)
	}
	if !strings.Contains(out, "2 step(s) indexed") {
		t.Errorf("pull output = %q, want two rebuilt steps", out)
	}

	rebuilt, err := store.Open(cacheDir)
	if err != nil {
		t.Fatalf("reopen cache: %v", err)
	}
	got, err := rebuilt.ReadRef(syncTestRef)
	if err != nil || got != tip {
		t.Fatalf("rebuilt ref = %s, %v; want %s", got, err, tip)
	}

	// The rebuilt cache must be usable, not merely present: step objects, file
	// contents, the blame sidecar and the query index all have to be back.
	step, err := rebuilt.ReadStep(tip)
	if err != nil {
		t.Fatalf("read rebuilt step: %v", err)
	}
	tree, err := rebuilt.ReadTree(step.Tree)
	if err != nil {
		t.Fatalf("read rebuilt tree: %v", err)
	}
	content, err := rebuilt.ReadBlob(tree.Entries[0].Blob)
	if err != nil || string(content) != "two" {
		t.Fatalf("rebuilt file content = %q, %v; want %q", content, err, "two")
	}
	if _, err := rebuilt.ReadBlameForFile(tip, "a.txt"); err != nil {
		t.Fatalf("blame was not rebuilt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "index.db")); err != nil {
		t.Fatalf("index was not rebuilt: %v", err)
	}
}

// sync --pull is explicit cache recovery, but it must still not silently
// replace a live cache's divergent session history with the server's tip.
func TestSyncPullRefusesToOverwriteDivergedLocalHistory(t *testing.T) {
	pusher := newSyncFixture(t)
	theirs := pusher.addStep(t, "a.txt", "theirs", "theirs")
	if _, err := runSyncCapturingOutput(t, pusher.cfg, syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	me := teammateFixture(t, pusher.srv)
	mine := me.addStep(t, "a.txt", "mine", "mine")

	out, err := runSyncCapturingOutput(t, me.cfg, syncOptions{pull: true, ref: syncTestRef})
	if err == nil {
		t.Fatalf("sync --pull over diverged history succeeded:\n%s", out)
	}
	report := out + err.Error()
	for _, want := range []string{syncTestRef, "diverged", string(theirs), string(mine)} {
		if !strings.Contains(report, want) {
			t.Errorf("divergence report does not mention %q:\n%s", want, report)
		}
	}

	local, readErr := me.cache.ReadRef(syncTestRef)
	if readErr != nil || local != mine {
		t.Fatalf("local ref = %s, %v; want it untouched at %s", local, readErr, mine)
	}
}

func TestSyncPullKeepsAndNamesLocalAheadHistory(t *testing.T) {
	f := newSyncFixture(t)
	serverTip := f.addStep(t, "a.txt", "server", "server")
	if _, err := runSyncCapturingOutput(t, f.cfg, syncOptions{}); err != nil {
		t.Fatalf("push server tip: %v", err)
	}
	localTip := f.addStep(t, "a.txt", "local", "local")

	out, err := runSyncCapturingOutput(t, f.cfg, syncOptions{pull: true, ref: syncTestRef})
	if err != nil {
		t.Fatalf("sync --pull with local-ahead history: %v\n%s", err, out)
	}
	for _, want := range []string{syncTestRef, "ahead of the server", string(serverTip), string(localTip)} {
		if !strings.Contains(out, want) {
			t.Errorf("local-ahead report does not mention %q:\n%s", want, out)
		}
	}

	got, readErr := f.cache.ReadRef(syncTestRef)
	if readErr != nil || got != localTip {
		t.Fatalf("local ref = %s, %v; want it untouched at %s", got, readErr, localTip)
	}
}

func TestSyncPullWithoutARefExplainsItself(t *testing.T) {
	f := newSyncFixture(t)
	_, err := runSyncCapturingOutput(t, f.cfg, syncOptions{pull: true})
	if err == nil || !strings.Contains(err.Error(), "rgt sync --pull sessions/") {
		t.Fatalf("error = %v, want an explicit usage hint", err)
	}
}

func TestSyncPullAndRepairAreMutuallyExclusive(t *testing.T) {
	f := newSyncFixture(t)
	_, err := runSyncCapturingOutput(t, f.cfg, syncOptions{pull: true, repair: true})
	if err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("error = %v, want a mutual-exclusion error", err)
	}
}

func TestSyncRepairRestoresObjectsTheServerLost(t *testing.T) {
	f := newSyncFixture(t)
	first := f.addStep(t, "a.txt", "one", "first")
	f.addStep(t, "a.txt", "two", "second")
	if _, err := runSyncCapturingOutput(t, f.cfg, syncOptions{}); err != nil {
		t.Fatalf("initial push: %v", err)
	}

	firstStep, err := f.cache.ReadStep(first)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}
	f.srv.DropObject(firstStep.Tree)

	out, err := runSyncCapturingOutput(t, f.cfg, syncOptions{repair: true})
	if err != nil {
		t.Fatalf("runSync --repair: %v", err)
	}
	if !strings.Contains(out, "1 missing object(s) restored") {
		t.Errorf("repair output = %q, want one restored object", out)
	}
	if _, ok := f.srv.Objects()[firstStep.Tree]; !ok {
		t.Fatal("repair did not restore the lost object")
	}
}

func TestQualifyRef(t *testing.T) {
	tests := []struct{ in, want string }{
		{"claude_code--abc", "sessions/claude_code--abc"},
		{"sessions/claude_code--abc", "sessions/claude_code--abc"},
		{"legacy-sessions/x", "legacy-sessions/x"},
	}
	for _, tt := range tests {
		if got := qualifyRef(tt.in); got != tt.want {
			t.Errorf("qualifyRef(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSyncCmdFlags(t *testing.T) {
	cmd := SyncCmd()
	for _, flag := range []string{"status", "pull", "repair"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("rgt sync is missing the --%s flag", flag)
		}
	}
	if cmd.Use != "sync [ref]" {
		t.Errorf("Use = %q", cmd.Use)
	}

	// The command must accept at most one positional ref.
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("two positional arguments should be rejected")
	}
}
