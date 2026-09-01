package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bonez-io/re_gent/internal/ignore"
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/snapshot"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/spf13/cobra"
)

// RewindCmd creates the rewind command.
func RewindCmd() *cobra.Command {
	var (
		rawTreeHash string
		dryRun      bool
		allowEmpty  bool
	)

	cmd := &cobra.Command{
		Use:   "rewind <step-hash>",
		Short: "Restore the workspace to an earlier step",
		Long: `Restore the workspace to the tree recorded at a given step.

Before any change the current workspace is auto-snapshotted so you have
a printed undo command. Use --dry-run to preview changes without touching
the filesystem.

The --tree flag accepts a raw tree hash instead of a step hash, and is
rejected if the hash resolves to a step object (type validation).`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && rawTreeHash == "" {
				return fmt.Errorf("requires a step hash argument or --tree <tree-hash>")
			}
			if len(args) > 0 && rawTreeHash != "" {
				return fmt.Errorf("cannot use both a step hash argument and --tree")
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}

			idx, err := index.Open(s)
			if err != nil {
				return err
			}
			defer func() { _ = idx.Close() }()

			var targetTree *store.Tree
			var targetTreeHash store.Hash

			if rawTreeHash != "" {
				targetTreeHash, targetTree, err = resolveTreeHash(s, store.Hash(rawTreeHash))
				if err != nil {
					return err
				}
			} else {
				fullStepHash, err := idx.NormalizeStepHash(args[0])
				if err != nil {
					return fmt.Errorf("resolve step %s: %w", args[0], err)
				}
				step, err := s.ReadStep(fullStepHash)
				if err != nil {
					return fmt.Errorf("read step: %w", err)
				}
				targetTreeHash = step.Tree
				targetTree, err = s.ReadTree(step.Tree)
				if err != nil {
					return fmt.Errorf("read tree %s: %w", step.Tree, err)
				}
			}

			ig := ignore.Default(cwd)

			// Snapshot current workspace before any change.
			currentTreeHash, err := snapshot.Snapshot(s, cwd, ig)
			if err != nil {
				return fmt.Errorf("snapshot workspace: %w", err)
			}
			currentTree, err := s.ReadTree(currentTreeHash)
			if err != nil {
				return fmt.Errorf("read current tree: %w", err)
			}

			// Refuse to restore an empty tree over a non-empty workspace
			// unless --allow-empty is set.
			if len(targetTree.Entries) == 0 && !allowEmpty && len(currentTree.Entries) > 0 {
				return fmt.Errorf(
					"refusing to restore an empty tree over a %d-file workspace; use --allow-empty to override",
					len(currentTree.Entries),
				)
			}

			d := computeRewindDiff(currentTree, targetTree)

			if dryRun {
				printRewindPreview(d)
				return nil
			}

			// Nothing to do?
			if len(d.adds)+len(d.modifies)+len(d.deletes) == 0 {
				fmt.Println("Already at that state — nothing to change.")
				return nil
			}

			// Write a checkpoint step pointing to the CURRENT tree so the user
			// has a printed undo reference before the workspace is touched.
			undoStepHash, err := writeRewindCheckpoint(s, idx, currentTreeHash, currentTree)
			if err != nil {
				return fmt.Errorf("write undo checkpoint: %w", err)
			}
			fmt.Printf("%s rgt rewind %s\n\n", style.Label("Undo with:"), undoStepHash)

			// Apply: write-before-delete to prevent data loss on partial failure.
			if err := applyRewindToWorkspace(s, cwd, d); err != nil {
				return fmt.Errorf("apply rewind: %w", err)
			}

			fmt.Printf("%s %d added, %d modified, %d deleted\n",
				style.Success("Rewind complete:"),
				len(d.adds), len(d.modifies), len(d.deletes))

			_ = targetTreeHash
			return nil
		},
	}

	cmd.Flags().StringVar(&rawTreeHash, "tree", "", "Restore from a raw tree hash (rejected if it resolves to a step)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without modifying the workspace")
	cmd.Flags().BoolVar(&allowEmpty, "allow-empty", false, "Allow restoring an empty tree over a non-empty workspace")

	return cmd
}

// resolveTreeHash resolves a hash (full or short) to a tree object.
// Returns an error if the blob is actually a step (type validation).
func resolveTreeHash(s *store.Store, h store.Hash) (store.Hash, *store.Tree, error) {
	fullHash := h
	if len(h) < 64 {
		var err error
		fullHash, err = s.ResolveShortHash(string(h))
		if err != nil {
			return "", nil, fmt.Errorf("resolve hash %s: %w", h, err)
		}
	}

	data, err := s.ReadBlob(fullHash)
	if err != nil {
		return "", nil, fmt.Errorf("read object %s: %w", fullHash, err)
	}

	// A step blob always has "session_id" set; trees never do.
	var stepProbe struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(data, &stepProbe) == nil && stepProbe.SessionID != "" {
		return "", nil, fmt.Errorf(
			"hash %s is a step, not a tree — pass it as the positional argument instead of --tree",
			fullHash[:16],
		)
	}

	var tree store.Tree
	if err := json.Unmarshal(data, &tree); err != nil {
		return "", nil, fmt.Errorf("decode tree %s: %w", fullHash, err)
	}

	return fullHash, &tree, nil
}

