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

const carryOverRef = "sessions/claude_code--afternoon-of-trying-it"

// carryOverFixture is the state of someone who tried rgt out locally before
// they thought about a server: a project with its own .regent/, a session, and
// the query index the read commands need. Connecting is about to point every
// read at a cache that has never seen any of it.
type carryOverFixture struct {
	local    *store.Store
	cfg      remote.Config
	srv      *remotetest.Server
	cacheDir string
}

func newCarryOverFixture(t *testing.T) *carryOverFixture {
	t.Helper()

	srv := remotetest.New()
	t.Cleanup(srv.Close)

	projectRoot := t.TempDir()
	local, err := store.Init(projectRoot)
	if err != nil {
		t.Fatalf("init local store: %v", err)
	}
	idx, err := index.Open(local)
	if err != nil {
		t.Fatalf("open local index: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close local index: %v", err)
	}

	cfg := remote.Config{
		ServerURL: srv.URL(),
		RepoID:    "tried-it-locally",
		CacheDir:  t.TempDir(),
		Timeout:   5 * time.Second,
	}
	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	return &carryOverFixture{local: local, cfg: cfg, srv: srv, cacheDir: cacheDir}
}

// addLocalStep records one step into the project's own store, the way a hook
// would have before the project was connected.
func addLocalStep(t *testing.T, s *store.Store, ref, path, content string) store.Hash {
	t.Helper()

	parent, err := s.ReadRef(ref)
	if err != nil {
		parent = ""
	}
	blob, err := s.WriteBlob([]byte(content))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	treeHash, err := s.WriteTree(&store.Tree{Entries: []store.TreeEntry{{Path: path, Blob: blob}}})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	stepHash, err := s.WriteStep(&store.Step{
		Parent:         parent,
		Tree:           treeHash,
		SessionID:      "claude_code--afternoon-of-trying-it",
		Origin:         "claude_code",
		TimestampNanos: time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := s.UpdateRef(ref, parent, stepHash); err != nil {
		t.Fatalf("update ref %s: %v", ref, err)
	}
	return stepHash
}

func (f *carryOverFixture) run(t *testing.T) (carryOver, string) {
	t.Helper()
	var buf bytes.Buffer
	res := carryOverLocalHistory(&buf, f.local, f.cfg)
	return res, buf.String()
}

func (f *carryOverFixture) cache(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(f.cacheDir)
	if err != nil {
		t.Fatalf("open cache %s: %v", f.cacheDir, err)
	}
	return s
}

// The whole ticket in one assertion: what was recorded before connecting is in
// the cache the reads will now use, and on the server, and the user is told.
func TestCarryOverMovesLocalHistoryIntoTheCacheAndOntoTheServer(t *testing.T) {
	f := newCarryOverFixture(t)
	addLocalStep(t, f.local, carryOverRef, "hello.go", "package main\n")
	tip := addLocalStep(t, f.local, carryOverRef, "goodbye.go", "package main\n")

	res, out := f.run(t)

	if res.Failed() {
		t.Fatalf("carry-over reported problems %v:\n%s", res.Problems, out)
	}
	if res.Sessions != 1 || res.Steps != 2 {
		t.Errorf("carried %d session(s) / %d step(s), want 1 / 2:\n%s", res.Sessions, res.Steps, out)
	}
	if got, err := f.cache(t).ReadRef(carryOverRef); err != nil || got != tip {
		t.Errorf("cache ref = %q (err %v), want the local tip %q; the reads point here now", got, err, tip)
	}
	if got := f.srv.Ref(carryOverRef); got != tip {
		t.Errorf("server ref = %q, want %q; history that exists only in a disposable cache is still lost", got, tip)
	}
	// Without the index, log and show come back empty even with every object
	// present — the objects are the history, the index is how it is read.
	if _, err := os.Stat(filepath.Join(f.cacheDir, "index.db")); err != nil {
		t.Errorf("cache has no index.db after carry-over: %v", err)
	}
	if !strings.Contains(out, "1 session") || !strings.Contains(out, "2 step") {
		t.Errorf("carry-over does not say what it carried:\n%s", out)
	}
}

// A cache that already holds a session is the authority on it: it may be ahead
// of the project's own store, and clobbering the ref would discard whatever the
// difference is. Leaving it alone is only safe if the user is told.
func TestCarryOverLeavesRefsTheCacheAlreadyHolds(t *testing.T) {
	f := newCarryOverFixture(t)
	addLocalStep(t, f.local, carryOverRef, "hello.go", "package main\n")

	if err := os.MkdirAll(f.cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	cache, err := store.Open(f.cacheDir)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	held := addLocalStep(t, cache, carryOverRef, "already-here.go", "package main\n")

	res, out := f.run(t)

	if got, _ := f.cache(t).ReadRef(carryOverRef); got != held {
		t.Errorf("cache ref moved from %q to %q; carry-over overwrote history the cache already had", held, got)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], carryOverRef) {
		t.Errorf("skipped = %v, want the one ref that was left alone", res.Skipped)
	}
	if !strings.Contains(out, carryOverRef) {
		t.Errorf("carry-over never names the session it left behind:\n%s", out)
	}
	if strings.Contains(out, carriedOverHeadline) {
		t.Errorf("carry-over reports success while a session was left behind:\n%s", out)
	}
}

// The property this ticket exists for. Carrying history into the cache but
// failing to deliver it leaves it on one machine, in a directory the design
// calls disposable. Reporting that as success is the original silent loss in
// better clothes.
func TestCarryOverReportsAnUndeliveredSessionAsFailureNotSuccess(t *testing.T) {
	f := newCarryOverFixture(t)
	addLocalStep(t, f.local, carryOverRef, "hello.go", "package main\n")
	f.srv.SetOffline(true)

	res, out := f.run(t)

	if !res.Failed() {
		t.Fatalf("carry-over reports no problem after the upload failed:\n%s", out)
	}
	if strings.Contains(out, carriedOverHeadline) {
		t.Errorf("carry-over prints its success headline after failing to deliver:\n%s", out)
	}
	if !strings.Contains(out, carryOverRef) {
		t.Errorf("carry-over never names the session that did not reach the server:\n%s", out)
	}
	// The work is still in the cache, so there is a way out. Naming it is the
	// difference between a recoverable failure and a lost afternoon.
	if !strings.Contains(out, "rgt sync") {
		t.Errorf("carry-over does not say how to retry the delivery:\n%s", out)
	}
}

// Connecting a project that never recorded anything is the ordinary case. It
// must say so plainly and touch no network.
func TestCarryOverSaysPlainlyWhenThereIsNothingToCarry(t *testing.T) {
	f := newCarryOverFixture(t)

	res, out := f.run(t)

	if res.Failed() || res.Sessions != 0 || res.Steps != 0 {
		t.Errorf("carry-over invented work for an empty store: %+v\n%s", res, out)
	}
	if !strings.Contains(strings.ToLower(out), "no history") {
		t.Errorf("carry-over does not say there was nothing recorded before connecting:\n%s", out)
	}
	if n := f.srv.Requests("POST"); n != 0 {
		t.Errorf("carry-over made %d upload request(s) with nothing to upload", n)
	}
}
