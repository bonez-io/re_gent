package remote

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/bonez-io/re_gent/internal/store"
)

// maxChainLength bounds every DAG walk. A cycle cannot occur in a
// content-addressed DAG, but a corrupt or hostile object could still describe
// one, and a hook must never spin forever inside an agent turn.
const maxChainLength = 100000

// ErrDiverged is returned when the server's tip for a ref is not an ancestor of
// the local tip. Pushing would discard someone else's history, so we refuse and
// leave the work pending instead.
var ErrDiverged = errors.New("server ref has diverged from the local cache")

// PushResult describes the outcome of one ref push.
type PushResult struct {
	Ref string
	// Tip is the tip now confirmed on the server.
	Tip store.Hash
	// Objects is the number of objects uploaded during this push.
	Objects int
	// Steps is the number of steps uploaded during this push.
	Steps int
	// AlreadyCurrent is true when the server was already at the local tip.
	AlreadyCurrent bool
}

// Push uploads everything the server is missing for refName and then advances
// the server's ref with a compare-and-swap.
//
// Ordering is the consistency guarantee: objects first, ref last. A failure at
// any point leaves the server with extra unreferenced objects — never with a
// ref pointing at a step whose contents were not delivered. Every part of the
// process is idempotent, so a retry after any partial failure converges.
func Push(ctx context.Context, cache *store.Store, client Client, spool *Spool, refName string) (PushResult, error) {
	return push(ctx, cache, client, spool, refName, false)
}

// Repair re-verifies the whole history behind refName against the server and
// re-uploads anything missing.
//
// Push only sends the delta the server does not claim to have, so it cannot
// notice that the server lost an *ancestor* object: the server's reachability
// check looks at the step being pointed at, not at its ancestry. Repair is the
// deliberate, explicit fix for that case ('rgt sync --repair'). It costs one
// existence check per object, so it is not on the hook path.
func Repair(ctx context.Context, cache *store.Store, client Client, spool *Spool, refName string) (PushResult, error) {
	return push(ctx, cache, client, spool, refName, true)
}

func push(ctx context.Context, cache *store.Store, client Client, spool *Spool, refName string, full bool) (PushResult, error) {
	res := PushResult{Ref: refName}

	if err := ValidateRefName(refName); err != nil {
		return res, err
	}

	local, err := cache.ReadRef(refName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil // nothing recorded locally yet
		}
		return res, fmt.Errorf("read local ref %s: %w", refName, err)
	}
	res.Tip = local

	// Fast path: the durable high-water mark already says the server has this
	// tip, so no network call is needed at all. A repair deliberately skips it.
	if spool != nil && !full {
		if pushed, err := spool.PushedTip(refName); err == nil && pushed == local {
			res.AlreadyCurrent = true
			return res, nil
		}
	}

	remoteTip, err := client.GetRef(ctx, refName)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return res, fmt.Errorf("read server ref %s: %w", refName, err)
		}
		remoteTip = ""
	}

	if remoteTip == local && !full {
		res.AlreadyCurrent = true
		return res, recordPushed(spool, refName, local)
	}
	if remoteTip != "" {
		ok, err := isAncestor(cache, remoteTip, local)
		if err != nil {
			return res, fmt.Errorf("check ancestry of server tip %s: %w", remoteTip, err)
		}
		if !ok {
			return res, fmt.Errorf("%w (server at %s, local at %s)", ErrDiverged, remoteTip, local)
		}
	}

	// A normal push uploads only the delta the server is known to be missing,
	// and skips existence checks because those objects are new by construction.
	// A repair walks the whole history and checks each object first.
	base := remoteTip
	if full {
		base = ""
	}
	objects, steps, err := uploadRange(ctx, cache, client, local, base, full)
	res.Objects += objects
	res.Steps = steps
	if err != nil {
		return res, err
	}

	if remoteTip == local {
		// A repair of an already-current ref: the objects were the point.
		res.AlreadyCurrent = true
		return res, recordPushed(spool, refName, local)
	}

	err = client.UpdateRef(ctx, refName, remoteTip, local)
	if errors.Is(err, ErrIncomplete) {
		// The server refused because it is missing objects this step depends
		// on: its ref is ahead of its object store, or objects were lost. Fall
		// back to a full push from the root, skipping what it already has.
		if spool != nil {
			_ = spool.ForgetPushed(refName)
		}
		repaired, _, repairErr := uploadRange(ctx, cache, client, local, "", true)
		res.Objects += repaired
		if repairErr != nil {
			return res, errors.Join(err, repairErr)
		}
		err = client.UpdateRef(ctx, refName, remoteTip, local)
	}
	if err != nil {
		return res, fmt.Errorf("update server ref %s: %w", refName, err)
	}

	return res, recordPushed(spool, refName, local)
}