// rewindDiff describes what needs to change to reach the target tree.
type rewindDiff struct {
	adds     []store.TreeEntry // in target, absent from current
	modifies []store.TreeEntry // in both, blob differs
	deletes  []store.TreeEntry // in current, absent from target
}

func computeRewindDiff(current, target *store.Tree) rewindDiff {
	cur := make(map[string]store.Hash, len(current.Entries))
	for _, e := range current.Entries {
		cur[e.Path] = e.Blob
	}
	tgt := make(map[string]bool, len(target.Entries))
	for _, e := range target.Entries {
		tgt[e.Path] = true
	}

	var d rewindDiff
	for _, te := range target.Entries {
		cb, exists := cur[te.Path]
		if !exists {
			d.adds = append(d.adds, te)
		} else if cb != te.Blob {
			d.modifies = append(d.modifies, te)
		}
	}
	for _, ce := range current.Entries {
		if !tgt[ce.Path] {
			d.deletes = append(d.deletes, ce)
		}
	}
	return d
}

func printRewindPreview(d rewindDiff) {
	if len(d.adds)+len(d.modifies)+len(d.deletes) == 0 {
		fmt.Println("(already at that state — nothing to change)")
		return
	}
	for _, e := range d.adds {
		fmt.Printf("  %s %s\n", style.Success("A"), e.Path)
	}
	for _, e := range d.modifies {
		fmt.Printf("  %s %s\n", style.Label("M"), e.Path)
	}
	for _, e := range d.deletes {
		fmt.Printf("  %s %s\n", style.Warning("D"), e.Path)
	}
}

// writeRewindCheckpoint writes a synthetic step pointing to the current
// (pre-rewind) tree so the user can undo. Returns the full step hash.
func writeRewindCheckpoint(s *store.Store, idx *index.DB, treeHash store.Hash, tree *store.Tree) (store.Hash, error) {
	sessionID := fmt.Sprintf("rewind--%d", time.Now().UnixNano())

	if err := idx.UpsertSession(index.SessionUpdate{
		ID:     sessionID,
		Origin: "rewind",
	}); err != nil {
		return "", fmt.Errorf("create rewind session: %w", err)
	}

	step := &store.Step{
		Tree:           treeHash,
		SessionID:      sessionID,
		Origin:         "rewind",
		TurnID:         "pre-rewind-snapshot",
		TimestampNanos: time.Now().UnixNano(),
	}

	stepHash, err := s.WriteStep(step)
	if err != nil {
		return "", err
	}

	// Index the step (best-effort; failure here does not block the rewind).
	_ = idx.IndexStep(stepHash, step, tree)

	// CAS write: new ref, expected-old = "" (ref does not yet exist).
	if err := s.UpdateRef("sessions/"+sessionID, "", stepHash); err != nil {
		return "", fmt.Errorf("write rewind session ref: %w", err)
	}

	return stepHash, nil
}

// applyRewindToWorkspace restores files using write-before-delete ordering
// so a partial failure never loses both old and new content.
func applyRewindToWorkspace(s *store.Store, cwd string, d rewindDiff) error {
	// Phase 1 — write new and modified files before deleting anything.
	toWrite := make([]store.TreeEntry, 0, len(d.adds)+len(d.modifies))
	toWrite = append(toWrite, d.adds...)
	toWrite = append(toWrite, d.modifies...)

	for _, e := range toWrite {
		content, err := s.ReadBlob(e.Blob)
		if err != nil {
			return fmt.Errorf("read blob for %s: %w", e.Path, err)
		}
		absPath := filepath.Join(cwd, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", e.Path, err)
		}
		mode := os.FileMode(0o644)
		if e.Mode != 0 {
			mode = os.FileMode(e.Mode)
		}
		if err := writeWorkspaceFile(absPath, content, mode); err != nil {
			return fmt.Errorf("write %s: %w", e.Path, err)
		}
	}

	// Phase 2 — delete files absent from the target tree.
	for _, e := range d.deletes {
		absPath := filepath.Join(cwd, filepath.FromSlash(e.Path))
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", e.Path, err)
		}
	}

	return nil
}

// writeWorkspaceFile writes content to path atomically using a temp-file rename.
func writeWorkspaceFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".rgt-rewind-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	var closed bool
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
