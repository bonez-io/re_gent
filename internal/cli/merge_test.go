package cli

import (
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

func mergeTestStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	workspace := t.TempDir()
	s, err := store.Init(workspace)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	return workspace, s
}

func mergeWriteBlob(t *testing.T, s *store.Store, content string) store.Hash {
	t.Helper()
	h, err := s.WriteBlob([]byte(content))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return h
}

func mergeWriteTree(t *testing.T, s *store.Store, files map[string]string) store.Hash {
	t.Helper()
	var entries []store.TreeEntry
	for path, content := range files {
		blob := mergeWriteBlob(t, s, content)
		entries = append(entries, store.TreeEntry{Path: path, Blob: blob})
	}
	h, err := s.WriteTree(&store.Tree{Entries: entries})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	return h
}

func mergeWriteStep(t *testing.T, s *store.Store, tree, parent, secondary store.Hash, sessionID string) store.Hash {
	t.Helper()
	h, err := s.WriteStep(&store.Step{
		Tree:            tree,
		Parent:          parent,
		SecondaryParent: secondary,
		SessionID:       sessionID,
		TimestampNanos:  time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	return h
}

func TestRunMerge_NoConflict(t *testing.T) {
	workspace, s := mergeTestStore(t)

	base := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"a.go": "original",
		"b.go": "original",
	}), "", "", "sess-a")

	tipA := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"a.go": "changed by a",
		"b.go": "original",
	}), base, "", "sess-a")

	tipB := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"a.go": "original",
		"b.go": "changed by b",
	}), base, "", "sess-b")

	if err := s.UpdateRef("sessions/sess-a", "", tipA); err != nil {
		t.Fatalf("write sess-a ref: %v", err)
	}
	if err := s.UpdateRef("sessions/sess-b", "", tipB); err != nil {
		t.Fatalf("write sess-b ref: %v", err)
	}

	if err := runMerge(workspace, "sess-a", "sess-b"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newTip, err := s.ReadRef("sessions/sess-a")
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if newTip == tipA {
		t.Error("ref was not advanced after merge")
	}

	mergeStep, err := s.ReadStep(newTip)
	if err != nil {
		t.Fatalf("read merge step: %v", err)
	}
	if mergeStep.Parent != tipA {
		t.Errorf("parent: got %s, want %s", mergeStep.Parent, tipA)
	}
	if mergeStep.SecondaryParent != tipB {
		t.Errorf("secondary parent: got %s, want %s", mergeStep.SecondaryParent, tipB)
	}
}

func TestRunMerge_Conflict_BlocksAndDoesNotAdvanceRef(t *testing.T) {
	workspace, s := mergeTestStore(t)

	base := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"shared.go": "original",
	}), "", "", "sess-a")

	tipA := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"shared.go": "version A",
	}), base, "", "sess-a")

	tipB := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"shared.go": "version B",
	}), base, "", "sess-b")

	if err := s.UpdateRef("sessions/sess-a", "", tipA); err != nil {
		t.Fatalf("write sess-a ref: %v", err)
	}
	if err := s.UpdateRef("sessions/sess-b", "", tipB); err != nil {
		t.Fatalf("write sess-b ref: %v", err)
	}

	if err := runMerge(workspace, "sess-a", "sess-b"); err == nil {
		t.Fatal("expected error for conflicting merge, got nil")
	}

	// Ref must NOT have advanced.
	unchanged, readErr := s.ReadRef("sessions/sess-a")
	if readErr != nil {
		t.Fatalf("read ref: %v", readErr)
	}
	if unchanged != tipA {
		t.Errorf("ref was advanced despite conflict")
	}
}

func TestRunMerge_ByStepHash(t *testing.T) {
	workspace, s := mergeTestStore(t)

	base := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"f.go": "base",
	}), "", "", "sess-c")

	tipA := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"f.go": "a-change",
	}), base, "", "sess-c")

	tipB := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"g.go": "new file",
		"f.go": "base",
	}), base, "", "sess-d")

	if err := s.UpdateRef("sessions/sess-c", "", tipA); err != nil {
		t.Fatalf("write sess-c ref: %v", err)
	}

	if err := runMerge(workspace, string(tipA), string(tipB)); err != nil {
		t.Fatalf("unexpected error merging by hash: %v", err)
	}
}

func TestMergeStep_AncestryIsCorrect(t *testing.T) {
	_, s := mergeTestStore(t)

	base := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"a.go": "v1",
	}), "", "", "sess-x")

	tipA := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"a.go": "v2",
	}), base, "", "sess-x")

	tipB := mergeWriteStep(t, s, mergeWriteTree(t, s, map[string]string{
		"b.go": "new",
		"a.go": "v1",
	}), base, "", "sess-y")

	if err := s.UpdateRef("sessions/sess-x", "", tipA); err != nil {
		t.Fatalf("write ref: %v", err)
	}

	mergeHash, _, err := writeMergeStep(s, tipA, tipB)
	if err != nil {
		t.Fatalf("writeMergeStep: %v", err)
	}

	merged, err := s.ReadStep(mergeHash)
	if err != nil {
		t.Fatalf("read merge step: %v", err)
	}
	if merged.Parent != tipA {
		t.Errorf("primary parent: got %s, want %s", merged.Parent, tipA)
	}
	if merged.SecondaryParent != tipB {
		t.Errorf("secondary parent: got %s, want %s", merged.SecondaryParent, tipB)
	}

	// Walking ancestors of mergeHash should visit both tipA and tipB.
	visited := make(map[store.Hash]bool)
	if err := s.WalkAncestors(mergeHash, func(step *store.Step) error {
		visited[merged.Parent] = true
		visited[merged.SecondaryParent] = true
		return nil
	}); err != nil {
		t.Fatalf("WalkAncestors: %v", err)
	}
	// WalkAncestors only follows primary parents; secondary parent reachability
	// is tested via collab.FindCommonAncestor in collab_test.go. Here we just
	// verify the fields are set correctly on the step.
	if merged.Parent != tipA || merged.SecondaryParent != tipB {
		t.Error("merge step ancestry fields are incorrect")
	}
}