func recordPushed(spool *Spool, refName string, tip store.Hash) error {
	if spool == nil || tip == "" {
		return nil
	}
	if err := spool.RecordPushed(refName, tip); err != nil {
		return fmt.Errorf("record pushed tip: %w", err)
	}
	return nil
}

// uploadRange uploads every object reachable from tip that is not already
// reachable from base, oldest step first.
//
// Oldest-first matters: if the connection dies halfway, what landed on the
// server is a valid prefix of the history rather than a scattering of orphans.
func uploadRange(ctx context.Context, cache *store.Store, client Client, tip, base store.Hash, checkFirst bool) (int, int, error) {
	chain, err := stepChain(cache, tip, base)
	if err != nil {
		return 0, 0, err
	}

	uploaded := 0
	seen := make(map[store.Hash]bool)

	for _, stepHash := range chain {
		step, err := cache.ReadStep(stepHash)
		if err != nil {
			return uploaded, 0, fmt.Errorf("read step %s: %w", stepHash, err)
		}

		hashes, err := stepObjects(cache, step, base, seen)
		if err != nil {
			return uploaded, 0, err
		}

		for _, h := range hashes {
			n, err := uploadObject(ctx, cache, client, h, checkFirst, seen)
			uploaded += n
			if err != nil {
				return uploaded, 0, err
			}
		}

		// The step object goes last: a step is only meaningful once everything
		// it references has landed.
		n, err := uploadObject(ctx, cache, client, stepHash, checkFirst, seen)
		uploaded += n
		if err != nil {
			return uploaded, 0, err
		}
	}

	return uploaded, len(chain), nil
}

// stepObjects returns the objects a step needs, in dependency order: file
// blobs, then the tree, then the tool-call payload blobs.
//
// When the step's parent is already on the server, only file blobs that differ
// from the parent's tree are uploaded. That is what keeps a per-turn push
// proportional to the diff instead of to the whole workspace.
func stepObjects(cache *store.Store, step *store.Step, base store.Hash, seen map[store.Hash]bool) ([]store.Hash, error) {
	var out []store.Hash

	if step.Tree != "" {
		tree, err := cache.ReadTree(step.Tree)
		if err != nil {
			return nil, fmt.Errorf("read tree %s: %w", step.Tree, err)
		}

		parentBlobs := map[store.Hash]bool{}
		if parentOnServer(step.Parent, base, seen) {
			parentTree, err := parentTreeOf(cache, step.Parent)
			if err != nil {
				return nil, err
			}
			for _, entry := range parentTree {
				parentBlobs[entry.Blob] = true
			}
		}

		for _, entry := range tree.Entries {
			if entry.Blob == "" || parentBlobs[entry.Blob] {
				continue
			}
			out = append(out, entry.Blob)
		}
		out = append(out, step.Tree)
	}

	for _, cause := range step.Causes {
		if cause.ArgsBlob != "" {
			out = append(out, cause.ArgsBlob)
		}
		if cause.ResultBlob != "" {
			out = append(out, cause.ResultBlob)
		}
	}
	if step.Transcript != "" {
		out = append(out, step.Transcript)
	}
	if step.Conversation != "" {
		out = append(out, step.Conversation)
	}

	return out, nil
}

// parentOnServer reports whether the parent step's objects are known to be on
// the server already — either because it is the delta base, or because this
// same run just uploaded it.
func parentOnServer(parent, base store.Hash, seen map[store.Hash]bool) bool {
	if parent == "" {
		return false
	}
	return parent == base || seen[parent]
}

func parentTreeOf(cache *store.Store, parent store.Hash) ([]store.TreeEntry, error) {
	parentStep, err := cache.ReadStep(parent)
	if err != nil {
		return nil, fmt.Errorf("read parent step %s: %w", parent, err)
	}
	if parentStep.Tree == "" {
		return nil, nil
	}
	parentTree, err := cache.ReadTree(parentStep.Tree)
	if err != nil {
		return nil, fmt.Errorf("read parent tree %s: %w", parentStep.Tree, err)
	}
	return parentTree.Entries, nil
}

