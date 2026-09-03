package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/store"
)

// Binding a project to a server moves every read to a machine-local cache keyed
// to that server. Everything recorded before that moment stays in the project's
// own .regent/, where no command reads it, no command uploads it, and nothing
// says so — `rgt log --session <id>` simply exits 1 for a session that worked a
// minute earlier.
//
// That is silent data loss, and it lands on the person least able to absorb it:
// someone who tried the tool locally for an afternoon and then wired it to a
// server, watching their first day's history vanish at the exact moment they
// are deciding whether the thing is trustworthy.
//
// Carry-over is the step that removes it. It runs inside connect, between
// writing the binding and wiring the hooks.

// carryOverTimeout bounds the upload. Unlike the hook path this is a command
// the user typed and is waiting on, so it can afford the same budget as a
// manual `rgt sync` rather than the few seconds a live agent turn allows.
const carryOverTimeout = manualSyncTimeout

// carriedOverHeadline is the phrase connect prints only when every session came
// across and reached the server. It is deliberately the one string a reader (or
// a test) can look for to mean "all of it": a partial carry-over that printed
// the same words would be the original silent loss in better clothes.
const carriedOverHeadline = "Carried over"

// maxCarriedChain bounds the step walk used to count what was carried. A
// content-addressed DAG cannot contain a cycle, but a corrupt object could
// describe one, and connect must not spin.
const maxCarriedChain = 100000

// carryOver is what happened to the history a project had before it was
// connected.
type carryOver struct {
	// Sessions and Steps count what was copied into the cache the reads now use.
	Sessions int
	Steps    int
	// Delivered counts the sessions confirmed on the server. History that
	// reached only the cache lives in a directory the design calls disposable,
	// so it is counted separately from history that reached the server.
	Delivered int
	// Skipped records history deliberately left where it was, one line each.
	Skipped []string
	// Problems records history that could not be carried, one line each.
	Problems []string
}

// Failed reports whether anything could not be carried over.
func (c carryOver) Failed() bool { return len(c.Problems) > 0 }

// Incomplete reports whether any history stayed behind, for any reason.
func (c carryOver) Incomplete() bool { return len(c.Problems) > 0 || len(c.Skipped) > 0 }

func (c *carryOver) problem(format string, args ...any) {
	c.Problems = append(c.Problems, fmt.Sprintf(format, args...))
}

func (c *carryOver) skip(format string, args ...any) {
	c.Skipped = append(c.Skipped, fmt.Sprintf(format, args...))
}

// carryOverConfig is the server-mode configuration the carried history has to
// land in: the same machine-local cache every read command will resolve for
// this project from now on.
//
// The url and repo id come from the registration that just happened, not from
// the resolver, so carry-over cannot upload to a different server than the one
// connect committed to. Everything else — a REGENT_CACHE_DIR override, a token
// from `rgt login` — is taken from the ambient resolution, because that is what
// the reads will use, and a cache written anywhere else would be unread.
func carryOverConfig(p connectParams, repoID, token string) remote.Config {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, p.projectRoot)
	if err != nil {
		cfg = remote.Config{}
	}
	cfg.ServerURL = strings.TrimRight(p.serverURL, "/")
	cfg.RepoID = repoID
	if token != "" {
		cfg.Token = token
	}
	return cfg
}

// carryOverConfigForProject is carryOverConfig's project-id counterpart (RFC
// 0004): the cache carried history lands in is keyed by Config.Key(), and a
// project-id binding's key is ProjectID, not RepoID. Setting only ProjectID
// (never RepoID) here is what keeps that cache directory distinct from any
// legacy repo_id cache the same working tree might already have used.
func carryOverConfigForProject(p connectParams, projectID, token string) remote.Config {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, p.projectRoot)
	if err != nil {
		cfg = remote.Config{}
	}
	cfg.ServerURL = strings.TrimRight(p.serverURL, "/")
	cfg.RepoID = ""
	cfg.ProjectID = projectID
	if token != "" {
		cfg.Token = token
	}
	return cfg
}

