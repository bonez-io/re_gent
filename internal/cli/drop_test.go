package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/regent-vcs/regent/internal/store"
)

// dropFixture builds a local repo whose sessions mirror how re_gent actually
// captures them: each session's step is a FULL workspace snapshot with NO
// parent pointer (Parent==""), ordered only by a monotonic timestamp. drop's
// base is reconstructed by time, so these fixtures exercise the real path (and
// would catch the "empty base → whole-workspace wipe" regression).
type dropFixture struct {
	dir string
	s   *store.Store
	ts  int64
}

func newDropFixture(t *testing.T) *dropFixture {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Init(dir)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	return &dropFixture{dir: dir, s: s, ts: 1000}
}

func (f *dropFixture) blob(t *testing.T, content string) store.Hash {
	t.Helper()
	h, err := f.s.WriteBlob([]byte(content))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return h
}

func (f *dropFixture) tree(t *testing.T, files map[string]string) store.Hash {
	t.Helper()
	tr := &store.Tree{}
	for p, c := range files {
		tr.Entries = append(tr.Entries, store.TreeEntry{Path: p, Blob: f.blob(t, c)})
	}
	h, err := f.s.WriteTree(tr)
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	return h
}

// session records one session capturing the full workspace `files`, rooted
// (Parent="") like real capture, at a strictly-increasing timestamp.
func (f *dropFixture) session(t *testing.T, sessionID string, files map[string]string) store.Hash {
	t.Helper()
	f.ts++
	st := &store.Step{
		Parent:         "",
		Tree:           f.tree(t, files),
		SessionID:      sessionID,
		Origin:         "claude_code",
		TimestampNanos: f.ts,
	}
	h, err := f.s.WriteStep(st)
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	old, _ := f.s.ReadRef("sessions/" + sessionID)
	if err := f.s.UpdateRef("sessions/"+sessionID, old, h); err != nil {
		t.Fatalf("update ref: %v", err)
	}
	return h
}

func (f *dropFixture) writeWorkspace(t *testing.T, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(f.dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatalf("write workspace file: %v", err)
		}
	}
}

func readFile(t *testing.T, dir, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b), true
}

// treeHasPath reports whether the tree at the session ref's tip contains path.
func (f *dropFixture) tipTreeHas(t *testing.T, sessionID, path string) bool {
	t.Helper()
	tip, err := f.s.ReadRef("sessions/" + sessionID)
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	st, err := f.s.ReadStep(tip)
	if err != nil {
		t.Fatalf("read step: %v", err)
	}
	tr, err := f.s.ReadTree(st.Tree)
	if err != nil {
		t.Fatalf("read tree: %v", err)
	}
	for _, e := range tr.Entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

// Disjoint: dropping B removes only the file B added; A's and C's work survive.
// Also asserts B's own ref advanced to a drop step whose tree no longer has the
// dropped file.
func TestDrop_DisjointRemovesOnlyTheSession(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"keep.txt": "A"})
	tipB := f.session(t, "claude_code--B", map[string]string{"keep.txt": "A", "drop.txt": "B"})
	f.session(t, "claude_code--C", map[string]string{"keep.txt": "A", "drop.txt": "B", "other.txt": "C"})
	f.writeWorkspace(t, map[string]string{"keep.txt": "A", "drop.txt": "B", "other.txt": "C"})

	if err := runDrop(f.dir, "claude_code--B", false); err != nil {
		t.Fatalf("drop B: %v", err)
	}
	if _, ok := readFile(t, f.dir, "drop.txt"); ok {
		t.Error("drop.txt should have been removed")
	}
	if v, ok := readFile(t, f.dir, "keep.txt"); !ok || v != "A" {
		t.Errorf("keep.txt = %q,%v; want A", v, ok)
	}
	if v, ok := readFile(t, f.dir, "other.txt"); !ok || v != "C" {
		t.Errorf("other.txt = %q,%v; want C (session C must survive)", v, ok)
	}
	// The session ref must have advanced past its old tip to a drop step whose
	// tree no longer contains the dropped file.
	if newTip, _ := f.s.ReadRef("sessions/claude_code--B"); newTip == tipB {
		t.Error("sessions/claude_code--B ref was not advanced to a drop step")
	}
	if f.tipTreeHas(t, "claude_code--B", "drop.txt") {
		t.Error("drop step's tree should no longer contain drop.txt")
	}
}

