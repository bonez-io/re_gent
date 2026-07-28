package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/regent-vcs/regent/internal/capture"
	"github.com/regent-vcs/regent/internal/collab"
	"github.com/regent-vcs/regent/internal/ignore"
	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/snapshot"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// DropCmd returns the `rgt drop` command.
//
// drop is the session-scoped counterpart to rewind: instead of restoring the
// whole workspace to a point in time, it surgically undoes ONE session's file
// changes while keeping every other session's work. ("Run 5 agents, drop the
// bad one, keep the 4 good ones.")
func DropCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "drop <session>",
		Short: "Undo one session's changes, keeping other sessions' work",
		Long: `Undo the file changes made by a single session, keeping every other
session's changes intact.

drop reverse-applies the target session's contribution onto the current
workspace (like 'git revert' for an agent). If another session later changed a
file that drop would revert, that is a conflict: drop prints it and aborts
without touching anything. Use --dry-run to preview.

The current workspace is auto-snapshotted first, so a printed 'rgt rewind'
command always lets you undo the drop.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return runDrop(cwd, args[0], dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without modifying the workspace")
	return cmd
}

func runDrop(cwd, sessionRef string, dryRun bool) error {
	s, err := openStoreForCWD(cwd)
	if err != nil {
		return err
	}
	idx, err := index.Open(s)
	if err != nil {
		return err
	}
	defer func() { _ = idx.Close() }()

	// 1. Resolve the session to its tip step + tip tree.
	tip, err := resolveRef(s, sessionRef)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", sessionRef, err)
	}
	tipStep, err := s.ReadStep(tip)
	if err != nil {
		return fmt.Errorf("read tip step: %w", err)
	}
	sessionID := tipStep.SessionID
	tipTree, err := s.ReadTree(tipStep.Tree)
	if err != nil {
		return fmt.Errorf("read tip tree: %w", err)
	}

	// 2. Base tree = the workspace as it was just before this session started.
	baseTree, err := sessionBaseTree(s, sessionID)
	if err != nil {
		return fmt.Errorf("resolve session base: %w", err)
	}

	// 3. Snapshot the current workspace.
	ig := ignore.Default(cwd)
	currentTreeHash, err := snapshot.Snapshot(s, cwd, ig)
	if err != nil {
		return fmt.Errorf("snapshot workspace: %w", err)
	}
	currentTree, err := s.ReadTree(currentTreeHash)
	if err != nil {
		return fmt.Errorf("read current tree: %w", err)
	}

	// 4. Reverse-apply the session's contribution (git-revert-style 3-way).
	reverted, conflicts := revertSession(baseTree, tipTree, currentTree)
	if len(conflicts) > 0 {
		printDropConflicts(conflicts)
		// TODO: interactive conflict resolution. For now drop aborts cleanly.
		return fmt.Errorf("%d conflict(s): another session changed files this drop would revert — resolve them first", len(conflicts))
	}

	d := computeRewindDiff(currentTree, reverted)
	if dryRun {
		printRewindPreview(d)
		return nil
	}
	if len(d.adds)+len(d.modifies)+len(d.deletes) == 0 {
		fmt.Println("Nothing to drop — this session made no net change to the current workspace.")
		return nil
	}

	// 5. Auto-snapshot for undo — the safety net, written before anything else
	// changes.
	undoStepHash, err := writeRewindCheckpoint(s, idx, currentTreeHash, currentTree)
	if err != nil {
		return fmt.Errorf("write undo checkpoint: %w", err)
	}

	// 6. Advance the session's own ref to the drop step BEFORE mutating the
	// workspace. The ref CAS is the only step that can fail on a concurrent
	// write (e.g. the target session's own Stop hook advancing it), so doing it
	// first keeps disk and refs consistent: a lost CAS aborts with the workspace
	// untouched instead of leaving reverted files behind an un-advanced ref.
	if _, err := writeDropStep(s, idx, tip, tipStep, reverted); err != nil {
		return fmt.Errorf("record drop step (workspace not modified): %w", err)
	}

	fmt.Printf("%s rgt rewind %s\n\n", style.Label("Undo with:"), undoStepHash)

	// 7. Apply the revert to disk (write-before-delete; the reliable step).
	if err := applyRewindToWorkspace(s, cwd, d); err != nil {
		return fmt.Errorf("apply drop (run the undo above to recover): %w", err)
	}

	fmt.Printf("%s %d added, %d modified, %d deleted (dropped session %s)\n",
		style.Success("Drop complete:"),
		len(d.adds), len(d.modifies), len(d.deletes), sessionID)

	// 7. In server mode, deliver the dropped session ref + undo checkpoint ref
	// to the server (no-op in local mode).
	if err := deliverDropToServer(s, cwd, "sessions/"+sessionID, undoStepHash); err != nil {
		return fmt.Errorf("deliver drop to server: %w", err)
	}
	return nil
}

// sessionBaseTree returns the workspace as it was just before the target
// session started: the tree of the most recent step across ALL sessions whose
// timestamp precedes the session's first step. An empty tree is returned when
// nothing preceded it (the target is the earliest session).
//
// re_gent captures each session's first step as a FULL workspace snapshot with
// no parent pointer, so a session's "before" state is not encoded in the parent
// chain; it is reconstructed here by time. This is correct for the common case
// of sequentially-run sessions. Known limitations: concurrent/interleaved
// sessions can select a mid-flight base, and dropping the earliest session
// reverts to an empty tree (nothing was recorded before it).
func sessionBaseTree(s *store.Store, sessionID string) (*store.Tree, error) {
	firstTS, err := sessionFirstStepTime(s, sessionID)
	if err != nil {
		return nil, err
	}

	refs, err := s.ListRefs("sessions")
	if err != nil {
		return nil, fmt.Errorf("list session refs: %w", err)
	}

	var baseTree store.Hash
	baseTS := int64(-1)
	for _, tip := range refs {
		for cur := tip; cur != ""; {
			st, err := s.ReadStep(cur)
			if err != nil {
				break
			}
			// Steps of the target session are never before its own start, so
			// the firstTS filter naturally excludes them.
			if st.TimestampNanos < firstTS && st.TimestampNanos > baseTS {
				baseTS = st.TimestampNanos
				baseTree = st.Tree
			}
			cur = st.Parent
		}
	}

	if baseTree == "" {
		return &store.Tree{}, nil
	}
	return s.ReadTree(baseTree)
}

// sessionFirstStepTime returns the earliest step timestamp within a session.
func sessionFirstStepTime(s *store.Store, sessionID string) (int64, error) {
	tip, err := s.ReadRef("sessions/" + sessionID)
	if err != nil {
		return 0, fmt.Errorf("read session ref: %w", err)
	}
	min := int64(-1)
	for cur := tip; cur != ""; {
		st, err := s.ReadStep(cur)
		if err != nil {
			return 0, fmt.Errorf("read step %s: %w", cur, err)
		}
		if st.SessionID != sessionID {
			break
		}
		if min < 0 || st.TimestampNanos < min {
			min = st.TimestampNanos
		}
		cur = st.Parent
	}
	if min < 0 {
		return 0, fmt.Errorf("session %s has no steps", sessionID)
	}
	return min, nil
}

// revertSession computes the workspace tree with the target session's
// contribution reversed. For every path the session changed (base != tip):
//   - if the current workspace still holds the session's value, revert it to
//     the base value (delete if the base had no such file);
//   - if the current value already equals the base value, it's already
//     reverted — a no-op;
//   - otherwise another session changed it afterward: a conflict.
func revertSession(base, tip, current *store.Tree) (*store.Tree, []collab.Conflict) {
	baseM := entryMap(base)
	tipM := entryMap(tip)
	curM := entryMap(current)

	result := make(map[string]store.TreeEntry, len(curM))
	for p, e := range curM {
		result[p] = e
	}

	changed := make(map[string]struct{})
	for p := range baseM {
		changed[p] = struct{}{}
	}
	for p := range tipM {
		changed[p] = struct{}{}
	}

	var conflicts []collab.Conflict
	for p := range changed {
		be, bok := baseM[p]
		te, tok := tipM[p]
		baseBlob, tipBlob := blobOf(be, bok), blobOf(te, tok)
		if baseBlob == tipBlob {
			continue // the session did not change this path
		}
		ce, cok := curM[p]
		curBlob := blobOf(ce, cok)

		switch {
		case curBlob == baseBlob:
			// already at base value — nothing to do
		case curBlob == tipBlob:
			// safe to revert to the base value
			if bok {
				result[p] = be
			} else {
				delete(result, p)
			}
		default:
			conflicts = append(conflicts, collab.Conflict{
				Path:       p,
				BaseBlob:   baseBlob,
				OursBlob:   curBlob,
				TheirsBlob: tipBlob,
			})
		}
	}

	if len(conflicts) > 0 {
		return nil, conflicts
	}
	return treeFromMap(result), nil
}

// writeDropStep records the post-drop tree as a step parented on the session's
// tip and advances the session's own ref to it, so the branch honestly records
// "…and then this session was dropped" and the drop is itself tracked/undoable.
func writeDropStep(s *store.Store, idx *index.DB, tip store.Hash, tipStep *store.Step, reverted *store.Tree) (store.Hash, error) {
	revertedHash, err := s.WriteTree(reverted)
	if err != nil {
		return "", fmt.Errorf("write reverted tree: %w", err)
	}
	dropStep := &store.Step{
		Parent:         tip,
		Tree:           revertedHash,
		SessionID:      tipStep.SessionID,
		Origin:         tipStep.Origin,
		Author:         capture.ResolveAuthor(),
		TurnID:         "drop",
		TimestampNanos: time.Now().UnixNano(),
	}
	dropHash, err := s.WriteStep(dropStep)
	if err != nil {
		return "", fmt.Errorf("write drop step: %w", err)
	}
	_ = idx.IndexStep(dropHash, dropStep, reverted)
	if err := s.UpdateRef("sessions/"+tipStep.SessionID, tip, dropHash); err != nil {
		return "", fmt.Errorf("advance session ref: %w", err)
	}
	return dropHash, nil
}

func printDropConflicts(conflicts []collab.Conflict) {
	fmt.Fprintln(os.Stderr, "CONFLICT: another session changed files this drop would revert — drop blocked")
	for _, c := range conflicts {
		fmt.Fprintf(os.Stderr, "  conflict: %s\n", c.Path)
	}
}

func entryMap(t *store.Tree) map[string]store.TreeEntry {
	m := make(map[string]store.TreeEntry, len(t.Entries))
	for _, e := range t.Entries {
		m[e.Path] = e
	}
	return m
}

func blobOf(e store.TreeEntry, ok bool) store.Hash {
	if !ok {
		return ""
	}
	return e.Blob
}

func treeFromMap(m map[string]store.TreeEntry) *store.Tree {
	entries := make([]store.TreeEntry, 0, len(m))
	for _, e := range m {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return &store.Tree{Entries: entries}
}

// deliverDropToServer pushes the dropped session ref and the undo-checkpoint ref
// to the server when server mode is configured. In local mode it is a no-op.
// Without this, a drop in server mode would live only in the disposable local
// cache and never reach the server (the failure mode we fixed for merge).
func deliverDropToServer(s *store.Store, cwd, sessionRef string, undoStepHash store.Hash) error {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil {
		return err
	}
	if !cfg.Enabled() {
		return nil // local mode — nothing to deliver
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// The undo checkpoint lives under its own synthetic session ref.
	undoStep, err := s.ReadStep(undoStepHash)
	if err != nil {
		return fmt.Errorf("read undo step: %w", err)
	}
	undoRef := "sessions/" + undoStep.SessionID

	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return err
	}
	spool, err := remote.OpenSpool(filepath.Join(cacheDir, "spool"))
	if err != nil {
		return err
	}
	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Push the undo checkpoint first so an undo target always exists on the
	// server before the drop itself lands. A push failure is NOT fatal: the
	// local drop already succeeded, and both are ordinary session refs that a
	// later `rgt sync` retries — so warn rather than fail the whole command.
	for _, ref := range []string{undoRef, sessionRef} {
		if _, err := remote.Push(ctx, s, client, spool, ref); err != nil {
			fmt.Fprintf(os.Stderr,
				"warning: drop applied locally but not yet delivered to %s: %v\n  run 'rgt sync' to retry delivery\n",
				cfg.ServerURL, err)
			return nil
		}
	}
	fmt.Printf("%s delivered drop to %s\n", style.Label("server:"), cfg.ServerURL)
	return nil
}
