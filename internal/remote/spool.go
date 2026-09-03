package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

// maxSpooledObjects bounds the loose-object outbox. Loose objects are the ones
// not reachable from any step (today: archived host transcripts), so this only
// grows while the server is unreachable. Refusing to grow without bound is
// preferable to filling the user's disk silently.
const maxSpooledObjects = 4096

// ErrSpoolFull is returned when the loose-object outbox is at capacity. The
// caller must log it: it means captured data is being dropped from the delivery
// queue, which is exactly the kind of thing that must never happen silently.
var ErrSpoolFull = errors.New("outbox is full; run 'rgt sync' to drain it")

// Spool is the durable offline queue that makes server mode survive an
// unreachable server without breaking the agent.
//
// It deliberately stores no payloads. Two kinds of durable state live here:
//
//   - refs/<escaped ref>  — a high-water mark: the tip most recently confirmed
//     on the server. Anything the local cache has beyond that mark is pending.
//   - objects/<hash>      — a marker for an object that is not reachable from
//     any step (an archived transcript) and therefore cannot be re-derived by
//     walking the DAG.
//
// Because pending work is derived from durable state rather than from an
// in-memory queue, a crash mid-turn loses nothing: the next hook invocation
// recomputes exactly the same pending set. Every entry is idempotent, so
// replaying the whole spool is always safe.
type Spool struct {
	dir string
}

// OpenSpool opens (creating if needed) the spool rooted at dir.
func OpenSpool(dir string) (*Spool, error) {
	if dir == "" {
		return nil, fmt.Errorf("spool dir is required")
	}
	for _, sub := range []string{"refs", "objects"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create spool dir: %w", err)
		}
	}
	return &Spool{dir: dir}, nil
}

// Dir returns the spool root, for diagnostics.
func (s *Spool) Dir() string { return s.dir }

// escapeRef encodes a ref name (which contains '/') as a single filename.
// url.QueryEscape is reversible, which keeps the mapping unambiguous.
func escapeRef(name string) string { return url.QueryEscape(name) }

func (s *Spool) refMarkPath(name string) string {
	return filepath.Join(s.dir, "refs", escapeRef(name))
}

