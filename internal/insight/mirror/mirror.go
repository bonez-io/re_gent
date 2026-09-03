// Package mirror fills an index from a store's session refs. A server holds
// objects and refs as the source of truth and no index of its own; the
// insight worker reads steps and messages from an index. Sync walks every
// session ref back to the last step the index already holds and indexes the
// rest, rebuilding each step's messages from its conversation blob, so the
// same pipeline that reads a machine-local capture reads pushed history.
package mirror

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

// maxWalk bounds one session walk, the same bound the read API uses.
const maxWalk = 100000

// Report says what Sync did.
type Report struct {
	Sessions int
	// NewSteps maps a session id to how many steps were newly indexed.
	NewSteps map[string]int
	// Errors are per-session failures that did not stop the sync.
	Errors []error
}

// Changed lists the sessions that gained steps.
func (r Report) Changed() []string {
	var out []string
	for id, n := range r.NewSteps {
		if n > 0 {
			out = append(out, id)
		}
	}
	return out
}

// Sync indexes every step reachable from refs/sessions/* that idx does not
// hold yet. It is idempotent and cheap when nothing is new: each session
// costs one ref read and one index lookup.
func Sync(st *store.Store, idx *index.DB) (Report, error) {
	report := Report{NewSteps: map[string]int{}}
	refs, err := st.ListRefs("sessions")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return report, nil
		}
		return report, fmt.Errorf("list session refs: %w", err)
	}
	for name, tip := range refs {
		sessionID := strings.ReplaceAll(name, "\\", "/")
		report.Sessions++
		indexedID, n, err := syncSession(st, idx, sessionID, tip)
		if indexedID != "" {
			sessionID = indexedID
		}
		report.NewSteps[sessionID] = n
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("session %s: %w", sessionID, err))
		}
	}
	return report, nil
}

// SyncSession indexes one session from its tip. Exported for the ref-update
// path, which knows exactly which session moved. It returns the session id
// the steps carry, which is what the index and the queue key on; the ref
// name is only the way in.
func SyncSession(st *store.Store, idx *index.DB, sessionID string, tip store.Hash) (string, int, error) {
	return syncSession(st, idx, sessionID, tip)
}

func syncSession(st *store.Store, idx *index.DB, sessionID string, tip store.Hash) (string, int, error) {
	// Walk back until a step the index already has, collecting the new ones.
	var pending []store.Hash
	var steps []*store.Step
	seen := map[store.Hash]bool{}
	h := tip
	for h != "" && len(pending) < maxWalk {
		if seen[h] {
			break
		}
		seen[h] = true
		if _, ok, err := idx.GetStepSummary(string(h)); err != nil {
			return sessionID, 0, err
		} else if ok {
			break
		}
		step, err := st.ReadStep(h)
		if err != nil {
			return sessionID, 0, fmt.Errorf("read step %s: %w", h, err)
		}
		pending = append(pending, h)
		steps = append(steps, step)
		h = step.Parent
	}

	// Index oldest first so parents exist before children and the session
	// row's head advances in order.
	indexed := 0
	for i := len(pending) - 1; i >= 0; i-- {
		hash, step := pending[i], steps[i]
		if step.SessionID == "" {
			step.SessionID = sessionID
		}
		sessionID = step.SessionID
		tree, err := st.ReadTree(step.Tree)
		if err != nil {
			return sessionID, indexed, fmt.Errorf("read tree of %s: %w", hash, err)
		}
		if err := idx.IndexStep(hash, step, tree); err != nil {
			return sessionID, indexed, fmt.Errorf("index step %s: %w", hash, err)
		}
		if err := index.RebuildConversation(st, idx, hash, step); err != nil {
			return sessionID, indexed, err
		}
		indexed++
	}
	if indexed > 0 {
		if err := idx.UpsertSession(index.SessionUpdate{ID: sessionID, Origin: steps[0].Origin, HeadStepID: tip}); err != nil {
			return sessionID, indexed, fmt.Errorf("record session head: %w", err)
		}
	}
	return sessionID, indexed, nil
}
