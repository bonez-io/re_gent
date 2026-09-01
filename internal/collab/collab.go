// Package collab provides multi-person collaboration primitives: conflict
// detection for concurrent edits and merge-step creation.
package collab

import (
	"errors"
	"fmt"

	"github.com/bonez-io/re_gent/internal/store"
)

// Conflict records a file that was modified by both sides of a merge.
type Conflict struct {
	Path       string
	BaseBlob   store.Hash // blob at the common ancestor (empty if new in both)
	OursBlob   store.Hash // blob at tip A
	TheirsBlob store.Hash // blob at tip B
}

// MergeResult holds the outcome of a three-way merge check.
type MergeResult struct {
	Base      store.Hash // common ancestor step hash (empty if no common ancestor)
	Conflicts []Conflict // non-empty iff the merge cannot proceed cleanly
}

// FindCommonAncestor returns the lowest common ancestor of steps a and b in
// the DAG.  Both primary and secondary parents are followed.  It returns ""
// when the two branches have no shared history.
func FindCommonAncestor(s *store.Store, a, b store.Hash) (store.Hash, error) {
	if a == "" || b == "" {
		return "", nil
	}
	if a == b {
		return a, nil
	}

	// Collect all ancestors of a (BFS, both parent edges).
	ancestorsA := make(map[store.Hash]struct{})
	if err := walkAll(s, a, func(h store.Hash) error {
		ancestorsA[h] = struct{}{}
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk ancestors of a: %w", err)
	}

	// Walk b; the first hash found in ancestorsA is the LCA.
	var lca store.Hash
	err := walkAll(s, b, func(h store.Hash) error {
		if _, ok := ancestorsA[h]; ok {
			lca = h
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return "", fmt.Errorf("walk ancestors of b: %w", err)
	}
	return lca, nil
}

// errStop is a sentinel returned from walkAll callbacks to stop traversal.
var errStop = errors.New("stop")

// walkAll traverses all ancestors of h (BFS, following primary and secondary
// parent edges) and calls fn for each visited hash including h itself.
func walkAll(s *store.Store, start store.Hash, fn func(store.Hash) error) error {
	visited := make(map[store.Hash]struct{})
	queue := []store.Hash{start}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if h == "" {
			continue
		}
		if _, seen := visited[h]; seen {
			continue
		}
		visited[h] = struct{}{}
		if err := fn(h); err != nil {
			return err
		}
		step, err := s.ReadStep(h)
		if err != nil {
			return err
		}
		queue = append(queue, step.Parent, step.SecondaryParent)
	}
	return nil
}

// filesChangedSince returns the set of file paths that differ between base and
// tip, keyed by path with the tip blob hash as value (empty hash = deleted).
func filesChangedSince(s *store.Store, base, tip store.Hash) (map[string]store.Hash, error) {
	tipTree, err := treeFor(s, tip)
	if err != nil {
		return nil, fmt.Errorf("read tip tree: %w", err)
	}

	baseEntries := make(map[string]store.Hash)
	if base != "" {
		baseTree, err := treeFor(s, base)
		if err != nil {
			return nil, fmt.Errorf("read base tree: %w", err)
		}
		for _, e := range baseTree.Entries {
			baseEntries[e.Path] = e.Blob
		}
	}

	changed := make(map[string]store.Hash)
	tipEntries := make(map[string]store.Hash, len(tipTree.Entries))
	for _, e := range tipTree.Entries {
		tipEntries[e.Path] = e.Blob
		if baseBlob, ok := baseEntries[e.Path]; !ok || baseBlob != e.Blob {
			changed[e.Path] = e.Blob
		}
	}
	// Deletions: paths in base that are gone in tip.
	for path := range baseEntries {
		if _, present := tipEntries[path]; !present {
			changed[path] = "" // deleted
		}
	}
	return changed, nil
}

func treeFor(s *store.Store, stepHash store.Hash) (*store.Tree, error) {
	step, err := s.ReadStep(stepHash)
	if err != nil {
		return nil, err
	}
	return s.ReadTree(step.Tree)
}

// Detect performs a three-way diff: it finds the common ancestor of tipA and
// tipB and returns any files that were modified in both branches relative to
// that ancestor.
func Detect(s *store.Store, tipA, tipB store.Hash) (MergeResult, error) {
	base, err := FindCommonAncestor(s, tipA, tipB)
	if err != nil {
		return MergeResult{}, fmt.Errorf("find common ancestor: %w", err)
	}

	changedA, err := filesChangedSince(s, base, tipA)
	if err != nil {
		return MergeResult{}, fmt.Errorf("diff tip A: %w", err)
	}
	changedB, err := filesChangedSince(s, base, tipB)
	if err != nil {
		return MergeResult{}, fmt.Errorf("diff tip B: %w", err)
	}

	var baseBlobs map[string]store.Hash
	if base != "" {
		baseTree, err := treeFor(s, base)
		if err != nil {
			return MergeResult{}, fmt.Errorf("read base tree: %w", err)
		}
		baseBlobs = make(map[string]store.Hash, len(baseTree.Entries))
		for _, e := range baseTree.Entries {
			baseBlobs[e.Path] = e.Blob
		}
	}

	var conflicts []Conflict
	for path, blobA := range changedA {
		blobB, ok := changedB[path]
		if !ok {
			continue
		}
		// Both branches changed this file; conflict unless identical outcome.
		if blobA == blobB {
			continue
		}
		conflicts = append(conflicts, Conflict{
			Path:       path,
			BaseBlob:   baseBlobs[path],
			OursBlob:   blobA,
			TheirsBlob: blobB,
		})
	}

	return MergeResult{Base: base, Conflicts: conflicts}, nil
}
