package capture

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

const (
	// SyncOrigin marks a step written outside any agent turn: a baseline
	// snapshot of the git working tree taken at `rgt init`/`rgt connect` or
	// refreshed afterwards by `rgt sync --workspace` or a git hook.
	SyncOrigin = "sync"

	// WorkspaceSyncSessionID is the fixed session id every workspace sync step
	// carries. It is deliberately not produced by canonicalSessionID /
	// normalizeSession: a sync step has no host session behind it to
	// normalize, and giving it one would make it show up wherever code assumes
	// a SessionID names an agent session.
	WorkspaceSyncSessionID = "sync:workspace"

	// WorkspaceSyncRefDir is the ref namespace workspace sync steps live
	// under, parallel to "sessions" but never enumerated as one. See
	// listSessionSummaries in internal/server/read_api.go, which lists
	// "sessions" only, and internal/server/feed_api.go, which already walks
	// this directory alongside "sessions" for the first-run tutorial feed.
	WorkspaceSyncRefDir = "sync"

	// WorkspaceSyncRef is the single ref name the workspace baseline chains
	// onto: refs/sync/workspace.
	WorkspaceSyncRef = WorkspaceSyncRefDir + "/workspace"
)

// WorkspaceSync computes a fresh snapshot of cwd's working tree and, when it
// differs from the tree already recorded at the tip of refs/sync/workspace,
// chains a new step onto that ref and computes blame for it.
//
// It exists so the Files view (and blame) are never empty before the first
// captured agent step: a tree only otherwise exists as a step snapshot, and an
// agent may not touch most of a repository for a long time, if ever. The step
// this writes carries no causes, transcript, or conversation — it records
// "what the workspace looked like", not an agent turn — and its Origin is
// SyncOrigin so it reads as "outside an agent turn" everywhere origin is
// shown, including blame (see computeBlameForStep and handleAPIBlame) and the
// first-run tutorial feed (internal/server/feed_api.go).
//
// It is idempotent: called again with nothing changed on disk, wrote is false
// and nothing is written — a sync step per unchanged poll would otherwise grow
// the DAG for free every time a git hook fires.
//
// fileCount is the number of files in the snapshot just taken, returned
// whether or not a new step was needed, so a caller can report the baseline's
// size even on a no-op run.
func WorkspaceSync(s *store.Store, cwd string) (stepHash store.Hash, wrote bool, fileCount int, err error) {
	for attempt := 0; attempt < maxRefUpdateAttempts; attempt++ {
		parentHash, readErr := s.ReadRef(WorkspaceSyncRef)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return "", false, 0, fmt.Errorf("read workspace sync ref: %w", readErr)
		}

		treeHash, snapErr := snapshotWorkspace(s, cwd)
		if snapErr != nil {
			return "", false, 0, fmt.Errorf("snapshot workspace: %w", snapErr)
		}
		tree, treeErr := s.ReadTree(treeHash)
		if treeErr != nil {
			return "", false, 0, fmt.Errorf("read snapshot tree: %w", treeErr)
		}
		fileCount = len(tree.Entries)

		if parentHash != "" {
			parentStep, parentErr := s.ReadStep(parentHash)
			if parentErr != nil {
				return "", false, fileCount, fmt.Errorf("read parent sync step: %w", parentErr)
			}
			if parentStep.Tree == treeHash {
				// Nothing changed since the last baseline: writing a step here
				// would only chain an identical tree onto itself.
				return parentHash, false, fileCount, nil
			}
		}

		step := &store.Step{
			Parent:         parentHash,
			Tree:           treeHash,
			SessionID:      WorkspaceSyncSessionID,
			Origin:         SyncOrigin,
			Author:         ResolveAuthor(),
			TimestampNanos: time.Now().UnixNano(),
		}
		newStepHash, writeErr := s.WriteStep(step)
		if writeErr != nil {
			return "", false, fileCount, fmt.Errorf("write sync step: %w", writeErr)
		}

		if blameErr := computeAndWriteBlame(s, parentHash, newStepHash, treeHash); blameErr != nil {
			LogHookError(s.Root, fmt.Sprintf("blame workspace sync step %s: %v", newStepHash, blameErr))
		}

		if updateErr := s.UpdateRef(WorkspaceSyncRef, parentHash, newStepHash); updateErr != nil {
			if errors.Is(updateErr, store.ErrRefConflict) {
				time.Sleep(refUpdateBackoff(attempt))
				continue
			}
			return "", false, fileCount, fmt.Errorf("update workspace sync ref: %w", updateErr)
		}

		return newStepHash, true, fileCount, nil
	}

	return "", false, fileCount, fmt.Errorf("update workspace sync ref: %w", store.ErrRefConflict)
}

// loadWorkspaceSyncBaseline reads the tree at the tip of refs/sync/workspace,
// or reports ok=false when there is no such ref, or it cannot be read — every
// one of those degrades to "no baseline" rather than an error, so a project
// that has never run a workspace sync behaves exactly as it did before this
// existed.
func loadWorkspaceSyncBaseline(s *store.Store) (tip store.Hash, tree *store.Tree, ok bool) {
	tip, err := s.ReadRef(WorkspaceSyncRef)
	if err != nil || tip == "" {
		return "", nil, false
	}
	step, err := s.ReadStep(tip)
	if err != nil || step.Tree == "" {
		return "", nil, false
	}
	tree, err = s.ReadTree(step.Tree)
	if err != nil {
		return "", nil, false
	}
	return tip, tree, true
}

// baselineEntry looks up path in the workspace sync baseline tree, returning
// its blob and, when available, its blame map. A missing blame sidecar still
// yields the blob with ok=true: ComputeBlame accepts a nil old map.
func baselineEntry(s *store.Store, tip store.Hash, tree *store.Tree, path string) (blob store.Hash, blame *store.BlameMap, ok bool) {
	if tree == nil {
		return "", nil, false
	}
	entry := tree.FindEntry(path)
	if entry == nil {
		return "", nil, false
	}
	blame, _ = s.ReadBlameForFile(tip, path)
	return entry.Blob, blame, true
}
