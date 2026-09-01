package capture

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/bonez-io/re_gent/internal/store"
)

// BlameRepairReport counts what one repair run looked at and what it changed.
type BlameRepairReport struct {
	Sessions  int // session refs walked
	Steps     int // distinct steps recomputed (a step shared by two sessions counts once)
	Rewritten int // sidecars whose bytes changed
	Unchanged int // sidecars that were already what the current diff produces
}

// Checked is how many (step, file) maps the run recomputed.
func (r BlameRepairReport) Checked() int { return r.Rewritten + r.Unchanged }

// BlameRepair recomputes the blame sidecars for history already on disk.
//
// Blame is annotated at write time (see the annotated-blob decision in
// CLAUDE.md), which makes a query O(1) and makes every stored map a permanent
// record of what the diff code believed the day it ran. When LineDiff was
// paired with a mismatched decode helper it named the line *after* the one an
// edit touched, and fixing LineDiff did nothing for the maps already written.
// This is the way to recompute them without deleting `.regent/` and losing the
// history with it.
//
// `rgt show` needs no equivalent: internal/treediff calls LineDiff at query
// time, so its diffs were correct the moment the binary was rebuilt.
type BlameRepair struct {
	Store *store.Store

	// Progress, when set, is called once per (step, file) map considered,
	// with rewritten reporting whether its bytes changed.
	Progress func(stepHash store.Hash, path string, rewritten bool)
}

// Run walks every session ref from its root and rewrites the blame sidecar for
// every (step, file) it finds, using the current diff.
//
// It is safe to interrupt. Nothing canonical is touched — only derived sidecars,
// each written atomically — so a cancelled run leaves every map on disk readable
// and simply stops early. It is not resumable: because a step's blame is derived
// from its parent's, the next run starts again from each root. That re-run is
// cheap in the only sense that matters here, since a map that is already correct
// is recognised and not rewritten.
//
// The report is returned even when the walk stops early, so a caller can say
// what got done before the interruption.
func (r *BlameRepair) Run(ctx context.Context) (BlameRepairReport, error) {
	var report BlameRepairReport

	refs, err := r.Store.ListRefs("sessions")
	if err != nil {
		return report, fmt.Errorf("list session refs: %w", err)
	}

	// Sorted so two runs over the same store do the same work in the same
	// order, which is what makes an interrupted run's output reproducible.
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)

	// Sessions share ancestors: the DAG dedupes common history rather than
	// copying it. Recomputing a step twice would be harmless but wasteful, and
	// would make the step count a lie.
	repaired := make(map[store.Hash]bool)

	var problems []error
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Sessions++

		chain, err := r.chainFromRoot(refs[name])
		if err != nil {
			problems = append(problems, fmt.Errorf("walk session %s: %w", name, err))
			continue
		}

		for _, stepHash := range chain {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if repaired[stepHash] {
				continue
			}

			if err := r.repairStep(stepHash, &report); err != nil {
				// A step's blame is computed against its parent's, so once one
				// step in a chain fails every descendant would be derived from
				// state we know is wrong. Stop this session and report it
				// rather than write maps we cannot stand behind.
				problems = append(problems, fmt.Errorf("session %s, step %s: %w", name, shortHash(stepHash), err))
				break
			}
			repaired[stepHash] = true
			report.Steps++
		}
	}

	return report, errors.Join(problems...)
}

func (r *BlameRepair) repairStep(stepHash store.Hash, report *BlameRepairReport) error {
	step, err := r.Store.ReadStep(stepHash)
	if err != nil {
		return fmt.Errorf("read step: %w", err)
	}

	return computeBlameForStep(r.Store, step.Parent, stepHash, step.Tree, func(h store.Hash, path string, bm *store.BlameMap) error {
		matches, err := r.Store.BlameForFileMatches(h, path, bm)
		if err != nil {
			return fmt.Errorf("compare blame for %s: %w", path, err)
		}
		if !matches {
			if err := r.Store.WriteBlameForFile(h, path, bm); err != nil {
				return fmt.Errorf("write blame for %s: %w", path, err)
			}
			report.Rewritten++
		} else {
			report.Unchanged++
		}
		if r.Progress != nil {
			r.Progress(h, path, !matches)
		}
		return nil
	})
}

// chainFromRoot returns tip's ancestry ordered root first. Repair has to go in
// that direction: ComputeBlame carries a parent's attribution forward into its
// child, so a child recomputed before its parent inherits the broken map it was
// meant to be rid of.
func (r *BlameRepair) chainFromRoot(tip store.Hash) ([]store.Hash, error) {
	var chain []store.Hash
	seen := make(map[store.Hash]bool)

	for h := tip; h != ""; {
		// A parent pointer that loops back cannot happen in a store we wrote —
		// hashes are content-addressed, so a step cannot name a descendant.
		// A corrupted or hand-edited one could, and this walk must terminate.
		if seen[h] {
			return nil, fmt.Errorf("parent chain loops at step %s", shortHash(h))
		}
		seen[h] = true

		step, err := r.Store.ReadStep(h)
		if err != nil {
			return nil, fmt.Errorf("read step %s: %w", shortHash(h), err)
		}
		chain = append(chain, h)
		h = step.Parent
	}

	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func shortHash(h store.Hash) string {
	if len(h) > 8 {
		return string(h[:8])
	}
	return string(h)
}
