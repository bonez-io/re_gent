package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
)

// openStoreFromCWD resolves the object store commands operate on (read commands
// log/show/blame/sessions/status/cat, and workspace commands rewind/drop).
//
// Server mode wins when it is configured, using the same precedence as
// capture.Open: once the server is the source of truth the repository has no
// .regent/ to read, and reads must come from the machine-local cache instead.
// A broken or absent configuration degrades to the local store, so a stray
// environment variable can never make a normal local repository unreadable.
func openStoreFromCWD() (*store.Store, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return openStoreForCWD(cwd)
}

// openStoreForCWD resolves the store for an explicit working directory: server
// mode wins when configured, otherwise the repository-local store. Keeping the
// resolver cwd-scoped lets callers that already know their directory avoid
// depending on the process working directory.
func openStoreForCWD(cwd string) (*store.Store, error) {
	s, ok, err := openServerModeCache(cwd)
	if ok {
		return s, err
	}
	if err != nil {
		// Configured-but-broken (malformed/unreadable config) must be visible,
		// not silently degraded to an empty local store for a server-mode repo.
		fmt.Fprintf(os.Stderr, "warning: server-mode config could not be loaded, using local store: %v\n", err)
	}
	return store.OpenFromDir(cwd)
}

// openServerModeCache opens the server-mode cache store for cwd. The bool
// reports whether server mode is configured at all — when false with a nil
// error the caller falls back to the repository-local store; a non-nil error
// means server mode is configured but unusable and must not be swallowed.
func openServerModeCache(cwd string) (*store.Store, bool, error) {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil {
		return nil, false, err
	}
	if !cfg.Enabled() {
		return nil, false, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, err
	}

	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return nil, true, err
	}

	s, err := store.Open(cacheDir)
	if err != nil {
		// The cache is disposable, so "missing" is an expected state rather
		// than corruption — and on a fresh clone it is the *normal* state, not
		// a fault. Report the situation the user is actually in and name the
		// command that ends it.
		return nil, true, &notPulledError{cfg: cfg}
	}
	return s, true, nil
}

// notPulledError is a connected project whose machine-local cache holds
// nothing yet.
//
// It is a distinct type rather than a message because the read commands have to
// recognise it: a teammate who has just cloned is in a perfectly ordinary state,
// so log, sessions and status report it and exit zero. It used to surface as
// "no local cache … run 'rgt sync --pull <ref>'", which asked the user to name a
// ref they have no way of knowing — the exact dead end 'rgt pull' exists to
// remove.
type notPulledError struct {
	cfg remote.Config
}

func (e *notPulledError) Error() string {
	return fmt.Sprintf("This machine has no cached history for %s. Run 'rgt status' to check the server.", e.cfg.RepoID)
}

// connectedNotPulledReport is the wording for the one case where a live server
// has proved that history exists and none of it is on this machine yet.
func connectedNotPulledReport(cfg remote.Config) string {
	return fmt.Sprintf(
		"Connected to %s as %s, not yet pulled.\n"+
			"This project's history is recorded on the server; none of it is on this machine yet.\n"+
			"  - Fetch it: rgt pull",
		cfg.ServerURL, cfg.RepoID)
}

// reportNotPulled prints the "connected, not yet pulled" report for a read
// command that could not open a cache, and reports whether that is what
// happened. Anything else is a real error and must keep propagating.
func reportNotPulled(w io.Writer, err error) bool {
	var notPulled *notPulledError
	if !errors.As(err, &notPulled) {
		return false
	}
	reportServerModeCache(w, notPulled.cfg)
	return true
}

// reportEmptyServerModeCache reports why a configured project's cache is empty.
//
// An empty cache is not evidence that history is waiting remotely: the project
// may never have been registered, the registered project may have no refs, or
// the server may be unavailable. ListRefs is the read-only protocol request
// that distinguishes those states without guessing a ref name. Keep this one
// reporter shared by log, sessions, and status so they cannot tell different
// stories about the same empty cache.
//
// It reports false in local mode, where "no sessions" is the honest answer.
func reportEmptyServerModeCache(w io.Writer) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil || !cfg.Enabled() {
		return false
	}

	reportServerModeCache(w, cfg)
	return true
}

// reportServerModeCache asks the live server what it knows before describing
// an otherwise empty local cache. cfg has already been validated by the caller.
func reportServerModeCache(w io.Writer, cfg remote.Config) {
	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		fmt.Fprintf(w, "Cannot check %s for this project's history: %v.\n", cfg.ServerURL, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	refs, err := client.ListRefs(ctx, "sessions")
	switch {
	case errors.Is(err, remote.ErrNotFound):
		fmt.Fprintf(w,
			"Connected to %s as %s, but the server does not know this project.\n"+
				"  - Re-register it: rgt connect %s\n",
			cfg.ServerURL, cfg.RepoID, cfg.ServerURL)
	case err != nil:
		// Do not translate a failed request into "not yet pulled": no remote
		// safety claim is justified until this request succeeds.
		fmt.Fprintf(w,
			"Cannot reach %s to check this project's history; this machine's cache is empty.\n"+
				"  - Check the server connection, then try: rgt pull\n"+
				"  - Detail: %v\n",
			cfg.ServerURL, err)
	case len(refs) == 0:
		fmt.Fprintf(w,
			"Connected to %s as %s; the server knows this project but holds no history yet.\n"+
				"  - Record a session here, or ask a teammate to deliver one with: rgt sync\n",
			cfg.ServerURL, cfg.RepoID)
	default:
		fmt.Fprintln(w, connectedNotPulledReport(cfg))
	}
}
