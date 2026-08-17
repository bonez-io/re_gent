package remote

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/regent-vcs/regent/internal/store"
)

// sessionRefDir is the ref directory every session tip lives under, on the
// server as well as in a local store.
const sessionRefDir = "sessions"

// PullStatus is what a pull decided to do with one ref.
type PullStatus int

const (
	// PullAdvanced means the local ref was moved forward onto the server's tip.
	PullAdvanced PullStatus = iota
	// PullAlreadyCurrent means the local ref was already at the server's tip.
	PullAlreadyCurrent
	// PullLocalAhead means the server's tip is an ancestor of the local one:
	// this machine holds steps that have not been delivered yet.
	PullLocalAhead
	// PullDiverged means neither tip is an ancestor of the other. The two
	// histories cannot both be described by one ref, so nothing is moved.
	PullDiverged
)

func (s PullStatus) String() string {
	switch s {
	case PullAdvanced:
		return "advanced"
	case PullAlreadyCurrent:
		return "already current"
	case PullLocalAhead:
		return "local ahead"
	case PullDiverged:
		return "diverged"
	default:
		return "unknown"
	}
}

// PullResult describes the outcome of pulling one ref.
type PullResult struct {
	Ref string
	// Tip is where the local ref points after the pull.
	Tip store.Hash
	// ServerTip is the tip the server held when the pull ran.
	ServerTip store.Hash
	// Objects is the number of objects downloaded.
	Objects int
	// Steps is the length of the server's history behind ServerTip.
	Steps int
	// Status is what was done to the local ref.
	Status PullStatus
}

// Pull downloads the history behind refName from the server and, when it is a
// fast-forward of what this machine already has, points the local ref at it.
//
// It is the read half of Push, and it inherits Push's refusal: a ref is only
// moved along its own history. Push declines to overwrite the server when the
// server has work the local cache does not; Pull declines to overwrite the
// local cache when the local cache has work the server does not. The asymmetric
// case matters more here, because the loss would be silent — the local steps
// would still be in the object store, but with no ref pointing at them no
// command would ever find them again.
//
// Objects are downloaded before the decision is made. That is not wasted work
// on the refusing paths: ancestry can only be computed by reading the server's
// steps, and writing content-addressed objects into the cache cannot destroy
// anything. Only the ref update is destructive, and only it is conditional.
func Pull(ctx context.Context, cache *store.Store, client Client, refName string) (PullResult, error) {
	res := PullResult{Ref: refName}

	if err := ValidateRefName(refName); err != nil {
		return res, err
	}

	serverTip, err := client.GetRef(ctx, refName)
	if err != nil {
		return res, fmt.Errorf("read server ref %s: %w", refName, err)
	}
	res.ServerTip = serverTip

	local, err := localTip(cache, refName)
	if err != nil {
		return res, err
	}
	res.Tip = local

	if local == serverTip {
		res.Status = PullAlreadyCurrent
		res.Steps = 0
		return res, nil
	}

	res.Objects, res.Steps, err = fetchHistory(ctx, cache, client, serverTip)
	if err != nil {
		return res, fmt.Errorf("pull %s: %w", refName, err)
	}

	if local != "" {
		fastForward, err := isAncestor(cache, local, serverTip)
		if err != nil {
			return res, fmt.Errorf("check ancestry of local tip %s: %w", local, err)
		}
		if !fastForward {
			behind, err := isAncestor(cache, serverTip, local)
			if err != nil {
				return res, fmt.Errorf("check ancestry of server tip %s: %w", serverTip, err)
			}
			if behind {
				// Nothing is wrong and nothing is lost: this machine simply has
				// steps the server has not been given yet. Rewinding the ref
				// onto the server's older tip would orphan them.
				res.Status = PullLocalAhead
				return res, nil
			}
			res.Status = PullDiverged
			return res, fmt.Errorf("%w (server at %s, local at %s)", ErrDiverged, serverTip, local)
		}
	}

	if err := casLocalRef(cache, refName, serverTip); err != nil {
		return res, err
	}
	res.Tip = serverTip
	res.Status = PullAdvanced
	return res, nil
}

// localTip reads a ref from the cache, treating "never recorded here" as the
// empty tip rather than as an error — that is the ordinary state of the machine
// this command exists for.
func localTip(cache *store.Store, refName string) (store.Hash, error) {
	tip, err := cache.ReadRef(refName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read local ref %s: %w", refName, err)
	}
	return tip, nil
}

// ServerSessionRefs asks the server which sessions it holds, fully qualified
// and in stable order.
//
// This is what makes `rgt pull` work with no arguments. Every other client call
// takes a ref name the caller must already know, which a machine that has
// pushed nothing does not — the local spool records only what this machine sent.
func ServerSessionRefs(ctx context.Context, client Client) ([]string, error) {
	refs, err := client.ListRefs(ctx, sessionRefDir)
	if err != nil {
		return nil, fmt.Errorf("list server sessions: %w", err)
	}
	out := make([]string, 0, len(refs))
	for name := range refs {
		out = append(out, sessionRefDir+"/"+name)
	}
	sort.Strings(out)
	return out, nil
}