func uploadObject(ctx context.Context, cache *store.Store, client Client, h store.Hash, checkFirst bool, seen map[store.Hash]bool) (int, error) {
	if h == "" || seen[h] {
		return 0, nil
	}
	seen[h] = true

	if checkFirst {
		present, err := client.HasObject(ctx, h)
		if err != nil {
			return 0, fmt.Errorf("check object %s: %w", h, err)
		}
		if present {
			return 0, nil
		}
	}

	data, err := cache.ReadBlob(h)
	if err != nil {
		return 0, fmt.Errorf("read cached object %s: %w", h, err)
	}
	got, err := client.PutObject(ctx, data)
	if err != nil {
		return 0, fmt.Errorf("upload object %s: %w", h, err)
	}
	if got != h {
		return 0, fmt.Errorf("server stored object %s under hash %s", h, got)
	}
	return 1, nil
}

// stepChain returns the steps from tip back to (but excluding) base, ordered
// oldest first.
func stepChain(cache *store.Store, tip, base store.Hash) ([]store.Hash, error) {
	var chain []store.Hash
	for current := tip; current != "" && current != base; {
		step, err := cache.ReadStep(current)
		if err != nil {
			return nil, fmt.Errorf("read step %s: %w", current, err)
		}
		chain = append(chain, current)
		if len(chain) > maxChainLength {
			return nil, fmt.Errorf("step chain from %s exceeds %d entries", tip, maxChainLength)
		}
		current = step.Parent
	}

	// Reverse into oldest-first order.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// isAncestor reports whether ancestor appears on descendant's primary parent
// chain. Session refs advance along primary parents only, which is the same
// definition capture uses when adopting refs.
func isAncestor(cache *store.Store, ancestor, descendant store.Hash) (bool, error) {
	steps := 0
	for current := descendant; current != ""; {
		if current == ancestor {
			return true, nil
		}
		step, err := cache.ReadStep(current)
		if err != nil {
			return false, err
		}
		steps++
		if steps > maxChainLength {
			return false, fmt.Errorf("step chain from %s exceeds %d entries", descendant, maxChainLength)
		}
		current = step.Parent
	}
	return false, nil
}

// FlushResult summarises one drain of the outbox.
type FlushResult struct {
	Refs    []PushResult
	Objects int
	// Errors holds one entry per ref or object that could not be delivered.
	// A non-empty Errors is never fatal: the work stays queued.
	Errors []error
}

// Failed reports whether anything could not be delivered.
func (f FlushResult) Failed() bool { return len(f.Errors) > 0 }

// Err joins the individual failures, or returns nil.
func (f FlushResult) Err() error { return errors.Join(f.Errors...) }

// Flush drains the outbox: every session ref whose local tip is ahead of its
// high-water mark, plus every queued loose object.
//
// Flush never returns an error for a delivery failure — failures are collected
// in the result so the caller (a hook running inside a live agent turn) can log
// and continue. Work that fails stays queued.
func Flush(ctx context.Context, cache *store.Store, client Client, spool *Spool) FlushResult {
	var res FlushResult

	status, err := spool.Status(cache)
	if err != nil {
		res.Errors = append(res.Errors, err)
		return res
	}

	for _, lag := range status.Refs {
		if !lag.Pending() {
			continue
		}
		if ctx.Err() != nil {
			res.Errors = append(res.Errors, fmt.Errorf("flush %s: %w", lag.Ref, ctx.Err()))
			return res
		}
		pushed, err := Push(ctx, cache, client, spool, lag.Ref)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("push %s: %w", lag.Ref, err))
			continue
		}
		res.Refs = append(res.Refs, pushed)
	}

	for _, h := range status.LooseObjects {
		if ctx.Err() != nil {
			res.Errors = append(res.Errors, fmt.Errorf("flush object %s: %w", h, ctx.Err()))
			return res
		}
		n, err := uploadObject(ctx, cache, client, h, true, map[store.Hash]bool{})
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("upload queued object %s: %w", h, err))
			continue
		}
		res.Objects += n
		if err := spool.ClearObject(h); err != nil {
			res.Errors = append(res.Errors, err)
		}
	}

	return res
}

// HydrateResult describes a hydrate of one ref.
type HydrateResult struct {
	Ref     string
	Tip     store.Hash
	Objects int
	Steps   int
}

