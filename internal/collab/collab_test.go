package collab

import (
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

func buildStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

func writeBlob(t *testing.T, s *store.Store, content string) store.Hash {
	t.Helper()
	h, err := s.WriteBlob([]byte(content))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return h
}

func writeTree(t *testing.T, s *store.Store, files map[string]string) store.Hash {
	t.Helper()
	var entries []store.TreeEntry
	for path, content := range files {
		blob := writeBlob(t, s, content)
		entries = append(entries, store.TreeEntry{Path: path, Blob: blob})
	}
	h, err := s.WriteTree(&store.Tree{Entries: entries})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	return h
}

func writeStep(t *testing.T, s *store.Store, tree, parent, secondaryParent store.Hash) store.Hash {
	t.Helper()
	h, err := s.WriteStep(&store.Step{
		Tree:            tree,
		Parent:          parent,
		SecondaryParent: secondaryParent,
		SessionID:       "test",
		TimestampNanos:  time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	return h
}

func TestFindCommonAncestor_SameStep(t *testing.T) {
	s := buildStore(t)
	tree := writeTree(t, s, map[string]string{"a.go": "v1"})
	step := writeStep(t, s, tree, "", "")

	lca, err := FindCommonAncestor(s, step, step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lca != step {
		t.Errorf("got %s, want %s", lca, step)
	}
}

func TestFindCommonAncestor_LinearHistory(t *testing.T) {
	s := buildStore(t)
	base := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "v1"}), "", "")
	tipA := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "v2"}), base, "")
	tipB := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "v3"}), base, "")

	lca, err := FindCommonAncestor(s, tipA, tipB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lca != base {
		t.Errorf("got %s, want %s", lca, base)
	}
}

func TestFindCommonAncestor_NoSharedHistory(t *testing.T) {
	s := buildStore(t)
	stepA := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "a"}), "", "")
	stepB := writeStep(t, s, writeTree(t, s, map[string]string{"b.go": "b"}), "", "")

	lca, err := FindCommonAncestor(s, stepA, stepB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lca != "" {
		t.Errorf("expected empty LCA, got %s", lca)
	}
}

func TestFindCommonAncestor_SecondaryParent(t *testing.T) {
	s := buildStore(t)
	base := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "v1"}), "", "")
	tipA := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "v2"}), base, "")
	tipB := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "v3"}), base, "")

	// Merge step: primary=tipA, secondary=tipB.
	merged := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "merged"}), tipA, tipB)
	child := writeStep(t, s, writeTree(t, s, map[string]string{"a.go": "v4"}), merged, "")

	// child's ancestry includes tipB via merged's secondary parent.
	lca, err := FindCommonAncestor(s, child, tipB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lca != tipB {
		t.Errorf("got %s, want %s", lca, tipB)
	}
}

func TestDetect_NoConflict(t *testing.T) {
	s := buildStore(t)
	base := writeStep(t, s, writeTree(t, s, map[string]string{
		"a.go": "original a",
		"b.go": "original b",
	}), "", "")
	tipA := writeStep(t, s, writeTree(t, s, map[string]string{
		"a.go": "changed a",
		"b.go": "original b",
	}), base, "")
	tipB := writeStep(t, s, writeTree(t, s, map[string]string{
		"a.go": "original a",
		"b.go": "changed b",
	}), base, "")

	result, err := Detect(s, tipA, tipB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Base != base {
		t.Errorf("base: got %s, want %s", result.Base, base)
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("expected no conflicts, got %v", result.Conflicts)
	}
}

func TestDetect_SameFileConflict(t *testing.T) {
	s := buildStore(t)
	base := writeStep(t, s, writeTree(t, s, map[string]string{
		"shared.go": "original",
	}), "", "")
	tipA := writeStep(t, s, writeTree(t, s, map[string]string{
		"shared.go": "version A",
	}), base, "")
	tipB := writeStep(t, s, writeTree(t, s, map[string]string{
		"shared.go": "version B",
	}), base, "")

	result, err := Detect(s, tipA, tipB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(result.Conflicts))
	}
	c := result.Conflicts[0]
	if c.Path != "shared.go" {
		t.Errorf("conflict path: got %s, want shared.go", c.Path)
	}
	if c.OursBlob == c.TheirsBlob {
		t.Error("ours and theirs blobs should differ")
	}
	if c.BaseBlob == "" {
		t.Error("base blob should be non-empty")
	}
}

func TestDetect_IdenticalChanges_NoConflict(t *testing.T) {
	s := buildStore(t)
	base := writeStep(t, s, writeTree(t, s, map[string]string{"f.go": "original"}), "", "")
	// Both branches make the identical change → not a conflict.
	tipA := writeStep(t, s, writeTree(t, s, map[string]string{"f.go": "same change"}), base, "")
	tipB := writeStep(t, s, writeTree(t, s, map[string]string{"f.go": "same change"}), base, "")

	result, err := Detect(s, tipA, tipB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("expected no conflicts for identical changes, got %v", result.Conflicts)
	}
}

func TestDetect_MultipleConflicts(t *testing.T) {
	s := buildStore(t)
	base := writeStep(t, s, writeTree(t, s, map[string]string{
		"x.go": "x",
		"y.go": "y",
		"z.go": "z",
	}), "", "")
	tipA := writeStep(t, s, writeTree(t, s, map[string]string{
		"x.go": "x-a",
		"y.go": "y-a",
		"z.go": "z",
	}), base, "")
	tipB := writeStep(t, s, writeTree(t, s, map[string]string{
		"x.go": "x-b",
		"y.go": "y-b",
		"z.go": "z",
	}), base, "")

	result, err := Detect(s, tipA, tipB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Conflicts) != 2 {
		t.Errorf("expected 2 conflicts, got %d: %v", len(result.Conflicts), result.Conflicts)
	}
}

func TestDetect_DeleteVsModify_Conflict(t *testing.T) {
	s := buildStore(t)
	base := writeStep(t, s, writeTree(t, s, map[string]string{"f.go": "content"}), "", "")
	// A deletes f.go; B modifies it — conflict.
	tipA := writeStep(t, s, writeTree(t, s, map[string]string{}), base, "")
	tipB := writeStep(t, s, writeTree(t, s, map[string]string{"f.go": "modified"}), base, "")

	result, err := Detect(s, tipA, tipB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict (delete vs modify), got %d", len(result.Conflicts))
	}
}
