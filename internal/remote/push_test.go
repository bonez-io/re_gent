package remote

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/remotetest"
	"github.com/regent-vcs/regent/internal/store"
)

const testRef = "sessions/claude_code--session1"

type fixture struct {
	cache *store.Store
	spool *Spool
	srv   *remotetest.Server
	cli   Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dir := t.TempDir()
	cache, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	spool, err := OpenSpool(filepath.Join(dir, "spool"))
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	srv := remotetest.New()
	t.Cleanup(srv.Close)

	cli, err := NewHTTPClient(Config{ServerURL: srv.URL(), RepoID: "test-repo", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &fixture{cache: cache, spool: spool, srv: srv, cli: cli}
}

// addStep appends a step to the local cache exactly the way capture does:
// file blobs, tree, tool payloads, step, then a CAS ref update.
func (f *fixture) addStep(t *testing.T, files map[string]string, tool string) store.Hash {
	t.Helper()

	parent, err := f.cache.ReadRef(testRef)
	if err != nil {
		parent = ""
	}

	tree := &store.Tree{}
	for path, content := range files {
		blob, err := f.cache.WriteBlob([]byte(content))
		if err != nil {
			t.Fatalf("write blob: %v", err)
		}
		tree.Entries = append(tree.Entries, store.TreeEntry{Path: path, Blob: blob})
	}
	treeHash, err := f.cache.WriteTree(tree)
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}

	args, err := f.cache.WriteBlob([]byte(`{"cmd":"` + tool + `"}`))
	if err != nil {
		t.Fatalf("write args: %v", err)
	}
	result, err := f.cache.WriteBlob([]byte(`{"ok":true,"tool":"` + tool + `"}`))
	if err != nil {
		t.Fatalf("write result: %v", err)
	}

	step := &store.Step{
		Parent:         parent,
		Tree:           treeHash,
		Causes:         []store.Cause{{ToolUseID: tool, ToolName: "Bash", ArgsBlob: args, ResultBlob: result}},
		SessionID:      "claude_code--session1",
		Origin:         "claude_code",
		TimestampNanos: time.Now().UnixNano(),
	}
	stepHash, err := f.cache.WriteStep(step)
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := f.cache.UpdateRef(testRef, parent, stepHash); err != nil {
		t.Fatalf("update ref: %v", err)
	}
	return stepHash
}

func TestPushDeliversHistoryAndAdvancesRef(t *testing.T) {
	f := newFixture(t)
	f.addStep(t, map[string]string{"a.txt": "one"}, "first")
	tip := f.addStep(t, map[string]string{"a.txt": "one", "b.txt": "two"}, "second")

	res, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.Tip != tip {
		t.Fatalf("pushed tip = %s, want %s", res.Tip, tip)
	}
	if f.srv.Ref(testRef) != tip {
		t.Fatalf("server ref = %s, want %s", f.srv.Ref(testRef), tip)
	}

	// Everything the tip depends on must be on the server, or the history is
	// not actually recoverable from it.
	assertServerHasChain(t, f, tip)

	pushed, err := f.spool.PushedTip(testRef)
	if err != nil || pushed != tip {
		t.Fatalf("high-water mark = %s, %v; want %s", pushed, err, tip)
	}
}

func TestPushIsIdempotent(t *testing.T) {
	f := newFixture(t)
	tip := f.addStep(t, map[string]string{"a.txt": "one"}, "first")

	if _, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef); err != nil {
		t.Fatalf("first push: %v", err)
	}
	before := f.srv.Requests("POST")

	res, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if !res.AlreadyCurrent {
		t.Fatal("second push should report the server is already current")
	}
	if got := f.srv.Requests("POST"); got != before {
		t.Fatalf("second push uploaded %d object(s); a no-op push must touch nothing", got-before)
	}
	if f.srv.Ref(testRef) != tip {
		t.Fatal("ref changed on a no-op push")
	}
}

func TestPushUploadsOnlyTheDelta(t *testing.T) {
	f := newFixture(t)
	f.addStep(t, map[string]string{"a.txt": "one", "big.txt": "unchanged"}, "first")
	if _, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef); err != nil {
		t.Fatalf("first push: %v", err)
	}
	firstUploads := f.srv.Requests("PUT")

	// Only a.txt changes; big.txt must not be re-uploaded.
	f.addStep(t, map[string]string{"a.txt": "two", "big.txt": "unchanged"}, "second")
	res, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}

	// changed blob + tree + args + result + step = 5.
	if res.Objects != 5 {
		t.Fatalf("delta push uploaded %d objects, want 5 (unchanged file must be skipped)", res.Objects)
	}
	if f.srv.Requests("PUT") != firstUploads+5 {
		t.Fatalf("unexpected upload count: %d", f.srv.Requests("PUT")-firstUploads)
	}
}

