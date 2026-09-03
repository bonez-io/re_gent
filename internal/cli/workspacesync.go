package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/remote/remotecapture"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/internal/style"
)

// workspaceSyncHookBudget bounds how long a git hook's workspace sync (and,
// in server mode, its delivery) may run before the hook stops waiting on it
// and moves on. Hooks must never make a git command wait indefinitely on
// hashing a large working tree or a slow server.
const workspaceSyncHookBudget = 2 * time.Second

// workspaceSyncResult reports what one workspace sync did, for callers that
// print a summary (rgt sync --workspace, rgt init, rgt connect).
type workspaceSyncResult struct {
	StepHash  string
	Wrote     bool
	FileCount int
}

// runWorkspaceSync writes (or extends) the workspace baseline for cwd.
//
// It resolves the same store an agent-turn hook would write to — the local
// .regent/ store in local mode, the machine-local server cache in server
// mode — by following openHookRecorder's exact choice (cmd/rgt/message_hook.go):
// remotecapture.Open when server mode is configured and valid, capture.Open
// otherwise. A baseline written here therefore always lives exactly where the
// next agent turn's steps will.
//
// This never delivers to a server itself — see deliverWorkspaceSyncServerMode
// for the write-then-push sequence every automatic caller (rgt init/connect's
// first-run baseline, the post-commit and pre-push git hooks) actually uses,
// and its doc comment for why delivery is never folded into Spool.Status's
// automatic queue.
func runWorkspaceSync(cwd string) (workspaceSyncResult, error) {
	return runWorkspaceSyncWithEnv(cwd, remote.OSEnv)
}

// runWorkspaceSyncWithEnv is runWorkspaceSync with an injectable environment
// reader, so a caller that already threads one for testability (the git
// hooks; see runGitPrePushHook) does not have this reach past it to the real
// process environment.
func runWorkspaceSyncWithEnv(cwd string, env remote.Env) (workspaceSyncResult, error) {
	cfg, cfgErr := remote.LoadConfigForCWD(env, cwd)
	serverMode := cfgErr == nil && cfg.Enabled() && cfg.Validate() == nil

	if serverMode {
		return runWorkspaceSyncServerMode(cwd, cfg)
	}
	return runWorkspaceSyncLocal(cwd)
}

// boundedRun runs fn but does not make the caller wait past budget, returning
// whether fn actually finished in time.
//
// A caller that stops waiting does not mean fn keeps running to completion.
// Every caller of this is inside a `rgt git-hook <name>` (or similarly
// short-lived) process: once its RunE returns, main() returns, and the whole
// process exits immediately — Go does not wait for stray goroutines on exit,
// so an unfinished fn is abandoned mid-flight, not merely deferred. That is
// fine here because every use of boundedRun does content-addressed writes and
// idempotent network calls: an abandoned attempt leaves nothing corrupt
// (atomicWriteFile is temp-file-plus-rename), only unfinished, and the next
// commit's post-commit hook, the next push's pre-push hook, or a person
// running `rgt sync --workspace` simply tries again from scratch. This is a
// deliberate choice not to spawn a genuinely detached child process (the way
// internal/insight/spawn.go's worker does) to let this specific best-effort
// work outlive the budget: the workspace-sync baseline is low-stakes and
// self-healing on the very next trigger, which does not justify a re-exec's
// complexity.
func boundedRun(budget time.Duration, fn func()) (finished bool) {
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		return false
	}
}

func runWorkspaceSyncLocal(cwd string) (workspaceSyncResult, error) {
	rec, ok, err := capture.Open(cwd)
	if err != nil {
		return workspaceSyncResult{}, fmt.Errorf("open store: %w", err)
	}
	if !ok {
		return workspaceSyncResult{}, fmt.Errorf("not a re_gent repository (no .regent/ in %s)", cwd)
	}
	defer func() { _ = rec.Close() }()

	stepHash, wrote, fileCount, err := capture.WorkspaceSync(rec.Store, cwd)
	if err != nil {
		return workspaceSyncResult{}, err
	}
	return workspaceSyncResult{StepHash: string(stepHash), Wrote: wrote, FileCount: fileCount}, nil
}

// runBaselineSync is the first-run baseline: `rgt init` and `rgt connect`
// call this once, after hooks are wired, so the Files view is never empty
// before the first captured agent step. It prints exactly one line — success,
// a skip, or (server mode only) a "written but not yet delivered" warning —
// and never returns an error: a baseline snapshot is a convenience over
// capture, and a failure here must not fail init or connect, which have
// already done everything else they needed to by this point.
func runBaselineSync(out io.Writer, cwd string) {
	cfg, cfgErr := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if cfgErr == nil && cfg.Enabled() && cfg.Validate() == nil {
		res, writeErr, deliverErr := deliverWorkspaceSyncServerMode(cwd, cfg, workspaceSyncHookBudget)
		switch {
		case writeErr != nil:
			fmt.Fprintf(out, "  %s Baseline snapshot skipped: %v\n", style.Warning(""), writeErr)
		case deliverErr != nil:
			fmt.Fprintf(out, "  %s Baseline snapshot: %d file(s), not delivered yet: %v; rgt sync --workspace retries\n",
				style.Warning(""), res.FileCount, deliverErr)
		default:
			fmt.Fprintf(out, "  %s Baseline snapshot: %d file(s)\n", style.Success(""), res.FileCount)
		}
		return
	}

	res, err := runWorkspaceSyncLocal(cwd)
	if err != nil {
		fmt.Fprintf(out, "  %s Baseline snapshot skipped: %v\n", style.Warning(""), err)
		return
	}
	fmt.Fprintf(out, "  %s Baseline snapshot: %d file(s)\n", style.Success(""), res.FileCount)
}

