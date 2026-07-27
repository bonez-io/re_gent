package capture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
)

// cooldownAfterFailure is how long capture stops attempting network work after
// a failed sync.
//
// Without it, an unreachable server would cost every hook invocation the full
// network timeout — turning "the server is down" into "the agent feels broken",
// which is exactly the outcome server mode must avoid. With it, the cost of a
// long outage is one timeout per cooldown window; everything else is spooled at
// local-disk speed.
const cooldownAfterFailure = 30 * time.Second

// ServerLink holds the server-mode dependencies of a Recorder. It is nil in
// local mode, which keeps every existing code path byte-identical.
type ServerLink struct {
	Client    remote.Client
	Spool     *remote.Spool
	Timeout   time.Duration
	ServerURL string
	RepoID    string
	// Now is the clock used for cooldown decisions; tests override it.
	Now func() time.Time
}

func (l *ServerLink) now() time.Time {
	if l == nil || l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

// ServerMode reports whether this recorder's source of truth is a server.
func (r *Recorder) ServerMode() bool {
	return r != nil && r.Server != nil
}

// OpenServerMode opens a recorder whose source of truth is a re_gent server.
//
// The working tree needs no .regent/ directory: object and ref state live in a
// machine-local cache outside the repository, and everything written there is
// either already confirmed on the server or listed in the outbox.
func OpenServerMode(cwd string, cfg remote.Config) (*Recorder, error) {
	if cwd == "" {
		return nil, fmt.Errorf("cwd is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return nil, err
	}

	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return nil, err
	}

	rec, err := openCachedRecorder(cwd, cacheDir)
	if err != nil {
		return nil, err
	}

	spool, err := remote.OpenSpool(filepath.Join(cacheDir, "spool"))
	if err != nil {
		_ = rec.Close()
		return nil, err
	}

	rec.Server = &ServerLink{
		Client:    client,
		Spool:     spool,
		Timeout:   cfg.Timeout,
		ServerURL: cfg.ServerURL,
		RepoID:    cfg.RepoID,
	}
	return rec, nil
}

// openCachedRecorder opens a Recorder backed by an arbitrary directory rather
// than by <cwd>/.regent. The directory is created if missing: in server mode
// there is nothing for the user to initialise.
func openCachedRecorder(cwd, cacheDir string) (*Recorder, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	s, err := store.Open(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("open cache store: %w", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		return nil, fmt.Errorf("open cache index: %w", err)
	}
	return &Recorder{Store: s, Index: idx, CWD: cwd}, nil
}

// SyncToServer drains the outbox: everything the local cache has that the
// server has not confirmed.
//
// It never returns an error. A hook runs inside a live agent turn, so a sync
// failure is logged and the work stays queued — the agent is never blocked,
// and nothing is silently discarded.
func (r *Recorder) SyncToServer(reason string) {
	if !r.ServerMode() {
		return
	}
	link := r.Server

	status, err := link.Spool.Status(r.Store)
	if err != nil {
		LogHookError(r.Store.Root, fmt.Sprintf("server sync (%s): read outbox: %v", reason, err))
		return
	}
	if status.Clean() {
		return
	}

	cooling, until, err := link.Spool.InCooldown(link.now())
	if err != nil {
		LogHookError(r.Store.Root, fmt.Sprintf("server sync (%s): read cooldown: %v", reason, err))
	}
	if cooling {
		logDebug(r.Store, fmt.Sprintf("server sync (%s) skipped: retrying after %s", reason, until.Format(time.RFC3339)))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), link.Timeout)
	defer cancel()

	res := remote.Flush(ctx, r.Store, link.Client, link.Spool)
	if !res.Failed() {
		if err := link.Spool.ClearCooldown(); err != nil {
			logDebug(r.Store, fmt.Sprintf("clear sync cooldown: %v", err))
		}
		return
	}

	// Delivery failed. Record the backoff and tell the user where the work is;
	// "silently broken" is the one outcome this branch exists to prevent.
	if err := link.Spool.StartCooldown(link.now().Add(cooldownAfterFailure)); err != nil {
		logDebug(r.Store, fmt.Sprintf("start sync cooldown: %v", err))
	}
	LogHookError(r.Store.Root, fmt.Sprintf(
		"server sync (%s) failed; %d step(s) queued in %s, run 'rgt sync' to retry: %v",
		reason, status.PendingSteps, link.Spool.Dir(), res.Err()))
}

// markLooseObject queues an object that no step references (today: an archived
// host transcript) so that it still reaches the server.
func (r *Recorder) markLooseObject(h store.Hash) {
	if !r.ServerMode() || h == "" {
		return
	}
	if err := r.Server.Spool.MarkObject(h); err != nil {
		// ErrSpoolFull means captured bytes will not be delivered until the
		// queue drains. That is a data-retention event, so it is logged as an
		// error rather than swallowed.
		LogHookError(r.Store.Root, fmt.Sprintf("queue object %s for server: %v", h, err))
	}
}

// serverConfigFor resolves server-mode configuration for a hook invocation.
// Any error (bad URL, unparsable config file) disables server mode rather than
// failing the turn; the reason is returned so the caller can log it.
func serverConfigFor(env remote.Env, cwd string) (remote.Config, bool, error) {
	cfg, err := remote.LoadConfigForCWD(env, cwd)
	if err != nil {
		return remote.Config{}, false, err
	}
	if !cfg.Enabled() {
		return cfg, false, nil
	}
	if err := cfg.Validate(); err != nil {
		return cfg, false, err
	}
	return cfg, true, nil
}

// logServerModeFallback records why server mode could not be used. It writes to
// the cache directory when one can be determined, and is a no-op otherwise —
// there is no repository-local place to log in server mode.
func logServerModeFallback(cfg remote.Config, err error) {
	if err == nil {
		return
	}
	cacheDir, cacheErr := remote.CacheDirFor(cfg)
	if cacheErr != nil {
		return
	}
	LogHookError(cacheDir, fmt.Sprintf("server mode unavailable, falling back to local capture: %v", err))
}
