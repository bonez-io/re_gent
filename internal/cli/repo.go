package cli

import (
	"fmt"
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
		// than corruption: point the user at the command that rebuilds it from
		// the server instead of at 'rgt init', which would be wrong here.
		return nil, true, fmt.Errorf(
			"no local cache for repo %q from %s\n\nRun 'rgt sync --pull <ref>' to rebuild it from the server",
			cfg.RepoID, cfg.ServerURL)
	}
	return s, true, nil
}