// runWorkspaceSyncServerMode writes the workspace baseline into the
// machine-local server cache, chaining the new step onto the server's
// current refs/sync/workspace tip when one can be resolved (see
// resolveServerSyncParent) — so every machine's sync chain converges onto
// shared history instead of each rooting its own, incompatible one. It never
// delivers anything itself; see deliverWorkspaceSyncServerMode.
func runWorkspaceSyncServerMode(cwd string, cfg remote.Config) (workspaceSyncResult, error) {
	// remotecapture.Open wires a *Link (capture.Delivery) onto the recorder
	// for parity with the hook path, but it is deliberately never invoked —
	// delivery here is always the explicit push in
	// deliverWorkspaceSyncServerMode / pushWorkspaceSyncRef.
	rec, _, err := remotecapture.Open(cwd, cfg)
	if err != nil {
		return workspaceSyncResult{}, fmt.Errorf("open server-mode cache: %w", err)
	}
	defer func() { _ = rec.Close() }()

	parentHint := resolveServerSyncParent(rec.Store, cfg, workspaceSyncHookBudget)

	stepHash, wrote, fileCount, err := capture.WorkspaceSyncOnto(rec.Store, cwd, parentHint)
	if err != nil {
		return workspaceSyncResult{}, err
	}
	return workspaceSyncResult{StepHash: string(stepHash), Wrote: wrote, FileCount: fileCount}, nil
}

// resolveServerSyncParent asks the server for its current
// refs/sync/workspace tip and ensures the step and tree objects (not the
// tree's blobs) are present in cache, so a new sync step written afterward
// can chain onto shared history — see capture.WorkspaceSyncOnto.
//
// Bounded by budget. Any failure — an unreachable server, no ref there yet,
// a fetch that times out or errors — degrades to "" (no hint), which
// WorkspaceSyncOnto already treats as "chain onto the local ref instead, the
// same as WorkspaceSync". Never fatal, and it costs at most two small object
// fetches.
func resolveServerSyncParent(cache *store.Store, cfg remote.Config, budget time.Duration) store.Hash {
	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	tip, err := client.GetRef(ctx, capture.WorkspaceSyncRef)
	if err != nil || tip == "" {
		return ""
	}

	fetched := map[store.Hash]bool{}
	if _, err := remote.FetchObject(ctx, cache, client, tip, fetched); err != nil {
		return ""
	}
	step, err := cache.ReadStep(tip)
	if err != nil {
		return ""
	}
	if step.Tree != "" {
		if _, err := remote.FetchObject(ctx, cache, client, step.Tree, fetched); err != nil {
			return ""
		}
	}
	return tip
}

// pushWorkspaceSyncRef delivers refs/sync/workspace to the server by name,
// bounded by budget — the same targeted push `rgt sync --workspace`'s own
// command path makes (with a longer, user-facing budget instead), reusing
// the exact same remote.Push used for every other ref.
func pushWorkspaceSyncRef(cfg remote.Config, budget time.Duration) error {
	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return err
	}
	cache, err := store.Open(cacheDir)
	if err != nil {
		return err
	}
	spool, err := remote.OpenSpool(filepath.Join(cacheDir, "spool"))
	if err != nil {
		return err
	}
	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	_, err = remote.Push(ctx, cache, client, spool, capture.WorkspaceSyncRef)
	return err
}

// deliverWorkspaceSyncServerMode writes the workspace baseline (chaining onto
// the server's tip when resolveServerSyncParent can find one) and then
// delivers it: pushes refs/sync/workspace by name. If the push loses a
// genuine race — another machine advanced the server's tip between our
// parent lookup and our push — it retries once, re-resolving the parent
// against the now-current tip. A second collision in the same window is left
// alone; the next automatic trigger (or a person running `rgt sync
// --workspace`) tries again.
//
// writeErr is set only when the local write itself failed, in which case
// there is no file count to report. deliverErr is set only when the write
// succeeded but the push did not — there is a count to report, just not
// delivery. At most one of them is non-nil.
func deliverWorkspaceSyncServerMode(cwd string, cfg remote.Config, budget time.Duration) (res workspaceSyncResult, writeErr, deliverErr error) {
	for attempt := 0; attempt < 2; attempt++ {
		res, writeErr = runWorkspaceSyncServerMode(cwd, cfg)
		if writeErr != nil {
			return res, writeErr, nil
		}
		if !res.Wrote {
			return res, nil, nil
		}

		deliverErr = pushWorkspaceSyncRef(cfg, budget)
		if deliverErr == nil {
			return res, nil, nil
		}
		if !errors.Is(deliverErr, remote.ErrDiverged) || attempt == 1 {
			return res, nil, deliverErr
		}
	}
	return res, nil, deliverErr
}

// syncWorkspaceAndDeliver is the shared write-then-deliver step every
// automatic caller uses: rgt init/connect's first-run baseline (through
// runBaselineSync) and the post-commit and pre-push git hooks (through
// boundedRun, since both need this to never outlast their own hook budget —
// see boundedRun's doc comment for what "bounded" actually guarantees there).
// Local mode writes only; there is no server to deliver to.
func syncWorkspaceAndDeliver(cwd string, env remote.Env, budget time.Duration) (res workspaceSyncResult, writeErr, deliverErr error) {
	cfg, cfgErr := remote.LoadConfigForCWD(env, cwd)
	if cfgErr == nil && cfg.Enabled() && cfg.Validate() == nil {
		return deliverWorkspaceSyncServerMode(cwd, cfg, budget)
	}
	res, writeErr = runWorkspaceSyncLocal(cwd)
	return res, writeErr, nil
}
