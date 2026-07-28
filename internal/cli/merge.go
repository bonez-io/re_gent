package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/regent-vcs/regent/internal/capture"
	"github.com/regent-vcs/regent/internal/collab"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/spf13/cobra"
)

// MergeCmd returns the `rgt merge` command.
func MergeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merge <session-a> <session-b>",
		Short: "Merge two sessions into a new step",
		Long: `Detect concurrent edits and merge two sessions.

Takes the tip step of session-a and session-b (or two step hashes directly),
finds their common ancestor, and checks for same-file conflicts.

If there are no conflicts, a new merge step is written with session-b's tip as
its secondary parent, advancing session-a's ref.

If conflicts exist, they are printed and the command exits non-zero without
writing any step.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			return runMerge(cwd, args[0], args[1])
		},
	}
	return cmd
}

func runMerge(cwd, refA, refB string) error {
	s, err := openMergeStore(cwd)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	tipA, err := resolveRef(s, refA)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", refA, err)
	}
	tipB, err := resolveRef(s, refB)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", refB, err)
	}

	result, err := collab.Detect(s, tipA, tipB)
	if err != nil {
		return fmt.Errorf("detect conflicts: %w", err)
	}

	if len(result.Conflicts) > 0 {
		fmt.Fprintln(os.Stderr, "CONFLICT: same-file concurrent edits detected — merge blocked")
		for _, c := range result.Conflicts {
			fmt.Fprintf(os.Stderr, "  conflict: %s\n", c.Path)
			if c.BaseBlob != "" {
				fmt.Fprintf(os.Stderr, "    base:   %s\n", c.BaseBlob)
			}
			fmt.Fprintf(os.Stderr, "    ours:   %s\n", c.OursBlob)
			fmt.Fprintf(os.Stderr, "    theirs: %s\n", c.TheirsBlob)
		}
		return fmt.Errorf("%d conflict(s) must be resolved before merging", len(result.Conflicts))
	}

	// No conflicts — write the merge step.
	mergeStepHash, sessionRef, err := writeMergeStep(s, tipA, tipB)
	if err != nil {
		return fmt.Errorf("write merge step: %w", err)
	}

	if result.Base != "" {
		fmt.Printf("merge: common ancestor %s\n", result.Base[:12])
	} else {
		fmt.Println("merge: no common ancestor (unrelated histories)")
	}
	fmt.Printf("merge: created step %s\n", mergeStepHash[:12])
	fmt.Printf("merge: ref %q advanced\n", sessionRef)
	return nil
}

// writeMergeStep creates a merge Step with tipB as secondary parent and
// advances the canonical session ref for tipA.
func writeMergeStep(s *store.Store, tipA, tipB store.Hash) (mergeHash store.Hash, advancedRef string, err error) {
	stepA, err := s.ReadStep(tipA)
	if err != nil {
		return "", "", fmt.Errorf("read tip-A step: %w", err)
	}

	mergeStep := &store.Step{
		Parent:          tipA,
		SecondaryParent: tipB,
		Tree:            stepA.Tree,
		SessionID:       stepA.SessionID,
		Origin:          stepA.Origin,
		Author:          capture.ResolveAuthor(),
		TimestampNanos:  time.Now().UnixNano(),
	}

	mergeHash, err = s.WriteStep(mergeStep)
	if err != nil {
		return "", "", fmt.Errorf("write step: %w", err)
	}

	canonRef := "sessions/" + stepA.SessionID
	if err := s.UpdateRef(canonRef, tipA, mergeHash); err != nil {
		return "", "", fmt.Errorf("advance ref %s: %w", canonRef, err)
	}

	return mergeHash, canonRef, nil
}

// openMergeStore opens the store for a merge, honouring server mode: when the
// repo is connected to a server the session refs live in the machine-local
// cache rather than a repo-local .regent/ directory.
func openMergeStore(cwd string) (*store.Store, error) {
	if s, ok, err := openServerModeCache(); ok {
		return s, err
	}
	return store.Open(filepath.Join(cwd, ".regent"))
}

// resolveRef accepts either a raw step hash or a session ID and returns the
// corresponding step hash.
func resolveRef(s *store.Store, ref string) (store.Hash, error) {
	// Try as a literal step hash first.
	if _, err := s.ReadStep(store.Hash(ref)); err == nil {
		return store.Hash(ref), nil
	}

	// Try as a session ref.
	h, err := s.ReadRef("sessions/" + ref)
	if err == nil {
		return h, nil
	}

	return "", fmt.Errorf("not a step hash or session ID: %q", ref)
}