// Content-modify: B changed a file A created; drop reverts it to A's value.
func TestDrop_RevertsAModifiedFile(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"config.txt": "v1"})
	f.session(t, "claude_code--B", map[string]string{"config.txt": "v2"})
	f.writeWorkspace(t, map[string]string{"config.txt": "v2"})

	if err := runDrop(f.dir, "claude_code--B", false); err != nil {
		t.Fatalf("drop B: %v", err)
	}
	if v, ok := readFile(t, f.dir, "config.txt"); !ok || v != "v1" {
		t.Errorf("config.txt = %q,%v; want v1 (reverted to A's value)", v, ok)
	}
}

// Overlap: a later session changed a file the drop would revert → conflict,
// abort, nothing on disk changes.
func TestDrop_ConflictAbortsWithoutWriting(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"keep.txt": "A"})
	f.session(t, "claude_code--B", map[string]string{"keep.txt": "A", "shared.txt": "B"})
	f.session(t, "claude_code--C", map[string]string{"keep.txt": "A", "shared.txt": "C-changed"})
	f.writeWorkspace(t, map[string]string{"keep.txt": "A", "shared.txt": "C-changed"})

	if err := runDrop(f.dir, "claude_code--B", false); err == nil {
		t.Fatal("expected a conflict error when a later session changed a reverted file")
	}
	if v, _ := readFile(t, f.dir, "shared.txt"); v != "C-changed" {
		t.Errorf("shared.txt = %q; a conflicting drop must not touch the workspace", v)
	}
}

// Delete-then-recreate: B deleted a file, C recreated it differently → dropping
// B (which would re-add the base value) conflicts with C's version.
func TestDrop_DeleteThenRecreateConflicts(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"x.txt": "A"})
	f.session(t, "claude_code--B", map[string]string{}) // B removed x.txt
	f.session(t, "claude_code--C", map[string]string{"x.txt": "C-new"})
	f.writeWorkspace(t, map[string]string{"x.txt": "C-new"})

	if err := runDrop(f.dir, "claude_code--B", false); err == nil {
		t.Fatal("expected a conflict: B deleted x.txt, C recreated it differently")
	}
	if v, _ := readFile(t, f.dir, "x.txt"); v != "C-new" {
		t.Errorf("x.txt = %q; conflicting drop must not touch the workspace", v)
	}
}

// --dry-run previews without changing disk.
func TestDrop_DryRunTouchesNothing(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"keep.txt": "A"})
	f.session(t, "claude_code--B", map[string]string{"keep.txt": "A", "drop.txt": "B"})
	f.writeWorkspace(t, map[string]string{"keep.txt": "A", "drop.txt": "B"})

	if err := runDrop(f.dir, "claude_code--B", true); err != nil {
		t.Fatalf("dry-run drop: %v", err)
	}
	if _, ok := readFile(t, f.dir, "drop.txt"); !ok {
		t.Error("--dry-run must not modify the workspace")
	}
}

// After a drop, an undo checkpoint session ref exists so the drop is reversible.
func TestDrop_RecordsUndoCheckpoint(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"keep.txt": "A"})
	f.session(t, "claude_code--B", map[string]string{"keep.txt": "A", "drop.txt": "B"})
	f.writeWorkspace(t, map[string]string{"keep.txt": "A", "drop.txt": "B"})

	if err := runDrop(f.dir, "claude_code--B", false); err != nil {
		t.Fatalf("drop B: %v", err)
	}
	refs, err := f.s.ListRefs("sessions")
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	found := false
	for name := range refs {
		if len(name) >= 8 && name[:8] == "rewind--" {
			found = true
		}
	}
	if !found {
		t.Error("expected a rewind-- undo checkpoint session ref after a drop")
	}
}

// turn records one captured turn chained on `parent` (the session's previous
// step, or "" for its first), so tests can build multi-turn sessions.
func (f *dropFixture) turn(t *testing.T, sessionID string, parent store.Hash, files map[string]string) store.Hash {
	t.Helper()
	f.ts++
	st := &store.Step{Parent: parent, Tree: f.tree(t, files), SessionID: sessionID, Origin: "claude_code", TimestampNanos: f.ts}
	h, err := f.s.WriteStep(st)
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	old, _ := f.s.ReadRef("sessions/" + sessionID)
	if err := f.s.UpdateRef("sessions/"+sessionID, old, h); err != nil {
		t.Fatalf("update ref: %v", err)
	}
	return h
}