// carryOverLocalHistory copies the history a project recorded before it was
// connected into the machine-local cache its reads will now use, uploads it to
// the server, and reports what happened.
//
// It never aborts connect: a project that stops capturing is a worse outcome
// than one whose old history is still sitting in .regent/ with a message on
// screen saying exactly that. Failures are reported and returned, not raised.
func carryOverLocalHistory(out io.Writer, local *store.Store, cfg remote.Config) carryOver {
	var res carryOver

	refs, err := local.ListRefs("sessions")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		res.problem("could not read the sessions in %s: %v", local.Root, err)
		res.report(out, local.Root)
		return res
	}
	if len(refs) == 0 {
		// The ordinary case — a project connected on day one. Say it plainly
		// rather than saying nothing: silence here is indistinguishable from the
		// bug this step exists to remove.
		fmt.Fprintf(out, "  - No history recorded before connecting — nothing to carry over.\n")
		return res
	}

	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		res.problem("could not locate the local cache for %s: %v", cfg.Key(), err)
		res.report(out, local.Root)
		return res
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		res.problem("could not create the local cache at %s: %v", cacheDir, err)
		res.report(out, local.Root)
		return res
	}
	cache, err := store.Open(cacheDir)
	if err != nil {
		res.problem("could not open the local cache at %s: %v", cacheDir, err)
		res.report(out, local.Root)
		return res
	}

	// Objects first, refs second — the same ordering Push uses on the wire, for
	// the same reason: a ref must never point at a step whose contents are not
	// there yet. Objects are content-addressed, so copying them twice is free
	// and re-running connect converges.
	if err := copyObjects(local, cache); err != nil {
		res.problem("could not copy recorded objects into %s: %v", cacheDir, err)
		res.report(out, local.Root)
		return res
	}

	carried := copyRefs(local, cache, refs, &res)

	// The objects are the history; the index is how log and show read it.
	// Copying the objects without it leaves every carried session present and
	// invisible, which is the original failure with extra steps.
	copyQueryIndex(local, cache, len(carried), &res)

	pushCarried(cache, cfg, carried, &res)

	res.report(out, local.Root)
	return res
}

// copyObjects copies every object in the project's store into the cache.
//
// Content addressing makes this safe rather than merely convenient: WriteBlob
// recomputes the hash, so an object that arrives under the wrong name is
// rejected by construction, and an object already present is a no-op.
func copyObjects(local, cache *store.Store) error {
	return local.WalkObjects(func(h store.Hash) error {
		// The object directory also holds the temp files atomicWriteFile leaves
		// behind on a crash. Only full hashes are objects.
		if len(h) != 64 {
			return nil
		}
		if cache.ObjectExists(h) {
			return nil
		}
		data, err := local.ReadBlob(h)
		if err != nil {
			return fmt.Errorf("read %s: %w", h, err)
		}
		got, err := cache.WriteBlob(data)
		if err != nil {
			return fmt.Errorf("write %s: %w", h, err)
		}
		if got != h {
			return fmt.Errorf("object %s hashed to %s on copy", h, got)
		}
		return nil
	})
}

// carriedRef is one session ref that moved into the cache.
type carriedRef struct {
	name string
	tip  store.Hash
}

// copyRefs points the cache at each session the project recorded, skipping any
// the cache already holds.
//
// Skipping rather than clobbering is the load-bearing choice. A cache that
// already has a session may be ahead of the project's own store — the same
// project connected before, or another checkout writing through the same cache
// — and overwriting the ref would discard exactly the difference. Being left
// behind is only acceptable because it is reported.
func copyRefs(local, cache *store.Store, refs map[string]store.Hash, res *carryOver) []carriedRef {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)

	var carried []carriedRef
	for _, name := range names {
		refName := "sessions/" + filepath.ToSlash(name)
		tip := refs[name]
		if tip == "" {
			continue
		}

		existing, err := cache.ReadRef(refName)
		switch {
		case err == nil && existing == tip:
			// Already where it needs to be; a re-run of connect.
		case err == nil && existing != "":
			res.skip("%s — the cache already holds this session at %s, so it was left as it was",
				refName, shortHash(existing))
			continue
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			res.problem("%s — could not read the cache's copy of this session: %v", refName, err)
			continue
		default:
			if err := cache.UpdateRef(refName, "", tip); err != nil {
				res.problem("%s — could not point the cache at this session: %v", refName, err)
				continue
			}
		}

		steps, err := countSteps(cache, tip)
		if err != nil {
			res.problem("%s — copied, but its history could not be walked: %v", refName, err)
			continue
		}
		res.Sessions++
		res.Steps += steps
		carried = append(carried, carriedRef{name: refName, tip: tip})
	}
	return carried
}