// Hydrate rebuilds a local cache from the server: it downloads the DAG behind
// refName and writes it into cache, then points the local ref at the server's
// tip.
//
// This is what makes "the server is the source of truth" a testable claim
// rather than a slogan: delete the cache, hydrate, and the recorded history is
// back. Derived artifacts (the SQLite index, blame maps) are rebuilt by the
// caller from the objects downloaded here.
func Hydrate(ctx context.Context, cache *store.Store, client Client, refName string) (HydrateResult, error) {
	res := HydrateResult{Ref: refName}

	if err := ValidateRefName(refName); err != nil {
		return res, err
	}

	tip, err := client.GetRef(ctx, refName)
	if err != nil {
		return res, fmt.Errorf("read server ref %s: %w", refName, err)
	}
	res.Tip = tip

	res.Objects, res.Steps, err = fetchHistory(ctx, cache, client, tip)
	if err != nil {
		return res, err
	}

	if tip != "" {
		if err := casLocalRef(cache, refName, tip); err != nil {
			return res, err
		}
	}
	return res, nil
}

// fetchHistory downloads every object reachable from tip into cache, and
// reports how many objects and steps that was.
//
// It deliberately touches no ref. Objects are content-addressed, so writing
// them is purely additive and can never destroy anything; moving a ref is the
// only destructive act in a pull, and separating the two is what lets Pull
// answer "is the server's history ahead of mine?" — a question that can only be
// asked once the server's steps are readable locally — before deciding whether
// to move anything.
func fetchHistory(ctx context.Context, cache *store.Store, client Client, tip store.Hash) (int, int, error) {
	objects, steps := 0, 0
	fetched := make(map[store.Hash]bool)

	for current := tip; current != ""; {
		n, err := fetchObject(ctx, cache, client, current, fetched)
		objects += n
		if err != nil {
			return objects, steps, err
		}

		step, err := cache.ReadStep(current)
		if err != nil {
			return objects, steps, fmt.Errorf("read fetched step %s: %w", current, err)
		}
		steps++
		if steps > maxChainLength {
			return objects, steps, fmt.Errorf("step chain from %s exceeds %d entries", tip, maxChainLength)
		}

		// The tree must be local before its entries can be enumerated.
		n, err = fetchObject(ctx, cache, client, step.Tree, fetched)
		objects += n
		if err != nil {
			return objects, steps, err
		}

		list, err := stepObjects(cache, step, "", map[store.Hash]bool{})
		if err != nil {
			return objects, steps, err
		}
		for _, h := range list {
			n, err := fetchObject(ctx, cache, client, h, fetched)
			objects += n
			if err != nil {
				return objects, steps, err
			}
		}

		current = step.Parent
	}

	return objects, steps, nil
}

// casLocalRef points a cached ref at tip using compare-and-swap, tolerating a
// concurrent writer that already got there.
func casLocalRef(cache *store.Store, refName string, tip store.Hash) error {
	current, err := cache.ReadRef(refName)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read local ref %s: %w", refName, err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		current = ""
	}
	if current == tip {
		return nil
	}
	if err := cache.UpdateRef(refName, current, tip); err != nil {
		return fmt.Errorf("point local ref %s at %s: %w", refName, tip, err)
	}
	return nil
}

func fetchObject(ctx context.Context, cache *store.Store, client Client, h store.Hash, fetched map[store.Hash]bool) (int, error) {
	if h == "" || fetched[h] {
		return 0, nil
	}
	fetched[h] = true
	if cache.ObjectExists(h) {
		return 0, nil
	}

	data, err := client.GetObject(ctx, h)
	if err != nil {
		return 0, fmt.Errorf("fetch object %s: %w", h, err)
	}
	// GetObject verified the hash; WriteBlob recomputes it, so a mismatch here
	// is impossible by construction rather than by trust.
	got, err := cache.WriteBlob(data)
	if err != nil {
		return 0, fmt.Errorf("write fetched object %s: %w", h, err)
	}
	if got != h {
		return 0, fmt.Errorf("fetched object %s hashed to %s", h, got)
	}
	return 1, nil
}

// SessionRefs lists the session refs present in a cache, in stable order.
func SessionRefs(cache *store.Store) ([]string, error) {
	return refsUnder(cache, "sessions")
}

// SyncRefs lists the workspace-sync refs present in a cache, in stable order.
// Today there is exactly one (see internal/capture.WorkspaceSyncRef,
// "sync/workspace"), but repair and pull enumerate it the same way they
// enumerate session refs rather than hard-coding the one name, so a second
// sync ref (a future per-worktree baseline, say) needs no change here.
func SyncRefs(cache *store.Store) ([]string, error) {
	return refsUnder(cache, "sync")
}

func refsUnder(cache *store.Store, dir string) ([]string, error) {
	refs, err := cache.ListRefs(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("list %s refs: %w", dir, err)
	}
	out := make([]string, 0, len(refs))
	for name := range refs {
		out = append(out, dir+"/"+name)
	}
	sort.Strings(out)
	return out, nil
}
