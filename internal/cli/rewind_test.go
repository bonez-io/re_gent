package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/ignore"
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/snapshot"
	"github.com/bonez-io/re_gent/internal/store"
)

// initTestRepo creates a temporary .regent store and index for testing.
func initTestRepo(t *testing.T) (string, *store.Store, *index.DB) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Init(dir)
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return dir, s, idx
}

// writeTestFile writes content to path relative to root, creating parent dirs.
func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// snapshotTestDir snapshots dir and returns the tree hash and tree object.
func snapshotTestDir(t *testing.T, s *store.Store, dir string) (store.Hash, *store.Tree) {
	t.Helper()
	ig := ignore.Default(dir)
	h, err := snapshot.Snapshot(s, dir, ig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	tree, err := s.ReadTree(h)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	return h, tree
}

// makeRewindStep writes a step and indexes it, returning the step hash.
func makeRewindStep(t *testing.T, s *store.Store, idx *index.DB, sessionID string, parentHash, treeHash store.Hash, tree *store.Tree) store.Hash {
	t.Helper()
	step := &store.Step{
		Parent:         parentHash,
		Tree:           treeHash,
		SessionID:      sessionID,
		Origin:         "claude_code",
		TurnID:         fmt.Sprintf("turn-%d", time.Now().UnixNano()),
		TimestampNanos: time.Now().UnixNano(),
	}
	h, err := s.WriteStep(step)
	if err != nil {
		t.Fatalf("WriteStep: %v", err)
	}
	if err := idx.IndexStep(h, step, tree); err != nil {
		t.Fatalf("IndexStep: %v", err)
	}
	if err := idx.UpsertSession(index.SessionUpdate{ID: sessionID, Origin: "claude_code"}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if err := s.UpdateRef("sessions/"+sessionID, parentHash, h); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	return h
}

// assertFileContent fails the test if the file's content doesn't match want.
func assertFileContent(t *testing.T, root, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if string(got) != want {
		t.Errorf("%s: got %q, want %q", rel, got, want)
	}
}

// TestResolveTreeHash_RejectsStepHash verifies that passing a step hash to
// resolveTreeHash returns an error mentioning "step, not a tree".
// This is the CLI regression test for the --tree-with-step-hash footgun.
func TestResolveTreeHash_RejectsStepHash(t *testing.T) {
	dir, s, idx := initTestRepo(t)
	writeTestFile(t, dir, "hello.txt", "hello\n")
	treeHash, tree := snapshotTestDir(t, s, dir)
	stepHash := makeRewindStep(t, s, idx, "sess-1", "", treeHash, tree)

	_, _, err := resolveTreeHash(s, stepHash)
	if err == nil {
		t.Fatal("expected error when passing step hash to resolveTreeHash, got nil")
	}
	if !strings.Contains(err.Error(), "step, not a tree") {
		t.Errorf("error %q does not contain 'step, not a tree'", err.Error())
	}
}

// TestResolveTreeHash_AcceptsTreeHash verifies that a real tree hash is accepted.
func TestResolveTreeHash_AcceptsTreeHash(t *testing.T) {
	dir, s, _ := initTestRepo(t)
	writeTestFile(t, dir, "hello.txt", "hello\n")
	treeHash, _ := snapshotTestDir(t, s, dir)

	gotHash, gotTree, err := resolveTreeHash(s, treeHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHash != treeHash {
		t.Errorf("hash mismatch: got %s, want %s", gotHash, treeHash)
	}
	if len(gotTree.Entries) != 1 {
		t.Errorf("expected 1 tree entry, got %d", len(gotTree.Entries))
	}
}

// TestResolveTreeHash_DetectionIsNotJustUnmarshal verifies the detection is
// based on session_id presence, not just successful JSON unmarshal.
// A tree blob unmarshals into a Step struct with all-zero values — the
// discriminator must be the non-empty session_id field.
func TestResolveTreeHash_DetectionIsNotJustUnmarshal(t *testing.T) {
	_, s, _ := initTestRepo(t)

	treeJSON, _ := json.Marshal(store.Tree{Entries: []store.TreeEntry{
		{Path: "a.txt", Blob: "aabbcc"},
	}})
	treeBlob, err := s.WriteBlob(treeJSON)
	if err != nil {
		t.Fatalf("WriteBlob tree: %v", err)
	}

	stepJSON, _ := json.Marshal(store.Step{
		SessionID:      "test-session",
		Origin:         "claude_code",
		TimestampNanos: time.Now().UnixNano(),
		Tree:           treeBlob,
	})
	stepBlob, err := s.WriteBlob(stepJSON)
	if err != nil {
		t.Fatalf("WriteBlob step: %v", err)
	}

	// Tree blob must succeed.
	if _, _, err := resolveTreeHash(s, treeBlob); err != nil {
		t.Errorf("tree blob unexpectedly rejected: %v", err)
	}

	// Step blob must be rejected.
	if _, _, err := resolveTreeHash(s, stepBlob); err == nil {
		t.Error("step blob was not rejected by resolveTreeHash")
	}
}

// TestComputeRewindDiff checks that adds, modifies, and deletes are computed correctly.
func TestComputeRewindDiff(t *testing.T) {
	_, s, _ := initTestRepo(t)

	blobA, _ := s.WriteBlob([]byte("aaa"))
	blobB, _ := s.WriteBlob([]byte("bbb"))
	blobC, _ := s.WriteBlob([]byte("ccc"))
	blobD, _ := s.WriteBlob([]byte("ddd"))

	current := &store.Tree{Entries: []store.TreeEntry{
		{Path: "same.txt", Blob: blobA},
		{Path: "changed.txt", Blob: blobB},
		{Path: "deleted.txt", Blob: blobC},
	}}
	target := &store.Tree{Entries: []store.TreeEntry{
		{Path: "same.txt", Blob: blobA},    // unchanged
		{Path: "changed.txt", Blob: blobD}, // modified
		{Path: "added.txt", Blob: blobC},   // new
	}}

	d := computeRewindDiff(current, target)

	if len(d.adds) != 1 || d.adds[0].Path != "added.txt" {
		t.Errorf("adds: got %v, want [{added.txt ...}]", d.adds)
	}
	if len(d.modifies) != 1 || d.modifies[0].Path != "changed.txt" {
		t.Errorf("modifies: got %v, want [{changed.txt ...}]", d.modifies)
	}
	if len(d.deletes) != 1 || d.deletes[0].Path != "deleted.txt" {
		t.Errorf("deletes: got %v, want [{deleted.txt ...}]", d.deletes)
	}
}

// TestApplyRewindToWorkspace_WritesAndDeletes verifies that files are written
// and deleted correctly with write-before-delete ordering.
func TestApplyRewindToWorkspace_WritesAndDeletes(t *testing.T) {
	dir, s, _ := initTestRepo(t)

	writeTestFile(t, dir, "keep.txt", "keep\n")
	writeTestFile(t, dir, "modify.txt", "old content\n")
	writeTestFile(t, dir, "delete.txt", "gone\n")

	ig := ignore.Default(dir)
	curHash, err := snapshot.Snapshot(s, dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	curTree, err := s.ReadTree(curHash)
	if err != nil {
		t.Fatal(err)
	}

	keepEntry := curTree.FindEntry("keep.txt")
	if keepEntry == nil {
		t.Fatal("keep.txt not in snapshot")
	}
	blobModNew, _ := s.WriteBlob([]byte("new content\n"))
	blobAdd, _ := s.WriteBlob([]byte("brand new\n"))

	target := &store.Tree{Entries: []store.TreeEntry{
		{Path: "keep.txt", Blob: keepEntry.Blob},
		{Path: "modify.txt", Blob: blobModNew},
		{Path: "add.txt", Blob: blobAdd},
	}}

	d := computeRewindDiff(curTree, target)
	if err := applyRewindToWorkspace(s, dir, d); err != nil {
		t.Fatalf("applyRewindToWorkspace: %v", err)
	}

	assertFileContent(t, dir, "keep.txt", "keep\n")
	assertFileContent(t, dir, "modify.txt", "new content\n")
	assertFileContent(t, dir, "add.txt", "brand new\n")
	if _, err := os.Stat(filepath.Join(dir, "delete.txt")); !os.IsNotExist(err) {
		t.Errorf("delete.txt should have been removed")
	}
}

// TestApplyRewindToWorkspace_IgnoredPathsUntouched verifies that .regent/,
// .git/, and node_modules/ are never snapshotted, so they provably never
// appear in a rewind diff and are never touched.
func TestApplyRewindToWorkspace_IgnoredPathsUntouched(t *testing.T) {
	dir, s, _ := initTestRepo(t)

	writeTestFile(t, dir, ".git/config", "[core]\n")
	writeTestFile(t, dir, "node_modules/pkg/index.js", "module.exports = {}\n")
	writeTestFile(t, dir, ".regent/log/hook.log", "hook log\n")
	writeTestFile(t, dir, "tracked.txt", "tracked\n")

	ig := ignore.Default(dir)
	h, err := snapshot.Snapshot(s, dir, ig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	tree, err := s.ReadTree(h)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}

	ignoredPrefixes := []string{".git/", "node_modules/", ".regent/"}
	for _, entry := range tree.Entries {
		for _, pfx := range ignoredPrefixes {
			if strings.HasPrefix(entry.Path, pfx) {
				t.Errorf("snapshot included ignored path: %s", entry.Path)
			}
		}
	}

	if len(tree.Entries) != 1 || tree.Entries[0].Path != "tracked.txt" {
		t.Errorf("expected only tracked.txt, got %v", tree.Entries)
	}

	// Build a diff from this tree to itself — zero changes.
	d := computeRewindDiff(tree, tree)
	if len(d.adds)+len(d.modifies)+len(d.deletes) != 0 {
		t.Errorf("self-diff should be empty, got %+v", d)
	}
}

// TestRewindCmd_RefusesEmptyTree ensures that rewinding to an empty tree
// over a non-empty workspace without --allow-empty returns an error.
func TestRewindCmd_RefusesEmptyTree(t *testing.T) {
	dir, s, idx := initTestRepo(t)

	emptyTree := &store.Tree{Entries: []store.TreeEntry{}}
	emptyTreeHash, err := s.WriteTree(emptyTree)
	if err != nil {
		t.Fatalf("WriteTree: %v", err)
	}
	emptyStep := &store.Step{
		Tree:           emptyTreeHash,
		SessionID:      "sess-empty",
		Origin:         "claude_code",
		TurnID:         "t1",
		TimestampNanos: time.Now().UnixNano(),
	}
	emptyStepHash, err := s.WriteStep(emptyStep)
	if err != nil {
		t.Fatalf("WriteStep: %v", err)
	}
	if err := idx.IndexStep(emptyStepHash, emptyStep, emptyTree); err != nil {
		t.Fatalf("IndexStep: %v", err)
	}
	if err := idx.UpsertSession(index.SessionUpdate{ID: "sess-empty", Origin: "claude_code"}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if err := s.UpdateRef("sessions/sess-empty", "", emptyStepHash); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	writeTestFile(t, dir, "real.txt", "content\n")

	withWorkingDir(t, dir)

	cmd := RewindCmd()
	cmd.SetArgs([]string{string(emptyStepHash)})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for empty-tree rewind, got nil")
	}
	if !strings.Contains(err.Error(), "allow-empty") {
		t.Errorf("error %q should mention --allow-empty", err.Error())
	}
}

// TestRewindCmd_TreeFlagRejectsStepHash is the CLI regression test for the
// --tree-with-step-hash footgun: passing a step hash to --tree must fail with
// a clear error and must not modify the workspace.
func TestRewindCmd_TreeFlagRejectsStepHash(t *testing.T) {
	dir, s, idx := initTestRepo(t)

	writeTestFile(t, dir, "a.txt", "a\n")
	treeHash, tree := snapshotTestDir(t, s, dir)
	stepHash := makeRewindStep(t, s, idx, "sess-tfrsh", "", treeHash, tree)

	withWorkingDir(t, dir)

	cmd := RewindCmd()
	cmd.SetArgs([]string{"--tree", string(stepHash)})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when passing step hash to --tree, got nil")
	}
	if !strings.Contains(err.Error(), "step, not a tree") {
		t.Errorf("error %q should contain 'step, not a tree'", err.Error())
	}
	// Workspace must be unmodified.
	assertFileContent(t, dir, "a.txt", "a\n")
}

// TestWriteRewindCheckpoint verifies the checkpoint step is written with
// a valid session ref and can be read back.
func TestWriteRewindCheckpoint(t *testing.T) {
	dir, s, idx := initTestRepo(t)

	writeTestFile(t, dir, "file.txt", "data\n")
	treeHash, tree := snapshotTestDir(t, s, dir)

	stepHash, err := writeRewindCheckpoint(s, idx, treeHash, tree)
	if err != nil {
		t.Fatalf("writeRewindCheckpoint: %v", err)
	}

	step, err := s.ReadStep(stepHash)
	if err != nil {
		t.Fatalf("ReadStep: %v", err)
	}
	if step.Tree != treeHash {
		t.Errorf("step.Tree = %s, want %s", step.Tree, treeHash)
	}
	if step.Origin != "rewind" {
		t.Errorf("step.Origin = %s, want rewind", step.Origin)
	}
	if !strings.HasPrefix(step.SessionID, "rewind--") {
		t.Errorf("step.SessionID = %s, want prefix rewind--", step.SessionID)
	}

	refHash, err := s.ReadRef("sessions/" + step.SessionID)
	if err != nil {
		t.Fatalf("ReadRef: %v", err)
	}
	if refHash != stepHash {
		t.Errorf("ref hash = %s, want %s", refHash, stepHash)
	}
}

// TestRewindDryRun_NoWorkspaceChange checks that --dry-run prints the diff
// without touching any files.
func TestRewindDryRun_NoWorkspaceChange(t *testing.T) {
	dir, s, idx := initTestRepo(t)

	writeTestFile(t, dir, "hello.txt", "hello\n")
	tree1Hash, tree1 := snapshotTestDir(t, s, dir)
	step1Hash := makeRewindStep(t, s, idx, "sess-dr", "", tree1Hash, tree1)

	// Advance workspace beyond step1.
	writeTestFile(t, dir, "hello.txt", "world\n")
	writeTestFile(t, dir, "extra.txt", "extra\n")

	withWorkingDir(t, dir)

	cmd := RewindCmd()
	cmd.SetArgs([]string{"--dry-run", string(step1Hash)})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry-run execute: %v", err)
	}

	// Workspace must remain unchanged.
	assertFileContent(t, dir, "hello.txt", "world\n")
	assertFileContent(t, dir, "extra.txt", "extra\n")
}
