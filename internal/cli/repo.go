package cli

import (
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
	return connectedNotPulledReport(e.cfg)
}

// connectedNotPulledReport is the single wording for "the history is on the
// server and none of it is here yet", used by the missing-cache error and by
// the read commands that find an empty one.
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
	fmt.Fprintln(w, notPulled.Error())
	return true
}

// reportEmptyServerModeCache prints the same report for the other shape of the
// same situation: a cache that exists but holds no session at all. A directory
// created by a hook that never reached the server looks nothing like a missing
// one, and answering it with "no sessions recorded" points a connected user at
// a wiring problem they do not have.
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
	fmt.Fprintln(w, connectedNotPulledReport(cfg))
	return true
}