// T1: dropping the earliest session is refused (its snapshot includes
// pre-existing files, so reverting would clear the workspace).
func TestDrop_RefusesEarliestSession(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"pre.txt": "existed", "a.txt": "A"})
	f.writeWorkspace(t, map[string]string{"pre.txt": "existed", "a.txt": "A"})

	if err := runDrop(f.dir, "claude_code--A", false); err == nil {
		t.Fatal("expected refusal when dropping the earliest recorded session")
	}
	if v, ok := readFile(t, f.dir, "pre.txt"); !ok || v != "existed" {
		t.Errorf("pre.txt = %q,%v; refusing must not touch the workspace", v, ok)
	}
}

// T2: a rewind-- checkpoint ref (holding a PRE-change tree) must NOT be chosen
// as a session's base.
func TestDrop_IgnoresRewindCheckpointAsBase(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"x.txt": "A"})
	// Synthetic checkpoint with a STALE tree, timestamped between A and B so a
	// naive "most recent before B" scan would wrongly prefer it.
	f.session(t, "rewind--999", map[string]string{"x.txt": "STALE"})
	f.session(t, "claude_code--B", map[string]string{"x.txt": "A", "b.txt": "B"})
	f.writeWorkspace(t, map[string]string{"x.txt": "A", "b.txt": "B"})

	if err := runDrop(f.dir, "claude_code--B", false); err != nil {
		t.Fatalf("drop B: %v", err)
	}
	if v, ok := readFile(t, f.dir, "x.txt"); !ok || v != "A" {
		t.Errorf("x.txt = %q,%v; want A (rewind checkpoint must not be the base)", v, ok)
	}
	if _, ok := readFile(t, f.dir, "b.txt"); ok {
		t.Error("b.txt should have been removed")
	}
}

// T5: a multi-turn session — sessionFirstStepTime must find the EARLIEST turn,
// so the whole session's contribution (both turns) is reverted.
func TestDrop_MultiTurnSession(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"keep.txt": "A"})
	t1 := f.turn(t, "claude_code--B", "", map[string]string{"keep.txt": "A", "b1.txt": "1"})
	f.turn(t, "claude_code--B", t1, map[string]string{"keep.txt": "A", "b1.txt": "1", "b2.txt": "2"})
	f.writeWorkspace(t, map[string]string{"keep.txt": "A", "b1.txt": "1", "b2.txt": "2"})

	if err := runDrop(f.dir, "claude_code--B", false); err != nil {
		t.Fatalf("drop B: %v", err)
	}
	if _, ok := readFile(t, f.dir, "b1.txt"); ok {
		t.Error("b1.txt (B turn 1) should be removed")
	}
	if _, ok := readFile(t, f.dir, "b2.txt"); ok {
		t.Error("b2.txt (B turn 2) should be removed")
	}
	if v, ok := readFile(t, f.dir, "keep.txt"); !ok || v != "A" {
		t.Errorf("keep.txt = %q,%v; want A", v, ok)
	}
}

// T6: after a drop, the session's new tip is a drop step parented on the old
// tip, tagged TurnID "drop", preserving lineage.
func TestDrop_DropStepMetadata(t *testing.T) {
	f := newDropFixture(t)
	f.session(t, "claude_code--A", map[string]string{"keep.txt": "A"})
	oldTip := f.session(t, "claude_code--B", map[string]string{"keep.txt": "A", "drop.txt": "B"})
	f.writeWorkspace(t, map[string]string{"keep.txt": "A", "drop.txt": "B"})

	if err := runDrop(f.dir, "claude_code--B", false); err != nil {
		t.Fatalf("drop B: %v", err)
	}
	newTip, err := f.s.ReadRef("sessions/claude_code--B")
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if newTip == oldTip {
		t.Fatal("session ref was not advanced to a drop step")
	}
	step, err := f.s.ReadStep(newTip)
	if err != nil {
		t.Fatalf("read drop step: %v", err)
	}
	if step.Parent != oldTip {
		t.Errorf("drop step Parent = %s; want old tip %s", step.Parent, oldTip)
	}
	if step.TurnID != "drop" {
		t.Errorf("drop step TurnID = %q; want \"drop\"", step.TurnID)
	}
	if step.SessionID != "claude_code--B" {
		t.Errorf("drop step SessionID = %q; want claude_code--B", step.SessionID)
	}
}
