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

// newFixtureOn is a second machine sharing one server: its own cache, its own
// spool, the same remote. Every question a pull has to answer — is this history
// new, mine, or someone else's — needs two caches to ask.
func newFixtureOn(t *testing.T, srv *remotetest.Server) *fixture {
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
	cli, err := NewHTTPClient(Config{ServerURL: srv.URL(), RepoID: "test-repo", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &fixture{cache: cache, spool: spool, srv: srv, cli: cli}
}

// The read half of the team story: a machine that has pushed nothing has to be
// able to get history it has never seen.
func TestPullBringsDownHistoryThisMachineNeverRecorded(t *testing.T) {
	teammate := newFixture(t)
	teammate.addStep(t, map[string]string{"a.txt": "one"}, "first")
	tip := teammate.addStep(t, map[string]string{"a.txt": "two"}, "second")
	if _, err := Push(context.Background(), teammate.cache, teammate.cli, teammate.spool, testRef); err != nil {
		t.Fatalf("teammate push: %v", err)
	}

	me := newFixtureOn(t, teammate.srv)
	res, err := Pull(context.Background(), me.cache, me.cli, testRef)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if res.Status != PullAdvanced {
		t.Errorf("status = %v, want PullAdvanced", res.Status)
	}
	if res.Steps != 2 {
		t.Errorf("pulled %d step(s), want 2", res.Steps)
	}
	local, err := me.cache.ReadRef(testRef)
	if err != nil || local != tip {
		t.Fatalf("local ref = %s, %v; want %s", local, err, tip)
	}
	// Present is not the same as readable: the file content behind the tip has
	// to be here too, or every history command is a broken link.
	step, err := me.cache.ReadStep(tip)
	if err != nil {
		t.Fatalf("read pulled step: %v", err)
	}
	tree, err := me.cache.ReadTree(step.Tree)
	if err != nil {
		t.Fatalf("read pulled tree: %v", err)
	}
	content, err := me.cache.ReadBlob(tree.Entries[0].Blob)
	if err != nil || string(content) != "two" {
		t.Fatalf("pulled file content = %q, %v; want %q", content, err, "two")
	}
}

// The failure that would be worse than the bug this command fixes: a pull that
// moves a session ref onto the server's history when the local history is not
// on the way there. The local work would still be in the object store, but no
// ref would point at it and no command would ever find it again.
func TestPullRefusesToOverwriteDivergedLocalHistory(t *testing.T) {
	teammate := newFixture(t)
	teammate.addStep(t, map[string]string{"a.txt": "theirs"}, "theirs")
	if _, err := Push(context.Background(), teammate.cache, teammate.cli, teammate.spool, testRef); err != nil {
		t.Fatalf("teammate push: %v", err)
	}

	me := newFixtureOn(t, teammate.srv)
	mine := me.addStep(t, map[string]string{"a.txt": "mine"}, "mine")

	res, err := Pull(context.Background(), me.cache, me.cli, testRef)
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("Pull on diverged ref = %v, want ErrDiverged", err)
	}
	if res.Status != PullDiverged {
		t.Errorf("status = %v, want PullDiverged", res.Status)
	}
	if local, err := me.cache.ReadRef(testRef); err != nil || local != mine {
		t.Fatalf("local ref = %s, %v; want it untouched at %s", local, err, mine)
	}
}

// Local work the server has not seen yet is not a conflict, but it is also not
// something to roll back: the server's tip is behind, so pointing the local ref
// at it would drop the steps that have not been delivered.
func TestPullDoesNotRewindARefThatIsAheadOfTheServer(t *testing.T) {
	me := newFixture(t)
	me.addStep(t, map[string]string{"a.txt": "delivered"}, "delivered")
	if _, err := Push(context.Background(), me.cache, me.cli, me.spool, testRef); err != nil {
		t.Fatalf("push: %v", err)
	}
	ahead := me.addStep(t, map[string]string{"a.txt": "not yet delivered"}, "pending")

	res, err := Pull(context.Background(), me.cache, me.cli, testRef)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if res.Status != PullLocalAhead {
		t.Errorf("status = %v, want PullLocalAhead", res.Status)
	}
	if local, err := me.cache.ReadRef(testRef); err != nil || local != ahead {
		t.Fatalf("local ref = %s, %v; want it left at %s", local, err, ahead)
	}
}

// Pulling twice must be a no-op rather than a second download.
func TestPullOnAnUpToDateRefFetchesNothing(t *testing.T) {
	teammate := newFixture(t)
	teammate.addStep(t, map[string]string{"a.txt": "one"}, "first")
	if _, err := Push(context.Background(), teammate.cache, teammate.cli, teammate.spool, testRef); err != nil {
		t.Fatalf("teammate push: %v", err)
	}

	me := newFixtureOn(t, teammate.srv)
	if _, err := Pull(context.Background(), me.cache, me.cli, testRef); err != nil {
		t.Fatalf("first Pull: %v", err)
	}

	res, err := Pull(context.Background(), me.cache, me.cli, testRef)
	if err != nil {
		t.Fatalf("second Pull: %v", err)
	}
	if res.Status != PullAlreadyCurrent {
		t.Errorf("status = %v, want PullAlreadyCurrent", res.Status)
	}
	if res.Objects != 0 {
		t.Errorf("second pull fetched %d object(s), want 0", res.Objects)
	}
}

// Discovery is the half of the ticket that makes the command usable: a fresh
// clone cannot name a session it has never seen.
func TestServerSessionRefsNamesEverySessionTheServerHolds(t *testing.T) {
	teammate := newFixture(t)
	teammate.addStep(t, map[string]string{"a.txt": "one"}, "first")
	if _, err := Push(context.Background(), teammate.cache, teammate.cli, teammate.spool, testRef); err != nil {
		t.Fatalf("teammate push: %v", err)
	}

	me := newFixtureOn(t, teammate.srv)
	refs, err := ServerSessionRefs(context.Background(), me.cli)
	if err != nil {
		t.Fatalf("ServerSessionRefs: %v", err)
	}
	if len(refs) != 1 || refs[0] != testRef {
		t.Fatalf("ServerSessionRefs = %v, want [%s]", refs, testRef)
	}
}