// countSteps counts the steps behind a tip along the primary parent chain,
// which is the same lineage a session ref advances along.
func countSteps(s *store.Store, tip store.Hash) (int, error) {
	n := 0
	for current := tip; current != ""; {
		step, err := s.ReadStep(current)
		if err != nil {
			return 0, err
		}
		n++
		if n > maxCarriedChain {
			return 0, fmt.Errorf("step chain from %s exceeds %d entries", tip, maxCarriedChain)
		}
		current = step.Parent
	}
	return n, nil
}

// copyQueryIndex copies the SQLite index, but only into a cache that has none.
//
// The index is not content-addressed, so there is no merge: overwriting one
// that already has rows would drop whatever the cache recorded through some
// other checkout. When the cache has its own index the carried sessions are on
// disk and on the server but not yet in any listing, and that is reported
// rather than left for the user to discover through an empty `rgt log`.
func copyQueryIndex(local, cache *store.Store, carried int, res *carryOver) {
	if carried == 0 {
		return
	}
	dst := filepath.Join(cache.Root, "index.db")
	if _, err := os.Stat(dst); err == nil {
		res.skip("the conversation index in %s was kept, so the carried session(s) will not appear in "+
			"'rgt log' until you run 'rgt sync --pull <ref>' to rebuild it from the server",
			cache.Root)
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		res.problem("could not check for an existing index at %s: %v", dst, err)
		return
	}

	src := filepath.Join(local.Root, "index.db")
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return
	}
	// The write-ahead log holds committed rows that are not in the main file
	// yet, so the pair travels together or the newest turns are simply absent.
	for _, suffix := range []string{"", "-wal"} {
		if err := copyFile(src+suffix, dst+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			res.problem("could not copy the conversation index into %s: %v", cache.Root, err)
			return
		}
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// pushCarried uploads each carried session to the server.
//
// Reaching the cache is not the same as being safe: the cache is machine-local
// and the design calls it disposable, so history that stops there exists in one
// place and is invisible to every teammate. A failure here leaves the work
// queued in the cache — recoverable with 'rgt sync' — and is reported as a
// failure, never folded into a success line.
func pushCarried(cache *store.Store, cfg remote.Config, carried []carriedRef, res *carryOver) {
	if len(carried) == 0 {
		return
	}

	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		res.problem("could not reach %s to upload the carried history: %v", cfg.ServerURL, err)
		return
	}
	spool, err := remote.OpenSpool(filepath.Join(cache.Root, "spool"))
	if err != nil {
		res.problem("could not open the delivery queue in %s: %v", cache.Root, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), carryOverTimeout)
	defer cancel()

	for _, ref := range carried {
		if _, err := remote.Push(ctx, cache, client, spool, ref.name); err != nil {
			res.problem("%s — copied into the cache but not delivered to %s: %v",
				ref.name, cfg.ServerURL, err)
			continue
		}
		res.Delivered++
	}
}

// report prints what happened. The success headline is printed only when every
// session came across and reached the server; anything less is printed as what
// it is, with the location of the history that stayed behind.
func (c carryOver) report(out io.Writer, localRoot string) {
	if !c.Incomplete() {
		fmt.Fprintf(out, "  ✓ %s %s recorded before connecting, and uploaded %s\n",
			carriedOverHeadline, plural(c.Sessions, "session")+" ("+plural(c.Steps, "step")+")",
			plural(c.Delivered, "session"))
		return
	}

	fmt.Fprintf(out, "  ⚠ History recorded before connecting did not all come across:\n")
	for _, s := range c.Skipped {
		fmt.Fprintf(out, "      · %s\n", s)
	}
	for _, p := range c.Problems {
		fmt.Fprintf(out, "      ! %s\n", p)
	}
	if c.Delivered > 0 {
		fmt.Fprintf(out, "    %s (%s) did reach %s.\n",
			plural(c.Delivered, "session"), plural(c.Steps, "step"), "the server")
	}
	fmt.Fprintf(out, "    Nothing was deleted: it is all still in %s.\n", localRoot)
	if c.Failed() {
		fmt.Fprintf(out, "    Run 'rgt sync' to retry the delivery.\n")
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