// KnownRefs lists every ref this spool has a high-water mark for. After a cache
// is wiped this is empty, which is why hydrate accepts an explicit ref name.
func (s *Spool) KnownRefs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "refs"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list pushed marks: %w", err)
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		name, err := url.QueryUnescape(e.Name())
		if err != nil || ValidateRefName(name) != nil {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// PushedTip returns the tip most recently confirmed on the server for name.
// An absent mark returns "" — treated as "the server has nothing", which is the
// safe direction: it causes a redundant push, never a skipped one.
func (s *Spool) PushedTip(name string) (store.Hash, error) {
	data, err := os.ReadFile(s.refMarkPath(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read pushed mark for %s: %w", name, err)
	}
	tip := store.Hash(strings.TrimSpace(string(data)))
	if tip != "" && validateFullHash(tip) != nil {
		// A corrupt mark must not be trusted as a delta base; fall back to a
		// full push rather than skipping objects the server may not have.
		return "", nil
	}
	return tip, nil
}

// RecordPushed stores the high-water mark for a ref after a confirmed push.
//
// Concurrent hook processes may race here; last writer wins. That is safe in
// both directions: an older mark only causes a redundant re-push, and a newer
// mark is only ever written after the server confirmed that tip.
func (s *Spool) RecordPushed(name string, tip store.Hash) error {
	if err := ValidateRefName(name); err != nil {
		return err
	}
	if err := validateFullHash(tip); err != nil {
		return fmt.Errorf("record pushed %s: %w", name, err)
	}
	return atomicWrite(s.refMarkPath(name), []byte(string(tip)+"\n"))
}

// ForgetPushed drops the high-water mark for a ref, forcing the next push to
// re-upload the full history. Used when the server reports it is missing
// objects the ref depends on.
func (s *Spool) ForgetPushed(name string) error {
	if err := os.Remove(s.refMarkPath(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("forget pushed mark for %s: %w", name, err)
	}
	return nil
}

// MarkObject queues a loose object for delivery. The bytes stay in the local
// cache; only the key is spooled.
func (s *Spool) MarkObject(h store.Hash) error {
	if err := validateFullHash(h); err != nil {
		return err
	}
	path := filepath.Join(s.dir, "objects", string(h))
	if _, err := os.Stat(path); err == nil {
		return nil // already queued
	}

	n, err := s.countObjects()
	if err != nil {
		return err
	}
	if n >= maxSpooledObjects {
		return fmt.Errorf("%w (%d objects queued)", ErrSpoolFull, n)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("queue object %s: %w", h, err)
	}
	return f.Close()
}

// ClearObject removes a loose-object marker after a confirmed upload.
func (s *Spool) ClearObject(h store.Hash) error {
	err := os.Remove(filepath.Join(s.dir, "objects", string(h)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear queued object %s: %w", h, err)
	}
	return nil
}

// PendingObjects lists queued loose objects in a stable order.
func (s *Spool) PendingObjects() ([]store.Hash, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "objects"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list queued objects: %w", err)
	}

	var out []store.Hash
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		h := store.Hash(e.Name())
		if validateFullHash(h) != nil {
			continue // ignore foreign files rather than failing the drain
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *Spool) countObjects() (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "objects"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("count queued objects: %w", err)
	}
	return len(entries), nil
}

// cooldownFile holds the unix-nano instant before which no network retry
// should be attempted.
const cooldownFile = "retry-after"

// StartCooldown records that network retries should be suppressed until until.
//
// This is what keeps a long outage cheap: without it every hook invocation
// would pay the full network timeout, and an unreachable server would feel to
// the user like a broken agent.
func (s *Spool) StartCooldown(until time.Time) error {
	return atomicWrite(filepath.Join(s.dir, cooldownFile),
		[]byte(strconv.FormatInt(until.UnixNano(), 10)+"\n"))
}

// ClearCooldown removes the retry suppression after a successful sync.
func (s *Spool) ClearCooldown() error {
	if err := os.Remove(filepath.Join(s.dir, cooldownFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear cooldown: %w", err)
	}
	return nil
}

// InCooldown reports whether retries are currently suppressed, and until when.
// An unreadable or malformed marker is treated as "not cooling": the safe
// direction is to attempt delivery, not to skip it.
func (s *Spool) InCooldown(now time.Time) (bool, time.Time, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, cooldownFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, fmt.Errorf("read cooldown: %w", err)
	}
	nanos, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("parse cooldown: %w", err)
	}
	until := time.Unix(0, nanos)
	return now.Before(until), until, nil
}

// RefLag describes how far a local ref has drifted ahead of the server.
type RefLag struct {
	// Ref is the ref name, e.g. "sessions/claude_code--abc".
	Ref string
	// Local is the tip in the local cache.
	Local store.Hash
	// Pushed is the last tip confirmed on the server ("" if none).
	Pushed store.Hash
	// Steps is the number of steps between Pushed and Local, or -1 when the
	// distance could not be computed (a broken or diverged chain).
	Steps int
}

// Pending reports whether this ref has unsent work.
func (l RefLag) Pending() bool { return l.Local != "" && l.Local != l.Pushed }

// Status summarises everything still owed to the server.
type Status struct {
	Refs          []RefLag
	LooseObjects  []store.Hash
	PendingRefs   int
	PendingSteps  int
	UnknownDeltas int
}

// Clean reports whether nothing is owed to the server.
func (st Status) Clean() bool {
	return st.PendingRefs == 0 && len(st.LooseObjects) == 0
}

// Status computes the outbox status from durable state only: local refs in the
// cache compared against the recorded high-water marks. No network is used, so
// this works while offline.
//
// It walks "sessions" only, deliberately not "sync" (see
// internal/capture.WorkspaceSyncRef). Sessions have one writer and a single
// linear history, so CAS-refusing a diverged push is the correct conflict
// signal. The workspace-sync ref does not: every machine that has ever run
// `rgt init`/`rgt connect` or fired a git hook writes its own root sync step
// with no shared ancestor, so two ordinary machines "diverge" on it as their
// normal starting state, not as an error. Folding it into the same automatic
// queue that git hooks and every agent turn drain made that ordinary state
// surface as delivery failures and pull refusals — see the incident that
// added this comment. Delivering the sync ref stays possible, just not
// automatic: `rgt sync --workspace` (and `rgt sync sync/workspace`, `rgt pull
// sync/workspace`, `rgt sync --repair sync/workspace`) target it explicitly,
// where a real divergence is reported the same way an explicit push to any
// other ref would report one — a decision for whoever typed the command, not
// something a hook silently forces.
func (s *Spool) Status(cache *store.Store) (Status, error) {
	var st Status

	if err := s.collectRefLags(cache, "sessions", &st); err != nil {
		return st, err
	}

	objects, err := s.PendingObjects()
	if err != nil {
		return st, err
	}
	st.LooseObjects = objects
	return st, nil
}

// collectRefLags appends one RefLag to st for every ref found under dir, in
// stable (sorted) order.
func (s *Spool) collectRefLags(cache *store.Store, dir string, st *Status) error {
	refs, err := cache.ListRefs(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("list cached %s refs: %w", dir, err)
	}

	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		refName := dir + "/" + filepath.ToSlash(name)
		pushed, err := s.PushedTip(refName)
		if err != nil {
			return err
		}
		lag := RefLag{Ref: refName, Local: refs[name], Pushed: pushed, Steps: -1}
		if n, err := countSteps(cache, refs[name], pushed); err == nil {
			lag.Steps = n
		}
		if lag.Pending() {
			st.PendingRefs++
			if lag.Steps >= 0 {
				st.PendingSteps += lag.Steps
			} else {
				st.UnknownDeltas++
			}
		}
		st.Refs = append(st.Refs, lag)
	}
	return nil
}

// countSteps counts steps from tip back to (but excluding) stop.
func countSteps(cache *store.Store, tip, stop store.Hash) (int, error) {
	n := 0
	for current := tip; current != "" && current != stop; {
		step, err := cache.ReadStep(current)
		if err != nil {
			return 0, err
		}
		n++
		if n > maxChainLength {
			return 0, fmt.Errorf("step chain from %s exceeds %d entries", tip, maxChainLength)
		}
		current = step.Parent
	}
	return n, nil
}

// atomicWrite writes data via a temp file + rename so a crash never leaves a
// half-written high-water mark (which would be read as a bad delta base).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	tmp = nil

	return os.Rename(tmpPath, path)
}
