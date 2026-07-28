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