func TestPushRefusesToClobberDivergedServer(t *testing.T) {
	f := newFixture(t)
	tip := f.addStep(t, map[string]string{"a.txt": "one"}, "first")

	// A tip the local cache has never seen: pushing over it would erase history
	// that belongs to someone else.
	foreign, err := f.cli.PutObject(context.Background(), []byte(`{"tree":"","session_id":"other"}`))
	if err != nil {
		t.Fatalf("seed foreign object: %v", err)
	}
	f.srv.SetRef(testRef, foreign)

	_, err = Push(context.Background(), f.cache, f.cli, f.spool, testRef)
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("Push = %v, want ErrDiverged", err)
	}
	if f.srv.Ref(testRef) != foreign {
		t.Fatal("a diverged push must leave the server ref untouched")
	}
	if pushed, _ := f.spool.PushedTip(testRef); pushed == tip {
		t.Fatal("a failed push must not record a high-water mark")
	}
}

// A server-side partial write that the server itself can detect: it still has
// the ref, but not the tree the next step points at. The 422 must trigger an
// automatic full re-upload rather than an error the user has to resolve.
func TestPushRecoversFromServerSideObjectLoss(t *testing.T) {
	f := newFixture(t)
	f.addStep(t, map[string]string{"a.txt": "one"}, "first")
	if _, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef); err != nil {
		t.Fatalf("first push: %v", err)
	}

	tip := f.addStep(t, map[string]string{"a.txt": "two"}, "second")
	tipStep, err := f.cache.ReadStep(tip)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}

	// Upload the delta, then delete the new tree behind the server's back and
	// force the ref update to be re-attempted from a clean high-water mark.
	if _, _, err := uploadRange(context.Background(), f.cache, f.cli, tip, "", false); err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	f.srv.DropObject(tipStep.Tree)
	if err := f.spool.ForgetPushed(testRef); err != nil {
		t.Fatalf("forget mark: %v", err)
	}

	if _, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef); err != nil {
		t.Fatalf("push after server data loss: %v", err)
	}
	if f.srv.Ref(testRef) != tip {
		t.Fatalf("server ref = %s, want %s", f.srv.Ref(testRef), tip)
	}
	assertServerHasChain(t, f, tip)
}

// A server-side loss deeper in the history is invisible to the server's own
// reachability check (it only inspects the step being pointed at), so a normal
// push cannot notice it. Repair is the explicit remedy, and it must actually
// restore every ancestor object.
func TestRepairRestoresLostAncestorObjects(t *testing.T) {
	f := newFixture(t)
	first := f.addStep(t, map[string]string{"a.txt": "one"}, "first")
	tip := f.addStep(t, map[string]string{"a.txt": "two"}, "second")
	if _, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef); err != nil {
		t.Fatalf("push: %v", err)
	}

	firstStep, err := f.cache.ReadStep(first)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}
	f.srv.DropObject(firstStep.Tree)

	// A normal push is a no-op here: as far as both sides know, they agree.
	res, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef)
	if err != nil || !res.AlreadyCurrent {
		t.Fatalf("push = %+v, %v; want a no-op", res, err)
	}
	if _, ok := f.srv.Objects()[firstStep.Tree]; ok {
		t.Fatal("test setup failed: the ancestor tree should still be missing")
	}

	res, err = Repair(context.Background(), f.cache, f.cli, f.spool, testRef)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if res.Objects != 1 {
		t.Fatalf("repair uploaded %d objects, want exactly the 1 that was missing", res.Objects)
	}
	if f.srv.Ref(testRef) != tip {
		t.Fatalf("repair changed the ref to %s, want %s", f.srv.Ref(testRef), tip)
	}
	assertServerHasChain(t, f, tip)
}

func TestPushLeavesNoDanglingRefWhenTheNetworkDies(t *testing.T) {
	f := newFixture(t)
	f.addStep(t, map[string]string{"a.txt": "one", "b.txt": "two", "c.txt": "three"}, "first")

	// Fail partway through the object uploads: enough succeed to be a partial
	// write, and the ref update never happens.
	f.srv.InjectFaults(
		remotetest.Fault{},
		remotetest.Fault{},
		remotetest.Fault{Hangup: true},
		remotetest.Fault{Hangup: true},
		remotetest.Fault{Hangup: true},
	)

	if _, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef); err == nil {
		t.Fatal("expected the push to fail")
	}
	if f.srv.Ref(testRef) != "" {
		t.Fatal("the server ref must not advance when objects did not all land")
	}
	if pushed, _ := f.spool.PushedTip(testRef); pushed != "" {
		t.Fatal("no high-water mark may be recorded for a failed push")
	}

	// Retrying after the network recovers must converge, not double-write.
	res, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef)
	if err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if f.srv.Ref(testRef) != res.Tip {
		t.Fatal("retry did not advance the server ref")
	}
	assertServerHasChain(t, f, res.Tip)
}

func TestFlushKeepsWorkQueuedWhileOffline(t *testing.T) {
	f := newFixture(t)
	f.addStep(t, map[string]string{"a.txt": "one"}, "first")

	loose, err := f.cache.WriteBlob([]byte("archived transcript"))
	if err != nil {
		t.Fatalf("write loose object: %v", err)
	}
	if err := f.spool.MarkObject(loose); err != nil {
		t.Fatalf("mark object: %v", err)
	}

	f.srv.SetOffline(true)
	res := Flush(context.Background(), f.cache, f.cli, f.spool)
	if !res.Failed() {
		t.Fatal("flush against an offline server must report failure")
	}

	status, err := f.spool.Status(f.cache)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Clean() {
		t.Fatal("work must stay queued after a failed flush")
	}
	if status.PendingRefs != 1 || len(status.LooseObjects) != 1 {
		t.Fatalf("status = %+v, want 1 pending ref and 1 loose object", status)
	}

	// Recovery delivers everything, with no manual intervention.
	f.srv.SetOffline(false)
	tip := f.addStep(t, map[string]string{"a.txt": "two"}, "second")
	res = Flush(context.Background(), f.cache, f.cli, f.spool)
	if res.Failed() {
		t.Fatalf("flush after recovery: %v", res.Err())
	}

	status, err = f.spool.Status(f.cache)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Clean() {
		t.Fatalf("outbox not drained: %+v", status)
	}
	if f.srv.Ref(testRef) != tip {
		t.Fatalf("server ref = %s, want %s", f.srv.Ref(testRef), tip)
	}
	if _, ok := f.srv.Objects()[loose]; !ok {
		t.Fatal("queued loose object was never delivered")
	}
}

func TestHydrateRebuildsACacheFromTheServer(t *testing.T) {
	f := newFixture(t)
	f.addStep(t, map[string]string{"a.txt": "one"}, "first")
	tip := f.addStep(t, map[string]string{"a.txt": "two", "b.txt": "new"}, "second")
	if _, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef); err != nil {
		t.Fatalf("push: %v", err)
	}

	// A brand new, empty cache: the only source of this history is the server.
	fresh, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open fresh cache: %v", err)
	}
	res, err := Hydrate(context.Background(), fresh, f.cli, testRef)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if res.Tip != tip || res.Steps != 2 {
		t.Fatalf("hydrate = %+v, want tip %s and 2 steps", res, tip)
	}
	if got, err := fresh.ReadRef(testRef); err != nil || got != tip {
		t.Fatalf("hydrated ref = %s, %v; want %s", got, err, tip)
	}

	// The rebuilt cache must contain identical bytes, step for step.
	for current := tip; current != ""; {
		original, err := f.cache.ReadStep(current)
		if err != nil {
			t.Fatalf("read original step: %v", err)
		}
		rebuilt, err := fresh.ReadStep(current)
		if err != nil {
			t.Fatalf("read hydrated step %s: %v", current, err)
		}
		if rebuilt.Tree != original.Tree || rebuilt.SessionID != original.SessionID {
			t.Fatalf("hydrated step %s differs from the original", current)
		}
		tree, err := fresh.ReadTree(rebuilt.Tree)
		if err != nil {
			t.Fatalf("read hydrated tree: %v", err)
		}
		for _, entry := range tree.Entries {
			if !fresh.ObjectExists(entry.Blob) {
				t.Fatalf("hydrated cache is missing file blob %s for %s", entry.Blob, entry.Path)
			}
		}
		current = rebuilt.Parent
	}
}

func TestPushRejectsUnsafeRefNames(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"", "sessions/../escape", "sessions/bad name"} {
		if _, err := Push(context.Background(), f.cache, f.cli, f.spool, name); err == nil {
			t.Errorf("Push(%q) = nil error, want a validation failure", name)
		}
	}
}

func TestPushOnMissingLocalRefIsANoOp(t *testing.T) {
	f := newFixture(t)
	res, err := Push(context.Background(), f.cache, f.cli, f.spool, testRef)
	if err != nil {
		t.Fatalf("Push on empty cache: %v", err)
	}
	if res.Tip != "" || res.Objects != 0 {
		t.Fatalf("result = %+v, want an empty no-op", res)
	}
}

// assertServerHasChain checks that every object the history depends on is on
// the server: steps, trees, file blobs and tool payloads.
func assertServerHasChain(t *testing.T, f *fixture, tip store.Hash) {
	t.Helper()

	objects := f.srv.Objects()
	for current := tip; current != ""; {
		if _, ok := objects[current]; !ok {
			t.Fatalf("server is missing step %s", current)
		}
		step, err := f.cache.ReadStep(current)
		if err != nil {
			t.Fatalf("read step %s: %v", current, err)
		}
		if _, ok := objects[step.Tree]; !ok {
			t.Fatalf("server is missing tree %s", step.Tree)
		}
		tree, err := f.cache.ReadTree(step.Tree)
		if err != nil {
			t.Fatalf("read tree %s: %v", step.Tree, err)
		}
		for _, entry := range tree.Entries {
			if _, ok := objects[entry.Blob]; !ok {
				t.Fatalf("server is missing blob %s for %s", entry.Blob, entry.Path)
			}
		}
		for _, cause := range step.Causes {
			if _, ok := objects[cause.ArgsBlob]; !ok {
				t.Fatalf("server is missing tool args %s", cause.ArgsBlob)
			}
			if _, ok := objects[cause.ResultBlob]; !ok {
				t.Fatalf("server is missing tool result %s", cause.ResultBlob)
			}
		}
		current = step.Parent
	}
}
